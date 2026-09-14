package core_test

import (
	"encoding/json"
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/go/core"
)

// replay runs ops over a fresh document in the given order - that is what a
// machine does in Load, and what the whole UID design rests on.
func replay(ops ...core.Op) *core.Data {
	d := core.NewData()
	for _, op := range ops {
		core.Apply(d, op)
	}
	return d
}

func uidOf(t *testing.T, d *core.Data, folder, mid string) uint32 {
	t.Helper()
	n, ok := d.UID(folder, mid)
	if !ok {
		t.Fatalf("%s has no UID in %s", mid, folder)
	}
	return n
}

func TestUIDsAreHandedOutInOrder(t *testing.T) {
	d := core.NewData()
	op := core.AssignUIDs(d, "inbox", []string{"a", "b", "c"})
	core.Apply(d, op)

	if got := uidOf(t, d, "inbox", "a"); got != 1 {
		t.Errorf("first message got %d, expected 1", got)
	}
	if got := uidOf(t, d, "inbox", "c"); got != 3 {
		t.Errorf("third message got %d, expected 3", got)
	}
	if got := d.UIDNext("inbox"); got != 4 {
		t.Errorf("UIDNEXT is %d, expected 4", got)
	}
	if got := d.UIDValidity("inbox"); got != 1 {
		t.Errorf("UIDVALIDITY is %d without a conflict, expected 1", got)
	}
}

// AssignUIDs must not touch the document - the caller writes the op, and only
// applying it may change anything.
func TestAssignDoesNotMutate(t *testing.T) {
	d := core.NewData()
	core.AssignUIDs(d, "inbox", []string{"a", "b"})
	if _, ok := d.UID("inbox", "a"); ok {
		t.Error("AssignUIDs numbered a message without the op being applied")
	}
	if n := d.UIDNext("inbox"); n != 1 {
		t.Errorf("UIDNEXT moved to %d without an op", n)
	}
}

func TestAlreadyNumberedIsSkipped(t *testing.T) {
	d := core.NewData()
	core.Apply(d, core.AssignUIDs(d, "inbox", []string{"a"}))
	op := core.AssignUIDs(d, "inbox", []string{"a", "b"})

	if len(op.Mids) != 1 || op.Mids[0] != "b" {
		t.Fatalf("op covers %v, expected only b", op.Mids)
	}
	core.Apply(d, op)
	if got := uidOf(t, d, "inbox", "b"); got != 2 {
		t.Errorf("b got %d, expected 2", got)
	}
}

// An op object left behind by an incomplete cleanup gets replayed. It must not
// change anything the second time.
func TestReplayIsIdempotent(t *testing.T) {
	op := core.Op{T: "uid", Folder: "inbox", Mids: []string{"a", "b"}, Nums: []uint32{1, 2}}
	once := replay(op)
	twice := replay(op, op, op)

	if uidOf(t, once, "inbox", "b") != uidOf(t, twice, "inbox", "b") {
		t.Error("a replayed op changed the number")
	}
	if once.UIDNext("inbox") != twice.UIDNext("inbox") {
		t.Error("a replayed op moved UIDNEXT")
	}
	if v := twice.UIDValidity("inbox"); v != 1 {
		t.Errorf("a replay counted as a conflict: UIDVALIDITY %d", v)
	}
}

// The load-bearing property: two machines that saw the same new message at the
// same time wrote two ops. Whatever order a third machine replays them in, it
// has to end up in the same place - because replay order is the key order, and
// that is the same everywhere.
func TestTwoMachinesConverge(t *testing.T) {
	fromA := core.Op{T: "uid", Folder: "inbox", Mids: []string{"m1"}, Nums: []uint32{1}}
	fromB := core.Op{T: "uid", Folder: "inbox", Mids: []string{"m2"}, Nums: []uint32{1}} // same number!

	ab := replay(fromA, fromB)
	if got := uidOf(t, ab, "inbox", "m1"); got != 1 {
		t.Errorf("the earlier op did not keep the number: m1 has %d", got)
	}
	if _, ok := ab.UID("inbox", "m2"); ok {
		t.Error("the loser was numbered anyway - two messages would share a UID")
	}
	if v := ab.UIDValidity("inbox"); v != 2 {
		t.Errorf("UIDVALIDITY is %d after a conflict, expected 2", v)
	}

	// The same two ops in the other order: whoever comes first wins, and the
	// result is the mirror image - one numbered message, no duplicate, and the
	// valve pulled. That is convergence, not equality of names.
	ba := replay(fromB, fromA)
	if got := uidOf(t, ba, "inbox", "m2"); got != 1 {
		t.Errorf("the earlier op did not keep the number: m2 has %d", got)
	}
	if _, ok := ba.UID("inbox", "m1"); ok {
		t.Error("the loser was numbered anyway")
	}
	if ab.UIDValidity("inbox") != ba.UIDValidity("inbox") {
		t.Error("UIDVALIDITY differs between the two orders")
	}
}

// The loser of a conflict must not stay unnumbered forever.
func TestLoserGetsAFreshNumber(t *testing.T) {
	d := replay(
		core.Op{T: "uid", Folder: "inbox", Mids: []string{"m1"}, Nums: []uint32{1}},
		core.Op{T: "uid", Folder: "inbox", Mids: []string{"m2"}, Nums: []uint32{1}},
	)
	core.Apply(d, core.AssignUIDs(d, "inbox", []string{"m1", "m2"}))

	if got := uidOf(t, d, "inbox", "m2"); got != 2 {
		t.Errorf("m2 got %d on the next pass, expected 2", got)
	}
	if uidOf(t, d, "inbox", "m1") == uidOf(t, d, "inbox", "m2") {
		t.Error("two messages share a UID")
	}
}

// A UID is never handed out twice, not even after the message is gone.
func TestRetiredNumberIsNotReused(t *testing.T) {
	d := core.NewData()
	core.Apply(d, core.AssignUIDs(d, "inbox", []string{"a", "b"}))
	core.Apply(d, core.Op{T: "uidretire", Folder: "inbox", Mids: []string{"b"}})

	if _, ok := d.UID("inbox", "b"); ok {
		t.Error("the retired message still carries a number")
	}
	core.Apply(d, core.AssignUIDs(d, "inbox", []string{"c"}))
	if got := uidOf(t, d, "inbox", "c"); got != 3 {
		t.Errorf("c got %d - number 2 was handed out a second time", got)
	}
}

// Dropping an object takes its number out of every folder: the op fires when
// the object has vanished from the bucket and does not say where it was.
func TestDropClearsEveryFolder(t *testing.T) {
	d := core.NewData()
	core.Apply(d, core.AssignUIDs(d, "inbox", []string{"a"}))
	core.Apply(d, core.AssignUIDs(d, "archiv", []string{"a"}))
	core.Apply(d, core.Op{T: "drop", Mids: []string{"a"}})

	for _, folder := range []string{"inbox", "archiv"} {
		if _, ok := d.UID(folder, "a"); ok {
			t.Errorf("%s still numbers the dropped message", folder)
		}
	}
}

func TestFoldersAreNumberedApart(t *testing.T) {
	d := core.NewData()
	core.Apply(d, core.AssignUIDs(d, "inbox", []string{"a"}))
	core.Apply(d, core.AssignUIDs(d, "archiv", []string{"b"}))

	if uidOf(t, d, "inbox", "a") != 1 || uidOf(t, d, "archiv", "b") != 1 {
		t.Error("the two folders do not number independently")
	}
}

// UID 0 is not a valid IMAP UID. An op carrying one is refused rather than
// stored - a client would choke on it.
func TestZeroIsRefused(t *testing.T) {
	d := replay(core.Op{T: "uid", Folder: "inbox", Mids: []string{"a"}, Nums: []uint32{0}})
	if _, ok := d.UID("inbox", "a"); ok {
		t.Error("UID 0 was handed out")
	}
}

// A malformed op - more messages than numbers - must not panic.
func TestShortOpDoesNotPanic(t *testing.T) {
	d := replay(core.Op{T: "uid", Folder: "inbox", Mids: []string{"a", "b"}, Nums: []uint32{1}})
	if got := uidOf(t, d, "inbox", "a"); got != 1 {
		t.Errorf("a got %d, expected 1", got)
	}
	if _, ok := d.UID("inbox", "b"); ok {
		t.Error("b was numbered from an op that carried no number for it")
	}
}

// The numbering has to survive the snapshot, including the reverse index that
// is deliberately not serialized.
func TestSurvivesJSON(t *testing.T) {
	d := core.NewData()
	core.Apply(d, core.AssignUIDs(d, "inbox", []string{"a", "b"}))

	blob, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var back core.Data
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatal(err)
	}
	back.Normalize()

	if got := uidOf(t, &back, "inbox", "b"); got != 2 {
		t.Errorf("b has %d after a round trip, expected 2", got)
	}
	// The reverse index was rebuilt, so a colliding op is still caught.
	core.Apply(&back, core.Op{T: "uid", Folder: "inbox", Mids: []string{"c"}, Nums: []uint32{1}})
	if _, ok := back.UID("inbox", "c"); ok {
		t.Error("the collision went unnoticed after a round trip")
	}
}

// A state document written before UIDs existed must stay readable.
func TestOldSnapshotStaysReadable(t *testing.T) {
	var d core.Data
	if err := json.Unmarshal([]byte(`{"messages":{"a":{"read":true}},"tags":{}}`), &d); err != nil {
		t.Fatal(err)
	}
	d.Normalize()
	if !d.Get("a").Read {
		t.Error("the old document lost its content")
	}
	core.Apply(&d, core.AssignUIDs(&d, "inbox", []string{"a"}))
	if got := uidOf(t, &d, "inbox", "a"); got != 1 {
		t.Errorf("numbering an old document gave %d, expected 1", got)
	}
}
