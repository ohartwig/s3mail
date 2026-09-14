// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"strings"
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/go/core"
)

func TestAHalfFilledRowFallsOutQuietly(t *testing.T) {
	got := core.CleanFilters([]core.Filter{
		{Name: "Rechnungen", Query: "rechnung has:anhang"},
		{Name: "", Query: "irgendwas"},   // somebody started and stopped
		{Name: "Ohne Suche", Query: " "}, // and here the other way round
	})
	if len(got) != 1 {
		t.Fatalf("%d filters, expected 1: %+v", len(got), got)
	}
	if got[0].Name != "Rechnungen" {
		t.Errorf("the wrong one survived: %+v", got[0])
	}
}

// The whole list is written back at once. An abandoned row must not block
// saving the rest - that would make the sidebar a place where nothing can be
// saved until somebody finds the empty line.
func TestSavingTheRestStillWorks(t *testing.T) {
	got := core.CleanFilters([]core.Filter{
		{Name: "", Query: ""},
		{Name: "A", Query: "a"},
		{Name: "B", Query: "b"},
	})
	if len(got) != 2 {
		t.Errorf("%d filters, expected 2", len(got))
	}
}

// Two entries under one name would show twice in the sidebar and nobody could
// tell which is which.
func TestOneNameOnce(t *testing.T) {
	got := core.CleanFilters([]core.Filter{
		{Name: "Kunde", Query: "from:alt@x.de"},
		{Name: "kunde", Query: "from:neu@x.de"},
	})
	if len(got) != 1 {
		t.Fatalf("%d filters under one name", len(got))
	}
	if got[0].Query != "from:alt@x.de" {
		t.Errorf("the later one won: %+v", got[0])
	}
}

func TestTheLimitsHold(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := core.CleanFilters([]core.Filter{{Name: long, Query: long}})
	if len(got) != 1 {
		t.Fatal("the long one fell out entirely")
	}
	if len(got[0].Name) > core.MaxFilterName || len(got[0].Query) > core.MaxFilterQuery {
		t.Errorf("not cut: name=%d query=%d", len(got[0].Name), len(got[0].Query))
	}

	many := make([]core.Filter, 100)
	for i := range many {
		many[i] = core.Filter{Name: string(rune('a'+i%26)) + strings.Repeat("z", i), Query: "q"}
	}
	if got := core.CleanFilters(many); len(got) > core.MaxFilters {
		t.Errorf("%d filters got through", len(got))
	}
}

// Filters live in the state, so they travel between machines through the op
// log like everything else there.
func TestFiltersGoThroughTheOpLog(t *testing.T) {
	d := core.NewData()
	core.Apply(d, core.Op{T: "filters", Filters: []core.Filter{{Name: "A", Query: "a"}}})
	if len(d.Filters) != 1 || d.Filters[0].Name != "A" {
		t.Fatalf("not applied: %+v", d.Filters)
	}
	// Replacing the whole list is the operation - like the rules.
	core.Apply(d, core.Op{T: "filters", Filters: []core.Filter{{Name: "B", Query: "b"}}})
	if len(d.Filters) != 1 || d.Filters[0].Name != "B" {
		t.Errorf("the list was not replaced: %+v", d.Filters)
	}
}

// A state document from before filters existed has to stay readable.
func TestAnOlderStateStaysReadable(t *testing.T) {
	d := core.NewData()
	d.Filters = nil
	d.Normalize()
	if d.Filters == nil {
		t.Error("Normalize left the list nil - the page would read null.map(...)")
	}
}
