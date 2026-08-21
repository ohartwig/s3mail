package core

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Ordner sind echte S3-Prefixe unter der Wurzel. Was direkt darunter liegt, ist
// der Posteingang; alles mit einem Schraegstrich ist ein Unterordner.
const (
	Inbox   = ""
	Trash   = "trash"
	Spam    = "spam"
	Archive = "archiv"
)

type SystemFolder struct {
	Name, Label, Icon string
}

var SystemFolders = []SystemFolder{
	{Inbox, "Posteingang", "\U0001F4E5"},
	{Archive, "Archiv", "\U0001F4E6"},
	{Spam, "Spam", "⚠️"},
	{Trash, "Papierkorb", "\U0001F5D1️"},
}

var folderRe = regexp.MustCompile(`^[\p{L}][\p{L}\p{N}_ .-]{0,39}$`)

// ValidFolder prueft einen Ordnernamen. Der Posteingang ist der leere Name.
func ValidFolder(name string) (string, error) {
	name = strings.Trim(strings.TrimSpace(name), "/")
	if name == "" {
		return Inbox, nil
	}
	if strings.Contains(name, "/") || name == "." || name == ".." || !folderRe.MatchString(name) {
		return "", fmt.Errorf("ungueltiger Ordnername: %q", name)
	}
	return name, nil
}

// Store haelt die Ordner- und Schluessellogik. Root endet immer auf "/", ausser
// er ist leer.
type Store struct {
	Root string
}

func NewStore(root string) *Store {
	root = strings.TrimLeft(root, "/")
	if root != "" && !strings.HasSuffix(root, "/") {
		root += "/"
	}
	return &Store{Root: root}
}

// Mid ist der Basename - der Schluessel, unter dem der Zustand haengt.
func (s *Store) Mid(key string) string {
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return key
}

// FolderOf liest den Ordner aus dem Key.
func (s *Store) FolderOf(key string) string {
	rest := strings.TrimPrefix(key, s.Root)
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		return rest[:i]
	}
	return Inbox
}

// KeyFor baut den Key aus Basename und Ordner.
func (s *Store) KeyFor(mid, folder string) (string, error) {
	f, err := ValidFolder(folder)
	if err != nil {
		return "", err
	}
	if f == "" {
		return s.Root + mid, nil
	}
	return s.Root + f + "/" + mid, nil
}

// Internal ist wahr fuer alles, was s3mail selbst im Bucket ablegt: den Snapshot
// und den Ops-Ordner darunter. Beginnt irgendein Pfadsegment unter der Wurzel mit
// einem Punkt, gehoert es uns - nicht dem Postfach.
func (s *Store) Internal(key string) bool {
	for _, teil := range strings.Split(strings.TrimPrefix(key, s.Root), "/") {
		if strings.HasPrefix(teil, ".") {
			return true
		}
	}
	return false
}

// Own ist die Sicherheitsgrenze: kein Key ausserhalb des Prefix, nichts Internes.
func (s *Store) Own(key string) error {
	if !strings.HasPrefix(key, s.Root) {
		return fmt.Errorf("Key liegt ausserhalb des Prefix")
	}
	if s.Internal(key) {
		return fmt.Errorf("interne Datei")
	}
	return nil
}

type FolderInfo struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Icon   string `json:"icon"`
	System bool   `json:"system"`
	Count  int    `json:"count"`
	Unread int    `json:"unread"`
}

// Folders zaehlt pro Ordner - Systemordner zuerst und immer sichtbar, danach die
// selbst angelegten in alphabetischer Reihenfolge.
func Folders(index []Message, d *Data) []FolderInfo {
	type zaehler struct{ count, unread int }
	counts := map[string]*zaehler{}
	for _, m := range index {
		c, ok := counts[m.Folder]
		if !ok {
			c = &zaehler{}
			counts[m.Folder] = c
		}
		c.count++
		if !d.Get(m.Mid).Read {
			c.unread++
		}
	}
	system := map[string]bool{}
	out := make([]FolderInfo, 0, len(counts)+len(SystemFolders))
	for _, sf := range SystemFolders {
		system[sf.Name] = true
		c := counts[sf.Name]
		if c == nil {
			c = &zaehler{}
		}
		out = append(out, FolderInfo{sf.Name, sf.Label, sf.Icon, true, c.count, c.unread})
	}
	eigene := make([]string, 0, len(counts))
	for name := range counts {
		if !system[name] {
			eigene = append(eigene, name)
		}
	}
	sort.Strings(eigene)
	for _, name := range eigene {
		c := counts[name]
		out = append(out, FolderInfo{name, name, "\U0001F4C1", false, c.count, c.unread})
	}
	return out
}
