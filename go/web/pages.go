// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
	"sync"

	"git.ole-hartwig.eu/development/s3mail/s3mail/go/i18n"
)

// Pages are templates now, one rendering per language, built on first use and
// kept. Two things follow from that and both are deliberate:
//
// The texts arrive through {{t "key"}} at render time, so the markup carries no
// language at all. And the script gets the same catalogue as JSON under
// __I18N__, because the strings it builds while running - "3 messages moved" -
// cannot come from a template that ran before the click.
//
// Rendering once per language rather than per request matters on the machine
// this runs on: a laptop, next to the browser, where a page that takes a
// millisecond too long is a page that feels slow.
type pageCache struct {
	mu   sync.Mutex
	done map[string]string
}

var rendered = pageCache{done: map[string]string{}}

// page renders one of the embedded pages in the given language.
func page(name, source, lang string, config map[string]any) string {
	cat := i18n.Get(lang)
	key := name + "/" + cat.Code

	rendered.mu.Lock()
	if out, ok := rendered.done[key]; ok && config == nil {
		rendered.mu.Unlock()
		return out
	}
	rendered.mu.Unlock()

	texts, _ := json.Marshal(cat.All())
	languages, _ := json.Marshal(languageList())

	tpl, err := template.New(name).Funcs(template.FuncMap{
		"t": cat.T,
	}).Parse(source)
	if err != nil {
		// A broken template is a programming error, not a runtime condition.
		// Returning the raw source keeps the page usable in the shipped
		// language rather than serving a blank screen.
		return source
	}

	var buf bytes.Buffer
	if tpl.Execute(&buf, map[string]any{"Lang": cat.Code}) != nil {
		return source
	}

	out := buf.String()
	out = strings.ReplaceAll(out, "__I18N__", string(texts))
	out = strings.ReplaceAll(out, "__LANGUAGES__", string(languages))

	if config != nil {
		raw, _ := json.Marshal(config)
		out = strings.ReplaceAll(out, "__CONFIG__", string(raw))
		return out
	}

	rendered.mu.Lock()
	rendered.done[key] = out
	rendered.mu.Unlock()

	return out
}

type languageEntry struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func languageList() []languageEntry {
	out := make([]languageEntry, 0, 3)
	for _, c := range i18n.Available() {
		out = append(out, languageEntry{Code: c.Code, Name: c.Name})
	}
	return out
}

// language decides which language a request gets, in this order: what the user
// picked (cookie), what the configuration says, what the browser asks for.
//
// The browser comes last on purpose. Somebody who has chosen a language in
// s3mail has said something more specific than their browser's setting, and a
// preference that gets overruled by the next request is not a preference.
func (s *Server) language(r *http.Request) string {
	if c, err := r.Cookie("s3mail_lang"); err == nil && c.Value != "" {
		if i18n.Get(c.Value).Code == strings.ToLower(c.Value) {
			return c.Value
		}
	}
	if s.Config != nil {
		if v, ok := s.Config["language"].(string); ok && v != "" {
			return v
		}
	}
	return i18n.FromHeader(r.Header.Get("Accept-Language"))
}
