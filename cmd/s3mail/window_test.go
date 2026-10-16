// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"runtime"
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
	if !strings.Contains(args[2], "s3mail") {
		t.Errorf("a profile directory of its own is missing: %v", args)
	}
}

// The second start is the one somebody was annoyed enough to report: every
// click left another browser beside the last, while the window they wanted sat
// open behind them.
//
// Raising is by process id, and the test is here because the obvious way is
// wrong in a way nobody notices on their own machine: `open -a "Google Chrome"`
// raises whichever Chrome the person already had open, not ours. Both are the
// same bundle. Measured on a real machine - the front process stayed the other
// one.
func TestTheWindowIsRaisedByProcessAndNotByName(t *testing.T) {
	script := raiseScript(31053)

	if !strings.Contains(script, "unix id is 31053") {
		t.Errorf("not addressed by process id: %q", script)
	}
	if strings.Contains(script, "Google Chrome") || strings.Contains(script, "open -a") {
		t.Errorf("addressed by application - that raises the wrong instance: %q", script)
	}
	if !strings.Contains(script, "frontmost") {
		t.Errorf("nothing in here brings a window forward: %q", script)
	}
}

// The profile path is what tells our window from any other browser. If it stops
// being distinctive, appModePID starts answering about somebody else's browser
// - and a start would raise a stranger's window instead of opening a mailbox.
func TestTheProfilePathIsOurs(t *testing.T) {
	if !strings.Contains(profileDir(), "s3mail") {
		t.Errorf("%q says nothing about who it belongs to", profileDir())
	}
	if !strings.Contains(strings.Join(windowArgs("http://x/"), " "), profileDir()) {
		t.Error("the window starts on a different profile than the one we look for")
	}
}

// The profile holds the session cookie. It used to live under a fixed name in
// os.TempDir(), which on Linux is /tmp - a directory another user can plant
// first. Now it is under the user's own cache directory, made 0700, and
// anything else standing at that path is refused (AUD-008-F04).
func TestTheProfileDirIsUnderTheUserAndOursAlone(t *testing.T) {
	if !strings.Contains(profileDir(), "s3mail") || strings.HasPrefix(profileDir(), os.TempDir()+string(filepath.Separator)) {
		t.Errorf("profile at %q - that is not the user's cache directory", profileDir())
	}

	base := t.TempDir()
	dir := filepath.Join(base, "s3mail", "window")
	if err := prepareProfileDir(dir); err != nil {
		t.Fatalf("a fresh directory was refused: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("created with %v, expected 0700", info.Mode().Perm())
	}
	if err := prepareProfileDir(dir); err != nil {
		t.Errorf("our own directory was refused the second time: %v", err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := prepareProfileDir(dir); err == nil {
			t.Error("a directory others may enter was accepted")
		}
		_ = os.Chmod(dir, 0o700)
	}

	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareProfileDir(file); err == nil {
		t.Error("a file at the profile path was accepted")
	}

	link := filepath.Join(base, "link")
	if err := os.Symlink(dir, link); err == nil {
		if err := prepareProfileDir(link); err == nil {
			t.Error("a symlink at the profile path was accepted")
		}
	}

	if err := prepareProfileDir(""); err == nil {
		t.Error("an empty path was accepted")
	}
}
