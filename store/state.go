// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/ohartwig/s3mail/core"
)

const (
	// StateObject is the snapshot, StateOps the prefix with the individual
	// changes. Both start with a dot and therefore count as internal.
	StateObject = ".s3mail-state.json"
	StateOps    = ".s3mail-state/"

	// CompactAfter is the number of open ops from which they get merged.
	CompactAfter = 50

	// JournalKeep is how many key changes the snapshot carries for machines
	// that have not read them yet. A machine further behind than that lists
	// the mailbox once. 500 changes are about 75 KB in a document that is only
	// fetched when it changed.
	JournalKeep = 500

	fetchWorkers = 8
)

// State is the shared state in the bucket.
//
// What gets written is not the whole document but the single change: every write
// gets a key of its own, and only that one write writes it. Two machines cannot
// get in each other's way - there is no shared key both point at, and therefore
// neither a lock nor an If-Match nor a 412.
//
// What gets read is the snapshot plus every op newer than its watermark (Upto),
// in key order. From CompactAfter open ops on, they are merged into a new
// snapshot.
//
// The watermark makes cleaning up uncritical: if an op stays behind because the
// delete failed, it is skipped on the next load instead of applied a second
// time.
type State struct {
	s3        S3
	bucket    string
	key       string
	opsPrefix string
	localFile string

	mu       sync.RWMutex
	data     *core.Data
	pending  []core.Op
	upto     string
	openOps  int
	remoteOK bool
	defers   int
	dirty    bool
	seq      int

	// The snapshot only changes when somebody compacts, and with a large
	// mailbox it is megabytes. A HEAD says whether the copy in hand is still the
	// one in the bucket; only then is it fetched again.
	snapETag string
	snapBody []byte
	// opCache holds the op objects already fetched. Each is written once under
	// a name of its own and never changed, so a body once read stays right
	// until compaction removes the object.
	opCache map[string][]core.Op
	// open are the op keys above the watermark, as of the last Load.
	open []string

	// The journal cursor - see Journal. seen are op keys whose key changes have
	// been handed out (or that this process wrote itself), maxSeen is the
	// highest op name among them.
	seen    map[string]bool
	maxSeen string
	unread  []journalEntry
	// gap is set when the watermark rose past everything seen - see Journal.
	// It stays set until a full listing (SeenAll) has made up for it.
	gap bool

	// instance tells this process apart from every other one writing to the same
	// bucket. Without it two machines could produce the same op name in the same
	// microsecond - and one of the two would be gone.
	instance string

	// exchangeable, so tests run deterministically
	Now     func() time.Time
	Workers int
}

func NewState(ctx context.Context, s3 S3, bucket, root, localFile string) *State {
	st := &State{
		s3: s3, bucket: bucket,
		key:       root + StateObject,
		opsPrefix: root + StateOps,
		localFile: localFile,
		data:      core.NewData(),
		remoteOK:  true,
		instance:  InstanceID(),
		opCache:   map[string][]core.Op{},
		seen:      map[string]bool{},
		Now:       func() time.Time { return time.Now().UTC() },
		Workers:   fetchWorkers,
	}
	st.Load(ctx)
	return st
}

// InstanceID is unique per State. Six random bytes from crypto/rand - so two
// machines do not collide even when their clocks agree to the microsecond.
func InstanceID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "000000000000"
	}
	return hex.EncodeToString(b)
}

// SetInstance is for tests that stage two machines.
func (s *State) SetInstance(k string) { s.instance = k }

// Data hands out a snapshot. Callers may read it, not change it.
func (s *State) Data() *core.Data {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data
}

func (s *State) Get(mid string) core.Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Get(mid)
}

// RemoteOK is false as soon as a write to S3 has failed - s3mail then carries on
// with the local file and says so in the sidebar.
func (s *State) RemoteOK() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.remoteOK
}

func (s *State) PendingOps() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.openOps
}

func (s *State) fetchSnapshot(ctx context.Context) *core.Data {
	body := s.snapshotBody(ctx)
	if body == nil {
		return nil
	}
	var d core.Data
	if json.Unmarshal(body, &d) != nil {
		return nil
	}
	return d.Normalize()
}

// snapshotBody returns the snapshot's bytes, from the copy in hand when the
// HEAD says it is still current. A HEAD that fails for another reason than a
// missing object falls back to the GET - the copy is a saving, not a source.
func (s *State) snapshotBody(ctx context.Context) []byte {
	head, err := s.s3.Head(ctx, s.bucket, s.key)
	if errors.Is(err, ErrNotFound) {
		s.mu.Lock()
		s.snapETag, s.snapBody = "", nil
		s.mu.Unlock()
		return nil
	}
	if err == nil && head.ETag != "" {
		s.mu.RLock()
		body, etag := s.snapBody, s.snapETag
		s.mu.RUnlock()
		if body != nil && etag == head.ETag {
			return body
		}
	}
	obj, err := s.s3.Get(ctx, s.bucket, s.key, "")
	if err != nil {
		return nil
	}
	s.mu.Lock()
	s.snapETag, s.snapBody = obj.ETag, obj.Body
	s.mu.Unlock()
	return obj.Body
}

func (s *State) readLocal() *core.Data {
	blob, err := os.ReadFile(s.localFile)
	if err != nil {
		return nil
	}
	var d core.Data
	if json.Unmarshal(blob, &d) != nil {
		return nil
	}
	return d.Normalize()
}

// listOps returns the keys of all stored ops, oldest first.
func (s *State) listOps(ctx context.Context) []string {
	objs, err := s.s3.List(ctx, s.bucket, s.opsPrefix)
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(objs))
	for _, o := range objs {
		keys = append(keys, o.Key)
	}
	slices.Sort(keys)
	return keys
}

// fetchOps loads the op objects concurrently; the order stays that of the keys.
// What was fetched before comes from opCache - see there.
func (s *State) fetchOps(ctx context.Context, keys []string) [][]core.Op {
	out := make([][]core.Op, len(keys))
	if len(keys) == 0 {
		return out
	}
	sem := make(chan struct{}, s.Workers)
	var wg sync.WaitGroup
	for i, key := range keys {
		s.mu.RLock()
		ops, cached := s.opCache[key]
		s.mu.RUnlock()
		if cached {
			out[i] = ops
			continue
		}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			obj, err := s.s3.Get(ctx, s.bucket, key, "")
			if err != nil {
				return // one broken op does not topple the rest
			}
			var h struct {
				Ops []core.Op `json:"ops"`
			}
			if json.Unmarshal(obj.Body, &h) != nil {
				return
			}
			out[i] = h.Ops
			s.mu.Lock()
			s.opCache[key] = h.Ops
			s.mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

// Load fetches snapshot and ops and builds the current state from them.
func (s *State) Load(ctx context.Context) {
	local := s.readLocal()
	snap := s.fetchSnapshot(ctx)

	var base *core.Data
	var upto string
	if snap != nil {
		base, upto = snap, snap.Upto
		if local != nil {
			core.MergeMissing(base, local) // do not lose what was done offline
		}
	} else if local != nil {
		base = local
		// The local copy's journal says nothing about the bucket.
		base.Journal, base.JournalSince, base.JournalOK = nil, "", false
	} else {
		base = core.NewData()
	}
	base.Upto = ""
	base.Normalize()

	cut := len(s.opsPrefix)
	var open []string
	for _, k := range s.listOps(ctx) {
		if len(k) > cut && k[cut:] > upto {
			open = append(open, k)
		}
	}
	for _, ops := range s.fetchOps(ctx, open) {
		for _, op := range ops {
			core.Apply(base, op)
		}
	}

	s.mu.Lock()
	s.data, s.upto, s.openOps = base, upto, len(open)
	for _, op := range s.pending { // eigene offene Aenderungen erneut drauf
		core.Apply(s.data, op)
	}
	s.open = open
	// What is no longer open is compacted or gone; its body is not needed.
	keep := make(map[string][]core.Op, len(open))
	for _, k := range open {
		if ops, ok := s.opCache[k]; ok {
			keep[k] = ops
		}
	}
	s.opCache = keep
	// Before absorb: the ops above the watermark raise maxSeen, and then a
	// watermark that jumped past unread ops would no longer show.
	s.catchUp()
	s.absorb()
	s.mu.Unlock()

	if len(open) >= CompactAfter {
		s.Compact(ctx, open)
	}
}

// opName is unique and sortable by write time: the timestamp first, so the
// lexicographic order matches the write order, then this process's identifier
// and a running number within it.
func (s *State) opName() string {
	s.seq++
	return fmt.Sprintf("%s-%s-%04d.json",
		s.Now().UTC().Format("20060102T150405.000000"), s.instance, s.seq)
}

func (s *State) writeLocal(payload []byte) {
	if s.localFile == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.localFile), 0o700)
	tmp := s.localFile + ".tmp"
	if os.WriteFile(tmp, payload, 0o600) == nil {
		_ = os.Rename(tmp, s.localFile)
	}
}

// Mutate applies a change and writes it away.
func (s *State) Mutate(ctx context.Context, ops ...core.Op) error {
	s.mu.Lock()
	for _, op := range ops {
		core.Apply(s.data, op)
		s.pending = append(s.pending, op)
	}
	s.dirty = true
	s.mu.Unlock()
	return s.Save(ctx)
}

// Save stores the open ops as one object. Inside a batch nothing happens - the
// end of the batch writes then.
func (s *State) Save(ctx context.Context) error {
	s.mu.Lock()
	if s.defers > 0 {
		s.dirty = true
		s.mu.Unlock()
		return nil
	}
	ops := s.pending
	s.pending = nil
	payload, _ := json.Marshal(s.data)
	s.writeLocal(payload)
	if len(ops) == 0 {
		s.dirty = false
		s.mu.Unlock()
		return nil
	}
	name := s.opName()
	// Our own op is no news to us: marked as seen before it exists, so a Load
	// running beside this one cannot hand it out as somebody else's.
	s.opCache[s.opsPrefix+name] = ops
	s.markSeen(s.opsPrefix + name)
	s.mu.Unlock()

	body, _ := json.Marshal(struct {
		Ops []core.Op `json:"ops"`
	}{ops})
	if err := s.s3.Put(ctx, s.bucket, s.opsPrefix+name, body, "application/json"); err != nil {
		s.mu.Lock()
		s.pending = append(ops, s.pending...) // lose nothing
		s.remoteOK = false
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	s.remoteOK, s.dirty = true, false
	s.openOps++
	due := s.openOps >= CompactAfter
	s.mu.Unlock()

	if due {
		s.Load(ctx) // brings foreign ops along and merges
	}
	return nil
}

// Compact writes a snapshot with a new watermark and clears away the ops it
// covers.
//
// merged are exactly the ops that sit in s.data - the watermark may only be
// raised that far, which is why Load hands Compact the list instead of letting
// it find the list itself.
func (s *State) Compact(ctx context.Context, merged []string) {
	if len(merged) == 0 {
		return
	}
	cut := len(s.opsPrefix)
	s.mu.Lock()
	clone := *s.data
	clone.Upto = merged[len(merged)-1][cut:]
	clone.Journal, clone.JournalSince, clone.JournalOK = s.journalTail(merged)
	payload, _ := json.Marshal(&clone)
	s.mu.Unlock()

	if err := s.s3.Put(ctx, s.bucket, s.key, payload, "application/json"); err != nil {
		return // it just stays behind, and comes round again next time
	}
	s.mu.Lock()
	s.upto, s.openOps = clone.Upto, 0
	s.data.Journal, s.data.JournalSince, s.data.JournalOK = clone.Journal, clone.JournalSince, true
	s.mu.Unlock()

	sem := make(chan struct{}, s.Workers)
	var wg sync.WaitGroup
	for _, key := range merged {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			_ = s.s3.Delete(ctx, s.bucket, key) // the watermark catches errors
		})
	}
	wg.Wait()
}

// journalTail is the snapshot's journal after merging these ops: the old tail,
// the key changes of the merged ops, cut to JournalKeep. Called with s.mu held.
//
// Without it every compaction would cost every other machine a full listing:
// the op that triggers a compaction is written a moment before it, so nobody
// else has read it yet when it disappears.
func (s *State) journalTail(merged []string) ([]core.JournalEntry, string, bool) {
	cut := len(s.opsPrefix)
	var tail []core.JournalEntry
	since := s.upto // complete above the old watermark: those are the merged ops
	if s.upto == "" || s.data.JournalOK {
		tail, since = slices.Clone(s.data.Journal), s.data.JournalSince
	}
	for _, k := range merged {
		ops, fetched := s.opCache[k]
		if !fetched {
			// Nobody can vouch for what this op said. Complete only above it.
			tail, since = nil, k[cut:]
			continue
		}
		var changes []core.KeyChange
		for _, op := range ops {
			if op.T == "keys" {
				changes = append(changes, op.Keys...)
			}
		}
		if len(changes) > 0 {
			tail = append(tail, core.JournalEntry{Op: k[cut:], Keys: changes})
		}
	}
	n := 0
	for _, e := range tail {
		n += len(e.Keys)
	}
	for n > JournalKeep && len(tail) > 0 {
		since, n = tail[0].Op, n-len(tail[0].Keys)
		tail = tail[1:]
	}
	return tail, since, true
}

// Batch collects several changes and writes a single op object at the end.
func (s *State) Batch(ctx context.Context, fn func() error) error {
	s.mu.Lock()
	s.defers++
	s.mu.Unlock()

	err := fn()

	s.mu.Lock()
	s.defers--
	flush := s.defers == 0 && s.dirty
	s.mu.Unlock()
	if flush {
		if e := s.Save(ctx); err == nil {
			err = e
		}
	}
	return err
}

// Upto is the current watermark - for tests and the display.
func (s *State) Upto() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.upto
}

// -- journal ------------------------------------------------------------------ //

// journalEntry is what one op object said about keys.
type journalEntry struct {
	op      string
	changes []core.KeyChange
}

// absorb moves every open op not yet seen into unread. Called with s.mu held.
//
// It runs inside Load, not when somebody asks: a compaction can follow a Load
// directly (Save does that), and an op that was compacted before anybody read
// its key changes would be lost to the journal.
func (s *State) absorb() {
	for _, k := range s.open {
		if s.seen[k] {
			continue
		}
		ops, fetched := s.opCache[k]
		if !fetched {
			continue // the GET failed; the next Load tries again
		}
		var changes []core.KeyChange
		for _, op := range ops {
			if op.T == "keys" {
				changes = append(changes, op.Keys...)
			}
		}
		s.unread = append(s.unread, journalEntry{op: k, changes: changes})
		s.markSeen(k)
	}
	// What lies at or below the watermark cannot come round again: Load skips
	// it. Forgetting it keeps the set as small as the open ops.
	cut := len(s.opsPrefix)
	for k := range s.seen {
		if len(k) > cut && k[cut:] <= s.upto {
			delete(s.seen, k)
		}
	}
}

// catchUp handles a watermark that rose past everything seen: somebody
// compacted ops this process had not read. The snapshot's journal tail says
// what they did about keys, if it reaches back far enough; otherwise it is a
// gap, and only a full listing knows. Called with s.mu held, before absorb.
func (s *State) catchUp() {
	if s.upto <= s.maxSeen {
		return
	}
	d := s.data
	if !d.JournalOK || d.JournalSince > s.maxSeen {
		s.gap = true
		return
	}
	for _, e := range d.Journal {
		if e.Op > s.maxSeen && e.Op <= s.upto && !s.seen[s.opsPrefix+e.Op] {
			s.unread = append(s.unread, journalEntry{op: s.opsPrefix + e.Op, changes: e.Keys})
		}
	}
	s.maxSeen = s.upto
}

// markSeen records an op key. Called with s.mu held.
func (s *State) markSeen(key string) {
	s.seen[key] = true
	if name := key[min(len(key), len(s.opsPrefix)):]; name > s.maxSeen {
		s.maxSeen = name
	}
}

// Journal hands out, oldest first, the key changes from op objects written by
// other processes since the last call, and how many such op objects there were
// - a flag or tag change is one too, and the page wants to hear about it.
//
// gap is true when ops may have been missed: a Load found the snapshot's
// watermark above everything this process had seen, so somebody compacted ops
// away before they were read here. Only a full listing knows what they said. Clock skew
// between machines can hide a gap from this test; the slow full reconcile is
// the net for that.
func (s *State) Journal() (changes []core.KeyChange, foreign int, gap bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.unread {
		changes = append(changes, e.changes...)
	}
	foreign = len(s.unread)
	s.unread = nil
	return changes, foreign, s.gap
}

// SeenAll marks everything open as seen and drops what is unread - after a
// full listing, which already says everything the journal would.
func (s *State) SeenAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.open {
		s.markSeen(k)
	}
	if s.upto > s.maxSeen {
		s.maxSeen = s.upto
	}
	s.unread, s.gap = nil, false
}

// Cursor is what has to survive a restart for the journal to carry on where
// it stopped: the highest op name seen and the open ones seen.
func (s *State) Cursor() (maxSeen string, seen []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for k := range s.seen {
		seen = append(seen, k)
	}
	slices.Sort(seen)
	return s.maxSeen, seen
}

// RestoreCursor puts a saved cursor back. NewState has loaded already and
// taken every open op for unread; with the cursor, only those it does not
// cover stay unread.
func (s *State) RestoreCursor(maxSeen string, seen []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen, s.maxSeen, s.unread = map[string]bool{}, maxSeen, nil
	for _, k := range seen {
		s.seen[k] = true
	}
	s.catchUp()
	s.absorb()
}
