// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package demo_test

import (
	"strings"
	"testing"
	"time"

	"git.ole-hartwig.eu/development/s3mail/s3mail/go/core"
	"git.ole-hartwig.eu/development/s3mail/s3mail/go/demo"
	"git.ole-hartwig.eu/development/s3mail/s3mail/go/mimeparse"
	"git.ole-hartwig.eu/development/s3mail/s3mail/go/s3fake"
	"git.ole-hartwig.eu/development/s3mail/s3mail/go/store"
)

var when = time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

// The sample has to survive the real mailbox, not a mock of it.
//
// This is the whole claim the package makes: the same listing, the same MIME
// parsing, the same folders. If it only held together against a stub, a
// screenshot would advertise something that does not exist.
func TestTheSampleIsAMailboxAndNotAMockOfOne(t *testing.T) {
	for _, lang := range demo.Languages() {
		t.Run(lang, func(t *testing.T) {
			mb := open(t, lang)

			msgs := inFolder(mb, "")
			if len(msgs) < 3 {
				t.Fatalf("%d messages in the inbox, the sample looks empty", len(msgs))
			}
			for _, m := range msgs {
				if strings.TrimSpace(m.Subject) == "" {
					t.Errorf("a message without a subject: %+v", m)
				}
				if strings.TrimSpace(m.From) == "" {
					t.Errorf("%q has no sender", m.Subject)
				}
			}
		})
	}
}

// Folders are real prefixes, so the sample has to put mail in them the same
// way - otherwise the folder list in a screenshot is empty and the feature
// looks broken.
func TestTheSampleFillsMoreThanTheInbox(t *testing.T) {
	mb := open(t, "en")

	// The constants and not the words: "archiv" is not the English spelling, and
	// writing it out by hand is what put the sample in a folder of its own.
	for _, folder := range []string{core.Sent, core.Archive} {
		if len(inFolder(mb, folder)) == 0 {
			t.Errorf("%s is empty - the folder list has nothing to show", folder)
		}
	}
}

// The subjects go through RFC 2047 on purpose: German and Spanish sample mail
// carries encoded words, which is what real mail does and what a demo written
// in ASCII would never exercise.
func TestAnEncodedSubjectArrivesReadable(t *testing.T) {
	mb := open(t, "de")

	for _, m := range inFolder(mb, "") {
		if strings.Contains(m.Subject, "=?") {
			t.Errorf("subject was not decoded: %q", m.Subject)
		}
	}
}

// HTML, an attachment, and a remote image: the three things the message view
// has anything to say about. A sample without them shows an empty client.
func TestTheSampleShowsWhatTheClientCanDo(t *testing.T) {
	mb := open(t, "en")

	var html, attached, remoteImage bool
	for _, m := range inFolder(mb, "") {
		raw, err := mb.Fetch(t.Context(), m.Key, 0)
		if err != nil {
			t.Fatalf("%q does not open: %v", m.Subject, err)
		}
		full := mimeparse.Read(raw, when)
		if strings.TrimSpace(full.HTML) != "" {
			html = true
			if strings.Contains(full.HTML, "https://") {
				remoteImage = true
			}
		}
		if len(full.Attachments) > 0 {
			attached = true
		}
	}
	if !html {
		t.Error("no HTML mail - the renderer has nothing to show")
	}
	if !attached {
		t.Error("no attachment - the attachment row never appears")
	}
	if !remoteImage {
		t.Error("no remote image - nothing demonstrates that they are blocked")
	}
}

// Sample mail dated in January looks abandoned in a screenshot taken in August.
func TestTheSampleIsDatedFromTheCallersClock(t *testing.T) {
	objs, err := demo.Objects("en", when)
	if err != nil {
		t.Fatal(err)
	}
	for key, raw := range objs {
		if strings.Contains(string(raw), "Date: Mon, 01 Jan 2026") {
			t.Errorf("%s still carries the date from the file", key)
		}
	}
}

// A language nobody wrote sample mail in is not an empty mailbox.
func TestAnUnknownLanguageStillGetsAMailbox(t *testing.T) {
	for _, lang := range []string{"fr", "", "de-DE", "es-419"} {
		objs, err := demo.Objects(lang, when)
		if err != nil {
			t.Fatalf("%q: %v", lang, err)
		}
		if len(objs) == 0 {
			t.Errorf("%q got nothing to look at", lang)
		}
	}
}

func open(t *testing.T, lang string) *store.Mailbox {
	t.Helper()
	objs, err := demo.Objects(lang, when)
	if err != nil {
		t.Fatal(err)
	}
	f := s3fake.New()
	for key, body := range objs {
		f.Objs[key] = body
	}
	mb := store.NewMailbox(t.Context(), f, nil, "demo", demo.Root,
		t.TempDir(), nil, false)
	if _, err := mb.Refresh(t.Context()); err != nil {
		t.Fatalf("the sample bucket does not refresh: %v", err)
	}
	return mb
}

// inFolder is Search with the one option this file ever needs.
func inFolder(mb *store.Mailbox, folder string) []core.Message {
	return mb.Search("", core.SearchOpts{Folder: &folder})
}
