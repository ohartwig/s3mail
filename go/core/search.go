package core

import (
	"regexp"
	"sort"
	"strings"
)

// Search syntax: free words, plus filters of the form field:value. Quotes hold
// spaces together. The field names exist in all three interface languages, so a
// reader types what they see:
//
//	invoice from:customer@x.com subject:"quote" after:2026-01-01
//	has:attachment is:unread tag:important in:archiv
var (
	tokenRe  = regexp.MustCompile(`\w+:"[^"]*"|\w+:\S+|"[^"]*"|\S+`)
	filterRe = regexp.MustCompile(`(?i)^(from|von|de|to|an|para|subject|betreff|asunto|after|nach|desde|before|vor|hasta|has|hat|tiene|tag|etiqueta|is|ist|es|in|en)\s*:\s*"?([^"]*)"?$`)
)

type filter struct{ field, value string }

// Query is a parsed search request.
type Query struct {
	Terms  []string
	Filter []filter
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
			out.Terms = append(out.Terms, b)
		}
	}
	return out
}

// Matches checks an already decorated message against the query.
func (q Query) Matches(m Message) bool {
	hay := strings.ToLower(strings.Join([]string{
		m.From, m.To, m.Cc, m.Subject, m.Snippet, m.Key,
		strings.Join(m.Tags, " "),
	}, " "))
	for _, b := range q.Terms {
		if !strings.Contains(hay, b) {
			return false
		}
	}
	for _, f := range q.Filter {
		if !matches(f, m) {
			return false
		}
	}
	return true
}

func matches(f filter, m Message) bool {
	has := func(s string) bool { return strings.Contains(strings.ToLower(s), f.value) }
	switch f.field {
	case "from", "von", "de":
		return has(m.From)
	case "to", "an", "para":
		return has(m.To + m.Cc)
	case "subject", "betreff", "asunto":
		return has(m.Subject)
	case "after", "nach", "desde":
		return datePart(m.Date) >= f.value
	case "before", "vor", "hasta":
		return datePart(m.Date) <= f.value
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
	return true // an unknown filter restricts nothing
}

func datePart(iso string) string {
	if len(iso) >= 10 {
		return iso[:10]
	}
	return iso
}

// SearchOpts are the restrictions that do not come from the text query.
type SearchOpts struct {
	Folder     *string // nil = alle Ordner
	Tag        string
	OnlyUnread bool
	OnlyStar   bool
}

// Search filters the index and sorts by date, newest first.
func Search(index []Message, d *Data, query string, o SearchOpts) []Message {
	q := ParseQuery(query)
	hits := make([]Message, 0, len(index))
	for _, raw := range index {
		if o.Folder != nil && raw.Folder != *o.Folder {
			continue
		}
		m := Decorate(raw, d)
		if o.OnlyUnread && m.Read {
			continue
		}
		if o.OnlyStar && !m.Star {
			continue
		}
		if o.Tag != "" && !contains(m.Tags, o.Tag) {
			continue
		}
		if q.Matches(m) {
			hits = append(hits, m)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Date > hits[j].Date })
	return hits
}
