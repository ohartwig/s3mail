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

// TestOpsAgainstPython: dieselbe Op-Folge, dasselbe Ergebnis wie in Python.
func TestOpsAgainstPython(t *testing.T) {
	ops, want := loadOps(t)
	got := run(ops, 1)

	if !reflect.DeepEqual(got.Tags, want.Tags) {
		t.Errorf("Tags:\n  python: %v\n  go:     %v", want.Tags, got.Tags)
	}
	for mid, w := range want.Messages {
		g, da := got.Messages[mid]
		if !da {
			t.Errorf("%s fehlt in Go", mid)
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
		if _, da := want.Messages[mid]; !da {
			t.Errorf("%s gibt es in Go zu viel", mid)
		}
	}
	if len(got.Rules) != len(want.Rules) {
		t.Errorf("Regeln: python=%d go=%d", len(want.Rules), len(got.Rules))
	}
}

// TestOpsAreIdempotent ist die Invariante, auf der der Wasserstand ruht: ein
// Op-Objekt, das beim Aufraeumen liegenbleibt und ein zweites Mal angewandt wird,
// darf nichts veraendern. Faellt dieser Test, ist das Op-Log-Design kaputt - nicht
// nur dieser Test.
func TestOpsAreIdempotent(t *testing.T) {
	ops, _ := loadOps(t)
	once, twice := run(ops, 1), run(ops, 2)
	if !reflect.DeepEqual(once, twice) {
		a, _ := json.MarshalIndent(once, "", " ")
		b, _ := json.MarshalIndent(twice, "", " ")
		t.Fatalf("doppelt angewandt ist nicht dasselbe:\n einmal:\n%s\n zweimal:\n%s", a, b)
	}
}

// TestOpsAreIdempotentIndividually zeigt genauer, welche Operation es waere.
func TestOpsAreIdempotentIndividually(t *testing.T) {
	ops, _ := loadOps(t)
	for i, op := range ops {
		base := run(ops[:i], 1)
		once := clone(t, base)
		Apply(once, op)
		twice := clone(t, once)
		Apply(twice, op)
		if !reflect.DeepEqual(once, twice) {
			t.Errorf("Op %d (%s) ist nicht idempotent", i, op.T)
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

// TestTwoMachines bildet den Fall nach, fuer den es das Op-Log ueberhaupt gibt:
// zwei Rechner mit demselben Ausgangsstand aendern verschiedene Mails, danach
// dieselbe. Nichts darf verlorengehen.
func TestTwoMachines(t *testing.T) {
	out := NewData()
	Apply(out, Op{T: "tags", Mids: []string{"m1"}, Add: []string{"start"}})

	a := []Op{{T: "tags", Mids: []string{"m1"}, Add: []string{"von-A"}},
		{T: "flags", Mids: []string{"m1"}, Star: Ptr(true)}}
	b := []Op{{T: "tags", Mids: []string{"m2"}, Add: []string{"von-B"}},
		{T: "tags", Mids: []string{"m1"}, Add: []string{"von-B"}}}

	// beide Reihenfolgen muessen dasselbe ergeben - keiner ueberschreibt den anderen
	for _, seq := range [][]Op{append(append([]Op{}, a...), b...), append(append([]Op{}, b...), a...)} {
		d := clone(t, out)
		for _, op := range seq {
			Apply(d, op)
		}
		e := d.Get("m1")
		for _, tag := range []string{"start", "von-A", "von-B"} {
			if !contains(e.Tags, tag) {
				t.Errorf("Tag %q verloren, m1 hat %v", tag, e.Tags)
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
			t.Errorf("%s: %s, erwartet %s", tag, d.Tags[tag], TagColors[i])
		}
	}
	// bestehender Tag behaelt seine Farbe
	Apply(d, Op{T: "tags", Mids: []string{"m2"}, Add: []string{"a"}})
	if d.Tags["a"] != TagColors[0] {
		t.Error("Farbe eines bestehenden Tags wurde ueberschrieben")
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
		t.Error("bekannter Eintrag wurde ueberschrieben statt stehengelassen")
	}
	if !contains(base.Get("m9").Tags, "nur-lokal") {
		t.Error("unbekannter Eintrag wurde nicht uebernommen")
	}
}

func normTags(t []string) []string {
	if len(t) == 0 {
		return []string{}
	}
	return t
}
