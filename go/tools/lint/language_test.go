// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package lint holds the checks that watch over the conventions rather than the
// behaviour. They live in their own package because they belong to nothing that
// ships - they walk the source tree instead of calling into it.
package lint

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// German words that are not also English words, and that turn up in prose
// rather than in identifiers. Kept deliberately small: a false alarm in a check
// that gates the pipeline costs more than a missed comment.
var german = regexp.MustCompile(`(?i)\b(nicht|kein|keine|keinen|noch|eine|einen|einem|einer|der|die|das|dem|den|und|oder|fuer|für|aus|ist|sind|wenn|dann|damit|weil|schon|sonst|hier|dort|jede[rsn]?|alle[sn]?|wird|werden|wurde|muss|soll|darf|gibt|steht|liegt|geht|macht|holt|kommt|bleibt|heisst|heißt|dieselbe|derselbe|waere|wäre|kaputt|kaputte|eben|ohne|ueber|über|beim|zum|zur|sich|dass|auch|vom|als|bei|mit|von|man|nur)\b`)

// If an English function word is in the same comment, it is English prose that
// happens to contain something like "die" or "man".
var english = regexp.MustCompile(`(?i)\b(the|and|is|are|was|were|of|to|that|this|with|from|for|not|it|its|a|an|be|been|which|what|who|so|by|only|as|at|on|in|or|but|if|when|then|there|here|has|have|does|do|no|any|all|each|every|would|could|should|can|may|must)\b`)

// TestCommentsAreEnglish holds the convention from CLAUDE.md: identifiers,
// comments and test messages are English, and every user-visible sentence lives
// in the catalogues under i18n/locales.
//
// The check exists because a first sweep missed fourteen comments: it only
// looked at lines starting with "//" and not at the ones where "//" follows
// code, nor at the continuation lines of a block whose first line had been
// translated. This one takes the comment text wherever it sits.
func TestCommentsAreEnglish(t *testing.T) {
	root := moduleRoot(t)
	var found []string

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		blob, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(blob), "\n") {
			text, ok := comment(line)
			if !ok {
				continue
			}
			if german.MatchString(text) && !english.MatchString(text) {
				found = append(found, rel+":"+itoa(i+1)+" "+strings.TrimSpace(text))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		t.Errorf("German comment: %s", f)
	}
}

// comment returns the comment part of a line - after "//" wherever it starts,
// and the bare text of a line inside a /* */ block or below a "//" block, which
// is where the first sweep lost its fourteen.
func comment(line string) (string, bool) {
	if before, after, ok := strings.Cut(line, "//"); ok && !inString(before) {
		return after, true
	}
	trimmed := strings.TrimSpace(line)
	// A continuation line of a comment block: no code, but prose.
	if strings.HasPrefix(trimmed, "*") && !strings.HasPrefix(trimmed, "*/") {
		return strings.TrimPrefix(trimmed, "*"), true
	}
	return "", false
}

// inString says whether the "//" found sits inside a string literal - a URL in
// quotes is not a comment.
func inString(before string) bool {
	quotes, ticks := strings.Count(before, `"`)-strings.Count(before, `\"`), strings.Count(before, "`")
	return quotes%2 == 1 || ticks%2 == 1
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// moduleRoot walks up until it finds go.mod, so the test does not depend on
// where it was started from.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found above the working directory")
		}
		dir = parent
	}
}

// German stems that must not appear in an identifier. A deliberate deny list,
// not a language detector: a heuristic over compound names produces false
// alarms, and a check that cries wolf gets switched off.
//
// It exists because the rename sweep worked one occurrence per pass and left
// wizard.Data.Absender behind - the field carried `json:"from"`, so nothing in
// the interface noticed, and no test looked at the Go name.
var germanStems = []string{
	"Absender", "Empfaenger", "Ordner", "Nachricht", "Datei", "Schluessel",
	"Sperr", "Entwurf", "Gesendet", "Verschieb", "Loesch", "Pruef", "Assistent",
	"Zustand", "Anhang", "Anhaeng", "Betreff", "Postfach", "Kopfzeile", "Suche",
	"Regel", "Zugang", "Fehler", "Meldung", "Antwort", "Sprache", "Konto",
	"Benutzer", "Nutzer", "Verzeichnis", "Zaehler", "Aufruf", "Eintrag",
}

// TestIdentifiersAreEnglish walks the declarations rather than every token: a
// German word inside a string or a comment is somebody else's business, a
// German declaration is ours.
func TestIdentifiersAreEnglish(t *testing.T) {
	root := moduleRoot(t)
	decl := regexp.MustCompile(`^\s*(func|type|var|const)\s+(\([^)]*\)\s+)?([A-Za-z_][A-Za-z0-9_]*)`)
	field := regexp.MustCompile(`^\t([A-Z][A-Za-z0-9_]*)\s+[\[\]*A-Za-z]`)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		blob, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(blob), "\n") {
			name := ""
			if m := decl.FindStringSubmatch(line); m != nil {
				name = m[3]
			} else if m := field.FindStringSubmatch(line); m != nil {
				name = m[1]
			}
			if name == "" {
				continue
			}
			for _, stem := range germanStems {
				if strings.Contains(name, stem) {
					t.Errorf("German identifier: %s:%d %s", rel, i+1, name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
