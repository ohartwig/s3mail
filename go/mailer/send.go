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

// Entwurf ist, was die Oberflaeche schickt.
type Entwurf struct {
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

// Nachricht ist die fertige Mail samt Empfaengerliste.
type Nachricht struct {
	Roh        []byte
	Absender   string
	Empfaenger []string
}

var (
	ErrKeinAbsender   = errors.New("kein Absender gesetzt")
	ErrKeinEmpfaenger = errors.New("kein Empfaenger angegeben")
)

// Bauen setzt die Mail zusammen.
func Bauen(e Entwurf, standardAbsender string, o Original, jetzt time.Time) (Nachricht, error) {
	absender := strings.TrimSpace(e.From)
	if absender == "" {
		absender = strings.TrimSpace(standardAbsender)
	}
	if absender == "" {
		return Nachricht{}, ErrKeinAbsender
	}
	an, err := adressen(e.To)
	if err != nil {
		return Nachricht{}, err
	}
	kopie, err := adressen(e.Cc)
	if err != nil {
		return Nachricht{}, err
	}
	if len(an) == 0 && len(kopie) == 0 {
		return Nachricht{}, ErrKeinEmpfaenger
	}

	kopf := textproto.MIMEHeader{}
	kopf.Set("From", absender)
	kopf.Set("To", strings.Join(an, ", "))
	if len(kopie) > 0 {
		kopf.Set("Cc", strings.Join(kopie, ", "))
	}
	kopf.Set("Subject", kodiere(e.Subject))
	kopf.Set("Date", jetzt.Format(time.RFC1123Z))
	kopf.Set("Message-Id", neueMessageID(absender))
	kopf.Set("MIME-Version", "1.0")

	// Beim Antworten die Faeden zusammenhalten: ohne In-Reply-To und References
	// macht die Antwort im Postfach des Empfaengers einen neuen Strang auf.
	if e.Mode == "reply" && o.MessageID != "" {
		kopf.Set("In-Reply-To", o.MessageID)
		refs := strings.TrimSpace(o.References + " " + o.MessageID)
		kopf.Set("References", strings.Join(strings.Fields(refs), " "))
	}

	var koerper bytes.Buffer
	if e.Mode == "forward" && len(o.Roh) > 0 {
		if err := mitAnhang(&koerper, kopf, e.Body, o); err != nil {
			return Nachricht{}, err
		}
	} else {
		kopf.Set("Content-Type", `text/plain; charset="utf-8"`)
		kopf.Set("Content-Transfer-Encoding", "8bit")
		koerper.WriteString(e.Body)
	}

	var roh bytes.Buffer
	schreibeKopf(&roh, kopf)
	roh.WriteString("\r\n")
	roh.Write(koerper.Bytes())

	return Nachricht{Roh: roh.Bytes(), Absender: absender,
		Empfaenger: append(append([]string{}, an...), kopie...)}, nil
}

// mitAnhang haengt die weitergeleitete Mail als .eml an.
func mitAnhang(koerper *bytes.Buffer, kopf textproto.MIMEHeader, text string, o Original) error {
	mw := multipart.NewWriter(koerper)
	kopf.Set("Content-Type", `multipart/mixed; boundary="`+mw.Boundary()+`"`)

	teil, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {`text/plain; charset="utf-8"`},
		"Content-Transfer-Encoding": {"8bit"},
	})
	if err != nil {
		return err
	}
	if _, err := teil.Write([]byte(text)); err != nil {
		return err
	}

	name := o.Subject
	if name == "" {
		name = "mail"
	}
	if r := []rune(name); len(r) > 60 {
		name = string(r[:60])
	}
	anhang, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"message/rfc822"},
		"Content-Disposition": {mime.FormatMediaType("attachment", map[string]string{"filename": name + ".eml"})},
	})
	if err != nil {
		return err
	}
	if _, err := anhang.Write(o.Roh); err != nil {
		return err
	}
	return mw.Close()
}

func schreibeKopf(w *bytes.Buffer, kopf textproto.MIMEHeader) {
	// feste Reihenfolge, damit die Ausgabe reproduzierbar ist
	for _, name := range []string{"From", "To", "Cc", "Subject", "Date", "Message-Id",
		"In-Reply-To", "References", "MIME-Version", "Content-Type",
		"Content-Transfer-Encoding"} {
		if v := kopf.Get(name); v != "" {
			fmt.Fprintf(w, "%s: %s\r\n", name, v)
		}
	}
}

// adressen zerlegt eine Empfaengerliste und liefert die reinen Adressen.
func adressen(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	liste, err := mail.ParseAddressList(s)
	if err != nil {
		return nil, fmt.Errorf("Empfaenger nicht lesbar: %w", err)
	}
	out := make([]string, 0, len(liste))
	for _, a := range liste {
		out = append(out, a.Address)
	}
	return out, nil
}

// kodiere macht aus einem Betreff mit Umlauten einen RFC-2047-Header.
func kodiere(s string) string {
	for _, r := range s {
		if r > 127 {
			return mime.QEncoding.Encode("utf-8", s)
		}
	}
	return s
}

func neueMessageID(absender string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	domain := "s3mail.local"
	if i := strings.LastIndex(absender, "@"); i >= 0 {
		domain = absender[i+1:]
	}
	return "<" + hex.EncodeToString(b) + "@" + strings.Trim(domain, "<> ") + ">"
}
