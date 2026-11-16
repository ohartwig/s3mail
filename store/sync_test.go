// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/s3mail/core"
	"github.com/ohartwig/s3mail/s3fake"
	"github.com/ohartwig/s3mail/store"
)

// The tests in this file are the regression guard for the S3 bill of November
// 2026: a mailbox of 227,000 objects listed in full every minute, 227 requests
// and 90 MB each time. What they pin down is how many listing pages a refresh
// costs, not only that the index comes out right.

// bigMailbox is a mailbox shaped like the real one - most of it in folders
// nobody touches, a small inbox at the top where SES writes.
func bigMailbox(t *testing.T) *s3fake.Fake {
	t.Helper()
	f := s3fake.New()
	add := func(folder string, n int) {
		for i := range n {
			key := fmt.Sprintf("mail/%s%s-%05d", folder, strings.TrimSuffix(folder, "/"), i)
			f.Objs[key] = buildMail("Bot <bot@example.org>", "me@example.org",
				fmt.Sprintf("Note %d", i), "Text.", "Mon, 03 Aug 2026 09:00:00 +0000",
				fmt.Sprintf("<%s-%d@x>", folder, i))
		}
	}
	add("gitlab/", 4000)
	add("archiv/", 1500)
	add("sent/", 300)
	add("", 40) // the inbox
	return f
}

func machine(t *testing.T, f *s3fake.Fake, dir string) *store.Mailbox {
	t.Helper()
	return store.NewMailbox(t.Context(), f, nil, "test-bucket", "mail/", dir, testCacheKey, true)
}

// listedRoot says whether anything listed the whole mailbox - List on the root
// itself, without a delimiter.
func listedRoot(f *s3fake.Fake) bool {
	for _, l := range f.Listings {
		if l.Prefix == "mail/" && !l.Dir {
			return true
		}
	}
	return false
}

func newMail(f *s3fake.Fake, key, subject string) {
	f.Store(key, buildMail("Anna <anna@example.org>", "me@example.org", subject,
		"Hallo.", "Tue, 04 Aug 2026 09:00:00 +0000", "<"+subject+"@x>"))
}

func indexed(m *store.Mailbox, key string) bool {
	for _, msg := range m.Index() {
		if msg.Key == key {
			return true
		}
	}
	return false
}

// TestTheFrequentRefreshDoesNotListTheMailbox is the number that matters: after
// the full listing at the start, an hour of minute-by-minute syncs costs about
// two requests each - the op journal and the inbox - and never the 6 pages of
// the full listing here, 227 in the real mailbox.
func TestTheFrequentRefreshDoesNotListTheMailbox(t *testing.T) {
	ctx := t.Context()
	f := bigMailbox(t)
	m := machine(t, f, t.TempDir())

	f.ClearCalls()
	res, err := m.Sync(ctx, store.SyncOpts{Inbox: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Full || !listedRoot(f) {
		t.Fatal("the first sync did not do the full listing - a start must")
	}
	if full := f.ListPages(); full < 6 {
		t.Fatalf("the full listing cost %d pages - the fixture is too small to tell", full)
	}

	f.ClearCalls()
	for range 60 {
		res, err := m.Sync(ctx, store.SyncOpts{Inbox: true})
		if err != nil {
			t.Fatal(err)
		}
		if res.Full {
			t.Fatal("a sync within the reconcile interval did the full listing")
		}
	}
	if listedRoot(f) {
		t.Error("an hour of syncs listed the whole mailbox")
	}
	if pages := f.ListPages(); pages > 2*60 {
		t.Errorf("an hour of syncs cost %d listing pages - expected at most 120", pages)
	}
	// The inbox listing returns the inbox and the folder names, not the
	// folders' contents: 40 messages and 3 common prefixes, plus the op prefix.
	if keys := f.ListedKeys(); keys > 60*(40+3+5) {
		t.Errorf("an hour of syncs listed %d keys - the folders are being paged through", keys)
	}
}

// TestWithAQueueTheSyncListsNoMail - with a queue the frequent sync reads the
// journal and nothing else: one page for the op prefix, no listing of mail.
func TestWithAQueueTheSyncListsNoMail(t *testing.T) {
	ctx := t.Context()
	f := bigMailbox(t)
	m := machine(t, f, t.TempDir())
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	f.ClearCalls()
	if _, err := m.Sync(ctx, store.SyncOpts{}); err != nil {
		t.Fatal(err)
	}
	for _, l := range f.Listings {
		if l.Prefix != "mail/"+store.StateOps {
			t.Errorf("listed %q (dir=%v) - only the op journal was expected", l.Prefix, l.Dir)
		}
	}
	// The snapshot is not fetched again while its ETag stands, and op objects
	// once read are not read again.
	for _, c := range f.Calls() {
		if strings.HasPrefix(c, "get ") {
			t.Errorf("a sync with nothing new fetched %s", c)
		}
	}
}

// TestNewMailArrivesThroughTheQueueKey - the key from the notification is
// fetched with a HEAD and a range GET; nothing is listed.
func TestNewMailArrivesThroughTheQueueKey(t *testing.T) {
	ctx := t.Context()
	f := bigMailbox(t)
	m := machine(t, f, t.TempDir())
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	newMail(f, "mail/ses-0001", "fresh")
	f.ClearCalls()
	res, err := m.Sync(ctx, store.SyncOpts{Keys: []string{"mail/ses-0001"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 1 || !indexed(m, "mail/ses-0001") {
		t.Fatalf("the announced mail is not in the index: %+v", res)
	}
	for _, l := range f.Listings {
		if l.Prefix != "mail/"+store.StateOps {
			t.Errorf("listed %q although the queue named the key", l.Prefix)
		}
	}
}

// TestAKeyOutsideTheMailboxIsIgnored - a notification is input from outside;
// it does not get to point the client at somebody else's prefix.
func TestAKeyOutsideTheMailboxIsIgnored(t *testing.T) {
	ctx := t.Context()
	f := bigMailbox(t)
	f.Objs["other/secret"] = []byte("Subject: no\r\n\r\nno")
	m := machine(t, f, t.TempDir())
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	f.ClearCalls()
	if _, err := m.Sync(ctx, store.SyncOpts{Keys: []string{"other/secret", "mail/.s3mail-state.json"}}); err != nil {
		t.Fatal(err)
	}
	if indexed(m, "other/secret") || indexed(m, "mail/.s3mail-state.json") {
		t.Error("a key outside the mailbox made it into the index")
	}
	for _, c := range f.Calls() {
		if strings.Contains(c, "other/secret") {
			t.Errorf("touched a foreign key: %s", c)
		}
	}
}

// TestTheSecondMachineHearsOfMailTheFirstTookFromTheQueue - SQS hands a
// notification to one receiver. The one that got it writes the key into the
// journal, and the other picks it up from there.
func TestTheSecondMachineHearsOfMailTheFirstTookFromTheQueue(t *testing.T) {
	ctx := t.Context()
	f := bigMailbox(t)
	a, b := machine(t, f, t.TempDir()), machine(t, f, t.TempDir())
	for _, m := range []*store.Mailbox{a, b} {
		if _, err := m.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
	}
	newMail(f, "mail/ses-0002", "shared")
	if _, err := a.Sync(ctx, store.SyncOpts{Keys: []string{"mail/ses-0002"}, Announce: true}); err != nil {
		t.Fatal(err)
	}

	f.ClearCalls()
	res, err := b.Sync(ctx, store.SyncOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !indexed(b, "mail/ses-0002") {
		t.Fatalf("the second machine did not hear of the mail: %+v", res)
	}
	if listedRoot(f) {
		t.Error("the second machine listed the mailbox to find it")
	}
}

// TestRemovalShowsUp - a named key that is gone leaves the index, and so does
// an inbox message somebody removed (the inbox listing sees it).
func TestRemovalShowsUp(t *testing.T) {
	ctx := t.Context()
	f := bigMailbox(t)
	m := machine(t, f, t.TempDir())
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	gone, alsoGone := "mail/-00001", "mail/-00002"
	if !indexed(m, gone) || !indexed(m, alsoGone) {
		t.Fatal("fixture: inbox keys not as expected")
	}
	delete(f.Objs, gone)
	delete(f.Objs, alsoGone)

	res, err := m.Sync(ctx, store.SyncOpts{Keys: []string{gone}})
	if err != nil {
		t.Fatal(err)
	}
	if indexed(m, gone) || res.Removed != 1 {
		t.Errorf("a named key that is gone stayed: %+v", res)
	}
	if _, err := m.Sync(ctx, store.SyncOpts{Inbox: true}); err != nil {
		t.Fatal(err)
	}
	if indexed(m, alsoGone) {
		t.Error("the inbox listing did not drop a removed message")
	}
	if !indexed(m, "mail/gitlab/gitlab-00001") {
		t.Error("the inbox listing dropped a message in a folder it did not list")
	}
}

// TestAnotherMachinesMoveArrivesThroughTheJournal - a move is copy and delete;
// the journal says which key went where, and the entry moves without a listing
// and without fetching the message again.
func TestAnotherMachinesMoveArrivesThroughTheJournal(t *testing.T) {
	ctx := t.Context()
	f := bigMailbox(t)
	a, b := machine(t, f, t.TempDir()), machine(t, f, t.TempDir())
	for _, m := range []*store.Mailbox{a, b} {
		if _, err := m.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
	}
	res, err := a.Move(ctx, []string{"mail/-00003"}, "archiv")
	if err != nil || res[0].Err != "" {
		t.Fatal(err, res)
	}
	moved := res[0].NewKey

	f.ClearCalls()
	sync, err := b.Sync(ctx, store.SyncOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if indexed(b, "mail/-00003") || !indexed(b, moved) {
		t.Fatalf("the move did not arrive: %+v", sync)
	}
	for _, c := range f.Calls() {
		if strings.HasPrefix(c, "get "+moved) || strings.HasPrefix(c, "list") && !strings.Contains(c, store.StateOps) {
			t.Errorf("the move cost more than the journal: %s", c)
		}
	}
	if sync.Changes == 0 {
		t.Error("the page would not be told about the move")
	}
}

// TestAnotherMachinesDeleteAndFlagsArrive - a delete from the trash and a read
// mark both reach the second machine through the journal.
func TestAnotherMachinesDeleteAndFlagsArrive(t *testing.T) {
	ctx := t.Context()
	f := bigMailbox(t)
	a, b := machine(t, f, t.TempDir()), machine(t, f, t.TempDir())
	if _, err := a.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := a.Move(ctx, []string{"mail/-00004"}, core.Trash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Delete(ctx, []string{res[0].NewKey}, false); err != nil {
		t.Fatal(err)
	}
	read := true
	if err := a.State.Mutate(ctx, core.Op{T: "flags", Mids: []string{"-00005"}, Read: &read}); err != nil {
		t.Fatal(err)
	}

	f.ClearCalls()
	if _, err := b.Sync(ctx, store.SyncOpts{}); err != nil {
		t.Fatal(err)
	}
	if indexed(b, res[0].NewKey) {
		t.Error("the deleted message is still in the second machine's index")
	}
	if !b.State.Get("-00005").Read {
		t.Error("the read mark did not arrive")
	}
	if listedRoot(f) {
		t.Error("listed the mailbox for it")
	}
}

// TestTheReconcileCatchesDrift - what bypasses the journal (an older client,
// the console) is invisible to the sync, and the full listing every
// ReconcileEvery is what finds it.
func TestTheReconcileCatchesDrift(t *testing.T) {
	ctx := t.Context()
	f := bigMailbox(t)
	m := machine(t, f, t.TempDir())
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	// An old client moves without a journal entry.
	f.Objs["mail/archiv/drifted"] = f.Objs["mail/gitlab/gitlab-00007"]
	delete(f.Objs, "mail/gitlab/gitlab-00007")

	if _, err := m.Sync(ctx, store.SyncOpts{Inbox: true}); err != nil {
		t.Fatal(err)
	}
	if indexed(m, "mail/archiv/drifted") {
		t.Fatal("fixture: the sync saw a folder it does not list - the test proves nothing")
	}

	m.ReconcileEvery = time.Nanosecond
	res, err := m.Sync(ctx, store.SyncOpts{Inbox: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Full || !indexed(m, "mail/archiv/drifted") || indexed(m, "mail/gitlab/gitlab-00007") {
		t.Errorf("the due reconcile did not catch the drift: %+v", res)
	}
}

// compactBehindB has machine a move a message and then write enough ops to
// compact them all away before b has read any of it.
func compactBehindB(t *testing.T) (f *s3fake.Fake, b *store.Mailbox, movedTo string) {
	t.Helper()
	ctx := t.Context()
	f = bigMailbox(t)
	a := machine(t, f, t.TempDir())
	b = machine(t, f, t.TempDir())
	for _, m := range []*store.Mailbox{a, b} {
		if _, err := m.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
	}
	res, err := a.Move(ctx, []string{"mail/-00008"}, "archiv")
	if err != nil {
		t.Fatal(err)
	}
	read := true
	for i := range store.CompactAfter + 5 {
		if err := a.State.Mutate(ctx, core.Op{T: "flags", Mids: []string{fmt.Sprintf("x%d", i)}, Read: &read}); err != nil {
			t.Fatal(err)
		}
	}
	if a.State.Upto() == "" {
		t.Fatal("fixture: nothing was compacted")
	}
	return f, b, res[0].NewKey
}

// TestACompactedMoveArrivesThroughTheSnapshot - every compaction removes at
// least one op nobody else has read (the one that triggered it). The snapshot
// keeps the key changes it merged, so the other machine catches up without
// listing.
func TestACompactedMoveArrivesThroughTheSnapshot(t *testing.T) {
	f, b, movedTo := compactBehindB(t)
	f.ClearCalls()
	sync, err := b.Sync(t.Context(), store.SyncOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if sync.Full || listedRoot(f) {
		t.Error("a compaction by another machine cost a full listing")
	}
	if !indexed(b, movedTo) || indexed(b, "mail/-00008") {
		t.Error("the compacted move did not arrive")
	}
}

// TestAGapForcesTheFullListing - a snapshot written by a version without the
// journal tail cannot say what the compacted ops did; the sync has to fall back
// to the listing instead of missing the move.
func TestAGapForcesTheFullListing(t *testing.T) {
	f, b, movedTo := compactBehindB(t)
	// What an older s3mail writes when it compacts: the same document, no tail.
	var snap map[string]any
	if err := json.Unmarshal(f.Objs["mail/"+store.StateObject], &snap); err != nil {
		t.Fatal(err)
	}
	delete(snap, "journal")
	delete(snap, "journal_since")
	delete(snap, "journal_ok")
	raw, _ := json.Marshal(snap)
	f.Store("mail/"+store.StateObject, raw)

	sync, err := b.Sync(t.Context(), store.SyncOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !sync.Full {
		t.Error("a gap in the journal did not force the full listing")
	}
	if !indexed(b, movedTo) || indexed(b, "mail/-00008") {
		t.Error("the move hidden in the gap did not arrive")
	}
}

// TestARestartNeedsNoSecondFullListing - the index, the time of the last full
// listing and the journal cursor live in the cache, so a restart within the
// interval carries on with the sync. A restart after it lists once.
func TestARestartNeedsNoSecondFullListing(t *testing.T) {
	ctx := t.Context()
	f := bigMailbox(t)
	dir := t.TempDir()
	first := machine(t, f, dir)
	if _, err := first.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	other := machine(t, f, t.TempDir())
	if _, err := other.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	// While the first is closed, another machine moves something.
	moved, err := other.Move(ctx, []string{"mail/-00009"}, "archiv")
	if err != nil {
		t.Fatal(err)
	}

	f.ClearCalls()
	again := machine(t, f, dir)
	res, err := again.Sync(ctx, store.SyncOpts{Inbox: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Full || listedRoot(f) {
		t.Error("a restart within the interval listed the whole mailbox again")
	}
	if !indexed(again, moved[0].NewKey) || indexed(again, "mail/-00009") {
		t.Error("the move made while it was closed did not arrive")
	}

	late := machine(t, f, dir)
	late.ReconcileEvery = time.Nanosecond
	res, err = late.Sync(ctx, store.SyncOpts{Inbox: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Full {
		t.Error("a restart after the interval did not list")
	}
}
