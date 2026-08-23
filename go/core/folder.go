package core

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Folders are real S3 prefixes below the root. What sits directly under it is
// the inbox; anything with a slash is a subfolder.
const (
	Inbox   = ""
	Trash   = "trash"
	Spam    = "spam"
	Archive = "archiv"
)

// SystemFolder carries a translation key, not a label. The Name is an S3
// prefix and must never be translated: rename it and a client in another
// language stops finding the mail a colleague filed. What the reader sees is
// the label, and that is looked up per request.
//
// Archive is spelled "archiv" for the same reason - it is a prefix that exists
// in live mailboxes, not a word on a screen.
type SystemFolder struct {
	Name, LabelKey, Icon string
}

var SystemFolders = []SystemFolder{
	{Inbox, "folder.inbox", "\U0001F4E5"},
	{Archive, "folder.archive", "\U0001F4E6"},
	{Spam, "folder.spam", "⚠️"},
	{Trash, "folder.trash", "\U0001F5D1️"},
}

var folderRe = regexp.MustCompile(`^[\p{L}][\p{L}\p{N}_ .-]{0,39}$`)

// ErrBadInput marks everything the caller got wrong: an impossible folder name,
// a key outside the prefix, one of our own files. The HTTP layer turns exactly
// these into a 400 - it used to look for German words in the message, which was
// a translation away from silently becoming a 502.
var ErrBadInput = errors.New("invalid input")

// ValidFolder checks a folder name. The inbox is the empty name.
func ValidFolder(name string) (string, error) {
	name = strings.Trim(strings.TrimSpace(name), "/")
	if name == "" {
		return Inbox, nil
	}
	if strings.Contains(name, "/") || name == "." || name == ".." || !folderRe.MatchString(name) {
		return "", fmt.Errorf("%w: folder name %q", ErrBadInput, name)
	}
	return name, nil
}

// Store holds the folder and key logic. Root always ends in "/", unless it is
// empty.
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

// Mid is the base name - the key the state hangs on.
func (s *Store) Mid(key string) string {
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return key
}

// FolderOf reads the folder out of a key.
func (s *Store) FolderOf(key string) string {
	rest := strings.TrimPrefix(key, s.Root)
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		return rest[:i]
	}
	return Inbox
}

// KeyFor builds the key from base name and folder.
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

// Internal is true for everything s3mail itself stores in the bucket: the
// snapshot and the ops folder below it. If any path segment under the root
// starts with a dot, it is ours - not the mailbox's.
func (s *Store) Internal(key string) bool {
	for _, part := range strings.Split(strings.TrimPrefix(key, s.Root), "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

// Own is the security boundary: no key outside the prefix, nothing internal.
func (s *Store) Own(key string) error {
	if !strings.HasPrefix(key, s.Root) {
		return fmt.Errorf("%w: the key lies outside the prefix", ErrBadInput)
	}
	if s.Internal(key) {
		return fmt.Errorf("%w: internal file", ErrBadInput)
	}
	return nil
}

type FolderInfo struct {
	Name string `json:"name"`
	// Label holds the translation key for a system folder and the plain name
	// for one somebody created. The web layer turns the first into text.
	Label  string `json:"label"`
	Icon   string `json:"icon"`
	System bool   `json:"system"`
	Count  int    `json:"count"`
	Unread int    `json:"unread"`
}

// Folders counts per folder - system folders first and always visible, then
// the ones somebody created, in alphabetical order.
func Folders(index []Message, d *Data) []FolderInfo {
	type counter struct{ count, unread int }
	counts := map[string]*counter{}
	for _, m := range index {
		c, ok := counts[m.Folder]
		if !ok {
			c = &counter{}
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
			c = &counter{}
		}
		out = append(out, FolderInfo{sf.Name, sf.LabelKey, sf.Icon, true, c.count, c.unread})
	}
	own := make([]string, 0, len(counts))
	for name := range counts {
		if !system[name] {
			own = append(own, name)
		}
	}
	sort.Strings(own)
	for _, name := range own {
		c := counts[name]
		out = append(out, FolderInfo{name, name, "\U0001F4C1", false, c.count, c.unread})
	}
	return out
}
