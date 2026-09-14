// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package i18n holds the interface texts in the languages s3mail ships with.
//
// The catalogues are plain JSON files, compiled into the binary. That is a
// deliberate limit: a mail client that fetches its own wording at runtime has
// one more thing that can be missing, and this one is meant to work on a
// laptop with the network off.
//
// Keys read like the thing they name, not like where they sit ("inbox.reply",
// not "button7"). A key that survives a redesign is worth more than a short one.
package i18n

import (
	"cmp"
	"embed"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
)

//go:embed locales/*.json
var files embed.FS

// Fallback is the language every other one is measured against: it is complete
// by definition, and a missing key elsewhere falls back to it rather than
// showing the reader a bare key.
const Fallback = "en"

// Catalog is one language: key to text.
type Catalog struct {
	Code  string
	Name  string
	texts map[string]string
	base  map[string]string
}

// T returns the text for a key. A key nobody translated yet falls back to
// English; a key nobody defined at all comes back as itself, visibly wrong -
// which is better than an empty label nobody notices.
func (c Catalog) T(key string) string {
	if v, ok := c.texts[key]; ok && v != "" {
		return v
	}
	if v, ok := c.base[key]; ok && v != "" {
		return v
	}
	return key
}

// Tf is T with arguments, for the handful of texts that carry a number or a name.
func (c Catalog) Tf(key string, args ...any) string {
	return fmt.Sprintf(c.T(key), args...)
}

// All returns every text of this catalogue, English filled in where this
// language has none. The interface script gets this as JSON: the strings it
// builds at runtime cannot come from the template.
func (c Catalog) All() map[string]string {
	out := make(map[string]string, len(c.base))
	for k, v := range c.base {
		out[k] = v
	}
	for k, v := range c.texts {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

var (
	catalogs = map[string]Catalog{}
	names    = map[string]string{"de": "Deutsch", "en": "English", "es": "Español"}
)

func init() {
	base := load(Fallback)
	for _, code := range []string{"de", "en", "es"} {
		catalogs[code] = Catalog{Code: code, Name: names[code], texts: load(code), base: base}
	}
}

func load(code string) map[string]string {
	raw, err := files.ReadFile("locales/" + code + ".json")
	if err != nil {
		// Cannot happen with an embedded file, and if it ever does, an empty
		// catalogue keeps the program running with English texts.
		return map[string]string{}
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil {
		return map[string]string{}
	}
	return m
}

// Get returns the catalogue for a language code, English if it is unknown.
func Get(code string) Catalog {
	if c, ok := catalogs[strings.ToLower(strings.TrimSpace(code))]; ok {
		return c
	}
	return catalogs[Fallback]
}

// Available lists the shipped languages, for the picker in the interface.
func Available() []Catalog {
	return slices.SortedFunc(maps.Values(catalogs), func(a, b Catalog) int {
		return cmp.Compare(a.Code, b.Code)
	})
}

// FromHeader picks a language from an Accept-Language header.
//
// Only the language tag is looked at, not the region: somebody asking for
// de-AT gets German, and nobody has to maintain a second German for it. The
// quality values decide the order, as the header says they should - a browser
// set to Spanish with English as a fallback should get Spanish, not whichever
// came first in the string.
func FromHeader(header string) string {
	type wish struct {
		code string
		q    float64
	}
	var wishes []wish

	for part := range strings.SplitSeq(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		code, rest, _ := strings.Cut(part, ";")
		code, _, _ = strings.Cut(strings.TrimSpace(code), "-")
		code = strings.ToLower(code)
		q := 1.0
		if rest = strings.TrimSpace(rest); strings.HasPrefix(rest, "q=") {
			if _, err := fmt.Sscanf(rest[2:], "%f", &q); err != nil {
				q = 1.0
			}
		}
		wishes = append(wishes, wish{code, q})
	}

	slices.SortStableFunc(wishes, func(a, b wish) int { return cmp.Compare(b.q, a.q) })
	for _, w := range wishes {
		if _, ok := catalogs[w.code]; ok {
			return w.code
		}
	}
	return Fallback
}

// FromEnv picks a language from the environment, for the moment before there
// is a browser to ask.
//
// At startup nothing has been requested yet, so FromHeader has nothing to work
// on - and falling straight back to English gives a German machine a German
// interface with English sample mail, which looks like a bug because it is one.
//
// The POSIX order is LC_ALL over LC_MESSAGES over LANG, and the value looks
// like "de_DE.UTF-8": the language is what comes before the underscore. "C"
// and "POSIX" mean "no language chosen" and are left alone rather than being
// read as a country.
func FromEnv() string {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		value := os.Getenv(name)
		if value == "" || value == "C" || value == "POSIX" {
			continue
		}
		code, _, _ := strings.Cut(value, ".")
		code, _, _ = strings.Cut(code, "_")
		code = strings.ToLower(strings.TrimSpace(code))
		if _, ok := catalogs[code]; ok {
			return code
		}
	}
	return Fallback
}
