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

// every test instance gets an identifier of its own - like two real machines.
var instanceCounter atomic.Uint64

// buildState returns a store.State with a fixed clock - otherwise the op names
// would not be reproducible.
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
	blob, present := f.Objs["mail/"+store.StateObject]
	if !present {
		return nil
	}
	var d core.Data
	if err := json.Unmarshal(blob, &d); err != nil {
		t.Fatal(err)
	}
	return d.Normalize()
}

// TestOneChangeOneSmallOp - the core of the rebuild: not the whole document,
// but the change.
func TestOneChangeOneSmallOp(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	s := buildState(t, f)

	if err := s.Mutate(ctx, core.Op{T: "flags", Mids: []string{"m1"}, Read: core.Ptr(true)}); err != nil {
		t.Fatal(err)
	}
	written := ops(f)
	if len(written) != 1 {
		t.Fatalf("%d objects for one change: %v", len(written), written)
	}
	if n := len(f.Objs[written[0]]); n > 200 {
		t.Errorf("%d bytes - that looks like the whole document", n)
	}
	if _, present := f.Objs["mail/"+store.StateObject]; present {
		t.Error("a snapshot was written for a single change")
	}
	if !s.Get("m1").Read {
		t.Error("change not in memory")
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
	// b does not know A's change yet - and writes safely all the same
	if err := b.Mutate(ctx, core.Op{T: "tags", Mids: []string{"m2"}, Add: []string{"von-B"}}); err != nil {
		t.Fatal(err)
	}
	if len(ops(f)) != 2 {
		t.Fatalf("%d op objects - do the two write to the same key?", len(ops(f)))
	}
	c := buildState(t, f) // a third machine reads afterwards
	if !has(c.Get("m1").Tags, "von-A") {
		t.Error("change from A lost")
	}
	if !has(c.Get("m2").Tags, "von-B") {
		t.Error("change from B lost")
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
		t.Fatal("no snapshot with a watermark")
	}
	if n := len(ops(f)); n > 3 {
		t.Errorf("%d ops left after merging", n)
	}
	fresh := buildState(t, f)
	if n := len(fresh.Get("m1").Tags); n != store.CompactAfter+2 {
		t.Errorf("a fresh machine sees %d tags, expected %d", n, store.CompactAfter+2)
	}
}

// TestWatermarkSkipsWhatIsAlreadyIn - that is exactly what the watermark is
// for: an op left behind must not take effect a second time.
func TestWatermarkSkipsWhatIsAlreadyIn(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	s := buildState(t, f)

	if err := s.Mutate(ctx, core.Op{T: "flags", Mids: []string{"m1"}, Read: core.Ptr(true)}); err != nil {
		t.Fatal(err)
	}
	altKey := ops(f)[0]
	altBody := append([]byte(nil), f.Objs[altKey]...)

	s.Compact(ctx, []string{altKey}) // merge, the op flies away
	if snapshot(t, f).Upto == "" {
		t.Fatal("no watermark set")
	}
	if err := s.Mutate(ctx, core.Op{T: "flags", Mids: []string{"m1"}, Read: core.Ptr(false)}); err != nil {
		t.Fatal(err)
	}
	f.Objs[altKey] = altBody // the delete had failed: the old op is back

	fresh := buildState(t, f)
	if fresh.Get("m1").Read {
		t.Error("an op left behind was applied again - the watermark does not hold")
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
		t.Errorf("%d op objects for one batch, expected 1", n)
	}
	fresh := buildState(t, f)
	for i := 0; i < 10; i++ {
		if !has(fresh.Get(fmt.Sprintf("m%d", i)).Tags, "stapel") {
			t.Fatalf("m%d missing after the batch", i)
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
		t.Fatal("write error was swallowed")
	}
	if s.RemoteOK() {
		t.Error("RemoteOK haette fallen muessen")
	}
	if !s.Get("m1").Star {
		t.Error("Aenderung lokal verloren")
	}

	f.PutErr = nil
	if err := s.Save(ctx); err != nil { // the next attempt catches up
		t.Fatal(err)
	}
	if !s.RemoteOK() || len(ops(f)) != 1 {
		t.Errorf("Nachholen misslungen: remoteOK=%v ops=%v", s.RemoteOK(), ops(f))
	}
	if !buildState(t, f).Get("m1").Star {
		t.Error("the caught-up change is missing from the bucket")
	}
}

func TestLocalFallback(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	local := filepath.Join(t.TempDir(), "state.json")
	s := store.NewState(ctx, f, "test-bucket", "mail/", local)
	f.PutErr = errors.New("AccessDenied")
	_ = s.Mutate(ctx, core.Op{T: "tags", Mids: []string{"m1"}, Add: []string{"offline"}})

	// a new process, still no write permission: the local state carries
	second := store.NewState(ctx, f, "test-bucket", "mail/", local)
	if !has(second.Get("m1").Tags, "offline") {
		t.Error("the local fallback does not take hold")
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
