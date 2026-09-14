// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package i18n

import (
	"regexp"
	"strings"
	"testing"
)

// The shipped languages have to agree on their keys. A missing key falls back
// to English, which is survivable - but it means a reader in that language sees
// a sentence in another, and nobody reports that as a bug because the page
// still looks finished.
func TestEveryLanguageCarriesEveryKey(t *testing.T) {
	base := Get(Fallback).All()

	for _, c := range Available() {
		if c.Code == Fallback {
			continue
		}
		for key := range base {
			if _, ok := c.texts[key]; !ok {
				t.Errorf("%s is missing %q", c.Code, key)
			}
		}
		for key := range c.texts {
			if _, ok := base[key]; !ok {
				t.Errorf("%s has %q, which %s does not - a typo, or a key nobody else knows",
					c.Code, key, Fallback)
			}
		}
	}
}

// A placeholder that differs between languages is a crash waiting for the one
// reader who uses that language: Tf would print the wrong argument or none.
func TestPlaceholdersMatchAcrossLanguages(t *testing.T) {
	base := Get(Fallback).All()

	for _, c := range Available() {
		for key, text := range c.texts {
			want, ok := base[key]
			if !ok {
				continue
			}
			if strings.Count(text, "%") != strings.Count(want, "%") {
				t.Errorf("%s/%s: %d placeholders, %s has %d", c.Code, key,
					strings.Count(text, "%"), Fallback, strings.Count(want, "%"))
			}
		}
	}
}

func TestUnknownKeyComesBackVisible(t *testing.T) {
	// Not empty: an empty label is a hole nobody notices, a key on screen is a
	// bug report.
	if got := Get("de").T("nichts.dergleichen"); got != "nichts.dergleichen" {
		t.Errorf("T(unknown) = %q", got)
	}
}

func TestUnknownLanguageFallsBackToEnglish(t *testing.T) {
	for _, code := range []string{"", "fr", "DE-at-x", "  "} {
		got := Get(code)
		if code == "DE-at-x" || strings.TrimSpace(code) == "" || code == "fr" {
			if got.Code != Fallback && got.Code != "de" {
				t.Errorf("Get(%q) = %q", code, got.Code)
			}
		}
	}
	if Get("de").Code != "de" || Get("es").Code != "es" {
		t.Error("a shipped language did not resolve to itself")
	}
}

func TestHeaderPicksTheHighestQuality(t *testing.T) {
	cases := map[string]string{
		"de-AT,de;q=0.9,en;q=0.8": "de", // the region is ignored
		"en-US,en;q=0.9":          "en",
		"fr-FR,fr;q=0.9,es;q=0.8": "es", // French does not exist
		"fr,it":                   "en", // gar nichts davon
		"":                        "en",
		"es;q=0.4,de;q=0.9":       "de", // the order does not decide, q does
		"*":                       "en",
	}
	for header, want := range cases {
		if got := FromHeader(header); got != want {
			t.Errorf("FromHeader(%q) = %q, want %q", header, got, want)
		}
	}
}

// The language list is a menu. Without a fixed order it jumps around between
// calls, which looks like a bug and is one.
func TestAvailableIsStable(t *testing.T) {
	var codes []string
	for _, c := range Available() {
		codes = append(codes, c.Code)
	}
	if strings.Join(codes, ",") != "de,en,es" {
		t.Errorf("Available() = %v", codes)
	}
	for _, c := range Available() {
		if c.Name == "" {
			t.Errorf("%s has no display name", c.Code)
		}
	}
}

// Every key the pages use has to exist. A typo shows the key on screen, which
// is visible - but only to whoever opens that dialog in that language.
func TestPagesUseOnlyKnownKeys(t *testing.T) {
	// The check itself lives in web/, which knows the pages; here it is enough
	// that the catalogue is loadable and non-trivial. A catalogue that silently
	// came back empty would make every text fall through to its key.
	if len(Get("en").All()) < 50 {
		t.Errorf("the English catalogue holds only %d texts", len(Get("en").All()))
	}
}

// TestNoMarkupInTheCatalogue keeps the trap shut that showed up in the Spanish
// interface: two hints carried <code> tags, and the setup page renders through
// html/template, which escapes them. The reader then saw the tags as text.
//
// The same string reached the mailbox page through innerHTML and looked right
// there - so the mistake was invisible in one place and glaring in the other.
// Markup belongs in the template, text in the catalogue.
func TestNoMarkupInTheCatalogue(t *testing.T) {
	tag := regexp.MustCompile(`<[a-zA-Z/]`)
	for _, cat := range Available() {
		for key, text := range cat.All() {
			if tag.MatchString(text) {
				t.Errorf("%s/%s carries markup: %q", cat.Code, key, text)
			}
		}
	}
}

func TestFromEnvReadsThePosixOrder(t *testing.T) {
	for _, c := range []struct {
		name              string
		all, messages, la string
		want              string
	}{
		{"LC_ALL wins", "es_ES.UTF-8", "de_DE.UTF-8", "de_DE.UTF-8", "es"},
		{"LC_MESSAGES over LANG", "", "es_ES.UTF-8", "de_DE.UTF-8", "es"},
		{"LANG when alone", "", "", "de_DE.UTF-8", "de"},
		{"region is dropped", "", "", "de_AT", "de"},
		{"no codeset", "", "", "es", "es"},
		{"C is not a language", "C", "", "", Fallback},
		{"POSIX neither", "POSIX", "", "", Fallback},
		// A language with no catalog must not win over one that has it -
		// otherwise a French machine gets English while LANG still says fr.
		{"unknown falls through", "fr_FR.UTF-8", "", "de_DE.UTF-8", "de"},
		{"nothing set", "", "", "", Fallback},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("LC_ALL", c.all)
			t.Setenv("LC_MESSAGES", c.messages)
			t.Setenv("LANG", c.la)
			if got := FromEnv(); got != c.want {
				t.Errorf("FromEnv() = %q, want %q", got, c.want)
			}
		})
	}
}
