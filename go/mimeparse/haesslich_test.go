// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package mimeparse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The ugly half of the corpus.
//
// The cases in expected.json were compared field by field against the Python
// parser. These here have no Python to compare with - they are the messages
// that real senders produce and that parsers quietly get wrong: Outlook's
// nested multiparts, a subject in three character sets, a TNEF blob, a missing
// Message-ID, a character set that does not exist, six levels of nesting.
//
// Every assertion below is a decision written down. Where the behaviour is
// deliberate rather than obviously right, the comment says so - a later change
// should then be a decision too, not an accident.

func korpus(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "corpus", name+".eml"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

var epoche = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// Three encoded words, three character sets, one line - and a bare word between
// them. RFC 2047 allows it, senders do it, and a parser that decodes only the
// first one produces a subject that looks almost right.
func TestSubjectInDreiZeichensaetzen(t *testing.T) {
	got := Summarize(korpus(t, "20-mixed-charset-subject"), epoche).Subject
	want := "Grüße aus München und Zürich"
	if got != want {
		t.Errorf("subject:\n  want %q\n  got  %q", want, got)
	}
}

// What Outlook actually sends: multipart/related nested inside
// multipart/alternative, the boundary folded onto the next line with a tab, and
// quoted-printable that carries its own CRLF as =0D=0A.
func TestOutlookVerschachtelt(t *testing.T) {
	raw := korpus(t, "21-outlook-related")
	s := Summarize(raw, epoche)
	f := Read(raw, epoche)

	if !s.HasHTML {
		t.Error("the HTML part in the nested multipart/related was not found")
	}
	if !strings.Contains(f.Text, "Grüße") {
		t.Errorf("the iso-8859-1 text part did not come through: %q", f.Text)
	}
	// =0D=0A is a line break the sender encoded; it must not survive as literal
	// characters in the preview.
	if strings.Contains(s.Preview, "=0D") || strings.Contains(s.Preview, "0D0A") {
		t.Errorf("quoted-printable line break stayed raw in the preview: %q", s.Preview)
	}
	if strings.Contains(f.HTML, "=3D") {
		t.Errorf("quoted-printable was not decoded in the HTML: %.80q", f.HTML)
	}
	// The embedded logo is referenced by cid: from the HTML. It is deliberately
	// neither an attachment nor preview text - see roleOf. Otherwise every
	// signature with a logo would grow a paperclip that is not one.
	if len(f.Attachments) != 0 {
		t.Errorf("the inline logo was counted as an attachment: %+v", f.Attachments)
	}
}

// The other side of that rule: a part that carries a Content-ID *and* says
// attachment is an attachment. Without this the rule would swallow files.
func TestContentIDMachtNochKeinenInlineTeil(t *testing.T) {
	f := Read(korpus(t, "26-cid-but-attachment"), epoche)
	if len(f.Attachments) != 1 {
		t.Fatalf("%d attachments, expected 1: %+v", len(f.Attachments), f.Attachments)
	}
	if f.Attachments[0].Filename != "foto.png" {
		t.Errorf("attachment is called %q", f.Attachments[0].Filename)
	}
}

// winmail.dat: Outlook packs attachments into a TNEF blob when it talks to
// itself. s3mail does not unpack it - that would be a decoder of its own - but
// it has to show the blob rather than swallow it. Whoever gets one at least
// sees that something is there.
func TestWinmailDatBleibtSichtbar(t *testing.T) {
	f := Read(korpus(t, "22-winmail-dat"), epoche)
	if len(f.Attachments) != 1 {
		t.Fatalf("%d attachments, expected 1", len(f.Attachments))
	}
	a := f.Attachments[0]
	if a.Filename != "winmail.dat" {
		t.Errorf("filename %q", a.Filename)
	}
	if a.ContentType != "application/ms-tnef" {
		t.Errorf("content type %q", a.ContentType)
	}
	if a.Size == 0 {
		t.Error("the blob arrived empty")
	}
}

// Not every sender sets a Message-ID. Nothing may fall over because of it -
// neither the index nor, further up, the name a draft is stored under.
func TestOhneMessageID(t *testing.T) {
	raw := korpus(t, "23-no-message-id")
	s := Summarize(raw, epoche)
	f := Read(raw, epoche)

	if s.MessageID != "" || f.MessageID != "" {
		t.Errorf("a Message-ID appeared out of nowhere: %q / %q", s.MessageID, f.MessageID)
	}
	if s.Subject != "Kein Message-ID-Kopf" {
		t.Errorf("subject %q", s.Subject)
	}
	if !strings.Contains(f.Text, "weg") {
		t.Errorf("the body is missing: %q", f.Text)
	}
}

// A character set that does not exist, in the header and in the body. Refusing
// to read the message would be the wrong answer: the text is almost always
// readable anyway.
func TestErfundenerZeichensatz(t *testing.T) {
	raw := korpus(t, "24-header-latin1-raw")
	s := Summarize(raw, epoche)
	f := Read(raw, epoche)

	if s.Subject == "" {
		t.Error("subject lost over an unknown character set")
	}
	if !strings.Contains(f.Text, "existiert nicht") {
		t.Errorf("body lost over an unknown character set: %q", f.Text)
	}
	if !strings.Contains(s.From, "wer@example.de") {
		t.Errorf("sender lost: %q", s.From)
	}
}

// Six levels of multipart. Deep nesting is what a mail bomb looks like, and it
// is also what a forwarded forward of a forward looks like.
func TestSechsEbenenTief(t *testing.T) {
	raw := korpus(t, "25-deep-nesting")
	done := make(chan struct{})
	go func() {
		defer close(done)
		f := Read(raw, epoche)
		if !strings.Contains(f.Text, "Ganz unten") {
			t.Errorf("the innermost text was not found: %q", f.Text)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("parsing six levels took longer than five seconds")
	}
}

// None of these may panic - that is the floor below every assertion above.
// Summarize runs on every message during indexing; one bad message would
// otherwise take the whole mailbox with it.
func TestKeineDerHaesslichenBringtDenParserUm(t *testing.T) {
	for _, name := range []string{
		"20-mixed-charset-subject", "21-outlook-related", "22-winmail-dat",
		"23-no-message-id", "24-header-latin1-raw", "25-deep-nesting",
		"26-cid-but-attachment",
	} {
		raw := korpus(t, name)
		t.Run(name, func(t *testing.T) {
			_ = Summarize(raw, epoche)
			_ = Read(raw, epoche)
			// Truncated in the middle: that is what a range GET delivers, and
			// during indexing it is the normal case rather than the exception.
			for _, cut := range []int{len(raw) / 2, len(raw) / 3, 64, 1, 0} {
				_ = Summarize(raw[:cut], epoche)
				_ = Read(raw[:cut], epoche)
			}
		})
	}
}
