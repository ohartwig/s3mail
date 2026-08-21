package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func laden(t *testing.T) ([]Message, *Data) {
	t.Helper()
	var index []Message
	var data Data
	for _, p := range []struct {
		datei string
		ziel  any
	}{{"index.json", &index}, {"state.json", &data}} {
		blob, err := os.ReadFile(filepath.Join("testdata", p.datei))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(blob, p.ziel); err != nil {
			t.Fatalf("%s: %v", p.datei, err)
		}
	}
	return index, data.Normalize()
}

// TestSucheGegenPython faehrt dieselben Abfragen wie die Python-Fassung und
// vergleicht Treffer *und* Reihenfolge.
func TestSucheGegenPython(t *testing.T) {
	index, data := laden(t)
	blob, err := os.ReadFile(filepath.Join("testdata", "searches.json"))
	if err != nil {
		t.Fatal(err)
	}
	var erwartet map[string][]string
	if err := json.Unmarshal(blob, &erwartet); err != nil {
		t.Fatal(err)
	}
	abfragen := make([]string, 0, len(erwartet))
	for q := range erwartet {
		abfragen = append(abfragen, q)
	}
	sort.Strings(abfragen)

	for _, q := range abfragen {
		got := []string{}
		for _, m := range Search(index, data, q, SearchOpts{}) {
			got = append(got, m.Mid)
		}
		want := erwartet[q]
		if len(want) == 0 {
			want = []string{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Abfrage %q:\n  python: %v\n  go:     %v", q, want, got)
		}
	}
}

// TestSuchOptionen deckt ab, was nicht ueber die Textanfrage laeuft.
func TestSuchOptionen(t *testing.T) {
	index, data := laden(t)
	archiv := Archive
	posteingang := Inbox

	if got := len(Search(index, data, "", SearchOpts{Folder: &archiv})); got != 2 {
		t.Errorf("archiv: %d Treffer, erwartet 2", got)
	}
	if got := len(Search(index, data, "", SearchOpts{Folder: &posteingang})); got != 3 {
		t.Errorf("posteingang: %d Treffer, erwartet 3", got)
	}
	for _, f := range Search(index, data, "", SearchOpts{OnlyStar: true}) {
		if !f.Star {
			t.Error("OnlyStar liefert Mail ohne Stern")
		}
	}
	for _, f := range Search(index, data, "", SearchOpts{OnlyUnread: true}) {
		if f.Read {
			t.Error("OnlyUnread liefert gelesene Mail")
		}
	}
	if got := Search(index, data, "", SearchOpts{Tag: "wichtig"}); len(got) != 1 || got[0].Mid != "m1" {
		t.Errorf("Tag-Filter: %v", got)
	}
}

// TestSortierungNeuesteZuerst - die Liste haengt daran, dass Date als Text
// sortierbar ist (RFC 3339 in UTC).
func TestSortierungNeuesteZuerst(t *testing.T) {
	index, data := laden(t)
	hits := Search(index, data, "", SearchOpts{})
	for i := 1; i < len(hits); i++ {
		if hits[i-1].Date < hits[i].Date {
			t.Fatalf("nicht absteigend: %s vor %s", hits[i-1].Date, hits[i].Date)
		}
	}
}
