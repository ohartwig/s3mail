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
	// ErrNurAusPapierkorb: endgueltig loeschen geht nur von dort aus.
	ErrNurAusPapierkorb = errors.New("endgueltig loeschen geht nur aus dem Papierkorb")
	// ErrLoeschenGesperrt: mit --no-delete gar nicht.
	ErrLoeschenGesperrt = errors.New("endgueltiges loeschen ist deaktiviert")
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
	// verschluesselt merkt sich, ob im Postfach client-seitig verschluesselte
	// Objekte liegen. Sobald ja, faellt der Range-GET weg - ein halbes Chiffrat
	// laesst sich nicht entschluesseln.
	verschluesselt bool

	State  *State
	inhalt *bodyCache
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
		m.cacheLesen()
		m.inhalt = neuerBodyCache(filepath.Join(cacheDir, slug+".bodies"))
	}
	lokal := ""
	if cacheDir != "" {
		lokal = filepath.Join(cacheDir, slug+".state.json")
	}
	m.State = NewState(ctx, s3, bucket, st.Root, lokal)
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

func (m *Mailbox) Suche(query string, o core.SearchOpts) []core.Message {
	return core.Search(m.Index(), m.State.Data(), query, o)
}

func (m *Mailbox) Ordner() []core.FolderInfo {
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
	teilweise := headBytes > 0 && !m.verschluesselt
	etag := m.index[key].ETag
	m.mu.RUnlock()

	if !teilweise && headBytes == 0 {
		if b, da := m.inhalt.lesen(etag); da {
			return b, nil
		}
	}

	byteRange := ""
	if teilweise {
		byteRange = fmt.Sprintf("bytes=0-%d", headBytes-1)
	}
	obj, err := m.s3.Get(ctx, m.bucket, key, byteRange)
	if err != nil {
		return nil, err
	}
	if !IstUmschlag(obj.Meta) {
		if headBytes == 0 {
			m.inhalt.schreiben(etag, obj.Body)
		}
		return obj.Body, nil
	}
	// Erste verschluesselte Mail: ab jetzt keine Teilstuecke mehr, und dieses
	// hier noch einmal ganz holen.
	m.mu.Lock()
	erstmalig := !m.verschluesselt
	m.verschluesselt = true
	m.mu.Unlock()
	if teilweise || erstmalig {
		if obj, err = m.s3.Get(ctx, m.bucket, key, ""); err != nil {
			return nil, err
		}
	}
	klar, err := Entschluesseln(obj.Body, obj.Meta, m.kms)
	if err == nil && headBytes == 0 {
		// Entschluesselt zwischenspeichern: das spart beim naechsten Oeffnen den
		// KMS-Aufruf mit, nicht nur den S3-GET. Der Zwischenspeicher liegt dafuer
		// im Klartext auf der Platte - genau wie der Index, der Absender und
		// Vorschautext ohnehin schon dort haelt.
		m.inhalt.schreiben(etag, klar)
	}
	return klar, err
}

// CacheLeeren wirft die zwischengespeicherten Inhalte weg.
func (m *Mailbox) CacheLeeren() { m.inhalt.Leeren() }

// Verschluesselt sagt, ob im Postfach client-seitig verschluesselte Objekte liegen.
func (m *Mailbox) Verschluesselt() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.verschluesselt
}

// -- Indexieren ------------------------------------------------------------- //

type RefreshErgebnis struct {
	Geprueft int `json:"geprueft"`
	Neu      int `json:"neu"`
	Entfernt int `json:"entfernt"`
}

// Refresh listet den Bucket, holt zu jeder neuen oder geaenderten Mail den Anfang
// und baut daraus den Index. Abgeglichen wird ueber ETags, es wird also nur
// geholt, was sich wirklich geaendert hat.
func (m *Mailbox) Refresh(ctx context.Context) (RefreshErgebnis, error) {
	m.State.Load(ctx)

	objs, err := m.s3.List(ctx, m.bucket, m.Root)
	if err != nil {
		return RefreshErgebnis{}, err
	}
	gelistet := make(map[string]ObjectInfo, len(objs))
	for _, o := range objs {
		if strings.HasSuffix(o.Key, "/") || o.Size == 0 || m.Internal(o.Key) {
			continue // Snapshot, Ops und Ordnermarkierungen sind keine Mail
		}
		gelistet[o.Key] = o
	}

	m.mu.Lock()
	var entfernt int
	for key := range m.index {
		if _, da := gelistet[key]; !da {
			delete(m.index, key)
			entfernt++
		}
	}
	var todo []ObjectInfo
	for key, o := range gelistet {
		if alt, da := m.index[key]; !da || alt.ETag != o.ETag {
			todo = append(todo, o)
		}
	}
	m.mu.Unlock()
	sort.Slice(todo, func(i, j int) bool { return todo[i].Key < todo[j].Key })

	sem := make(chan struct{}, m.Workers)
	var wg sync.WaitGroup
	frisch := make([]core.Message, len(todo))
	for i, o := range todo {
		wg.Add(1)
		go func(i int, o ObjectInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			frisch[i] = m.zusammenfassen(ctx, o)
		}(i, o)
	}
	wg.Wait()

	m.mu.Lock()
	for _, msg := range frisch {
		m.index[msg.Key] = msg
	}
	m.mu.Unlock()
	m.cacheSchreiben()

	return RefreshErgebnis{Geprueft: len(gelistet), Neu: len(todo), Entfernt: entfernt}, nil
}

// zusammenfassen baut den Indexeintrag. Eine kaputte Mail kippt nicht den Lauf -
// sie landet als Platzhalter im Index, damit sie sichtbar und verschiebbar bleibt.
func (m *Mailbox) zusammenfassen(ctx context.Context, o ObjectInfo) core.Message {
	basis := core.Message{
		Key: o.Key, Mid: m.Mid(o.Key), Folder: m.FolderOf(o.Key),
		ETag: o.ETag, Size: o.Size,
		Date: o.LastModified.UTC().Format(time.RFC3339),
	}
	roh, err := m.Fetch(ctx, o.Key, HeaderChunk)
	if err != nil {
		basis.Subject = "(nicht lesbar)"
		basis.Snippet = err.Error()
		return basis
	}
	s := mimeparse.Summarize(roh, o.LastModified.UTC())
	basis.Date = s.Date
	basis.From, basis.To, basis.Cc = s.From, s.To, s.Cc
	basis.Subject = s.Subject
	if basis.Subject == "" {
		basis.Subject = "(kein Betreff)"
	}
	basis.Snippet = s.Preview
	basis.HasAttachment = len(s.Attachments) > 0
	basis.Spam = s.Spam == "FAIL"
	return basis
}

// -- Verschieben / Loeschen ------------------------------------------------- //

type MoveErgebnis struct {
	Key     string `json:"key"`
	NewKey  string `json:"new_key"`
	Folder  string `json:"folder"`
	Skipped bool   `json:"skipped,omitempty"`
}

// Move kopiert und loescht - S3 kennt kein Umbenennen. Verschluesselung und
// Speicherklasse des Originals werden dabei mitgenommen, sonst landete die Kopie
// unter dem Standardschluessel des Buckets.
func (m *Mailbox) Move(ctx context.Context, keys []string, folder string) ([]MoveErgebnis, error) {
	ziel, err := core.ValidFolder(folder)
	if err != nil {
		return nil, err
	}
	var out []MoveErgebnis
	err = m.State.Batch(ctx, func() error {
		for _, key := range keys {
			if err := m.Own(key); err != nil {
				return err
			}
			m.mu.RLock()
			eintrag, bekannt := m.index[key]
			m.mu.RUnlock()
			if !bekannt {
				continue
			}
			if m.FolderOf(key) == ziel {
				out = append(out, MoveErgebnis{Key: key, NewKey: key, Skipped: true})
				continue
			}
			mid := m.Mid(key)
			neuerKey, neueMid := m.freierKey(mid, ziel)

			if err := m.s3.Copy(ctx, m.bucket, key, neuerKey, m.kopierOpts(ctx, key)); err != nil {
				return err
			}
			if err := m.s3.Delete(ctx, m.bucket, key); err != nil {
				return err
			}
			m.mu.Lock()
			delete(m.index, key)
			eintrag.Key, eintrag.Mid, eintrag.Folder = neuerKey, neueMid, ziel
			m.index[neuerKey] = eintrag
			m.mu.Unlock()

			if neueMid != mid { // Zustand mitziehen
				if err := m.State.Mutate(ctx, core.Op{T: "rekey", Old: mid, New: neueMid}); err != nil {
					return err
				}
			}
			out = append(out, MoveErgebnis{Key: key, NewKey: neuerKey, Folder: ziel})
		}
		return nil
	})
	m.cacheSchreiben()
	return out, err
}

// freierKey entschaerft eine Namenskollision im Zielordner.
func (m *Mailbox) freierKey(mid, ziel string) (string, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	kandidat, _ := m.KeyFor(mid, ziel)
	for n := 1; ; n++ {
		if _, belegt := m.index[kandidat]; !belegt {
			return kandidat, m.Mid(kandidat)
		}
		stamm, punkt, endung := mid, "", ""
		if i := strings.Index(mid, "."); i >= 0 {
			stamm, punkt, endung = mid[:i], ".", mid[i+1:]
		}
		kandidat, _ = m.KeyFor(fmt.Sprintf("%s-%d%s%s", stamm, n, punkt, endung), ziel)
	}
}

// kopierOpts liest die Verschluesselungseinstellung des Originals.
func (m *Mailbox) kopierOpts(ctx context.Context, key string) CopyOpts {
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
		return 0, ErrLoeschenGesperrt
	}
	n := 0
	err := m.State.Batch(ctx, func() error {
		for _, key := range keys {
			if err := m.Own(key); err != nil {
				return err
			}
			if !force && m.FolderOf(key) != core.Trash {
				return ErrNurAusPapierkorb
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
	m.cacheSchreiben()
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
	verschoben := 0
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
				verschoben++
			}
		}
		return nil
	})
	return verschoben, err
}

// -- Zwischenspeicher ------------------------------------------------------- //

func (m *Mailbox) cacheLesen() {
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

func (m *Mailbox) cacheSchreiben() {
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
