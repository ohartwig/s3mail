// Package mailer baut die ausgehende Mail und uebergibt sie an SES. Das Bauen ist
// von der Zustellung getrennt, damit die Kopfzeilen ohne AWS pruefbar sind - an
// ihnen haengt, ob eine Antwort im Postfach des Empfaengers am richtigen Faden
// haengt oder einen neuen Strang aufmacht.
package mailer

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"strings"
	"time"
)

// Draft ist, was die Oberflaeche schickt.
type Draft struct {
	Mode    string `json:"mode"` // "reply", "forward" oder "new"
	Key     string `json:"key"`
	From    string `json:"from"`
	To      string `json:"to"`
	Cc      string `json:"cc"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// Original sind die Kopfzeilen der Mail, auf die geantwortet wird.
type Original struct {
	MessageID  string
	References string
	Subject    string
	Roh        []byte // fuer das Weiterleiten als .eml
}

// Message ist die fertige Mail samt Empfaengerliste.
type Message struct {
	Roh        []byte
	Absender   string
	Empfaenger []string
}

var (
	ErrKeinAbsender   = errors.New("kein Absender gesetzt")
	ErrKeinEmpfaenger = errors.New("kein Empfaenger angegeben")
)

// Build setzt die Mail zusammen.
func Build(e Draft, standardAbsender string, o Original, jetzt time.Time) (Message, error) {
	sender := strings.TrimSpace(e.From)
	if sender == "" {
		sender = strings.TrimSpace(standardAbsender)
	}
	if sender == "" {
		return Message{}, ErrKeinAbsender
	}
	an, err := addresses(e.To)
	if err != nil {
		return Message{}, err
	}
	kopie, err := addresses(e.Cc)
	if err != nil {
		return Message{}, err
	}
	if len(an) == 0 && len(kopie) == 0 {
		return Message{}, ErrKeinEmpfaenger
	}

	header := textproto.MIMEHeader{}
	header.Set("From", sender)
	header.Set("To", strings.Join(an, ", "))
	if len(kopie) > 0 {
		header.Set("Cc", strings.Join(kopie, ", "))
	}
	header.Set("Subject", encodeWord(e.Subject))
	header.Set("Date", jetzt.Format(time.RFC1123Z))
	header.Set("Message-Id", newMessageID(sender))
	header.Set("MIME-Version", "1.0")

	// Beim Antworten die Faeden zusammenhalten: ohne In-Reply-To und References
	// macht die Antwort im Postfach des Empfaengers einen neuen Strang auf.
	if e.Mode == "reply" && o.MessageID != "" {
		header.Set("In-Reply-To", o.MessageID)
		refs := strings.TrimSpace(o.References + " " + o.MessageID)
		header.Set("References", strings.Join(strings.Fields(refs), " "))
	}

	var body bytes.Buffer
	if e.Mode == "forward" && len(o.Roh) > 0 {
		if err := withAttachment(&body, header, e.Body, o); err != nil {
			return Message{}, err
		}
	} else {
		header.Set("Content-Type", `text/plain; charset="utf-8"`)
		header.Set("Content-Transfer-Encoding", "8bit")
		body.WriteString(e.Body)
	}

	var roh bytes.Buffer
	writeHeader(&roh, header)
	roh.WriteString("\r\n")
	roh.Write(body.Bytes())

	return Message{Roh: roh.Bytes(), Absender: sender,
		Empfaenger: append(append([]string{}, an...), kopie...)}, nil
}

// withAttachment haengt die weitergeleitete Mail als .eml an.
func withAttachment(body *bytes.Buffer, header textproto.MIMEHeader, text string, o Original) error {
	mw := multipart.NewWriter(body)
	header.Set("Content-Type", `multipart/mixed; boundary="`+mw.Boundary()+`"`)

	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {`text/plain; charset="utf-8"`},
		"Content-Transfer-Encoding": {"8bit"},
	})
	if err != nil {
		return err
	}
	if _, err := part.Write([]byte(text)); err != nil {
		return err
	}

	name := o.Subject
	if name == "" {
		name = "mail"
	}
	if r := []rune(name); len(r) > 60 {
		name = string(r[:60])
	}
	attachment, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"message/rfc822"},
		"Content-Disposition": {mime.FormatMediaType("attachment", map[string]string{"filename": name + ".eml"})},
	})
	if err != nil {
		return err
	}
	if _, err := attachment.Write(o.Roh); err != nil {
		return err
	}
	return mw.Close()
}

func writeHeader(w *bytes.Buffer, header textproto.MIMEHeader) {
	// feste Reihenfolge, damit die Ausgabe reproduzierbar ist
	for _, name := range []string{"From", "To", "Cc", "Subject", "Date", "Message-Id",
		"In-Reply-To", "References", "MIME-Version", "Content-Type",
		"Content-Transfer-Encoding"} {
		if v := header.Get(name); v != "" {
			fmt.Fprintf(w, "%s: %s\r\n", name, v)
		}
	}
}

// addresses zerlegt eine Empfaengerliste und liefert die reinen Adressen.
func addresses(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	list, err := mail.ParseAddressList(s)
	if err != nil {
		return nil, fmt.Errorf("Empfaenger nicht lesbar: %w", err)
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.Address)
	}
	return out, nil
}

// encodeWord macht aus einem Betreff mit Umlauten einen RFC-2047-Header.
func encodeWord(s string) string {
	for _, r := range s {
		if r > 127 {
			return mime.QEncoding.Encode("utf-8", s)
		}
	}
	return s
}

func newMessageID(sender string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	domain := "s3mail.local"
	if i := strings.LastIndex(sender, "@"); i >= 0 {
		domain = sender[i+1:]
	}
	return "<" + hex.EncodeToString(b) + "@" + strings.Trim(domain, "<> ") + ">"
}
