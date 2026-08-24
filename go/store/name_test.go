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
