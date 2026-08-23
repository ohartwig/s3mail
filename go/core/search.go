package core

import (
	"regexp"
	"sort"
	"strings"
)

// Suchsyntax: freie Woerter, dazu Filter der Form feld:wert. Anfuehrungszeichen
// halten Leerzeichen zusammen:
//
//	rechnung from:kunde@x.de subject:"Angebot" after:2026-01-01
//	has:anhang is:ungelesen tag:wichtig in:archiv
var (
	tokenRe  = regexp.MustCompile(`\w+:"[^"]*"|\w+:\S+|"[^"]*"|\S+`)
	filterRe = regexp.MustCompile(`(?i)^(from|von|de|to|an|para|subject|betreff|asunto|after|nach|desde|before|vor|hasta|has|hat|tiene|tag|etiqueta|is|ist|es|in|en)\s*:\s*"?([^"]*)"?$`)
)

type filter struct{ feld, value string }

// Query ist eine geparste Suchanfrage.
type Query struct {
	Begriffe []string
	Filter   []filter
}

func ParseQuery(q string) Query {
	var out Query
	for _, token := range tokenRe.FindAllString(q, -1) {
		token = strings.TrimSpace(token)
		if m := filterRe.FindStringSubmatch(token); m != nil {
			out.Filter = append(out.Filter, filter{strings.ToLower(m[1]), strings.ToLower(m[2])})
			continue
		}
		if b := strings.ToLower(strings.Trim(token, `"`)); b != "" {
			out.Begriffe = append(out.Begriffe, b)
		}
	}
	return out
}

// Matches prueft eine bereits dekorierte Mail gegen die Anfrage.
func (q Query) Matches(m Message) bool {
	hay := strings.ToLower(strings.Join([]string{
		m.From, m.To, m.Cc, m.Subject, m.Snippet, m.Key,
		strings.Join(m.Tags, " "),
	}, " "))
	for _, b := range q.Begriffe {
		if !strings.Contains(hay, b) {
			return false
		}
	}
	for _, f := range q.Filter {
		if !passt(f, m) {
			return false
		}
	}
	return true
}

func passt(f filter, m Message) bool {
	hat := func(s string) bool { return strings.Contains(strings.ToLower(s), f.value) }
	switch f.feld {
	case "from", "von", "de":
		return hat(m.From)
	case "to", "an", "para":
		return hat(m.To + m.Cc)
	case "subject", "betreff", "asunto":
		return hat(m.Subject)
	case "after", "nach", "desde":
		return datumsteil(m.Date) >= f.value
	case "before", "vor", "hasta":
		return datumsteil(m.Date) <= f.value
	case "tag", "etiqueta":
		for _, t := range m.Tags {
			if strings.ToLower(t) == f.value {
				return true
			}
		}
		return false
	case "in", "en":
		// The inbox has no prefix of its own - its key is the empty string.
		// Which word a reader types for it depends on their language, so all
		// three are accepted rather than one being declared canonical.
		folder := strings.ToLower(m.Folder)
		if folder == "" {
			switch f.value {
			case "posteingang", "inbox", "entrada", "bandeja":
				return true
			}
			return false
		}
		return folder == f.value
	case "has", "hat", "tiene":
		if strings.HasPrefix(f.value, "att") || strings.HasPrefix(f.value, "anh") ||
			strings.HasPrefix(f.value, "adj") {
			return m.HasAttachment
		}
		if f.value == "spam" {
			return m.Spam
		}
	case "is", "ist", "es":
		switch f.value {
		case "ungelesen", "unread", "sinleer":
			return !m.Read
		case "gelesen", "read", "leido", "leído":
			return m.Read
		case "stern", "star", "starred", "destacado":
			return m.Star
		}
	}
	return true // unbekannter Filter schraenkt nicht ein
}

func datumsteil(iso string) string {
	if len(iso) >= 10 {
		return iso[:10]
	}
	return iso
}

// SearchOpts sind die Einschraenkungen, die nicht aus der Textanfrage kommen.
type SearchOpts struct {
	Folder     *string // nil = alle Ordner
	Tag        string
	OnlyUnread bool
	OnlyStar   bool
}

// Search filtert den Index und sortiert nach Datum, neueste zuerst.
func Search(index []Message, d *Data, query string, o SearchOpts) []Message {
	q := ParseQuery(query)
	hits := make([]Message, 0, len(index))
	for _, roh := range index {
		if o.Folder != nil && roh.Folder != *o.Folder {
			continue
		}
		m := Decorate(roh, d)
		if o.OnlyUnread && m.Read {
			continue
		}
		if o.OnlyStar && !m.Star {
			continue
		}
		if o.Tag != "" && !enthaelt(m.Tags, o.Tag) {
			continue
		}
		if q.Matches(m) {
			hits = append(hits, m)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Date > hits[j].Date })
	return hits
}
