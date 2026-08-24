// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package mailer builds the outgoing message and hands it to SES. Building is
// separated from delivery so the headers can be checked without AWS - they
// decide whether a reply lands on the right thread in the recipient's mailbox
// or starts a new one.
package mailer

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
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

// Draft is what the interface sends.
type Draft struct {
	Mode    string `json:"mode"` // "reply", "forward" or "new"
	Key     string `json:"key"`
	From    string `json:"from"`
	To      string `json:"to"`
	Cc      string `json:"cc"`
	Bcc     string `json:"bcc"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// DraftKey is the stored draft this was written in. After sending it is
	// deleted - otherwise every sent message would leave its draft behind.
	DraftKey    string       `json:"draft_key"`
	Attachments []Attachment `json:"attachments"`
}

// Attachment is a file on its way out. Content arrives base64-encoded, which is
// what encoding/json does with a []byte by itself.
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Content     []byte `json:"content"`
}

// Original are the headers of the message being replied to.
type Original struct {
	MessageID  string
	References string
	Subject    string
	Raw        []byte // for forwarding as an .eml
}

// Message is the finished mail together with its recipient list.
type Message struct {
	Raw  []byte
	From string
	To   []string
	// ID is the Message-Id we generated. The sent copy is stored under it, so
	// storing the same message twice cannot produce two objects.
	ID string
}

// MaxSize is what SES accepts per message, base64 overhead included. Checking
// here instead of at the SDK means the reader learns it before the upload, not
// after it.
const MaxSize = 10 << 20

var (
	ErrNoSender    = errors.New("no sender set")
	ErrNoRecipient = errors.New("no recipient given")
	ErrTooLarge    = errors.New("message larger than SES accepts")
)

// Build assembles the message ready for sending.
func Build(e Draft, defaultFrom string, o Original, now time.Time) (Message, error) {
	return build(e, defaultFrom, o, now, false)
}

// BuildDraft assembles the same message for storage. The one difference is Bcc:
// a draft keeps the header, because whoever reopens it has to see whom they
// meant to blind-copy. On the way out the header must be gone.
func BuildDraft(e Draft, defaultFrom string, o Original, now time.Time) (Message, error) {
	return build(e, defaultFrom, o, now, true)
}

func build(e Draft, defaultFrom string, o Original, now time.Time, keepBcc bool) (Message, error) {
	sender := strings.TrimSpace(e.From)
	if sender == "" {
		sender = strings.TrimSpace(defaultFrom)
	}
	if sender == "" {
		return Message{}, ErrNoSender
	}
	to, err := addresses(e.To)
	if err != nil {
		return Message{}, err
	}
	cc, err := addresses(e.Cc)
	if err != nil {
		return Message{}, err
	}
	bcc, err := addresses(e.Bcc)
	if err != nil {
		return Message{}, err
	}
	if len(to) == 0 && len(cc) == 0 && len(bcc) == 0 && !keepBcc {
		return Message{}, ErrNoRecipient
	}

	id := newMessageID(sender)
	header := textproto.MIMEHeader{}
	header.Set("From", sender)
	header.Set("To", strings.Join(to, ", "))
	if len(cc) > 0 {
		header.Set("Cc", strings.Join(cc, ", "))
	}
	if keepBcc && len(bcc) > 0 {
		header.Set("Bcc", strings.Join(bcc, ", "))
	}
	header.Set("Subject", encodeWord(e.Subject))
	header.Set("Date", now.Format(time.RFC1123Z))
	header.Set("Message-Id", id)
	header.Set("MIME-Version", "1.0")

	// Keep the thread together when replying: without In-Reply-To and
	// References the answer starts a new strand in the recipient's mailbox.
	if e.Mode == "reply" && o.MessageID != "" {
		header.Set("In-Reply-To", o.MessageID)
		refs := strings.TrimSpace(o.References + " " + o.MessageID)
		header.Set("References", strings.Join(strings.Fields(refs), " "))
	}

	forwarded := e.Mode == "forward" && len(o.Raw) > 0
	var body bytes.Buffer
	if forwarded || len(e.Attachments) > 0 {
		if err := withAttachments(&body, header, e.Body, o, forwarded, e.Attachments); err != nil {
			return Message{}, err
		}
	} else {
		header.Set("Content-Type", `text/plain; charset="utf-8"`)
		header.Set("Content-Transfer-Encoding", "8bit")
		body.WriteString(e.Body)
	}

	var raw bytes.Buffer
	writeHeader(&raw, header, keepBcc)
	raw.WriteString("\r\n")
	raw.Write(body.Bytes())

	if raw.Len() > MaxSize {
		return Message{}, ErrTooLarge
	}

	// Bcc reaches SES through the recipient list, never through the header.
	rcpt := append(append(append([]string{}, to...), cc...), bcc...)
	return Message{Raw: raw.Bytes(), From: sender, To: rcpt, ID: id}, nil
}

// withAttachments builds the multipart body: the text first, then the forwarded
// message as an .eml, then the files somebody picked.
func withAttachments(body *bytes.Buffer, header textproto.MIMEHeader, text string,
	o Original, forwarded bool, files []Attachment) error {
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

	if forwarded {
		name := o.Subject
		if name == "" {
			name = "mail"
		}
		if r := []rune(name); len(r) > 60 {
			name = string(r[:60])
		}
		if err := writePart(mw, "message/rfc822", name+".eml", o.Raw, false); err != nil {
			return err
		}
	}

	for _, f := range files {
		ct := strings.TrimSpace(f.ContentType)
		if ct == "" {
			ct = "application/octet-stream"
		}
		if err := writePart(mw, ct, attachmentName(f.Filename), f.Content, true); err != nil {
			return err
		}
	}
	return mw.Close()
}

// writePart adds one attachment. Anything that is not itself a mail goes out
// base64-encoded: a raw byte in a mail body survives no hop unscathed.
func writePart(mw *multipart.Writer, contentType, filename string, content []byte, encode bool) error {
	h := textproto.MIMEHeader{
		"Content-Type": {contentType},
		"Content-Disposition": {mime.FormatMediaType("attachment",
			map[string]string{"filename": filename})},
	}
	if encode {
		h.Set("Content-Transfer-Encoding", "base64")
	}
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if !encode {
		_, err = part.Write(content)
		return err
	}
	_, err = part.Write(wrap76(base64.StdEncoding.EncodeToString(content)))
	return err
}

// wrap76 breaks the base64 text into lines. Without it the whole attachment
// becomes one line, and RFC 5322 allows 998 characters - some servers cut
// there, others refuse the message.
func wrap76(s string) []byte {
	var out bytes.Buffer
	out.Grow(len(s) + len(s)/76*2)
	for len(s) > 76 {
		out.WriteString(s[:76])
		out.WriteString("\r\n")
		s = s[76:]
	}
	out.WriteString(s)
	return out.Bytes()
}

// attachmentName keeps the file name harmless: no path, no control characters,
// and not endless.
func attachmentName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if name == "" {
		name = "anhang"
	}
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}
	return name
}

func writeHeader(w *bytes.Buffer, header textproto.MIMEHeader, keepBcc bool) {
	// A fixed order, so the output is reproducible - and a fixed list, so a Bcc
	// cannot slip into a sent message by accident. It is only written where it
	// is explicitly wanted: in a stored draft.
	names := []string{"From", "To", "Cc", "Subject", "Date", "Message-Id",
		"In-Reply-To", "References", "MIME-Version", "Content-Type",
		"Content-Transfer-Encoding"}
	if keepBcc {
		names = append(names, "Bcc")
	}
	for _, name := range names {
		if v := header.Get(name); v != "" {
			fmt.Fprintf(w, "%s: %s\r\n", name, v)
		}
	}
}

// addresses splits a recipient list and returns the bare addresses.
func addresses(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	list, err := mail.ParseAddressList(s)
	if err != nil {
		return nil, fmt.Errorf("recipient not readable: %w", err)
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.Address)
	}
	return out, nil
}

// encodeWord turns a subject with non-ASCII characters into an RFC 2047 header.
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
