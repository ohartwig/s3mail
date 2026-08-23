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
		OK   bool   `json:"ok"`
		Wert string `json:"wert"`
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
		if err == nil && got != w.Wert {
			t.Errorf("%q: python=%q go=%q", name, w.Wert, got)
		}
	}
}

func TestKeysAndFolders(t *testing.T) {
	s := NewStore("mail/")
	faelle := []struct{ key, mid, folder string }{
		{"mail/m1", "m1", Inbox},
		{"mail/archiv/alt1", "alt1", Archive},
		{"mail/trash/x", "x", Trash},
		{"mail/kunden/a b.eml", "a b.eml", "kunden"},
	}
	for _, f := range faelle {
		if got := s.Mid(f.key); got != f.mid {
			t.Errorf("Mid(%q)=%q, erwartet %q", f.key, got, f.mid)
		}
		if got := s.FolderOf(f.key); got != f.folder {
			t.Errorf("FolderOf(%q)=%q, erwartet %q", f.key, got, f.folder)
		}
		zurueck, err := s.KeyFor(f.mid, f.folder)
		if err != nil || zurueck != f.key {
			t.Errorf("KeyFor(%q,%q)=%q,%v - erwartet %q", f.mid, f.folder, zurueck, err, f.key)
		}
	}
	// Wurzel ohne Schraegstrich wird ergaenzt
	if NewStore("mail").Root != "mail/" {
		t.Error("Root wurde nicht normalisiert")
	}
}

// TestSicherheitsgrenze - hier haengt dran, dass ueber die API niemand an den
// Zustand oder an fremde Prefixe kommt.
func TestSicherheitsgrenze(t *testing.T) {
	s := NewStore("mail/")
	erlaubt := []string{"mail/m1", "mail/archiv/a", "mail/kunden/x.eml"}
	verboten := []string{
		"andere/nicht-meins",                // ausserhalb des Prefix
		"mail/.s3mail-state.json",           // Snapshot
		"mail/.s3mail-state/2026-abc.json",  // Op-Objekt
		"mail/.versteckt",                   // Punktdatei
		"mail/.s3mail-state/tief/drin.json", // im Ops-Ordner
	}
	for _, k := range erlaubt {
		if err := s.Own(k); err != nil {
			t.Errorf("%q sollte erlaubt sein: %v", k, err)
		}
	}
	for _, k := range verboten {
		if err := s.Own(k); err == nil {
			t.Errorf("%q haette abgelehnt werden muessen", k)
		}
	}
}

func TestFolderCounts(t *testing.T) {
	index, data := laden(t)
	f := Folders(index, data)
	if len(f) < 4 {
		t.Fatalf("Systemordner fehlen: %v", f)
	}
	for i, sf := range SystemFolders {
		if f[i].Name != sf.Name || !f[i].System {
			t.Errorf("Position %d: %q, erwartet %q", i, f[i].Name, sf.Name)
		}
	}
	nach := map[string]FolderInfo{}
	for _, x := range f {
		nach[x.Name] = x
	}
	if nach[Inbox].Count != 3 {
		t.Errorf("Posteingang: %d, erwartet 3", nach[Inbox].Count)
	}
	if nach[Archive].Count != 2 {
		t.Errorf("Archiv: %d, erwartet 2", nach[Archive].Count)
	}
	// m2 wurde gelesen markiert und liegt im Archiv
	if nach[Archive].Unread != 1 {
		t.Errorf("Archiv ungelesen: %d, erwartet 1", nach[Archive].Unread)
	}
}
