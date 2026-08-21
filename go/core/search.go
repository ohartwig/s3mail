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
	filterRe = regexp.MustCompile(`(?i)^(from|to|subject|betreff|after|before|has|tag|is|in)\s*:\s*"?([^"]*)"?$`)
)

type filter struct{ feld, wert string }

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
	hat := func(s string) bool { return strings.Contains(strings.ToLower(s), f.wert) }
	switch f.feld {
	case "from":
		return hat(m.From)
	case "to":
		return hat(m.To + m.Cc)
	case "subject", "betreff":
		return hat(m.Subject)
	case "after":
		return datumsteil(m.Date) >= f.wert
	case "before":
		return datumsteil(m.Date) <= f.wert
	case "tag":
		for _, t := range m.Tags {
			if strings.ToLower(t) == f.wert {
				return true
			}
		}
		return false
	case "in":
		ordner := m.Folder
		if ordner == "" {
			ordner = "posteingang"
		}
		return strings.ToLower(ordner) == f.wert
	case "has":
		if strings.HasPrefix(f.wert, "att") || strings.HasPrefix(f.wert, "anh") {
			return m.HasAttachment
		}
		if f.wert == "spam" {
			return m.Spam
		}
	case "is":
		switch f.wert {
		case "ungelesen", "unread":
			return !m.Read
		case "gelesen", "read":
			return m.Read
		case "stern", "star", "starred":
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
