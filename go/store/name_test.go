package store

import (
	"strings"
	"testing"
	"time"
)

// TestOpNamesSort - die lexikografische Ordnung der Op-Namen muss der
// Schreibreihenfolge entsprechen, sonst spielt ein Rechner die Aenderungen eines
// anderen in der falschen Reihenfolge ab.
func TestOpNamesSort(t *testing.T) {
	s := &State{instanz: "aaaaaaaaaaaa"}
	n := 0
	s.Now = func() time.Time { n++; return time.Date(2026, 8, 21, 10, 0, n, 0, time.UTC) }
	var vorher string
	for i := 0; i < 5; i++ {
		name := s.opName()
		if !strings.HasSuffix(name, ".json") {
			t.Fatalf("%q", name)
		}
		if name <= vorher {
			t.Fatalf("nicht aufsteigend: %q nach %q", name, vorher)
		}
		vorher = name
	}
}

// TestInstanceIDIsUnique - ohne das koennten zwei Rechner in derselben
// Mikrosekunde denselben Op-Namen erzeugen, und einer waere weg.
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
