package main

import (
	"strings"
	"testing"
)

// TestMacCommandForcesANewInstance haelt den Fehler fest, der v0.2.1 unbrauchbar
// machte: ohne "-n" reicht macOS die Adresse an eine bereits laufende
// Browserinstanz weiter ("Wird in einer aktuellen Browsersitzung geoeffnet") und
// verwirft --app= und --user-data-dir. Fuer den, der doppelgeklickt hat, sieht es
// aus, als passiere gar nichts.
func TestMacCommandForcesANewInstance(t *testing.T) {
	prog, args := macCommand("Google Chrome", "http://127.0.0.1:8765/?t=abc")
	if prog != "open" {
		t.Errorf("Programm: %q", prog)
	}
	zusammen := strings.Join(args, " ")
	if !strings.Contains(zusammen, "-na") {
		t.Errorf("ohne -n landet die Adresse in der laufenden Instanz: %q", zusammen)
	}
	if args[1] != "Google Chrome" || args[2] != "--args" {
		t.Errorf("Reihenfolge stimmt nicht: %v", args)
	}
	for _, muss := range []string{"--app=http://127.0.0.1:8765/?t=abc", "--user-data-dir="} {
		if !strings.Contains(zusammen, muss) {
			t.Errorf("%q fehlt in %q", muss, zusammen)
		}
	}
}

// TestWindowArgs - das eigene Profil verhindert, dass s3mail die Sitzung
// eines laufenden Browserfensters mitbenutzt.
func TestWindowArgs(t *testing.T) {
	args := windowArgs("http://x/")
	if len(args) != 3 {
		t.Fatalf("%v", args)
	}
	if !strings.HasPrefix(args[0], "--app=") {
		t.Errorf("--app fehlt an erster Stelle: %v", args)
	}
	if !strings.Contains(args[2], "s3mail-fenster") {
		t.Errorf("eigenes Profilverzeichnis fehlt: %v", args)
	}
}
