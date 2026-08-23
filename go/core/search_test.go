package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func load(t *testing.T) ([]Message, *Data) {
	t.Helper()
	var index []Message
	var data Data
	for _, p := range []struct {
		file   string
		target any
	}{{"index.json", &index}, {"state.json", &data}} {
		blob, err := os.ReadFile(filepath.Join("testdata", p.file))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(blob, p.target); err != nil {
			t.Fatalf("%s: %v", p.file, err)
		}
	}
	return index, data.Normalize()
}

// TestSearchAgainstPython runs the same queries as the Python version and
// compares hits *and* order.
func TestSearchAgainstPython(t *testing.T) {
	index, data := load(t)
	blob, err := os.ReadFile(filepath.Join("testdata", "searches.json"))
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string][]string
	if err := json.Unmarshal(blob, &expected); err != nil {
		t.Fatal(err)
	}
	query := make([]string, 0, len(expected))
	for q := range expected {
		query = append(query, q)
	}
	sort.Strings(query)

	for _, q := range query {
		got := []string{}
		for _, m := range Search(index, data, q, SearchOpts{}) {
			got = append(got, m.Mid)
		}
		want := expected[q]
		if len(want) == 0 {
			want = []string{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Abfrage %q:\n  python: %v\n  go:     %v", q, want, got)
		}
	}
}

// TestSearchOptions covers what does not run through the text query.
func TestSearchOptions(t *testing.T) {
	index, data := load(t)
	archiv := Archive
	inboxName := Inbox

	if got := len(Search(index, data, "", SearchOpts{Folder: &archiv})); got != 2 {
		t.Errorf("archiv: %d hits, expected 2", got)
	}
	if got := len(Search(index, data, "", SearchOpts{Folder: &inboxName})); got != 3 {
		t.Errorf("inbox: %d hits, expected 3", got)
	}
	for _, f := range Search(index, data, "", SearchOpts{OnlyStar: true}) {
		if !f.Star {
			t.Error("OnlyStar returns a message without a star")
		}
	}
	for _, f := range Search(index, data, "", SearchOpts{OnlyUnread: true}) {
		if f.Read {
			t.Error("OnlyUnread returns a read message")
		}
	}
	if got := Search(index, data, "", SearchOpts{Tag: "wichtig"}); len(got) != 1 || got[0].Mid != "m1" {
		t.Errorf("tag filter: %v", got)
	}
}

// TestSortingNewestFirst - the list hangs on Date being sortable as text
// (RFC 3339 in UTC).
func TestSortingNewestFirst(t *testing.T) {
	index, data := load(t)
	hits := Search(index, data, "", SearchOpts{})
	for i := 1; i < len(hits); i++ {
		if hits[i-1].Date < hits[i].Date {
			t.Fatalf("not descending: %s before %s", hits[i-1].Date, hits[i].Date)
		}
	}
}
