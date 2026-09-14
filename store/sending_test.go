// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"strings"
	"testing"

	"github.com/ohartwig/s3mail/core"
	"github.com/ohartwig/s3mail/s3fake"
	"github.com/ohartwig/s3mail/store"
)

// The send path is the only place where a crash costs a second mail. These
// tests play the crash - at every point where it can happen - and check that
// the next start does the right thing without asking, or asks where the bucket
// genuinely cannot know.

const draftRaw = "From: post@firma.de\r\nTo: kunde@x.de\r\n" +
	"Subject: Rechnung 7\r\nMessage-ID: <r7@firma.de>\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n\r\nAnbei.\r\n"

func mailboxFor(t *testing.T) (*s3fake.Fake, *store.Mailbox) {
	t.Helper()
	f := s3fake.New()
	mb := store.NewMailbox(t.Context(), f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	return f, mb
}

// storeDraft puts a draft into the bucket the way the compose dialog does.
func storeDraft(t *testing.T, mb *store.Mailbox) string {
	t.Helper()
	key, err := mb.Put(t.Context(), core.Drafts, "<r7@firma.de>", []byte(draftRaw))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func sendingFor(draftKey string) store.Sending {
	return store.Sending{To: "kunde@x.de", Subject: "Rechnung 7",
		DraftKey: draftKey, MessageID: "<r7@firma.de>", Raw: []byte(draftRaw)}
}

func keysContaining(f *s3fake.Fake, part string) []string {
	var out []string
	for k := range f.Objs {
		if strings.Contains(k, part) {
			out = append(out, k)
		}
	}
	return out
}

func TestMarkerSitsNextToTheDraft(t *testing.T) {
	ctx := t.Context()
	f, mb := mailboxFor(t)
	draft := storeDraft(t, mb)

	key, err := mb.BeginSend(ctx, sendingFor(draft))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "mail/drafts/") {
		t.Errorf("the marker is not in the drafts folder: %s", key)
	}
	if !strings.HasSuffix(key, ".sending") {
		t.Errorf("the marker is not recognisable as one: %s", key)
	}
	if _, ok := f.Objs[key]; !ok {
		t.Error("the marker was not written")
	}
}

// The state that matters most: SES took the message, then the program died.
// Nobody may be asked about this - the answer is known - and the copy has to
// appear without anyone doing anything.
func TestCrashAfterSESIsFinishedQuietly(t *testing.T) {
	ctx := t.Context()
	f, mb := mailboxFor(t)
	draft := storeDraft(t, mb)

	key, err := mb.BeginSend(ctx, sendingFor(draft))
	if err != nil {
		t.Fatal(err)
	}
	if err := mb.MarkSent(ctx, key, "ses-0815"); err != nil {
		t.Fatal(err)
	}
	// crash here: no copy, no draft removed, no marker deleted.

	second := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	questions, err := second.RecoverSends(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 0 {
		t.Fatalf("asked about a send whose answer was known: %+v", questions)
	}
	if n := len(keysContaining(f, "mail/sent/")); n != 1 {
		t.Errorf("%d copies in sent/, expected 1", n)
	}
	if _, ok := f.Objs[draft]; ok {
		t.Error("the draft is still there after the send was finished")
	}
	if _, ok := f.Objs[key]; ok {
		t.Error("the marker was not cleared")
	}
}

// The other state: the program died between writing the marker and hearing back
// from SES. The bucket cannot know, so it must not guess - in either direction.
func TestCrashBeforeTheAnswerAsks(t *testing.T) {
	ctx := t.Context()
	f, mb := mailboxFor(t)
	draft := storeDraft(t, mb)

	if _, err := mb.BeginSend(ctx, sendingFor(draft)); err != nil {
		t.Fatal(err)
	}
	// crash here: SES may have taken it, or not.

	second := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	questions, err := second.RecoverSends(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 1 {
		t.Fatalf("%d questions, expected 1", len(questions))
	}
	if questions[0].Subject != "Rechnung 7" || questions[0].To != "kunde@x.de" {
		t.Errorf("the question does not say what it is about: %+v", questions[0])
	}
	if n := len(keysContaining(f, "mail/sent/")); n != 0 {
		t.Error("a copy was filed although nobody knows whether it went out")
	}
	if _, ok := f.Objs[draft]; !ok {
		t.Error("the draft was removed although the send is unclear")
	}
}

func TestAnsweringYesFilesTheCopy(t *testing.T) {
	ctx := t.Context()
	f, mb := mailboxFor(t)
	draft := storeDraft(t, mb)
	key, err := mb.BeginSend(ctx, sendingFor(draft))
	if err != nil {
		t.Fatal(err)
	}
	if err := mb.ResolveSending(ctx, key, true); err != nil {
		t.Fatal(err)
	}
	if n := len(keysContaining(f, "mail/sent/")); n != 1 {
		t.Errorf("%d copies in sent/", n)
	}
	if _, ok := f.Objs[key]; ok {
		t.Error("the marker survived the answer")
	}
}

// "It did not go out" has to leave the draft alone - that is what somebody
// sends again afterwards.
func TestAnsweringNoKeepsTheDraft(t *testing.T) {
	ctx := t.Context()
	f, mb := mailboxFor(t)
	draft := storeDraft(t, mb)
	key, err := mb.BeginSend(ctx, sendingFor(draft))
	if err != nil {
		t.Fatal(err)
	}
	if err := mb.ResolveSending(ctx, key, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Objs[draft]; !ok {
		t.Error("the draft is gone although the mail was never sent")
	}
	if n := len(keysContaining(f, "mail/sent/")); n != 0 {
		t.Error("a copy was filed for a mail that did not go out")
	}
	if _, ok := f.Objs[key]; ok {
		t.Error("the marker survived")
	}
}

// SES refused: nothing went out, so the marker has to go rather than turn into
// a question with no content.
func TestRefusedLeavesNoQuestion(t *testing.T) {
	ctx := t.Context()
	f, mb := mailboxFor(t)
	draft := storeDraft(t, mb)
	key, err := mb.BeginSend(ctx, sendingFor(draft))
	if err != nil {
		t.Fatal(err)
	}
	if err := mb.AbandonSend(ctx, key); err != nil {
		t.Fatal(err)
	}
	questions, err := mb.RecoverSends(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 0 {
		t.Errorf("a refused send left a question behind: %+v", questions)
	}
	if _, ok := f.Objs[draft]; !ok {
		t.Error("the draft is gone although SES refused")
	}
}

// Recovery runs at every start. Running it twice must not produce a second
// copy - that would be the very thing this is built to prevent.
func TestRecoveringTwiceFilesOneCopy(t *testing.T) {
	ctx := t.Context()
	f, mb := mailboxFor(t)
	draft := storeDraft(t, mb)
	key, _ := mb.BeginSend(ctx, sendingFor(draft))
	if err := mb.MarkSent(ctx, key, "ses-0815"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := mb.RecoverSends(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(keysContaining(f, "mail/sent/")); n != 1 {
		t.Errorf("%d copies in sent/ after three recoveries", n)
	}
}

// The marker carries the message itself, and that is not redundancy: step 5
// removes the draft before step 6 removes the marker, so a crash in between
// leaves a marker whose draft is already gone. The copy still has to be
// possible.
func TestCopyWorksWithoutTheDraft(t *testing.T) {
	ctx := t.Context()
	f, mb := mailboxFor(t)
	draft := storeDraft(t, mb)
	key, _ := mb.BeginSend(ctx, sendingFor(draft))
	if err := mb.MarkSent(ctx, key, "ses-0815"); err != nil {
		t.Fatal(err)
	}
	if err := mb.DropDraft(ctx, draft); err != nil { // step 5 ran, step 6 did not
		t.Fatal(err)
	}
	if _, err := mb.RecoverSends(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(keysContaining(f, "mail/sent/")); n != 1 {
		t.Errorf("%d copies in sent/ - the marker could not rebuild it", n)
	}
}

// A marker is not a message. It must not show up in the mailbox as one.
func TestMarkerIsNotAMessage(t *testing.T) {
	ctx := t.Context()
	f, mb := mailboxFor(t)
	draft := storeDraft(t, mb)
	if _, err := mb.BeginSend(ctx, sendingFor(draft)); err != nil {
		t.Fatal(err)
	}
	second := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := second.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for _, msg := range second.Index() {
		if strings.HasSuffix(msg.Key, ".sending") {
			t.Errorf("the marker was indexed as a message: %s", msg.Key)
		}
	}
}

// AbandonSend takes a key from outside. It must not become a way to delete
// something that is not a marker.
func TestAbandonDeletesOnlyMarkers(t *testing.T) {
	ctx := t.Context()
	f, mb := mailboxFor(t)
	draft := storeDraft(t, mb)
	if err := mb.AbandonSend(ctx, draft); err == nil {
		t.Error("AbandonSend deleted a draft")
	}
	if _, ok := f.Objs[draft]; !ok {
		t.Error("the draft is gone")
	}
}
