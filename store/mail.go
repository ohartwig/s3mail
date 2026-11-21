// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ohartwig/s3mail/core"
	"github.com/ohartwig/s3mail/mimeparse"
)

// HeaderChunk is how much is fetched per message for the index - enough for the
// headers and the preview text.
const HeaderChunk = 65536

var (
	// ErrTrashOnly: deleting for good works only from there.
	ErrTrashOnly = errors.New("deleting for good works only from the trash")
	// ErrDeleteBlocked: with --no-delete not at all.
	ErrDeleteBlocked = errors.New("deleting for good is switched off")
)

// Mailbox is the index over the bucket, state included.
type Mailbox struct {
	s3     S3
	kms    KMS
	bucket string
	*core.Store

	AllowDelete bool
	Workers     int
	CacheFile   string
	// ReconcileEvery is how old the last full listing may get before Sync
	// makes one again. See Sync for what the full listing is still needed for.
	ReconcileEvery time.Duration

	// syncMu keeps Refresh and Sync from running side by side - the queue
	// watcher and the page can both ask at once, and two full listings in
	// parallel are the bill twice over.
	syncMu sync.Mutex
	// reconciled is when the last full listing finished. Kept in the cache, so
	// a restart within ReconcileEvery does not list the whole mailbox again.
	reconciled time.Time

	mu    sync.RWMutex
	index map[string]core.Message
	// encrypted remembers whether client-side encrypted objects lie in the
	// mailbox. Once they do, the range GET falls away - half a ciphertext cannot
	// be decrypted.
	encrypted bool

	State   *State
	content *bodyCache
	// cacheKey encrypts the index and the message bodies on disk. Empty when
	// plain text was asked for deliberately.
	cacheKey []byte
}

// cacheKey encrypts everything this mailbox writes to disk. Empty means plain
// text, and that is a decision the caller has to make out loud - see
// cachecrypt.go for why it matters and cmd/s3mail for who decides.
func NewMailbox(ctx context.Context, s3 S3, kms KMS, bucket, root, cacheDir string,
	cacheKey []byte, allowDelete bool) *Mailbox {
	st := core.NewStore(root)
	slug := regexp.MustCompile(`[^A-Za-z0-9_.-]`).ReplaceAllString(bucket+"__"+st.Root, "_")
	if slug == "" {
		slug = "default"
	}
	m := &Mailbox{
		s3: s3, kms: kms, bucket: bucket, Store: st,
		AllowDelete: allowDelete, Workers: 8,
		ReconcileEvery: DefaultReconcile,
		index:          map[string]core.Message{},
		cacheKey:       cacheKey,
	}
	var cursor *cachedCursor
	if cacheDir != "" {
		_ = os.MkdirAll(cacheDir, 0o700)
		m.CacheFile = filepath.Join(cacheDir, slug+".json")
		cursor = m.readCache()
		m.content = newBodyCache(filepath.Join(cacheDir, slug+".bodies"), cacheKey)
	}
	local := ""
	if cacheDir != "" {
		local = filepath.Join(cacheDir, slug+".state.json")
	}
	m.State = NewState(ctx, s3, bucket, st.Root, local)
	if cursor != nil {
		m.State.RestoreCursor(cursor.MaxSeen, cursor.Seen)
	}
	return m
}

// Index hands the entries out as a list, sorted by key.
func (m *Mailbox) Index() []core.Message {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]core.Message, 0, len(m.index))
	for _, v := range m.index {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b core.Message) int { return cmp.Compare(a.Key, b.Key) })
	return out
}

func (m *Mailbox) Search(query string, o core.SearchOpts) []core.Message {
	return core.Search(m.Index(), m.State.Data(), query, o)
}

func (m *Mailbox) Folders() []core.FolderInfo {
	return core.Folders(m.Index(), m.State.Data())
}

// Fetch gets an object and opens it if needed. headBytes > 0 fetches only the
// beginning - unless the mailbox is client-side encrypted, then always the whole
// thing.
//
// Whole messages come from the cache when they lie there. Only whole ones:
// storing a fragment would mean missing the rest on the next open without
// noticing.
func (m *Mailbox) Fetch(ctx context.Context, key string, headBytes int) ([]byte, error) {
	m.mu.RLock()
	partial := headBytes > 0 && !m.encrypted
	etag := m.index[key].ETag
	m.mu.RUnlock()

	if !partial && headBytes == 0 {
		if b, present := m.content.read(etag); present {
			return b, nil
		}
	}

	byteRange := ""
	if partial {
		byteRange = fmt.Sprintf("bytes=0-%d", headBytes-1)
	}
	obj, err := m.s3.Get(ctx, m.bucket, key, byteRange)
	if err != nil {
		return nil, err
	}
	if !IsEnvelope(obj.Meta) {
		if headBytes == 0 {
			m.content.put(etag, obj.Body)
		}
		return obj.Body, nil
	}
	// First encrypted message: no fragments from here on, and fetch this one
	// again in full.
	m.mu.Lock()
	firstTime := !m.encrypted
	m.encrypted = true
	m.mu.Unlock()
	if partial || firstTime {
		if obj, err = m.s3.Get(ctx, m.bucket, key, ""); err != nil {
			return nil, err
		}
	}
	plain, err := Decrypt(obj.Body, obj.Meta, m.kms)
	if err == nil && headBytes == 0 {
		// Cache it decrypted: that saves the KMS call on the next open, not only the
		// S3 GET. In exchange the cache lies in plain text on the disk - exactly like
		// the index, which holds sender and preview text there anyway.
		m.content.put(etag, plain)
	}
	return plain, err
}

// ClearCache throws the cached contents away.
func (m *Mailbox) ClearCache() { m.content.Clear() }

// Encrypted sagt, ob im Postfach client-seitig verschluesselte Objekte liegen.
func (m *Mailbox) Encrypted() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.encrypted
}

type RefreshResult struct {
	Checked int `json:"checked"`
	New     int `json:"new"`
	Removed int `json:"removed"`
	// Changes counts the op objects other machines wrote since the last look -
	// flags and tags included, which change no key and still change the page.
	Changes int `json:"changes"`
	// Full says whether this was the full listing.
	Full bool `json:"full"`
}

// Changed says whether the page has anything new to show.
func (r RefreshResult) Changed() bool {
	return r.New > 0 || r.Removed > 0 || r.Changes > 0 || r.Full
}

// DefaultReconcile is how often Sync falls back to the full listing. The
// listing is what the S3 bill is made of - about 400 bytes per object, so 90 MB
// for a mailbox of 227,000 - and between two of them the journal and the queue
// carry everything an up-to-date s3mail does. What only the full listing sees
// is what bypasses both: an older client, a phone, a change in the console.
const DefaultReconcile = 2 * time.Hour

// Refresh is the full reconcile: it lists the whole mailbox, fetches the
// beginning of every new or changed message and builds the index from it.
// Comparison runs over ETags, so only what really changed is fetched - but the
// listing itself is one request per thousand objects, every time. Sync is what
// runs often; this runs on start, on the reader's explicit request and every
// ReconcileEvery.
func (m *Mailbox) Refresh(ctx context.Context) (RefreshResult, error) {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	return m.refresh(ctx)
}

func (m *Mailbox) refresh(ctx context.Context) (RefreshResult, error) {
	m.State.Load(ctx)
	// Taken before the listing, so that whatever the journal says from here on
	// is newer than the listing or repeats it - applying it again is harmless.
	_, foreign, _ := m.State.Journal()
	m.State.SeenAll()

	objs, err := m.s3.List(ctx, m.bucket, m.Root)
	if err != nil {
		return RefreshResult{}, err
	}
	listed := make(map[string]ObjectInfo, len(objs))
	for _, o := range objs {
		if strings.HasSuffix(o.Key, "/") || o.Size == 0 || m.Internal(o.Key) {
			continue // snapshot, ops and folder markers are not mail
		}
		listed[o.Key] = o
	}

	m.mu.Lock()
	var removed int
	for key := range m.index {
		if _, present := listed[key]; !present {
			delete(m.index, key)
			removed++
		}
	}
	var todo []ObjectInfo
	for key, o := range listed {
		if old, present := m.index[key]; !present || old.ETag != o.ETag {
			todo = append(todo, o)
		}
	}
	m.mu.Unlock()
	slices.SortFunc(todo, func(a, b ObjectInfo) int { return cmp.Compare(a.Key, b.Key) })

	sem := make(chan struct{}, m.Workers)
	var wg sync.WaitGroup
	fresh := make([]core.Message, len(todo))
	for i, o := range todo {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			fresh[i] = m.summarize(ctx, o)
		})
	}
	wg.Wait()

	m.mu.Lock()
	for _, msg := range fresh {
		m.index[msg.Key] = msg
	}
	m.reconciled = time.Now()
	m.mu.Unlock()
	m.writeCache()

	// No UID numbering here on purpose - see EnsureUIDs in uids.go. Numbers cost
	// about fifteen bytes per message in the state document that every machine
	// downloads, and a mailbox whose owner never touches IMAP should not pay it.
	return RefreshResult{Checked: len(listed), New: len(todo), Removed: removed,
		Changes: foreign, Full: true}, nil
}

// SyncOpts says what Sync is to look at besides the journal.
type SyncOpts struct {
	// Keys are objects somebody named - the queue's notification carries the
	// key SES wrote. Each is looked at with a HEAD, not found by a listing.
	Keys []string
	// Inbox lists the top level of the mailbox, where SES writes, with "/" as
	// the delimiter: the folders below are not paged through. For a mailbox
	// without a queue that is the only way to hear of new mail.
	Inbox bool
	// Announce writes the keys that turned out new into the journal, so the
	// other machines on the mailbox learn of them. SQS hands each notification
	// to one receiver only; without this, a second machine on the same queue
	// would never hear of the mail the first one took.
	Announce bool
}

// ReconcileDue says whether the next Sync will be the full listing.
func (m *Mailbox) ReconcileDue() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.reconciled.IsZero() || (m.ReconcileEvery > 0 && time.Since(m.reconciled) >= m.ReconcileEvery)
}

// Sync is the refresh that runs often, and it does not list the mailbox. What
// it reads instead:
//
//   - the op journal under .s3mail-state/, which it reads anyway for the
//     state: every move, delete, sent copy and draft by an s3mail writes the
//     keys it touched there (core.KeyChange), so another machine's changes
//     arrive without anybody listing for them;
//   - the keys in o.Keys, each with one HEAD - that is how mail announced by
//     the queue comes in;
//   - with o.Inbox, the top level of the mailbox and nothing below it.
//
// It falls back to the full listing when that is due (ReconcileDue - never
// done, or older than ReconcileEvery) and when the journal has a gap: ops were
// compacted away before this process read them.
func (m *Mailbox) Sync(ctx context.Context, o SyncOpts) (RefreshResult, error) {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	if m.ReconcileDue() {
		return m.refreshAndAnnounce(ctx, o)
	}
	m.State.Load(ctx)
	changes, foreign, gap := m.State.Journal()
	if gap {
		return m.refreshAndAnnounce(ctx, o)
	}
	res := RefreshResult{Changes: foreign}

	// Moves carry their index entry along: the content is the one already
	// summarised, and a copy keeps it. What arrived without an entry to carry
	// is looked at like a named key.
	var look []string
	m.mu.Lock()
	for _, c := range changes {
		switch {
		case c.From != "" && c.To != "":
			entry, known := m.index[c.From]
			delete(m.index, c.From)
			if known && m.Own(c.To) == nil {
				entry.Key, entry.Mid, entry.Folder = c.To, m.Mid(c.To), m.FolderOf(c.To)
				m.index[c.To] = entry
			} else {
				look = append(look, c.To)
			}
		case c.To != "":
			look = append(look, c.To)
		case c.From != "":
			if _, known := m.index[c.From]; known {
				delete(m.index, c.From)
				res.Removed++
			}
		}
	}
	m.mu.Unlock()

	var arrived []string
	for _, key := range slices.Concat(look, o.Keys) {
		isNew, gone, err := m.lookAt(ctx, key)
		if err != nil {
			continue // the next Sync or the reconcile catches it
		}
		res.Checked++
		if gone {
			res.Removed++
		}
		if isNew {
			res.New++
			if slices.Contains(o.Keys, key) {
				arrived = append(arrived, key)
			}
		}
	}

	if o.Inbox {
		n, fresh, removed, err := m.syncInbox(ctx)
		if err != nil {
			return res, err
		}
		res.Checked += n
		res.New += fresh
		res.Removed += removed
	}

	if o.Announce {
		m.announce(ctx, arrived)
	}
	m.writeCache()
	return res, nil
}

// refreshAndAnnounce is the full listing in Sync's place. The keys the queue
// named are announced all the same - the listing found them here, but the other
// machines are still waiting to hear.
func (m *Mailbox) refreshAndAnnounce(ctx context.Context, o SyncOpts) (RefreshResult, error) {
	res, err := m.refresh(ctx)
	if err == nil && o.Announce {
		var here []string
		m.mu.RLock()
		for _, k := range o.Keys {
			if _, ok := m.index[k]; ok {
				here = append(here, k)
			}
		}
		m.mu.RUnlock()
		m.announce(ctx, here)
	}
	return res, err
}

// announce writes arrived keys into the journal. A failed write is no reason to
// fail the sync: the mail is in this index, and the others find it at their
// next reconcile.
func (m *Mailbox) announce(ctx context.Context, keys []string) {
	if len(keys) == 0 {
		return
	}
	kc := make([]core.KeyChange, 0, len(keys))
	for _, k := range keys {
		kc = append(kc, core.KeyChange{To: k})
	}
	_ = m.State.Mutate(ctx, core.Op{T: "keys", Keys: kc})
}

// lookAt brings one key into the index with a HEAD, and the header with a
// range GET when the ETag is new. A key that is not there leaves the index.
func (m *Mailbox) lookAt(ctx context.Context, key string) (isNew, gone bool, err error) {
	if m.Own(key) != nil {
		return false, false, core.ErrBadInput // not ours, whatever a notification says
	}
	head, err := m.s3.Head(ctx, m.bucket, key)
	if errors.Is(err, ErrNotFound) {
		m.mu.Lock()
		_, known := m.index[key]
		delete(m.index, key)
		m.mu.Unlock()
		return false, known, nil
	}
	if err != nil {
		return false, false, err
	}
	if head.ContentLength == 0 {
		return false, false, nil // a folder marker, not mail
	}
	m.mu.RLock()
	old, known := m.index[key]
	m.mu.RUnlock()
	if known && old.ETag == head.ETag {
		return false, false, nil
	}
	msg := m.summarize(ctx, ObjectInfo{Key: key, ETag: head.ETag, Size: head.ContentLength,
		LastModified: head.LastModified})
	m.mu.Lock()
	m.index[key] = msg
	m.mu.Unlock()
	return true, false, nil
}

// syncInbox is Refresh for the top level only: list it with the delimiter,
// fetch what is new or changed, drop inbox entries that are no longer there.
func (m *Mailbox) syncInbox(ctx context.Context) (checked, fresh, removed int, err error) {
	objs, err := m.s3.ListDir(ctx, m.bucket, m.Root)
	if err != nil {
		return 0, 0, 0, err
	}
	listed := map[string]ObjectInfo{}
	for _, o := range objs {
		if strings.HasSuffix(o.Key, "/") || o.Size == 0 || m.Internal(o.Key) {
			continue
		}
		listed[o.Key] = o
	}
	var todo []ObjectInfo
	m.mu.Lock()
	for key := range m.index {
		if m.FolderOf(key) != core.Inbox {
			continue
		}
		if _, present := listed[key]; !present {
			delete(m.index, key)
			removed++
		}
	}
	for key, o := range listed {
		if old, present := m.index[key]; !present || old.ETag != o.ETag {
			todo = append(todo, o)
		}
	}
	m.mu.Unlock()
	for _, o := range todo {
		msg := m.summarize(ctx, o)
		m.mu.Lock()
		m.index[msg.Key] = msg
		m.mu.Unlock()
	}
	return len(listed), len(todo), removed, nil
}

// summarize builds the index entry. A broken message does not topple the run -
// it lands in the index as a placeholder, so it stays visible and movable.
func (m *Mailbox) summarize(ctx context.Context, o ObjectInfo) core.Message {
	base := core.Message{
		Key: o.Key, Mid: m.Mid(o.Key), Folder: m.FolderOf(o.Key),
		ETag: o.ETag, Size: o.Size,
		Date: o.LastModified.UTC().Format(time.RFC3339),
	}
	raw, err := m.Fetch(ctx, o.Key, HeaderChunk)
	if err != nil {
		base.Subject = mimeparse.SubjectUnreadable
		base.Snippet = err.Error()
		return base
	}
	return m.fill(base, raw, o.LastModified.UTC())
}

// summarizeRaw builds the index entry for a message we hold in hand - our own,
// just written. No ETag: the next refresh brings the one S3 assigned, and until
// then a mismatch only means the message is fetched once more.
func (m *Mailbox) summarizeRaw(key string, raw []byte) core.Message {
	now := time.Now().UTC()
	return m.fill(core.Message{
		Key: key, Mid: m.Mid(key), Folder: m.FolderOf(key),
		Size: int64(len(raw)), Date: now.Format(time.RFC3339),
	}, raw, now)
}

// fill takes over what the header says.
func (m *Mailbox) fill(base core.Message, raw []byte, fallback time.Time) core.Message {
	s := mimeparse.Summarize(raw, fallback)
	base.Date = s.Date
	base.From, base.To, base.Cc = s.From, s.To, s.Cc
	base.FromAddr, base.ToAddrs = s.FromAddr, s.ToAddrs
	base.MessageID = s.MessageID
	base.Subject = s.Subject
	if base.Subject == "" {
		base.Subject = mimeparse.SubjectNone
	}
	base.Snippet = s.Preview
	base.HasAttachment = len(s.Attachments) > 0
	base.Spam = s.Spam == "FAIL"
	base.Virus = s.Virus == "FAIL"
	base.AuthFailed = s.Auth.Failed()
	return base
}

// -- move / delete ---------------------------------------------------------- //

type MoveResult struct {
	Key     string `json:"key"`
	NewKey  string `json:"new_key"`
	Folder  string `json:"folder"`
	Skipped bool   `json:"skipped,omitzero"`
	// Err is set when this one message could not be moved. The others were
	// still moved: a bulk action that reports one failure as total failure
	// leaves somebody guessing which half happened.
	Err string `json:"error,omitempty"`
}

// Move copies and deletes - S3 knows no rename. Encryption and storage class of
// the original come along, or the copy would land under the bucket's default
// key.
func (m *Mailbox) Move(ctx context.Context, keys []string, folder string) ([]MoveResult, error) {
	target, err := core.ValidFolder(folder)
	if err != nil {
		return nil, err
	}
	var out []MoveResult
	failedInARow := 0
	// Which numbers to give up in which folder. A moved message keeps no UID
	// where it no longer is - see uids.go.
	retire := map[string][]string{}
	err = m.State.Batch(ctx, func() error {
		for _, key := range keys {
			if err := m.Own(key); err != nil {
				return err
			}
			m.mu.RLock()
			entry, known := m.index[key]
			m.mu.RUnlock()
			if !known {
				continue
			}
			if m.FolderOf(key) == target {
				out = append(out, MoveResult{Key: key, NewKey: key, Skipped: true})
				continue
			}
			mid := m.Mid(key)
			newKey, newMid := m.freeKey(mid, target)

			// One message that will not move must not take the rest with it. A
			// bulk move over a search result is the normal case, and reporting
			// the first failure as total failure leaves somebody guessing which
			// half happened - while the mailbox already shows both.
			//
			// The guard against the other extreme is failedInARow below: if the
			// access is gone, every remaining message fails the same way, and a
			// list of five thousand identical sentences helps nobody. store does
			// not get to ask awsx what kind of error this is - that would turn
			// the layering upside down - so it counts instead.
			if err := m.s3.Copy(ctx, m.bucket, key, newKey, m.copyOpts(ctx, key)); err != nil {
				out = append(out, MoveResult{Key: key, Err: err.Error()})
				failedInARow++
				if failedInARow >= movesGiveUpAfter {
					return err
				}
				continue
			}
			if err := m.s3.Delete(ctx, m.bucket, key); err != nil {
				// The copy is already there. Saying "not moved" now would be the
				// worse lie: the message exists twice, and the new one is the one
				// the reader will find.
				out = append(out, MoveResult{Key: key, NewKey: newKey, Folder: target,
					Err: err.Error()})
				failedInARow++
				if failedInARow >= movesGiveUpAfter {
					return err
				}
				continue
			}
			failedInARow = 0
			// The number in the source folder goes before the index does - the
			// folder has to be read off the old key. See uids.go.
			retire[m.FolderOf(key)] = append(retire[m.FolderOf(key)], mid)
			m.mu.Lock()
			delete(m.index, key)
			entry.Key, entry.Mid, entry.Folder = newKey, newMid, target
			m.index[newKey] = entry
			m.mu.Unlock()

			if newMid != mid { // Zustand mitziehen
				if err := m.State.Mutate(ctx, core.Op{T: "rekey", Old: mid, New: newMid}); err != nil {
					return err
				}
			}
			// Into the journal, so the other machines move their entry instead
			// of listing the mailbox to find out. Inside the batch: one op
			// object for the whole move, however many messages.
			if err := m.State.Mutate(ctx, core.Op{T: "keys",
				Keys: []core.KeyChange{{From: key, To: newKey}}}); err != nil {
				return err
			}
			out = append(out, MoveResult{Key: key, NewKey: newKey, Folder: target})
		}
		return nil
	})
	m.writeCache()
	for folder, mids := range retire {
		_ = m.retireUIDs(ctx, folder, mids)
	}
	return out, err
}

// movesGiveUpAfter is how many messages in a row may fail before Move stops.
// Below it the failures are per message and the rest still moves; at it,
// something systemic is wrong - the access, the bucket, the network - and
// carrying on would only lengthen the list.
const movesGiveUpAfter = 10

// freeKey defuses a name collision in the target folder.
func (m *Mailbox) freeKey(mid, target string) (string, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	candidate, _ := m.KeyFor(mid, target)
	for n := 1; ; n++ {
		if _, taken := m.index[candidate]; !taken {
			return candidate, m.Mid(candidate)
		}
		stem, dot, ext := mid, "", ""
		if before, after, ok := strings.Cut(mid, "."); ok {
			stem, dot, ext = before, ".", after
		}
		candidate, _ = m.KeyFor(fmt.Sprintf("%s-%d%s%s", stem, n, dot, ext), target)
	}
}

// copyOpts reads the encryption setting of the original.
func (m *Mailbox) copyOpts(ctx context.Context, key string) CopyOpts {
	head, err := m.s3.Head(ctx, m.bucket, key)
	if err != nil {
		return CopyOpts{}
	}
	o := CopyOpts{
		ServerSideEncryption: head.ServerSideEncryption,
		SSEKMSKeyID:          head.SSEKMSKeyID,
		BucketKeyEnabled:     head.BucketKeyEnabled,
	}
	if head.StorageClass != "" && head.StorageClass != "STANDARD" {
		o.StorageClass = head.StorageClass
	}
	return o
}

// Delete deletes for good - only from the trash and only when allowed. The check
// sits here and not in the interface.
func (m *Mailbox) Delete(ctx context.Context, keys []string, force bool) (int, error) {
	if !m.AllowDelete {
		return 0, ErrDeleteBlocked
	}
	n := 0
	err := m.State.Batch(ctx, func() error {
		for _, key := range keys {
			if err := m.Own(key); err != nil {
				return err
			}
			if !force && m.FolderOf(key) != core.Trash {
				return ErrTrashOnly
			}
			if err := m.s3.Delete(ctx, m.bucket, key); err != nil {
				return err
			}
			m.mu.Lock()
			delete(m.index, key)
			m.mu.Unlock()
			if err := m.State.Mutate(ctx, core.Op{T: "drop", Mids: []string{m.Mid(key)}},
				core.Op{T: "keys", Keys: []core.KeyChange{{From: key}}}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	m.writeCache()
	return n, err
}

// EmptyTrash clears the trash.
func (m *Mailbox) EmptyTrash(ctx context.Context) (int, error) {
	var keys []string
	for _, msg := range m.Index() {
		if msg.Folder == core.Trash {
			keys = append(keys, msg.Key)
		}
	}
	return m.Delete(ctx, keys, false)
}

// -- own mail ---------------------------------------------------------------- //

// Put places a message we wrote ourselves into a folder of the mailbox and
// takes it straight into the index - the sent copy and the draft both go this
// way.
//
// The name comes from the Message-Id, so writing the same message twice cannot
// leave two objects behind. Own mail counts as read: nobody needs to be told
// about a message they just wrote.
func (m *Mailbox) Put(ctx context.Context, folder, messageID string, raw []byte) (string, error) {
	folder, err := core.ValidFolder(folder)
	if err != nil {
		return "", err
	}
	key, err := m.KeyFor(baseFromID(messageID), folder)
	if err != nil {
		return "", err
	}
	if err := m.s3.Put(ctx, m.bucket, key, raw, "message/rfc822"); err != nil {
		return "", err
	}
	read := true
	if err := m.State.Mutate(ctx, core.Op{T: "flags", Mids: []string{m.Mid(key)}, Read: &read},
		core.Op{T: "keys", Keys: []core.KeyChange{{To: key}}}); err != nil {
		return key, err
	}
	// Into the index right away: without this the message would be missing until
	// the next refresh, and the folder it went into would look empty.
	m.mu.Lock()
	m.index[key] = m.summarizeRaw(key, raw)
	m.mu.Unlock()
	m.writeCache()
	return key, nil
}

// DropDraft removes a stored draft.
//
// It goes past --no-delete on purpose. That switch protects received mail from
// a wrong click; a draft is our own scratch paper, and if it could not be
// removed, every sent message would leave its draft standing next to the copy
// in the sent folder. The folder check stays: nothing outside the drafts folder
// can be deleted this way.
func (m *Mailbox) DropDraft(ctx context.Context, key string) error {
	if err := m.Own(key); err != nil {
		return err
	}
	if m.FolderOf(key) != core.Drafts {
		return core.ErrBadInput
	}
	if err := m.s3.Delete(ctx, m.bucket, key); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.index, key)
	m.mu.Unlock()
	m.writeCache()
	return m.State.Mutate(ctx, core.Op{T: "drop", Mids: []string{m.Mid(key)}},
		core.Op{T: "keys", Keys: []core.KeyChange{{From: key}}})
}

// baseFromID turns "<a1b2@example.org>" into the base name "a1b2.eml". The
// domain drops out: it says nothing here and would only bring characters into
// the key that mean something to S3.
func baseFromID(messageID string) string {
	id := strings.Trim(strings.TrimSpace(messageID), "<>")
	if i := strings.Index(id, "@"); i > 0 {
		id = id[:i]
	}
	id = regexp.MustCompile(`[^A-Za-z0-9_.-]`).ReplaceAllString(id, "")
	if id == "" {
		id = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return id + ".eml"
}

// ApplyRules runs the rules and carries out what PlanRules decides.
func (m *Mailbox) ApplyRules(ctx context.Context, pool []core.Message, force bool) (int, error) {
	if pool == nil {
		pool = m.Index()
	}
	plan := core.PlanRules(pool, m.State.Data(), force)
	if len(plan) == 0 {
		return 0, nil
	}
	moved := 0
	err := m.State.Batch(ctx, func() error {
		for _, a := range plan {
			if a.MarkRuled {
				if err := m.State.Mutate(ctx, core.Op{T: "ruled", Mids: []string{a.Mid}}); err != nil {
					return err
				}
			}
			if len(a.AddTags) > 0 {
				if err := m.State.Mutate(ctx, core.Op{T: "tags", Mids: []string{a.Mid}, Add: a.AddTags}); err != nil {
					return err
				}
			}
			if a.SetRead != nil || a.SetStar != nil {
				if err := m.State.Mutate(ctx, core.Op{T: "flags", Mids: []string{a.Mid},
					Read: a.SetRead, Star: a.SetStar}); err != nil {
					return err
				}
			}
			if a.MoveTo != nil {
				if _, err := m.Move(ctx, []string{a.Key}, *a.MoveTo); err != nil {
					return err
				}
				moved++
			}
		}
		return nil
	})
	return moved, err
}

// indexVersion is the shape of the cached index. Raising it throws the old file
// away and forces one full pass over the headers.
//
// It has to be raised whenever a field is added, and the reason is subtle:
// Refresh only fetches what changed its ETag, so an entry written before the
// field existed would keep its empty value forever. The version was written
// from the start and not read - which made it a comment rather than a check.
const indexVersion = 5

// cacheFile is the shape on disk. Reconciled and the cursor are what lets a
// restart carry on with Sync instead of listing the whole mailbox again; a file
// without them (written before they existed) simply means one full listing.
type cacheFile struct {
	Version    int                     `json:"version"`
	Messages   map[string]core.Message `json:"messages"`
	Reconciled time.Time               `json:"reconciled,omitzero"`
	Cursor     *cachedCursor           `json:"cursor,omitempty"`
}

type cachedCursor struct {
	MaxSeen string   `json:"max_seen"`
	Seen    []string `json:"seen"`
}

func (m *Mailbox) readCache() *cachedCursor {
	raw, err := os.ReadFile(m.CacheFile)
	if err != nil {
		return nil
	}
	blob, err := open(m.cacheKey, raw)
	if err != nil {
		// A key that no longer fits means a new key, or a file from another
		// machine. The cache is a cache: it gets rebuilt.
		return nil
	}
	var d cacheFile
	if json.Unmarshal(blob, &d) == nil && d.Messages != nil && d.Version == indexVersion {
		m.index = d.Messages
		if d.Cursor != nil {
			// Without the cursor the journal cannot say what was missed, and
			// the reconcile time would promise what it cannot keep.
			m.reconciled = d.Reconciled
		}
		return d.Cursor
	}
	return nil
}

func (m *Mailbox) writeCache() {
	if m.CacheFile == "" {
		return
	}
	maxSeen, seen := m.State.Cursor()
	m.mu.RLock()
	blob, err := json.Marshal(cacheFile{Version: indexVersion, Messages: m.index,
		Reconciled: m.reconciled, Cursor: &cachedCursor{MaxSeen: maxSeen, Seen: seen}})
	m.mu.RUnlock()
	if err != nil {
		return
	}
	blob, err = seal(m.cacheKey, blob)
	if err != nil {
		return
	}
	tmp := m.CacheFile + ".tmp"
	if os.WriteFile(tmp, blob, 0o600) == nil {
		_ = os.Rename(tmp, m.CacheFile)
	}
}
