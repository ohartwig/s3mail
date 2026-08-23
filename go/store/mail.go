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
	"strings"
	"sync"
	"time"

	"s3mail/core"
	"s3mail/mimeparse"
)

// HeaderChunk ist, wieviel pro Mail fuer den Index geholt wird - reicht fuer
// Header und Vorschautext.
const HeaderChunk = 65536

var (
	// ErrTrashOnly: endgueltig loeschen geht nur von dort aus.
	ErrTrashOnly = errors.New("endgueltig loeschen geht nur aus dem Papierkorb")
	// ErrDeleteBlocked: mit --no-delete gar nicht.
	ErrDeleteBlocked = errors.New("endgueltiges loeschen ist deaktiviert")
)

// Mailbox ist der Index ueber den Bucket samt Zustand.
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
	// encrypted merkt sich, ob im Postfach client-seitig verschluesselte
	// Objekte liegen. Sobald ja, faellt der Range-GET weg - ein halbes Chiffrat
	// laesst sich nicht entschluesseln.
	encrypted bool

	State   *State
	content *bodyCache
}

func NewMailbox(ctx context.Context, s3 S3, kms KMS, bucket, root, cacheDir string,
	allowDelete bool) *Mailbox {
	st := core.NewStore(root)
	slug := regexp.MustCompile(`[^A-Za-z0-9_.-]`).ReplaceAllString(bucket+"__"+st.Root, "_")
	if slug == "" {
		slug = "default"
	}
	m := &Mailbox{
		s3: s3, kms: kms, bucket: bucket, Store: st,
		AllowDelete: allowDelete, Workers: 8,
		index: map[string]core.Message{},
	}
	if cacheDir != "" {
		_ = os.MkdirAll(cacheDir, 0o700)
		m.CacheFile = filepath.Join(cacheDir, slug+".json")
		m.readCache()
		m.content = newBodyCache(filepath.Join(cacheDir, slug+".bodies"))
	}
	local := ""
	if cacheDir != "" {
		local = filepath.Join(cacheDir, slug+".state.json")
	}
	m.State = NewState(ctx, s3, bucket, st.Root, local)
	return m
}

// Index gibt die Eintraege als Liste heraus, nach Key sortiert.
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

// -- Holen ------------------------------------------------------------------ //

// Fetch holt ein Objekt und macht es bei Bedarf auf. headBytes > 0 holt nur den
// Anfang - ausser das Postfach ist client-seitig verschluesselt, dann immer ganz.
//
// Ganze Mails kommen aus dem Zwischenspeicher, wenn sie dort liegen. Nur ganze:
// ein Teilstueck zu speichern hiesse, beim naechsten Oeffnen den Rest zu
// vermissen, ohne es zu merken.
func (m *Mailbox) Fetch(ctx context.Context, key string, headBytes int) ([]byte, error) {
	m.mu.RLock()
	partial := headBytes > 0 && !m.encrypted
	etag := m.index[key].ETag
	m.mu.RUnlock()

	if !partial && headBytes == 0 {
		if b, da := m.content.read(etag); da {
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
	// Erste verschluesselte Mail: ab jetzt keine Teilstuecke mehr, und dieses
	// hier noch einmal ganz holen.
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
		// Entschluesselt zwischenspeichern: das spart beim naechsten Oeffnen den
		// KMS-Aufruf mit, nicht nur den S3-GET. Der Zwischenspeicher liegt dafuer
		// im Klartext auf der Platte - genau wie der Index, der Absender und
		// Vorschautext ohnehin schon dort haelt.
		m.content.put(etag, plain)
	}
	return plain, err
}

// ClearCache wirft die zwischengespeicherten Inhalte weg.
func (m *Mailbox) ClearCache() { m.content.Clear() }

// Encrypted sagt, ob im Postfach client-seitig verschluesselte Objekte liegen.
func (m *Mailbox) Encrypted() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.encrypted
}

// -- Indexieren ------------------------------------------------------------- //

type RefreshResult struct {
	Checked int `json:"checked"`
	New     int `json:"new"`
	Removed int `json:"removed"`
}

// Refresh listet den Bucket, holt zu jeder neuen oder geaenderten Mail den Anfang
// und baut daraus den Index. Abgeglichen wird ueber ETags, es wird also nur
// geholt, was sich wirklich geaendert hat.
func (m *Mailbox) Refresh(ctx context.Context) (RefreshResult, error) {
	m.State.Load(ctx)

	objs, err := m.s3.List(ctx, m.bucket, m.Root)
	if err != nil {
		return RefreshResult{}, err
	}
	listed := make(map[string]ObjectInfo, len(objs))
	for _, o := range objs {
		if strings.HasSuffix(o.Key, "/") || o.Size == 0 || m.Internal(o.Key) {
			continue // Snapshot, Ops und Ordnermarkierungen sind keine Mail
		}
		listed[o.Key] = o
	}

	m.mu.Lock()
	var removed int
	for key := range m.index {
		if _, da := listed[key]; !da {
			delete(m.index, key)
			removed++
		}
	}
	var todo []ObjectInfo
	for key, o := range listed {
		if old, da := m.index[key]; !da || old.ETag != o.ETag {
			todo = append(todo, o)
		}
	}
	m.mu.Unlock()
	sort.Slice(todo, func(i, j int) bool { return todo[i].Key < todo[j].Key })

	sem := make(chan struct{}, m.Workers)
	var wg sync.WaitGroup
	fresh := make([]core.Message, len(todo))
	for i, o := range todo {
		wg.Add(1)
		go func(i int, o ObjectInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fresh[i] = m.summarize(ctx, o)
		}(i, o)
	}
	wg.Wait()

	m.mu.Lock()
	for _, msg := range fresh {
		m.index[msg.Key] = msg
	}
	m.mu.Unlock()
	m.writeCache()

	return RefreshResult{Checked: len(listed), New: len(todo), Removed: removed}, nil
}

// summarize baut den Indexeintrag. Eine kaputte Mail kippt nicht den Lauf -
// sie landet als Platzhalter im Index, damit sie sichtbar und verschiebbar bleibt.
func (m *Mailbox) summarize(ctx context.Context, o ObjectInfo) core.Message {
	base := core.Message{
		Key: o.Key, Mid: m.Mid(o.Key), Folder: m.FolderOf(o.Key),
		ETag: o.ETag, Size: o.Size,
		Date: o.LastModified.UTC().Format(time.RFC3339),
	}
	raw, err := m.Fetch(ctx, o.Key, HeaderChunk)
	if err != nil {
		base.Subject = "(nicht lesbar)"
		base.Snippet = err.Error()
		return base
	}
	s := mimeparse.Summarize(raw, o.LastModified.UTC())
	base.Date = s.Date
	base.From, base.To, base.Cc = s.From, s.To, s.Cc
	base.Subject = s.Subject
	if base.Subject == "" {
		base.Subject = "(kein Betreff)"
	}
	base.Snippet = s.Preview
	base.HasAttachment = len(s.Attachments) > 0
	base.Spam = s.Spam == "FAIL"
	return base
}

// -- Verschieben / Loeschen ------------------------------------------------- //

type MoveResult struct {
	Key     string `json:"key"`
	NewKey  string `json:"new_key"`
	Folder  string `json:"folder"`
	Skipped bool   `json:"skipped,omitempty"`
}

// Move kopiert und loescht - S3 kennt kein Umbenennen. Verschluesselung und
// Speicherklasse des Originals werden dabei mitgenommen, sonst landete die Kopie
// unter dem Standardschluessel des Buckets.
func (m *Mailbox) Move(ctx context.Context, keys []string, folder string) ([]MoveResult, error) {
	target, err := core.ValidFolder(folder)
	if err != nil {
		return nil, err
	}
	var out []MoveResult
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

			if err := m.s3.Copy(ctx, m.bucket, key, newKey, m.copyOpts(ctx, key)); err != nil {
				return err
			}
			if err := m.s3.Delete(ctx, m.bucket, key); err != nil {
				return err
			}
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
	return out, err
}

// freeKey entschaerft eine Namenskollision im Zielordner.
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

// copyOpts liest die Verschluesselungseinstellung des Originals.
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

// Delete loescht endgueltig - nur aus dem Papierkorb und nur, wenn erlaubt.
// Die Pruefung sitzt hier und nicht in der Oberflaeche.
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

// EmptyTrash raeumt den Papierkorb.
func (m *Mailbox) EmptyTrash(ctx context.Context) (int, error) {
	var keys []string
	for _, msg := range m.Index() {
		if msg.Folder == core.Trash {
			keys = append(keys, msg.Key)
		}
	}
	return m.Delete(ctx, keys, false)
}

// -- Regeln ----------------------------------------------------------------- //

// ApplyRules laesst die Regeln laufen und fuehrt aus, was PlanRules entscheidet.
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

// -- Zwischenspeicher ------------------------------------------------------- //

func (m *Mailbox) readCache() {
	blob, err := os.ReadFile(m.CacheFile)
	if err != nil {
		return
	}
	var d struct {
		Version  int                     `json:"version"`
		Messages map[string]core.Message `json:"messages"`
	}
	if json.Unmarshal(blob, &d) == nil && d.Messages != nil {
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
	}{2, m.index})
	m.mu.RUnlock()
	if err != nil {
		return
	}
	tmp := m.CacheFile + ".tmp"
	if os.WriteFile(tmp, blob, 0o600) == nil {
		_ = os.Rename(tmp, m.CacheFile)
	}
}
