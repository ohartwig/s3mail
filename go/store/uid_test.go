package store_test

import (
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/core"
	"git.ole-hartwig.eu/development/s3mail/s3mail/s3fake"
	"git.ole-hartwig.eu/development/s3mail/s3mail/store"
)

// The UID design of IMAP.md, run through the real op log rather than over a
// document in memory: two machines see the same new mail, both number it, and a
// third machine has to end up with a numbering an IMAP client can live with -
// every number owned once, and the valve pulled where it had to be.

func TestUIDTwoMachinesSameNumber(t *testing.T) {
	ctx := t.Context()
	f := s3fake.New()
	a, b := buildState(t, f), buildState(t, f)

	// Neither has seen the other's write - both hand out number 1.
	if err := a.Mutate(ctx, core.Op{T: "uid", Folder: "inbox",
		Mids: []string{"von-a"}, Nums: []uint32{1}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Mutate(ctx, core.Op{T: "uid", Folder: "inbox",
		Mids: []string{"von-b"}, Nums: []uint32{1}}); err != nil {
		t.Fatal(err)
	}
	if n := len(ops(f)); n != 2 {
		t.Fatalf("%d op objects - the two wrote to the same key", n)
	}

	c := buildState(t, f) // a third machine reads both
	d := c.Data()

	numbered := 0
	for _, mid := range []string{"von-a", "von-b"} {
		if _, ok := d.UID("inbox", mid); ok {
			numbered++
		}
	}
	if numbered != 1 {
		t.Fatalf("%d of two messages carry number 1 - a client would see a duplicate", numbered)
	}
	if v := d.UIDValidity("inbox"); v < 2 {
		t.Errorf("UIDVALIDITY is %d - the conflict did not pull the valve", v)
	}
	if next := d.UIDNext("inbox"); next != 2 {
		t.Errorf("UIDNEXT is %d, expected 2", next)
	}
}

// Every machine has to arrive at the same numbering - that is what makes the
// numbers usable at all. Two more machines read the same log afterwards.
func TestUIDAllMachinesAgree(t *testing.T) {
	ctx := t.Context()
	f := s3fake.New()
	a, b := buildState(t, f), buildState(t, f)

	if err := a.Mutate(ctx, core.Op{T: "uid", Folder: "inbox",
		Mids: []string{"m1", "m2"}, Nums: []uint32{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Mutate(ctx, core.Op{T: "uid", Folder: "inbox",
		Mids: []string{"m2", "m3"}, Nums: []uint32{2, 3}}); err != nil {
		t.Fatal(err)
	}

	first, second := buildState(t, f).Data(), buildState(t, f).Data()
	for _, mid := range []string{"m1", "m2", "m3"} {
		x, okx := first.UID("inbox", mid)
		y, oky := second.UID("inbox", mid)
		if okx != oky || x != y {
			t.Errorf("%s: one machine says %d/%v, the other %d/%v", mid, x, okx, y, oky)
		}
	}
	if first.UIDValidity("inbox") != second.UIDValidity("inbox") {
		t.Error("the machines disagree about UIDVALIDITY")
	}
}

// The numbering has to survive compaction into the snapshot - otherwise every
// client would resync as soon as the op log is folded up.
func TestUIDSurvivesCompaction(t *testing.T) {
	ctx := t.Context()
	f := s3fake.New()
	s := buildState(t, f)

	if err := s.Mutate(ctx, core.Op{T: "uid", Folder: "inbox",
		Mids: []string{"m1"}, Nums: []uint32{1}}); err != nil {
		t.Fatal(err)
	}
	for i := range store.CompactAfter + 2 {
		if err := s.Mutate(ctx, core.Op{T: "flags", Mids: []string{"m1"},
			Read: core.Ptr(i%2 == 0)}); err != nil {
			t.Fatal(err)
		}
	}
	snap := snapshot(t, f)
	if snap == nil {
		t.Fatal("no snapshot was written")
	}
	if n, ok := snap.UID("inbox", "m1"); !ok || n != 1 {
		t.Errorf("the snapshot carries %d/%v for m1, expected 1/true", n, ok)
	}

	fresh := buildState(t, f).Data()
	if n, ok := fresh.UID("inbox", "m1"); !ok || n != 1 {
		t.Errorf("a fresh machine sees %d/%v after compaction", n, ok)
	}
	if next := fresh.UIDNext("inbox"); next != 2 {
		t.Errorf("UIDNEXT is %d after compaction, expected 2", next)
	}
}
