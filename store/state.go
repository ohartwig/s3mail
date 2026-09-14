// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	obj, err := s.s3.Get(ctx, s.bucket, s.key, "")
	if err != nil {
		return nil
	}
	var d core.Data
	if json.Unmarshal(obj.Body, &d) != nil {
		return nil
	}
	return d.Normalize()
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
func (s *State) fetchOps(ctx context.Context, keys []string) [][]core.Op {
	out := make([][]core.Op, len(keys))
	if len(keys) == 0 {
		return out
	}
	sem := make(chan struct{}, s.Workers)
	var wg sync.WaitGroup
	for i, key := range keys {
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
	payload, _ := json.Marshal(&clone)
	s.mu.Unlock()

	if err := s.s3.Put(ctx, s.bucket, s.key, payload, "application/json"); err != nil {
		return // it just stays behind, and comes round again next time
	}
	s.mu.Lock()
	s.upto, s.openOps = clone.Upto, 0
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
