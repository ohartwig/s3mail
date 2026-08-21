package store

import (
	"strings"
	"testing"
	"time"
)

// TestOpNamenSindSortierbar - die lexikografische Ordnung der Op-Namen muss der
// Schreibreihenfolge entsprechen, sonst spielt ein Rechner die Aenderungen eines
// anderen in der falschen Reihenfolge ab.
func TestOpNamenSindSortierbar(t *testing.T) {
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

// TestInstanzKennungIstEinmalig - ohne das koennten zwei Rechner in derselben
// Mikrosekunde denselben Op-Namen erzeugen, und einer waere weg.
func TestInstanzKennungIstEinmalig(t *testing.T) {
	gesehen := map[string]bool{}
	for i := 0; i < 200; i++ {
		k := InstanzKennung()
		if gesehen[k] {
			t.Fatalf("Kennung %q zweimal", k)
		}
		gesehen[k] = true
	}
}
