package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func loadOps(t *testing.T) ([]Op, *Data) {
	t.Helper()
	var ops []Op
	var want Data
	for _, p := range []struct {
		file   string
		target any
	}{{"ops.json", &ops}, {"ops_result.json", &want}} {
		blob, err := os.ReadFile(filepath.Join("testdata", p.file))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(blob, p.target); err != nil {
			t.Fatalf("%s: %v", p.file, err)
		}
	}
	return ops, want.Normalize()
}

func run(ops []Op, repeats int) *Data {
	d := NewData()
	for _, op := range ops {
		for i := 0; i < repeats; i++ {
			Apply(d, op)
		}
	}
	return d
}

// TestOpsAgainstPython: the same sequence of ops, the same result as in Python.
func TestOpsAgainstPython(t *testing.T) {
	ops, want := loadOps(t)
	got := run(ops, 1)

	if !reflect.DeepEqual(got.Tags, want.Tags) {
		t.Errorf("Tags:\n  python: %v\n  go:     %v", want.Tags, got.Tags)
	}
	for mid, w := range want.Messages {
		g, present := got.Messages[mid]
		if !present {
			t.Errorf("%s missing in Go", mid)
			continue
		}
		if g.Read != w.Read || g.Star != w.Star || g.Ruled != w.Ruled {
			t.Errorf("%s Flags: python=%+v go=%+v", mid, *w, *g)
		}
		if !reflect.DeepEqual(normTags(g.Tags), normTags(w.Tags)) {
			t.Errorf("%s Tags: python=%v go=%v", mid, w.Tags, g.Tags)
		}
	}
	for mid := range got.Messages {
		if _, present := want.Messages[mid]; !present {
			t.Errorf("%s exists in Go on top", mid)
		}
	}
	if len(got.Rules) != len(want.Rules) {
		t.Errorf("Regeln: python=%d go=%d", len(want.Rules), len(got.Rules))
	}
}

// TestOpsAreIdempotent is the invariant the watermark rests on: an op object
// left behind by an incomplete cleanup and applied a second time must change
// nothing. If this test falls, the op log design is broken - not just this
// test.
func TestOpsAreIdempotent(t *testing.T) {
	ops, _ := loadOps(t)
	once, twice := run(ops, 1), run(ops, 2)
	if !reflect.DeepEqual(once, twice) {
		a, _ := json.MarshalIndent(once, "", " ")
		b, _ := json.MarshalIndent(twice, "", " ")
		t.Fatalf("applied twice is not the same:\n once:\n%s\n twice:\n%s", a, b)
	}
}

// TestOpsAreIdempotentIndividually shows more precisely which operation it would be.
func TestOpsAreIdempotentIndividually(t *testing.T) {
	ops, _ := loadOps(t)
	for i, op := range ops {
		base := run(ops[:i], 1)
		once := clone(t, base)
		Apply(once, op)
		twice := clone(t, once)
		Apply(twice, op)
		if !reflect.DeepEqual(once, twice) {
			t.Errorf("op %d (%s) is not idempotent", i, op.T)
		}
	}
}

func clone(t *testing.T, d *Data) *Data {
	t.Helper()
	blob, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var out Data
	if err := json.Unmarshal(blob, &out); err != nil {
		t.Fatal(err)
	}
	return out.Normalize()
}

// TestTwoMachines stages the case the op log exists for in the first place:
// two machines with the same starting state change different messages, then
// read each other's. Nothing may get lost.
func TestTwoMachines(t *testing.T) {
	out := NewData()
	Apply(out, Op{T: "tags", Mids: []string{"m1"}, Add: []string{"start"}})

	a := []Op{{T: "tags", Mids: []string{"m1"}, Add: []string{"von-A"}},
		{T: "flags", Mids: []string{"m1"}, Star: Ptr(true)}}
	b := []Op{{T: "tags", Mids: []string{"m2"}, Add: []string{"von-B"}},
		{T: "tags", Mids: []string{"m1"}, Add: []string{"von-B"}}}

	// both orders have to yield the same - neither overwrites the other
	for _, seq := range [][]Op{append(append([]Op{}, a...), b...), append(append([]Op{}, b...), a...)} {
		d := clone(t, out)
		for _, op := range seq {
			Apply(d, op)
		}
		e := d.Get("m1")
		for _, tag := range []string{"start", "von-A", "von-B"} {
			if !contains(e.Tags, tag) {
				t.Errorf("tag %q lost, m1 has %v", tag, e.Tags)
			}
		}
		if !e.Star {
			t.Error("fremdes Stern-Flag verloren")
		}
		if !contains(d.Get("m2").Tags, "von-B") {
			t.Error("Aenderung an m2 verloren")
		}
	}
}

func TestTagFarbenDerReihenach(t *testing.T) {
	d := NewData()
	for i, tag := range []string{"a", "b", "c"} {
		Apply(d, Op{T: "tags", Mids: []string{"m1"}, Add: []string{tag}})
		if d.Tags[tag] != TagColors[i] {
			t.Errorf("%s: %s, expected %s", tag, d.Tags[tag], TagColors[i])
		}
	}
	// an existing tag keeps its colour
	Apply(d, Op{T: "tags", Mids: []string{"m2"}, Add: []string{"a"}})
	if d.Tags["a"] != TagColors[0] {
		t.Error("the colour of an existing tag was overwritten")
	}
}

func TestMergeMissing(t *testing.T) {
	base := NewData()
	Apply(base, Op{T: "tags", Mids: []string{"m1"}, Add: []string{"remote"}})
	local := NewData()
	Apply(local, Op{T: "tags", Mids: []string{"m1"}, Add: []string{"lokal"}})
	Apply(local, Op{T: "tags", Mids: []string{"m9"}, Add: []string{"nur-lokal"}})

	MergeMissing(base, local)
	if contains(base.Get("m1").Tags, "lokal") {
		t.Error("a known entry was overwritten instead of left alone")
	}
	if !contains(base.Get("m9").Tags, "nur-lokal") {
		t.Error("an unknown entry was not taken over")
	}
}

func normTags(t []string) []string {
	if len(t) == 0 {
		return []string{}
	}
	return t
}
