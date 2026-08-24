// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"strings"
	"testing"
	"time"
)

// TestOpNamesSort - the lexicographic order of the op names has to match the
// write order, or one machine replays another machine's changes in the wrong
// order.
func TestOpNamesSort(t *testing.T) {
	s := &State{instance: "aaaaaaaaaaaa"}
	n := 0
	s.Now = func() time.Time { n++; return time.Date(2026, 8, 21, 10, 0, n, 0, time.UTC) }
	var before string
	for i := 0; i < 5; i++ {
		name := s.opName()
		if !strings.HasSuffix(name, ".json") {
			t.Fatalf("%q", name)
		}
		if name <= before {
			t.Fatalf("not ascending: %q after %q", name, before)
		}
		before = name
	}
}

// TestInstanceIDIsUnique - without it two machines could produce the same op
// name in the same microsecond, and one would be gone.
func TestInstanceIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		k := InstanceID()
		if seen[k] {
			t.Fatalf("Kennung %q zweimal", k)
		}
		seen[k] = true
	}
}

// TestBaseFromIDSurvivesAnything - the name a self-written message is stored under
// comes from its Message-ID. Not every sender sets one, and the ones that do
// put things in it that have no business in an S3 key. A collision here would
// overwrite a stored draft with a different one.
func TestBaseFromIDSurvivesAnything(t *testing.T) {
	cases := []struct{ name, id, want string }{
		{"gewoehnlich", "<abc123@firma.de>", "abc123.eml"},
		{"ohne Klammern", "abc123@firma.de", "abc123.eml"},
		{"mit Leerraum", "  <abc123@firma.de>  ", "abc123.eml"},
		{"Schraegstrich im Namen", "<a/b/../c@firma.de>", "ab..c.eml"},
		{"Umlaute", "<grüße@firma.de>", "gre.eml"},
	}
	for _, f := range cases {
		if got := baseFromID(f.id); got != f.want {
			t.Errorf("%s: baseFromID(%q) = %q, expected %q", f.name, f.id, got, f.want)
		}
	}

	// No Message-ID at all, and a header that boils down to nothing: both have
	// to yield a usable name rather than ".eml", which every message would then
	// share.
	for _, empty := range []string{"", "<>", "<@firma.de>", "<///>"} {
		got := baseFromID(empty)
		if got == ".eml" || !strings.HasSuffix(got, ".eml") || len(got) < 6 {
			t.Errorf("baseFromID(%q) = %q - that is not a name of its own", empty, got)
		}
	}
	if baseFromID("") == baseFromID("") {
		t.Log("two calls in the same nanosecond give the same name - " +
			"acceptable, they would be the same draft saved twice")
	}
}
