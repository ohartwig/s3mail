// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package demo is a mailbox somebody can look at before they own one.
//
// It exists for three reasons that turn out to be the same reason. Apple will
// not review an app a reviewer cannot use, and pairing needs an AWS account and
// a desktop program. Anybody arriving from a store page wants to see what this
// is before setting up a bucket. And screenshots have to come from somewhere
// that is not real mail.
//
// What matters is what it is *not*: a mock of the mailbox. The sample runs
// through store.Mailbox over s3fake, so it is the real listing, the real MIME
// parsing, the real folders and the real state log - only the bucket is made
// up. A demo built any other way drifts away from the product it advertises,
// and the drift shows up as a screenshot of something that no longer exists.
package demo

import (
	"embed"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"git.ole-hartwig.eu/development/s3mail/s3mail/go/core"
)

// Sample mail as files rather than as string constants in Go: the code here is
// English and free of umlauts by convention, and this content is neither.
//
//go:embed mail
var files embed.FS

// Root is the prefix the sample mailbox lives under. Any prefix would do - it
// is a made-up bucket - but a name that says what it is beats "mail/" in a
// screenshot of a cache directory.
const Root = "demo/"

// Where each sample message sits, and how far in the past.
//
// The age is subtracted from the caller's clock rather than baked into the
// files, because a mailbox whose newest mail is from January looks abandoned in
// a screenshot taken in August.
// The folder names come from core and are not written out here. They are real
// S3 prefixes, and one of them is not the English word: Archive is "archiv".
// Spelling it by hand put the sample into a folder of its own, next to the real
// archive - which a screenshot showed and a test did not, because a folder
// somebody made up lists exactly like one that was always there.
var placement = map[string]struct {
	folder string
	age    time.Duration
}{
	"10-welcome.eml":    {core.Inbox, 35 * time.Minute},
	"20-newsletter.eml": {core.Inbox, 5 * time.Hour},
	"30-attachment.eml": {core.Inbox, 26 * time.Hour},
	"40-sent.eml":       {core.Sent, 25 * time.Hour},
	"50-archive.eml":    {core.Archive, 9 * 24 * time.Hour},
}

// Languages lists what the sample mail has been written in.
func Languages() []string {
	entries, err := files.ReadDir("mail")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out
}

// Objects builds the sample bucket, keyed the way a real one would be: the
// folder is the prefix and the message id is the basename.
//
// An unknown language falls back to English rather than to an empty mailbox. A
// person who set their phone to a language nobody has written sample mail in
// should still see a mailbox.
func Objects(lang string, now time.Time) (map[string][]byte, error) {
	dir := "mail/" + language(lang)
	entries, err := files.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("no sample mail for %q: %w", lang, err)
	}

	out := make(map[string][]byte, len(entries))
	for _, e := range entries {
		body, err := files.ReadFile(path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		where, known := placement[e.Name()]
		if !known {
			// A file nobody placed lands in the inbox rather than nowhere: a
			// sample message that silently disappears is harder to notice than
			// one in the wrong folder.
			where.folder = ""
		}
		key := Root + strings.TrimSuffix(e.Name(), ".eml")
		if where.folder != "" {
			key = Root + where.folder + "/" + strings.TrimSuffix(e.Name(), ".eml")
		}
		out[key] = dated(body, now.Add(-where.age))
	}
	return out, nil
}

// language picks the closest sample mail we have.
func language(want string) string {
	want = strings.ToLower(strings.TrimSpace(want))
	if i := strings.IndexAny(want, "-_"); i > 0 {
		// "de-DE" and "es-419" are the same sample mail as "de" and "es".
		want = want[:i]
	}
	for _, have := range Languages() {
		if have == want {
			return want
		}
	}
	return "en"
}

// dated replaces the Date header so the sample looks like it arrived recently.
//
// Only the first one, and only in the head: a Date inside a forwarded message
// or an attachment is part of the content, and rewriting it would be a demo
// quietly editing mail.
func dated(raw []byte, when time.Time) []byte {
	text := string(raw)
	head, body, split := strings.Cut(text, "\n\n")
	if !split {
		return raw
	}
	lines := strings.Split(head, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "Date:") {
			lines[i] = "Date: " + when.UTC().Format(time.RFC1123Z)
			break
		}
	}
	return []byte(strings.Join(lines, "\n") + "\n\n" + body)
}
