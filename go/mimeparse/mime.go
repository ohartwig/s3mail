// Package mimeparse is s3mail's MIME layer - the part a port would founder on
// if it foundered anywhere. It is measured against the output of the previous
// Python parser (testdata/expected.json).
package mimeparse

import (
	"fmt"
	"io"
	"mime"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // registriert iso-8859-*, windows-125*, …
)

// Summary is what s3mail puts into the index for each message.
type Summary struct {
	From string `json:"from"`
	To   string `json:"to"`
	Cc   string `json:"cc"`
	// FromAddr is the bare address out of From: "Anna <anna@x.de>" becomes
	// "anna@x.de". Everything that groups by sender needs it - a display name
	// varies between two messages from the same person.
	FromAddr string `json:"from_addr"`
	// ToAddrs are the bare recipient addresses. Anything asking "did they
	// answer" needs them: a display name is no key.
	ToAddrs     []string     `json:"to_addrs"`
	MessageID   string       `json:"message_id"`
	Subject     string       `json:"subject"`
	Date        string       `json:"date"`
	Preview     string       `json:"preview"`
	HasHTML     bool         `json:"has_html"`
	Attachments []Attachment `json:"attachments"`
	Spam        string       `json:"spam"`
	Virus       string       `json:"virus"`
}

// The two placeholders a message can carry instead of a subject. They land in
// the index as well, so they cannot be translated where they are written - the
// HTTP layer swaps them for the reader's language on the way out.
const (
	SubjectUnreadable = "(not readable)"
	SubjectNone       = "(no subject)"
)

type Attachment struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	CType string `json:"ctype"`
}

// Dec matches Python's dec(): make MIME-encoded headers readable.
func Dec(value string) string {
	if value == "" {
		return ""
	}
	dec := &mime.WordDecoder{CharsetReader: charsetReader}
	out, err := dec.DecodeHeader(value)
	if err != nil {
		out = value
	}
	return repairLatin(out)
}

// repairLatin rescues headers that carry raw 8-bit characters instead of
// encoding them properly per RFC 2047 - Outlook and older server chains do this
// constantly. If the result is not valid UTF-8, it was most likely
// windows-1252. Python inserts replacement characters at this point, and the
// text is then gone for good.
func repairLatin(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	if out, err := charmap.Windows1252.NewDecoder().String(s); err == nil {
		return out
	}
	return strings.ToValidUTF8(s, "\ufffd")
}

// AddrStr matches addr_str(): "Name <address>, …" as one readable string.
// BareAddr takes the address out of a header and nothing else: "Anna
// <anna@x.de>" becomes "anna@x.de", lowercased, because a sender who writes
// their address in capitals one day is the same sender.
func BareAddr(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	p := &mail.AddressParser{WordDecoder: &mime.WordDecoder{CharsetReader: charsetReader}}
	list, err := p.ParseList(value)
	if err != nil || len(list) == 0 {
		return ""
	}
	return strings.ToLower(list[0].Address)
}

// BareAddrs takes the addresses out of several headers at once, lowercased and
// without duplicates.
func BareAddrs(values ...string) []string {
	seen := map[string]bool{}
	out := []string{}
	p := &mail.AddressParser{WordDecoder: &mime.WordDecoder{CharsetReader: charsetReader}}
	for _, v := range values {
		if strings.TrimSpace(v) == "" {
			continue
		}
		list, err := p.ParseList(v)
		if err != nil {
			continue
		}
		for _, a := range list {
			addr := strings.ToLower(a.Address)
			if addr != "" && !seen[addr] {
				seen[addr] = true
				out = append(out, addr)
			}
		}
	}
	return out
}

func AddrStr(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	p := &mail.AddressParser{WordDecoder: &mime.WordDecoder{CharsetReader: charsetReader}}
	list, err := p.ParseList(value)
	if err != nil {
		return Dec(value)
	}
	parts := make([]string, 0, len(list))
	for _, a := range list {
		if a.Name != "" {
			parts = append(parts, a.Name+" <"+a.Address+">")
		} else {
			parts = append(parts, a.Address)
		}
	}
	return strings.Join(parts, ", ")
}

// ParseDate matches parse_date(): the date from the header, else the fallback.
func ParseDate(header string, fallback time.Time) time.Time {
	if t, err := mail.ParseDate(header); err == nil {
		return t
	}
	return fallback
}

var (
	scriptRe = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	tagRe    = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRe  = regexp.MustCompile(`\s+`)
)

// StripHTML entspricht strip_html().
func StripHTML(raw string) string {
	s := scriptRe.ReplaceAllString(raw, " ")
	s = tagRe.ReplaceAllString(s, " ")
	s = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<",
		"&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// role of a part within the document.
type role int

const (
	roleText       role = iota // body text or HTML
	roleAttachment             // shows up in the attachment list
	roleInline                 // image the HTML points at by cid: - neither of the two
)

func roleOf(h message.Header) role {
	disp, dparams, _ := h.ContentDisposition()
	ctype, cparams, _ := h.ContentType()
	_, hasFilename := dparams["filename"]
	if _, ok := cparams["name"]; ok {
		hasFilename = true
	}

	// An image the HTML refers to by cid: belongs neither in the attachment
	// list nor in the preview text. Python counts it as an attachment - every
	// signature with a logo grows a paperclip there that is not one.
	if h.Get("Content-Id") != "" && !strings.HasPrefix(ctype, "text/") && disp != "attachment" {
		return roleInline
	}
	if disp == "attachment" {
		return roleAttachment
	}
	if strings.HasPrefix(ctype, "text/") {
		return roleText
	}
	if hasFilename {
		return roleAttachment
	}
	// A binary part with no name and no Content-ID: keep it out of the preview
	if !strings.HasPrefix(ctype, "text/") && ctype != "" {
		return roleAttachment
	}
	return roleText
}

// Summarize reads a raw message and builds the index entry from it.
func Summarize(raw []byte, fallback time.Time) Summary {
	s := Summary{Attachments: []Attachment{}}
	msg, err := message.Read(strings.NewReader(string(raw)))
	if msg == nil {
		s.Preview = rawPreview(raw) // no MIME at all - then the raw text it is
		return s
	}
	_ = err // a header error does not topple the message, the rest is often usable
	h := msg.Header
	s.From = AddrStr(h.Get("From"))
	s.FromAddr = BareAddr(h.Get("From"))
	s.ToAddrs = BareAddrs(h.Get("To"), h.Get("Cc"))
	s.MessageID = strings.TrimSpace(h.Get("Message-Id"))
	s.To = AddrStr(h.Get("To"))
	s.Cc = AddrStr(h.Get("Cc"))
	s.Subject = Dec(h.Get("Subject"))
	s.Date = ParseDate(h.Get("Date"), fallback).UTC().Format(time.RFC3339)
	s.Spam = strings.ToUpper(h.Get("X-Ses-Spam-Verdict"))
	s.Virus = strings.ToUpper(h.Get("X-Ses-Virus-Verdict"))

	var text, html strings.Builder
	idx := 0
	walk(msg, &idx, &text, &html, &s)

	preview := text.String()
	if strings.TrimSpace(preview) == "" {
		preview = StripHTML(html.String())
	}
	s.Preview = strings.Join(strings.Fields(preview), " ")
	if r := []rune(s.Preview); len(r) > 160 {
		s.Preview = string(r[:160])
	}
	s.HasHTML = strings.TrimSpace(html.String()) != ""

	// Something that does not parse as MIME is still a file in the mailbox -
	// better to show the raw text than an empty line.
	if s.Preview == "" && s.Subject == "" && len(s.Attachments) == 0 {
		s.Preview = rawPreview(raw)
	}
	return s
}

func rawPreview(raw []byte) string {
	out := strings.Join(strings.Fields(repairLatin(string(raw))), " ")
	if r := []rune(out); len(r) > 160 {
		out = string(r[:160])
	}
	return out
}

func walk(msg *message.Entity, idx *int, text, html *strings.Builder, s *Summary) {
	if mr := msg.MultipartReader(); mr != nil {
		for {
			part, err := mr.NextPart()
			if err != nil {
				return // io.EOF or a broken part - the rest stays usable
			}
			walk(part, idx, text, html, s)
		}
	}
	ctype, cparams, _ := msg.Header.ContentType()
	i := *idx
	*idx++
	switch roleOf(msg.Header) {
	case roleInline:
		return
	case roleAttachment:
		_, dparams, _ := msg.Header.ContentDisposition()
		name := dparams["filename"]
		if name == "" {
			name = cparams["name"]
		}
		if name == "" {
			name = "unbenannt"
		}
		s.Attachments = append(s.Attachments,
			Attachment{Index: i, Name: Dec(name), CType: ctype})
		return
	}
	body, err := io.ReadAll(msg.Body)
	if err != nil {
		return
	}
	if ctype == "text/html" {
		html.Write(body)
	} else {
		text.Write(body)
	}
}

// Full is the whole message for the detail view - body text, HTML and the
// attachments including their content.
type Full struct {
	Subject string `json:"subject"`
	From    string `json:"from"`
	ReplyTo string `json:"reply_to"`
	To      string `json:"to"`
	Cc      string `json:"cc"`
	// Bcc appears in a draft only: a sent message never carries the header, or
	// the recipients would have the blind copy in front of them.
	Bcc         string           `json:"bcc"`
	Date        string           `json:"date"`
	MessageID   string           `json:"message_id"`
	References  string           `json:"references"`
	Spam        bool             `json:"spam"`
	Virus       bool             `json:"virus"`
	Text        string           `json:"text"`
	HTML        string           `json:"html"`
	Attachments []FullAttachment `json:"attachments"`
}

// Attachment carries its content along, so the HTTP layer can serve it without
// parsing the message a second time.
type FullAttachment struct {
	Index       int    `json:"index"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
	Content     []byte `json:"-"`
}

// Read parses a whole message.
func Read(raw []byte, fallback time.Time) Full {
	v := Full{Attachments: []FullAttachment{}}
	msg, err := message.Read(strings.NewReader(string(raw)))
	if msg == nil {
		v.Subject = SubjectUnreadable
		v.Text = rawPreview(raw)
		return v
	}
	_ = err
	h := msg.Header
	v.Subject = Dec(h.Get("Subject"))
	if v.Subject == "" {
		v.Subject = SubjectNone
	}
	v.From = AddrStr(h.Get("From"))
	v.ReplyTo = AddrStr(h.Get("Reply-To"))
	v.To = AddrStr(h.Get("To"))
	v.Cc = AddrStr(h.Get("Cc"))
	v.Bcc = AddrStr(h.Get("Bcc"))
	v.Date = ParseDate(h.Get("Date"), fallback).UTC().Format(time.RFC3339)
	v.MessageID = strings.TrimSpace(h.Get("Message-Id"))
	v.References = strings.TrimSpace(h.Get("References"))
	v.Spam = strings.EqualFold(h.Get("X-Ses-Spam-Verdict"), "FAIL")
	v.Virus = strings.EqualFold(h.Get("X-Ses-Virus-Verdict"), "FAIL")

	var text, html []string
	idx := 0
	fullWalk(msg, &idx, &text, &html, &v)

	v.HTML = strings.Join(html, "\n<hr>\n")
	v.Text = strings.Join(text, "\n\n")
	if strings.TrimSpace(v.Text) == "" && v.HTML != "" {
		v.Text = StripHTML(v.HTML)
	}
	return v
}

func fullWalk(msg *message.Entity, idx *int, text, html *[]string, v *Full) {
	if mr := msg.MultipartReader(); mr != nil {
		for {
			part, err := mr.NextPart()
			if err != nil {
				return
			}
			fullWalk(part, idx, text, html, v)
		}
	}
	ctype, cparams, _ := msg.Header.ContentType()
	i := *idx
	*idx++
	switch roleOf(msg.Header) {
	case roleInline:
		return
	case roleAttachment:
		content, _ := io.ReadAll(msg.Body)
		_, dparams, _ := msg.Header.ContentDisposition()
		name := dparams["filename"]
		if name == "" {
			name = cparams["name"]
		}
		if name == "" {
			name = fmt.Sprintf("anhang-%d", i)
		}
		v.Attachments = append(v.Attachments, FullAttachment{Index: i, Filename: Dec(name),
			ContentType: ctype, Size: len(content), Content: content})
		return
	}
	body, err := io.ReadAll(msg.Body)
	if err != nil {
		return
	}
	if ctype == "text/html" {
		*html = append(*html, string(body))
	} else {
		*text = append(*text, string(body))
	}
}
