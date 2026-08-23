package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestFolderNamesAgainstPython: dieselben Namen, dasselbe Urteil.
func TestFolderNamesAgainstPython(t *testing.T) {
	blob, err := os.ReadFile(filepath.Join("testdata", "folders.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]struct {
		OK    bool   `json:"ok"`
		Value string `json:"wert"`
	}
	if err := json.Unmarshal(blob, &want); err != nil {
		t.Fatal(err)
	}
	for name, w := range want {
		got, err := ValidFolder(name)
		if (err == nil) != w.OK {
			t.Errorf("%q: python ok=%v, go ok=%v (%v)", name, w.OK, err == nil, err)
			continue
		}
		if err == nil && got != w.Value {
			t.Errorf("%q: python=%q go=%q", name, w.Value, got)
		}
	}
}

func TestKeysAndFolders(t *testing.T) {
	s := NewStore("mail/")
	cases := []struct{ key, mid, folder string }{
		{"mail/m1", "m1", Inbox},
		{"mail/archiv/alt1", "alt1", Archive},
		{"mail/trash/x", "x", Trash},
		{"mail/kunden/a b.eml", "a b.eml", "kunden"},
	}
	for _, f := range cases {
		if got := s.Mid(f.key); got != f.mid {
			t.Errorf("Mid(%q)=%q, expected %q", f.key, got, f.mid)
		}
		if got := s.FolderOf(f.key); got != f.folder {
			t.Errorf("FolderOf(%q)=%q, expected %q", f.key, got, f.folder)
		}
		back, err := s.KeyFor(f.mid, f.folder)
		if err != nil || back != f.key {
			t.Errorf("KeyFor(%q,%q)=%q,%v - expected %q", f.mid, f.folder, back, err, f.key)
		}
	}
	// a root without a slash gets one
	if NewStore("mail").Root != "mail/" {
		t.Error("Root was not normalised")
	}
}

// TestSecurityBoundary - this is what keeps anybody from reaching the state or
// foreign prefixes through the API.
func TestSecurityBoundary(t *testing.T) {
	s := NewStore("mail/")
	allowed := []string{"mail/m1", "mail/archiv/a", "mail/kunden/x.eml"}
	forbidden := []string{
		"andere/nicht-meins",                // outside the prefix
		"mail/.s3mail-state.json",           // Snapshot
		"mail/.s3mail-state/2026-abc.json",  // Op-Objekt
		"mail/.versteckt",                   // Punktdatei
		"mail/.s3mail-state/tief/drin.json", // in the ops folder
	}
	for _, k := range allowed {
		if err := s.Own(k); err != nil {
			t.Errorf("%q should be allowed: %v", k, err)
		}
	}
	for _, k := range forbidden {
		if err := s.Own(k); err == nil {
			t.Errorf("%q haette abgelehnt werden muessen", k)
		}
	}
}

func TestFolderCounts(t *testing.T) {
	index, data := load(t)
	f := Folders(index, data)
	if len(f) < 4 {
		t.Fatalf("Systemordner fehlen: %v", f)
	}
	for i, sf := range SystemFolders {
		if f[i].Name != sf.Name || !f[i].System {
			t.Errorf("position %d: %q, expected %q", i, f[i].Name, sf.Name)
		}
	}
	to := map[string]FolderInfo{}
	for _, x := range f {
		to[x.Name] = x
	}
	if to[Inbox].Count != 3 {
		t.Errorf("inbox: %d, expected 3", to[Inbox].Count)
	}
	if to[Archive].Count != 2 {
		t.Errorf("archive: %d, expected 2", to[Archive].Count)
	}
	// m2 was marked read and lies in the archive
	if to[Archive].Unread != 1 {
		t.Errorf("archive unread: %d, expected 1", to[Archive].Unread)
	}
}
