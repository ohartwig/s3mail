package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"s3mail/core"
)

const (
	// StateObject ist der Snapshot, StateOps das Prefix mit den einzelnen
	// Aenderungen. Beide beginnen mit einem Punkt und gelten damit als intern.
	StateObject = ".s3mail-state.json"
	StateOps    = ".s3mail-state/"

	// CompactAfter ist die Zahl offener Ops, ab der zusammengefasst wird.
	CompactAfter = 50

	holWorkers = 8
)

// State ist der geteilte Zustand im Bucket.
//
// Geschrieben wird nicht das ganze Dokument, sondern die einzelne Aenderung: jedes
// Save legt ein kleines Objekt unter <root>.s3mail-state/ ab, auf dessen Schluessel
// nur dieser eine Schreibvorgang schreibt. Zwei Rechner koennen sich dabei nicht
// ins Gehege kommen - es gibt keinen gemeinsamen Schluessel, auf den beide zeigen,
// und damit weder Sperre noch If-Match noch 412.
//
// Gelesen wird der Snapshot und darauf alle Ops, die neuer sind als sein
// Wasserstand (Upto), in Schluesselreihenfolge. Ab CompactAfter offenen Ops wird
// zusammengefasst.
//
// Der Wasserstand macht das Aufraeumen unkritisch: bleibt ein Op liegen, weil das
// Loeschen scheitert, wird es beim naechsten Laden uebersprungen statt ein zweites
// Mal angewandt.
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

	// instanz unterscheidet diesen Prozess von jedem anderen, der auf denselben
	// Bucket schreibt. Ohne das koennten zwei Rechner in derselben Mikrosekunde
	// denselben Op-Namen erzeugen - und einer der beiden waere weg.
	instanz string

	// austauschbar, damit Tests deterministisch laufen
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
		instanz:   InstanzKennung(),
		Now:       func() time.Time { return time.Now().UTC() },
		Workers:   holWorkers,
	}
	st.Load(ctx)
	return st
}

// InstanzKennung ist pro State einmalig. Sechs Zufallsbytes aus crypto/rand -
// damit stossen zwei Rechner auch dann nicht zusammen, wenn ihre Uhren auf die
// Mikrosekunde genau gleich stehen.
func InstanzKennung() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "000000000000"
	}
	return hex.EncodeToString(b)
}

// SetInstanz ist fuer Tests, die zwei Rechner nachstellen.
func (s *State) SetInstanz(k string) { s.instanz = k }

// Data gibt eine Momentaufnahme heraus. Aufrufer duerfen sie lesen, nicht aendern.
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

// RemoteOK ist falsch, sobald ein Schreibvorgang nach S3 gescheitert ist - dann
// laeuft s3mail auf der lokalen Datei weiter und zeigt das in der Seitenleiste.
func (s *State) RemoteOK() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.remoteOK
}

func (s *State) OffeneOps() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.openOps
}

// -- Laden ------------------------------------------------------------------ //

func (s *State) snapshotHolen(ctx context.Context) *core.Data {
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

func (s *State) lokalLesen() *core.Data {
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

// opsAuflisten liefert die Schluessel aller abgelegten Ops, aelteste zuerst.
func (s *State) opsAuflisten(ctx context.Context) []string {
	objs, err := s.s3.List(ctx, s.bucket, s.opsPrefix)
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(objs))
	for _, o := range objs {
		keys = append(keys, o.Key)
	}
	sort.Strings(keys)
	return keys
}

// opsHolen laedt die Op-Objekte nebenlaeufig; die Reihenfolge bleibt die der Schluessel.
func (s *State) opsHolen(ctx context.Context, keys []string) [][]core.Op {
	out := make([][]core.Op, len(keys))
	if len(keys) == 0 {
		return out
	}
	sem := make(chan struct{}, s.Workers)
	var wg sync.WaitGroup
	for i, key := range keys {
		wg.Add(1)
		go func(i int, key string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			obj, err := s.s3.Get(ctx, s.bucket, key, "")
			if err != nil {
				return // ein kaputtes Op kippt nicht den Rest
			}
			var h struct {
				Ops []core.Op `json:"ops"`
			}
			if json.Unmarshal(obj.Body, &h) != nil {
				return
			}
			out[i] = h.Ops
		}(i, key)
	}
	wg.Wait()
	return out
}

// Load holt Snapshot und Ops und baut daraus den aktuellen Stand.
func (s *State) Load(ctx context.Context) {
	lokal := s.lokalLesen()
	snap := s.snapshotHolen(ctx)

	var basis *core.Data
	var upto string
	if snap != nil {
		basis, upto = snap, snap.Upto
		if lokal != nil {
			core.MergeMissing(basis, lokal) // offline Gemachtes nicht verlieren
		}
	} else if lokal != nil {
		basis = lokal
	} else {
		basis = core.NewData()
	}
	basis.Upto = ""
	basis.Normalize()

	schnitt := len(s.opsPrefix)
	var offen []string
	for _, k := range s.opsAuflisten(ctx) {
		if len(k) > schnitt && k[schnitt:] > upto {
			offen = append(offen, k)
		}
	}
	for _, ops := range s.opsHolen(ctx, offen) {
		for _, op := range ops {
			core.Apply(basis, op)
		}
	}

	s.mu.Lock()
	s.data, s.upto, s.openOps = basis, upto, len(offen)
	for _, op := range s.pending { // eigene offene Aenderungen erneut drauf
		core.Apply(s.data, op)
	}
	s.mu.Unlock()

	if len(offen) >= CompactAfter {
		s.Compact(ctx, offen)
	}
}

// -- Schreiben -------------------------------------------------------------- //

// opName ist eindeutig und nach Schreibzeit sortierbar: Zeitstempel zuerst,
// damit die lexikografische Ordnung der Schreibreihenfolge entspricht, danach die
// Kennung dieses Prozesses und eine laufende Nummer darin.
func (s *State) opName() string {
	s.seq++
	return fmt.Sprintf("%s-%s-%04d.json",
		s.Now().UTC().Format("20060102T150405.000000"), s.instanz, s.seq)
}

func (s *State) lokalSchreiben(payload []byte) {
	if s.localFile == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.localFile), 0o700)
	tmp := s.localFile + ".tmp"
	if os.WriteFile(tmp, payload, 0o600) == nil {
		_ = os.Rename(tmp, s.localFile)
	}
}

// Mutate wendet eine Aenderung an und schreibt sie weg.
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

// Save legt die offenen Ops als ein Objekt ab. Innerhalb eines Batch passiert
// nichts - dann schreibt erst das Ende des Batch.
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
	s.lokalSchreiben(payload)
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
		s.pending = append(ops, s.pending...) // nichts verlieren
		s.remoteOK = false
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	s.remoteOK, s.dirty = true, false
	s.openOps++
	faellig := s.openOps >= CompactAfter
	s.mu.Unlock()

	if faellig {
		s.Load(ctx) // holt fremde Ops mit dazu und fasst zusammen
	}
	return nil
}

// Compact schreibt einen Snapshot mit neuem Wasserstand und raeumt die
// eingearbeiteten Ops weg.
//
// merged sind genau die Ops, die in s.data stecken - nur bis dorthin darf der
// Wasserstand steigen, sonst gingen fremde Aenderungen verloren. Deshalb bekommt
// Compact die Liste uebergeben und ermittelt sie nicht selbst.
func (s *State) Compact(ctx context.Context, merged []string) {
	if len(merged) == 0 {
		return
	}
	schnitt := len(s.opsPrefix)
	s.mu.Lock()
	kopie := *s.data
	kopie.Upto = merged[len(merged)-1][schnitt:]
	payload, _ := json.Marshal(&kopie)
	s.mu.Unlock()

	if err := s.s3.Put(ctx, s.bucket, s.key, payload, "application/json"); err != nil {
		return // bleibt eben liegen, beim naechsten Mal wieder
	}
	s.mu.Lock()
	s.upto, s.openOps = kopie.Upto, 0
	s.mu.Unlock()

	sem := make(chan struct{}, s.Workers)
	var wg sync.WaitGroup
	for _, key := range merged {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			_ = s.s3.Delete(ctx, s.bucket, key) // der Wasserstand faengt Fehler ab
		}(key)
	}
	wg.Wait()
}

// Batch sammelt mehrere Aenderungen und schreibt am Ende ein einziges Op-Objekt.
func (s *State) Batch(ctx context.Context, fn func() error) error {
	s.mu.Lock()
	s.defers++
	s.mu.Unlock()

	err := fn()

	s.mu.Lock()
	s.defers--
	spuelen := s.defers == 0 && s.dirty
	s.mu.Unlock()
	if spuelen {
		if e := s.Save(ctx); err == nil {
			err = e
		}
	}
	return err
}

// Upto ist der aktuelle Wasserstand - fuer Tests und die Anzeige.
func (s *State) Upto() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.upto
}
