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
var instanceCounter atomic.Uint64

// buildState liefert einen store.State mit fester Uhr - sonst waeren die Op-Namen
// nicht reproduzierbar.
func buildState(t *testing.T, f *s3fake.Fake) *store.State {
	t.Helper()
	ctx := context.Background()
	s := store.NewState(ctx, f, "test-bucket", "mail/", filepath.Join(t.TempDir(), "state.json"))
	n := 0
	s.Now = func() time.Time {
		n++
		return time.Date(2026, 8, 21, 10, 0, n, 0, time.UTC)
	}
	s.SetInstance(fmt.Sprintf("%012x", instanceCounter.Add(1)))
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

// TestOneChangeOneSmallOp - der Kern des Umbaus: nicht das ganze Dokument,
// sondern die Aenderung.
func TestOneChangeOneSmallOp(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	s := buildState(t, f)

	if err := s.Mutate(ctx, core.Op{T: "flags", Mids: []string{"m1"}, Read: core.Ptr(true)}); err != nil {
		t.Fatal(err)
	}
	written := ops(f)
	if len(written) != 1 {
		t.Fatalf("%d Objekte fuer eine Aenderung: %v", len(written), written)
	}
	if n := len(f.Objs[written[0]]); n > 200 {
		t.Errorf("%d Byte - das sieht nach dem ganzen Dokument aus", n)
	}
	if _, da := f.Objs["mail/"+store.StateObject]; da {
		t.Error("Snapshot wurde bei einer einzelnen Aenderung geschrieben")
	}
	if !s.Get("m1").Read {
		t.Error("Aenderung nicht im Speicher")
	}
}

// TestTwoMachinesNoConflict - beide schreiben, keiner ueberschreibt.
func TestTwoMachinesNoConflict(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	a, b := buildState(t, f), buildState(t, f)

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
	c := buildState(t, f) // dritter Rechner liest nach
	if !has(c.Get("m1").Tags, "von-A") {
		t.Error("Aenderung von A verloren")
	}
	if !has(c.Get("m2").Tags, "von-B") {
		t.Error("Aenderung von B verloren")
	}
}

func TestZusammenfassen(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	s := buildState(t, f)
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
	fresh := buildState(t, f)
	if n := len(fresh.Get("m1").Tags); n != store.CompactAfter+2 {
		t.Errorf("frischer Rechner sieht %d Tags, erwartet %d", n, store.CompactAfter+2)
	}
}

// TestWatermarkSkipsWhatIsAlreadyIn - genau dafuer gibt es den
// Wasserstand: ein liegengebliebenes Op darf nicht ein zweites Mal wirken.
func TestWatermarkSkipsWhatIsAlreadyIn(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	s := buildState(t, f)

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

	fresh := buildState(t, f)
	if fresh.Get("m1").Read {
		t.Error("liegengebliebenes Op wurde erneut angewandt - der Wasserstand traegt nicht")
	}
}

func TestBatchWritesOnce(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	s := buildState(t, f)

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
	fresh := buildState(t, f)
	for i := 0; i < 10; i++ {
		if !has(fresh.Get(fmt.Sprintf("m%d", i)).Tags, "stapel") {
			t.Fatalf("m%d fehlt nach dem Batch", i)
		}
	}
}

func TestSchreibfehlerBehaeltAenderung(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	s := buildState(t, f)
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
	if !buildState(t, f).Get("m1").Star {
		t.Error("nachgeholte Aenderung fehlt im Bucket")
	}
}

func TestLocalFallback(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	local := filepath.Join(t.TempDir(), "state.json")
	s := store.NewState(ctx, f, "test-bucket", "mail/", local)
	f.PutErr = errors.New("AccessDenied")
	_ = s.Mutate(ctx, core.Op{T: "tags", Mids: []string{"m1"}, Add: []string{"offline"}})

	// neuer Prozess, immer noch kein Schreibrecht: der lokale Stand traegt
	second := store.NewState(ctx, f, "test-bucket", "mail/", local)
	if !has(second.Get("m1").Tags, "offline") {
		t.Error("lokaler Rueckfall greift nicht")
	}
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
