package main

import (
	"strings"
	"testing"
)

// TestMacCommandForcesANewInstance pins down the bug that made v0.2.1
// unusable: without "-n" macOS hands the address to a browser instance that
// already runs ("opened in a current browser session") and discards --app= and
// --user-data-dir. To whoever double clicked, it looks like nothing happens at
// all.
func TestMacCommandForcesANewInstance(t *testing.T) {
	prog, args := macCommand("Google Chrome", "http://127.0.0.1:8765/?t=abc")
	if prog != "open" {
		t.Errorf("Programm: %q", prog)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-na") {
		t.Errorf("without -n the address lands in the running instance: %q", joined)
	}
	if args[1] != "Google Chrome" || args[2] != "--args" {
		t.Errorf("the order is wrong: %v", args)
	}
	for _, must := range []string{"--app=http://127.0.0.1:8765/?t=abc", "--user-data-dir="} {
		if !strings.Contains(joined, must) {
			t.Errorf("%q missing from %q", must, joined)
		}
	}
}

// TestWindowArgs - a profile of its own keeps s3mail from sharing the session
// of a running browser window.
func TestWindowArgs(t *testing.T) {
	args := windowArgs("http://x/")
	if len(args) != 3 {
		t.Fatalf("%v", args)
	}
	if !strings.HasPrefix(args[0], "--app=") {
		t.Errorf("--app is missing from the first position: %v", args)
	}
	if !strings.Contains(args[2], "s3mail-fenster") {
		t.Errorf("a profile directory of its own is missing: %v", args)
	}
}
