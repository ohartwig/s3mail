// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.ole-hartwig.eu/development/s3mail/s3mail/core"
	"git.ole-hartwig.eu/development/s3mail/s3mail/mimeparse"
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
		index:    map[string]core.Message{},
		cacheKey: cacheKey,
	}
	if cacheDir != "" {
		_ = os.MkdirAll(cacheDir, 0o700)
		m.CacheFile = filepath.Join(cacheDir, slug+".json")
		m.readCache()
		m.content = newBodyCache(filepath.Join(cacheDir, slug+".bodies"), cacheKey)
	}
	local := ""
	if cacheDir != "" {
		local = filepath.Join(cacheDir, slug+".state.json")
	}
	m.State = NewState(ctx, s3, bucket, st.Root, local)
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
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
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
}

// Refresh lists the bucket, fetches the beginning of every new or changed
// message and builds the index from it. Comparison runs over ETags, so only what
// really changed is fetched.
func (m *Mailbox) Refresh(ctx context.Context) (RefreshResult, error) {
	m.State.Load(ctx)

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
	sort.Slice(todo, func(i, j int) bool { return todo[i].Key < todo[j].Key })

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
	m.mu.Unlock()
	m.writeCache()

	// No UID numbering here on purpose - see EnsureUIDs in uids.go. Numbers cost
	// about fifteen bytes per message in the state document that every machine
	// downloads, and a mailbox whose owner never touches IMAP should not pay it.
	return RefreshResult{Checked: len(listed), New: len(todo), Removed: removed}, nil
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
	Skipped bool   `json:"skipped,omitempty"`
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
		if i := strings.Index(mid, "."); i >= 0 {
			stem, dot, ext = mid[:i], ".", mid[i+1:]
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
			if err := m.State.Mutate(ctx, core.Op{T: "drop", Mids: []string{m.Mid(key)}}); err != nil {
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
	if err := m.State.Mutate(ctx, core.Op{T: "flags", Mids: []string{m.Mid(key)}, Read: &read}); err != nil {
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
	return m.State.Mutate(ctx, core.Op{T: "drop", Mids: []string{m.Mid(key)}})
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
const indexVersion = 4

func (m *Mailbox) readCache() {
	raw, err := os.ReadFile(m.CacheFile)
	if err != nil {
		return
	}
	blob, err := open(m.cacheKey, raw)
	if err != nil {
		// A key that no longer fits means a new key, or a file from another
		// machine. The cache is a cache: it gets rebuilt.
		return
	}
	var d struct {
		Version  int                     `json:"version"`
		Messages map[string]core.Message `json:"messages"`
	}
	if json.Unmarshal(blob, &d) == nil && d.Messages != nil && d.Version == indexVersion {
		m.index = d.Messages
	}
}

func (m *Mailbox) writeCache() {
	if m.CacheFile == "" {
		return
	}
	m.mu.RLock()
	blob, err := json.Marshal(struct {
		Version  int                     `json:"version"`
		Messages map[string]core.Message `json:"messages"`
	}{indexVersion, m.index})
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
