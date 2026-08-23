// Package mimeparse portiert die MIME-Schicht von s3mail nach Go - der Teil, an
// dem eine Portierung scheitern wuerde, wenn sie scheitert. Gemessen wird gegen
// die Ausgabe des bestehenden Python-Parsers (testdata/expected.json).
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

// Summary ist das, was s3mail pro Mail in den Index legt.
type Summary struct {
	From        string       `json:"from"`
	To          string       `json:"to"`
	Cc          string       `json:"cc"`
	Subject     string       `json:"subject"`
	Date        string       `json:"date"`
	Preview     string       `json:"preview"`
	HasHTML     bool         `json:"has_html"`
	Attachments []Attachment `json:"attachments"`
	Spam        string       `json:"spam"`
}

type Attachment struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	CType string `json:"ctype"`
}

// Dec entspricht dem Python-dec(): MIME-kodierte Header lesbar machen.
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

// repairLatin rettet Header, die rohe 8-Bit-Zeichen enthalten, statt sie sauber
// nach RFC 2047 zu kodieren - Outlook und aeltere Serverketten tun das staendig.
// Ist das Ergebnis kein gueltiges UTF-8, war es mit grosser Wahrscheinlichkeit
// windows-1252. Python wirft an dieser Stelle Ersatzzeichen ein, der Text ist
// dann unwiederbringlich weg.
func repairLatin(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	if out, err := charmap.Windows1252.NewDecoder().String(s); err == nil {
		return out
	}
	return strings.ToValidUTF8(s, "\ufffd")
}

// AddrStr entspricht addr_str(): "Name <adresse>, …" als ein lesbarer String.
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

// ParseDate entspricht parse_date(): Datum aus dem Header, sonst der Rueckfallwert.
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

// Rolle eines Parts im Dokument.
type role int

const (
	rolleText   role = iota // Fliesstext oder HTML
	rolleAnhang             // taucht in der Anhangsliste auf
	rolleInline             // Bild, auf das das HTML per cid: zeigt - beides nicht
)

func roleOf(h message.Header) role {
	disp, dparams, _ := h.ContentDisposition()
	ctype, cparams, _ := h.ContentType()
	_, hatDateiname := dparams["filename"]
	if _, ok := cparams["name"]; ok {
		hatDateiname = true
	}

	// Ein Bild, auf das das HTML per cid: verweist, gehoert weder in die
	// Anhangsliste noch in den Vorschautext. Python zaehlt es als Anhang - jede
	// Signatur mit Logo erzeugt dort eine Bueroklammer, die keine ist.
	if h.Get("Content-Id") != "" && !strings.HasPrefix(ctype, "text/") && disp != "attachment" {
		return rolleInline
	}
	if disp == "attachment" {
		return rolleAnhang
	}
	if strings.HasPrefix(ctype, "text/") {
		return rolleText
	}
	if hatDateiname {
		return rolleAnhang
	}
	// Binaerteil ohne Namen und ohne Content-ID: nicht in die Vorschau kippen
	if !strings.HasPrefix(ctype, "text/") && ctype != "" {
		return rolleAnhang
	}
	return rolleText
}

// Summarize liest eine rohe Mail und baut daraus den Indexeintrag.
func Summarize(raw []byte, fallback time.Time) Summary {
	s := Summary{Attachments: []Attachment{}}
	ent, err := message.Read(strings.NewReader(string(raw)))
	if ent == nil {
		s.Preview = rohvorschau(raw) // gar kein MIME - dann eben der Rohtext
		return s
	}
	_ = err // ein Header-Fehler kippt die Mail nicht, der Rest ist oft brauchbar
	h := ent.Header
	s.From = AddrStr(h.Get("From"))
	s.To = AddrStr(h.Get("To"))
	s.Cc = AddrStr(h.Get("Cc"))
	s.Subject = Dec(h.Get("Subject"))
	s.Date = ParseDate(h.Get("Date"), fallback).UTC().Format(time.RFC3339)
	s.Spam = strings.ToUpper(h.Get("X-Ses-Spam-Verdict"))

	var text, html strings.Builder
	idx := 0
	walk(ent, &idx, &text, &html, &s)

	preview := text.String()
	if strings.TrimSpace(preview) == "" {
		preview = StripHTML(html.String())
	}
	s.Preview = strings.Join(strings.Fields(preview), " ")
	if r := []rune(s.Preview); len(r) > 160 {
		s.Preview = string(r[:160])
	}
	s.HasHTML = strings.TrimSpace(html.String()) != ""

	// Was nicht als MIME durchgeht, ist trotzdem eine Datei im Postfach - dann
	// lieber den Rohtext zeigen als eine leere Zeile.
	if s.Preview == "" && s.Subject == "" && len(s.Attachments) == 0 {
		s.Preview = rohvorschau(raw)
	}
	return s
}

func rohvorschau(raw []byte) string {
	out := strings.Join(strings.Fields(repairLatin(string(raw))), " ")
	if r := []rune(out); len(r) > 160 {
		out = string(r[:160])
	}
	return out
}

func walk(ent *message.Entity, idx *int, text, html *strings.Builder, s *Summary) {
	if mr := ent.MultipartReader(); mr != nil {
		for {
			part, err := mr.NextPart()
			if err != nil {
				return // io.EOF oder kaputter Part - Rest bleibt brauchbar
			}
			walk(part, idx, text, html, s)
		}
	}
	ctype, cparams, _ := ent.Header.ContentType()
	i := *idx
	*idx++
	switch roleOf(ent.Header) {
	case rolleInline:
		return
	case rolleAnhang:
		_, dparams, _ := ent.Header.ContentDisposition()
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
	body, err := io.ReadAll(ent.Body)
	if err != nil {
		return
	}
	if ctype == "text/html" {
		html.Write(body)
	} else {
		text.Write(body)
	}
}

// Full ist die ganze Mail fuer die Detailansicht - mit Fliesstext, HTML und den
// Anhaengen samt Inhalt.
type Full struct {
	Subject    string   `json:"subject"`
	From       string   `json:"from"`
	ReplyTo    string   `json:"reply_to"`
	To         string   `json:"to"`
	Cc         string   `json:"cc"`
	Date       string   `json:"date"`
	MessageID  string   `json:"message_id"`
	References string   `json:"references"`
	Spam       bool     `json:"spam"`
	Virus      bool     `json:"virus"`
	Text       string   `json:"text"`
	HTML       string   `json:"html"`
	Anhaenge   []Anhang `json:"attachments"`
}

// Anhang traegt den Inhalt mit, damit die HTTP-Schicht ihn ausliefern kann, ohne
// die Mail ein zweites Mal zu parsen.
type Anhang struct {
	Index       int    `json:"index"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
	Inhalt      []byte `json:"-"`
}

// Read parst eine ganze Mail.
func Read(raw []byte, fallback time.Time) Full {
	v := Full{Anhaenge: []Anhang{}}
	ent, err := message.Read(strings.NewReader(string(raw)))
	if ent == nil {
		v.Subject = "(nicht lesbar)"
		v.Text = rohvorschau(raw)
		return v
	}
	_ = err
	h := ent.Header
	v.Subject = Dec(h.Get("Subject"))
	if v.Subject == "" {
		v.Subject = "(kein Betreff)"
	}
	v.From = AddrStr(h.Get("From"))
	v.ReplyTo = AddrStr(h.Get("Reply-To"))
	v.To = AddrStr(h.Get("To"))
	v.Cc = AddrStr(h.Get("Cc"))
	v.Date = ParseDate(h.Get("Date"), fallback).UTC().Format(time.RFC3339)
	v.MessageID = strings.TrimSpace(h.Get("Message-Id"))
	v.References = strings.TrimSpace(h.Get("References"))
	v.Spam = strings.EqualFold(h.Get("X-Ses-Spam-Verdict"), "FAIL")
	v.Virus = strings.EqualFold(h.Get("X-Ses-Virus-Verdict"), "FAIL")

	var text, html []string
	idx := 0
	vollWalk(ent, &idx, &text, &html, &v)

	v.HTML = strings.Join(html, "\n<hr>\n")
	v.Text = strings.Join(text, "\n\n")
	if strings.TrimSpace(v.Text) == "" && v.HTML != "" {
		v.Text = StripHTML(v.HTML)
	}
	return v
}

func vollWalk(ent *message.Entity, idx *int, text, html *[]string, v *Full) {
	if mr := ent.MultipartReader(); mr != nil {
		for {
			part, err := mr.NextPart()
			if err != nil {
				return
			}
			vollWalk(part, idx, text, html, v)
		}
	}
	ctype, cparams, _ := ent.Header.ContentType()
	i := *idx
	*idx++
	switch roleOf(ent.Header) {
	case rolleInline:
		return
	case rolleAnhang:
		content, _ := io.ReadAll(ent.Body)
		_, dparams, _ := ent.Header.ContentDisposition()
		name := dparams["filename"]
		if name == "" {
			name = cparams["name"]
		}
		if name == "" {
			name = fmt.Sprintf("anhang-%d", i)
		}
		v.Anhaenge = append(v.Anhaenge, Anhang{Index: i, Filename: Dec(name),
			ContentType: ctype, Size: len(content), Inhalt: content})
		return
	}
	body, err := io.ReadAll(ent.Body)
	if err != nil {
		return
	}
	if ctype == "text/html" {
		*html = append(*html, string(body))
	} else {
		*text = append(*text, string(body))
	}
}
