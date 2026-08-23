package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func opsLaden(t *testing.T) ([]Op, *Data) {
	t.Helper()
	var ops []Op
	var want Data
	for _, p := range []struct {
		datei string
		ziel  any
	}{{"ops.json", &ops}, {"ops_result.json", &want}} {
		blob, err := os.ReadFile(filepath.Join("testdata", p.datei))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(blob, p.ziel); err != nil {
			t.Fatalf("%s: %v", p.datei, err)
		}
	}
	return ops, want.Normalize()
}

func lauf(ops []Op, wiederholungen int) *Data {
	d := NewData()
	for _, op := range ops {
		for i := 0; i < wiederholungen; i++ {
			Apply(d, op)
		}
	}
	return d
}

// TestOpsAgainstPython: dieselbe Op-Folge, dasselbe Ergebnis wie in Python.
func TestOpsAgainstPython(t *testing.T) {
	ops, want := opsLaden(t)
	got := lauf(ops, 1)

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
	ops, _ := opsLaden(t)
	einmal, zweimal := lauf(ops, 1), lauf(ops, 2)
	if !reflect.DeepEqual(einmal, zweimal) {
		a, _ := json.MarshalIndent(einmal, "", " ")
		b, _ := json.MarshalIndent(zweimal, "", " ")
		t.Fatalf("doppelt angewandt ist nicht dasselbe:\n einmal:\n%s\n zweimal:\n%s", a, b)
	}
}

// TestOpsAreIdempotentIndividually zeigt genauer, welche Operation es waere.
func TestOpsAreIdempotentIndividually(t *testing.T) {
	ops, _ := opsLaden(t)
	for i, op := range ops {
		basis := lauf(ops[:i], 1)
		einmal := klon(t, basis)
		Apply(einmal, op)
		zweimal := klon(t, einmal)
		Apply(zweimal, op)
		if !reflect.DeepEqual(einmal, zweimal) {
			t.Errorf("Op %d (%s) ist nicht idempotent", i, op.T)
		}
	}
}

func klon(t *testing.T, d *Data) *Data {
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
	ausgang := NewData()
	Apply(ausgang, Op{T: "tags", Mids: []string{"m1"}, Add: []string{"start"}})

	a := []Op{{T: "tags", Mids: []string{"m1"}, Add: []string{"von-A"}},
		{T: "flags", Mids: []string{"m1"}, Star: Ptr(true)}}
	b := []Op{{T: "tags", Mids: []string{"m2"}, Add: []string{"von-B"}},
		{T: "tags", Mids: []string{"m1"}, Add: []string{"von-B"}}}

	// beide Reihenfolgen muessen dasselbe ergeben - keiner ueberschreibt den anderen
	for _, folge := range [][]Op{append(append([]Op{}, a...), b...), append(append([]Op{}, b...), a...)} {
		d := klon(t, ausgang)
		for _, op := range folge {
			Apply(d, op)
		}
		e := d.Get("m1")
		for _, tag := range []string{"start", "von-A", "von-B"} {
			if !enthaelt(e.Tags, tag) {
				t.Errorf("Tag %q verloren, m1 hat %v", tag, e.Tags)
			}
		}
		if !e.Star {
			t.Error("fremdes Stern-Flag verloren")
		}
		if !enthaelt(d.Get("m2").Tags, "von-B") {
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
	basis := NewData()
	Apply(basis, Op{T: "tags", Mids: []string{"m1"}, Add: []string{"remote"}})
	lokal := NewData()
	Apply(lokal, Op{T: "tags", Mids: []string{"m1"}, Add: []string{"lokal"}})
	Apply(lokal, Op{T: "tags", Mids: []string{"m9"}, Add: []string{"nur-lokal"}})

	MergeMissing(basis, lokal)
	if enthaelt(basis.Get("m1").Tags, "lokal") {
		t.Error("bekannter Eintrag wurde ueberschrieben statt stehengelassen")
	}
	if !enthaelt(basis.Get("m9").Tags, "nur-lokal") {
		t.Error("unbekannter Eintrag wurde nicht uebernommen")
	}
}

func normTags(t []string) []string {
	if len(t) == 0 {
		return []string{}
	}
	return t
}
