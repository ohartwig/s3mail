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

func corpus(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "corpus", name+".eml"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReadTextAndHTML(t *testing.T) {
	v := Read(corpus(t, "04-alternative"), time.Unix(0, 0).UTC())
	if !strings.Contains(v.Text, "Nur-Text-Fassung mit Ümlaut") {
		t.Errorf("Fliesstext: %q", v.Text)
	}
	if !strings.Contains(v.HTML, "<b>Ümlaut</b>") {
		t.Errorf("HTML: %q", v.HTML)
	}
	if len(v.Attachments) != 0 {
		t.Errorf("an alternative has no attachments: %v", v.Attachments)
	}
}

// TestReadAttachmentsWithContent - the detail view has to deliver the
// attachment along, or the message is parsed a second time for the download.
func TestReadAttachmentsWithContent(t *testing.T) {
	v := Read(corpus(t, "05-nested-mixed"), time.Unix(0, 0).UTC())
	if len(v.Attachments) != 1 {
		t.Fatalf("%d Anhaenge", len(v.Attachments))
	}
	a := v.Attachments[0]
	if a.Filename != "Rechnung.pdf" || a.ContentType != "application/pdf" {
		t.Errorf("%+v", a)
	}
	if !strings.HasPrefix(string(a.Content), "%PDF") || a.Size != len(a.Content) {
		t.Errorf("content missing or the size is wrong: %d bytes", a.Size)
	}
	if !strings.Contains(v.Text, "Anbei die Rechnung") {
		t.Errorf("body text from the nested part is missing: %q", v.Text)
	}
}

func TestReadFilenames(t *testing.T) {
	for _, f := range []struct{ file, name string }{
		{"06-rfc2231-filename", "Bescheid Übersicht.pdf"},
		{"07-raw-filename", "Übersicht Größe.xlsx"},
	} {
		v := Read(corpus(t, f.file), time.Unix(0, 0).UTC())
		if len(v.Attachments) != 1 || v.Attachments[0].Filename != f.name {
			t.Errorf("%s: %v, expected %q", f.file, v.Attachments, f.name)
		}
	}
}

// TestReadInlineStaysOut - the logo from the signature is not an attachment
// and must not land in the text as binary garbage either.
func TestReadInlineStaysOut(t *testing.T) {
	v := Read(corpus(t, "08-inline-cid"), time.Unix(0, 0).UTC())
	if len(v.Attachments) != 0 {
		t.Errorf("inline image counted as an attachment: %v", v.Attachments)
	}
	if strings.Contains(v.Text, "PNGFAKE") || strings.Contains(v.HTML, "PNGFAKE") {
		t.Errorf("Bildinhalt im Text gelandet: %q / %q", v.Text, v.HTML)
	}
	if !strings.Contains(v.Text, "Hallo") {
		t.Errorf("text from the HTML is missing: %q", v.Text)
	}
}

func TestReadHeaders(t *testing.T) {
	v := Read(corpus(t, "13-address-commas"), time.Unix(0, 0).UTC())
	if !strings.Contains(v.From, "Firma GmbH, Abteilung Vertrieb") {
		t.Errorf("display name split at the comma: %q", v.From)
	}
	if strings.Count(v.To, "@") != 3 {
		t.Errorf("Empfaengerliste: %q", v.To)
	}
}

func TestReadSESVerdicts(t *testing.T) {
	v := Read(corpus(t, "15-ses-verdicts"), time.Unix(0, 0).UTC())
	if !v.Spam || v.Virus {
		t.Errorf("Verdicts: spam=%v virus=%v", v.Spam, v.Virus)
	}
}

func TestReadBrokenMail(t *testing.T) {
	v := Read(corpus(t, "11-broken"), time.Unix(0, 0).UTC())
	if v.Subject != SubjectNone && v.Subject != SubjectUnreadable {
		t.Errorf("subject: %q", v.Subject)
	}
	if !strings.Contains(v.Text, "kein gueltiges mime") {
		t.Errorf("raw text not shown as a fallback: %q", v.Text)
	}
}
