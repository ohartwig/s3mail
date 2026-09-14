// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package mimeparse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestAgainstPython runs every message from the corpus through the Go parser
// and compares field by field with what the existing Python parser delivers.
func TestAgainstPython(t *testing.T) {
	blob, err := os.ReadFile(filepath.Join("testdata", "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]Summary
	if err := json.Unmarshal(blob, &want); err != nil {
		t.Fatal(err)
	}
	fallback := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	slices.Sort(names)

	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join("testdata", "corpus", name+".eml"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := Summarize(raw, fallback)
		w := want[name]

		cmp := func(field, a, b string) {
			if a == b {
				return
			}
			if reason, wanted := deviations[name+"/"+field]; wanted {
				t.Logf("%s / %s: bewusst anders (%s)\n  python: %q\n  go:     %q",
					name, field, reason, b, a)
				return
			}
			t.Errorf("%s / %s:\n  python: %q\n  go:     %q", name, field, b, a)
		}
		cmp("subject", got.Subject, w.Subject)
		cmp("from", got.From, w.From)
		cmp("to", got.To, w.To)
		cmp("cc", got.Cc, w.Cc)
		cmp("preview", got.Preview, w.Preview)
		cmp("spam", got.Spam, w.Spam)
		if got.HasHTML != w.HasHTML {
			t.Errorf("%s / has_html: python=%v go=%v", name, w.HasHTML, got.HasHTML)
		}
		if len(got.Attachments) != len(w.Attachments) {
			if reason, wanted := deviations[name+"/anhaenge"]; wanted {
				t.Logf("%s / anhaenge: bewusst anders (%s) python=%v go=%v",
					name, reason, attachmentNames(w.Attachments), attachmentNames(got.Attachments))
				continue
			}
			t.Errorf("%s / anhaenge: python=%d %v, go=%d %v",
				name, len(w.Attachments), attachmentNames(w.Attachments),
				len(got.Attachments), attachmentNames(got.Attachments))
			continue
		}
		for i := range got.Attachments {
			cmp("anhang-name", got.Attachments[i].Name, w.Attachments[i].Name)
			cmp("anhang-ctype", got.Attachments[i].CType, w.Attachments[i].CType)
		}
	}
}

// deviations are the places where the Go parser deliberately delivers
// something else than the Python parser - all of them because Python loses
// data there. Every line here is a decision, not carelessness.
var deviations = map[string]string{
	"03-cp1252-8bit/subject": "roher 8-Bit-Header wird als windows-1252 gerettet; " +
		"Python setzt Ersatzzeichen, der Text ist dort verloren",
	"08-inline-cid/anhaenge": "Bild, auf das das HTML per cid: zeigt, ist kein Anhang; " +
		"Python haengt an jede Signatur mit Logo eine Bueroklammer",
	"10-lying-charset/preview": "Body ist UTF-8, obwohl us-ascii deklariert ist - " +
		"Go erkennt das, Python zerlegt die Umlaute",
}

func attachmentNames(a []Attachment) []string {
	out := make([]string, len(a))
	for i, x := range a {
		out[i] = x.Name
	}
	return out
}

// TestBareAddrGroupsTheSameSender - a display name varies between two messages
// from the same person, and capitals vary too. Anything that groups by sender
// has to see one sender, not three.
func TestBareAddrGroupsTheSameSender(t *testing.T) {
	same := []string{"anna@x.de", "Anna <anna@x.de>", "\"Meier, Anna\" <Anna@X.de>",
		"=?utf-8?q?Anna_M=C3=BCller?= <anna@x.de>"}
	for _, header := range same {
		if got := BareAddr(header); got != "anna@x.de" {
			t.Errorf("%q -> %q, expected anna@x.de", header, got)
		}
	}
	for _, junk := range []string{"", "   ", "kein-at-zeichen"} {
		if got := BareAddr(junk); got != "" {
			t.Errorf("%q -> %q, expected nothing", junk, got)
		}
	}
}

// TestTheIndexCarriesTheSenderAddress - the field existed and nothing filled
// it. Anything reaching for it silently got "".
func TestTheIndexCarriesTheSenderAddress(t *testing.T) {
	raw := []byte("From: \"Shop\" <News@Shop.IO>\r\nTo: a@b.de\r\nSubject: x\r\n" +
		"Date: Mon, 03 Aug 2026 09:00:00 +0000\r\n\r\nText\r\n")
	s := Summarize(raw, time.Unix(0, 0).UTC())
	if s.FromAddr != "news@shop.io" {
		t.Errorf("FromAddr = %q", s.FromAddr)
	}
}

// The preview of an HTML mail has to read like text, not like markup.
//
// The entity list here used to have six entries, and a newsletter showed up in
// the message list as "Die Wochenk&uuml;che &middot; Januar" — found by looking
// at a screenshot, not by a test, which is why there is one now. Every entity
// worth handling is in the standard library; a subset maintained by hand could
// only ever be incomplete.
func TestThePreviewResolvesEveryEntityAndNotSix(t *testing.T) {
	for in, want := range map[string]string{
		// The six that used to be covered.
		"<p>A&nbsp;B</p>":        "A B",
		"<p>Tom &amp; Jerry</p>": "Tom & Jerry",
		"<p>&lt;tag&gt;</p>":     "<tag>",
		// And the ones that were not.
		"<h1>Die Wochenk&uuml;che</h1>":     "Die Wochenküche",
		"<p>Ausgabe 12 &middot; Januar</p>": "Ausgabe 12 · Januar",
		"<p>caf&eacute;</p>":                "café",
		"<p>&#8217;s</p>":                   "’s",
		"<p>3 &times; 4 &ndash; 5</p>":      "3 × 4 – 5",
	} {
		if got := StripHTML(in); got != want {
			t.Errorf("StripHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

// Script and style never belong in a preview: their content is code, and a
// mail whose first hundred characters are CSS shows nothing in the list.
func TestThePreviewDropsScriptAndStyle(t *testing.T) {
	got := StripHTML(
		`<style>.a{color:red}</style><p>Hallo</p><script>alert(1)</script>`)
	if got != "Hallo" {
		t.Errorf("got %q", got)
	}
}
