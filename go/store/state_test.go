package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"s3mail/core"
	"s3mail/s3fake"
	"s3mail/store"
)

// jede Testinstanz bekommt eine eigene Kennung - so wie zwei echte Rechner.
var instanzZaehler atomic.Uint64

// zustandBauen liefert einen store.State mit fester Uhr - sonst waeren die Op-Namen
// nicht reproduzierbar.
func zustandBauen(t *testing.T, f *s3fake.Fake) *store.State {
	t.Helper()
	ctx := context.Background()
	s := store.NewState(ctx, f, "test-bucket", "mail/", filepath.Join(t.TempDir(), "state.json"))
	n := 0
	s.Now = func() time.Time {
		n++
		return time.Date(2026, 8, 21, 10, 0, n, 0, time.UTC)
	}
	s.SetInstanz(fmt.Sprintf("%012x", instanzZaehler.Add(1)))
	return s
}

func ops(f *s3fake.Fake) []string { return f.Keys("mail/" + store.StateOps) }

func snapshot(t *testing.T, f *s3fake.Fake) *core.Data {
	t.Helper()
	blob, da := f.Objs["mail/"+store.StateObject]
	if !da {
		return nil
	}
	var d core.Data
	if err := json.Unmarshal(blob, &d); err != nil {
		t.Fatal(err)
	}
	return d.Normalize()
}

// TestEineAenderungEinKleinesOp - der Kern des Umbaus: nicht das ganze Dokument,
// sondern die Aenderung.
func TestEineAenderungEinKleinesOp(t *testing.T) {
	ctx := context.Background()
	f := s3fake.Neu()
	s := zustandBauen(t, f)

	if err := s.Mutate(ctx, core.Op{T: "flags", Mids: []string{"m1"}, Read: core.Ptr(true)}); err != nil {
		t.Fatal(err)
	}
	geschrieben := ops(f)
	if len(geschrieben) != 1 {
		t.Fatalf("%d Objekte fuer eine Aenderung: %v", len(geschrieben), geschrieben)
	}
	if n := len(f.Objs[geschrieben[0]]); n > 200 {
		t.Errorf("%d Byte - das sieht nach dem ganzen Dokument aus", n)
	}
	if _, da := f.Objs["mail/"+store.StateObject]; da {
		t.Error("Snapshot wurde bei einer einzelnen Aenderung geschrieben")
	}
	if !s.Get("m1").Read {
		t.Error("Aenderung nicht im Speicher")
	}
}

// TestZweiRechnerKeinKonflikt - beide schreiben, keiner ueberschreibt.
func TestZweiRechnerKeinKonflikt(t *testing.T) {
	ctx := context.Background()
	f := s3fake.Neu()
	a, b := zustandBauen(t, f), zustandBauen(t, f)

	if err := a.Mutate(ctx, core.Op{T: "tags", Mids: []string{"m1"}, Add: []string{"von-A"}}); err != nil {
		t.Fatal(err)
	}
	// b kennt A's Aenderung noch nicht - schreibt trotzdem gefahrlos
	if err := b.Mutate(ctx, core.Op{T: "tags", Mids: []string{"m2"}, Add: []string{"von-B"}}); err != nil {
		t.Fatal(err)
	}
	if len(ops(f)) != 2 {
		t.Fatalf("%d Op-Objekte - schreiben die beiden auf denselben Schluessel?", len(ops(f)))
	}
	c := zustandBauen(t, f) // dritter Rechner liest nach
	if !hat(c.Get("m1").Tags, "von-A") {
		t.Error("Aenderung von A verloren")
	}
	if !hat(c.Get("m2").Tags, "von-B") {
		t.Error("Aenderung von B verloren")
	}
}

func TestZusammenfassen(t *testing.T) {
	ctx := context.Background()
	f := s3fake.Neu()
	s := zustandBauen(t, f)
	for i := 0; i < store.CompactAfter+2; i++ {
		if err := s.Mutate(ctx, core.Op{T: "tags", Mids: []string{"m1"},
			Add: []string{fmt.Sprintf("t%02d", i)}}); err != nil {
			t.Fatal(err)
		}
	}
	snap := snapshot(t, f)
	if snap == nil || snap.Upto == "" {
		t.Fatal("kein Snapshot mit Wasserstand")
	}
	if n := len(ops(f)); n > 3 {
		t.Errorf("%d Ops nach dem Zusammenfassen uebrig", n)
	}
	frisch := zustandBauen(t, f)
	if n := len(frisch.Get("m1").Tags); n != store.CompactAfter+2 {
		t.Errorf("frischer Rechner sieht %d Tags, erwartet %d", n, store.CompactAfter+2)
	}
}

// TestWasserstandUeberspringtEingearbeitetes - genau dafuer gibt es den
// Wasserstand: ein liegengebliebenes Op darf nicht ein zweites Mal wirken.
func TestWasserstandUeberspringtEingearbeitetes(t *testing.T) {
	ctx := context.Background()
	f := s3fake.Neu()
	s := zustandBauen(t, f)

	if err := s.Mutate(ctx, core.Op{T: "flags", Mids: []string{"m1"}, Read: core.Ptr(true)}); err != nil {
		t.Fatal(err)
	}
	altKey := ops(f)[0]
	altBody := append([]byte(nil), f.Objs[altKey]...)

	s.Compact(ctx, []string{altKey}) // zusammenfassen, Op fliegt weg
	if snapshot(t, f).Upto == "" {
		t.Fatal("kein Wasserstand gesetzt")
	}
	if err := s.Mutate(ctx, core.Op{T: "flags", Mids: []string{"m1"}, Read: core.Ptr(false)}); err != nil {
		t.Fatal(err)
	}
	f.Objs[altKey] = altBody // Loeschen war gescheitert: altes Op ist wieder da

	frisch := zustandBauen(t, f)
	if frisch.Get("m1").Read {
		t.Error("liegengebliebenes Op wurde erneut angewandt - der Wasserstand traegt nicht")
	}
}

func TestBatchSchreibtEinmal(t *testing.T) {
	ctx := context.Background()
	f := s3fake.Neu()
	s := zustandBauen(t, f)

	err := s.Batch(ctx, func() error {
		for i := 0; i < 10; i++ {
			if err := s.Mutate(ctx, core.Op{T: "tags", Mids: []string{fmt.Sprintf("m%d", i)},
				Add: []string{"stapel"}}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(ops(f)); n != 1 {
		t.Errorf("%d Op-Objekte fuer einen Batch, erwartet 1", n)
	}
	frisch := zustandBauen(t, f)
	for i := 0; i < 10; i++ {
		if !hat(frisch.Get(fmt.Sprintf("m%d", i)).Tags, "stapel") {
			t.Fatalf("m%d fehlt nach dem Batch", i)
		}
	}
}

func TestSchreibfehlerBehaeltAenderung(t *testing.T) {
	ctx := context.Background()
	f := s3fake.Neu()
	s := zustandBauen(t, f)
	f.PutErr = errors.New("AccessDenied")

	err := s.Mutate(ctx, core.Op{T: "flags", Mids: []string{"m1"}, Star: core.Ptr(true)})
	if err == nil {
		t.Fatal("Schreibfehler wurde verschluckt")
	}
	if s.RemoteOK() {
		t.Error("RemoteOK haette fallen muessen")
	}
	if !s.Get("m1").Star {
		t.Error("Aenderung lokal verloren")
	}

	f.PutErr = nil
	if err := s.Save(ctx); err != nil { // naechster Versuch holt es nach
		t.Fatal(err)
	}
	if !s.RemoteOK() || len(ops(f)) != 1 {
		t.Errorf("Nachholen misslungen: remoteOK=%v ops=%v", s.RemoteOK(), ops(f))
	}
	if !zustandBauen(t, f).Get("m1").Star {
		t.Error("nachgeholte Aenderung fehlt im Bucket")
	}
}

func TestLokalerRueckfall(t *testing.T) {
	ctx := context.Background()
	f := s3fake.Neu()
	lokal := filepath.Join(t.TempDir(), "state.json")
	s := store.NewState(ctx, f, "test-bucket", "mail/", lokal)
	f.PutErr = errors.New("AccessDenied")
	_ = s.Mutate(ctx, core.Op{T: "tags", Mids: []string{"m1"}, Add: []string{"offline"}})

	// neuer Prozess, immer noch kein Schreibrecht: der lokale Stand traegt
	zweiter := store.NewState(ctx, f, "test-bucket", "mail/", lokal)
	if !hat(zweiter.Get("m1").Tags, "offline") {
		t.Error("lokaler Rueckfall greift nicht")
	}
}

func hat(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
