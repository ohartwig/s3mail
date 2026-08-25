// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"git.ole-hartwig.eu/development/s3mail/s3mail/s3fake"
	"git.ole-hartwig.eu/development/s3mail/s3mail/store"
)

// The UID rules were proved in core. Here they meet the bucket: does a refresh
// number what arrived, does a move give the number back, and do two machines
// looking at the same bucket arrive at the same numbering.

func numbered(t *testing.T, mb *store.Mailbox, folder string, mids ...string) []uint32 {
	t.Helper()
	out := make([]uint32, 0, len(mids))
	for _, mid := range mids {
		n, ok := mb.UID(folder, mid)
		if !ok {
			t.Fatalf("%s has no UID in %q", mid, folder)
		}
		out = append(out, n)
	}
	return out
}

// storeAt puts a message in the bucket with a given arrival time - the order of
// arrival is what the numbering has to follow.
func storeAt(f *s3fake.Fake, key string, when time.Time) {
	f.Store(key, []byte(fmt.Sprintf(
		"From: a@b.de\r\nTo: post@firma.de\r\nSubject: %s\r\n\r\nText.\r\n", key)))
	f.SetModified(key, when)
}

func TestRefreshNumbersWhatArrived(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	base := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	storeAt(f, "mail/zweite", base.Add(time.Hour))
	storeAt(f, "mail/erste", base)

	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mb.EnsureAllUIDs(ctx); err != nil {
		t.Fatal(err)
	}

	// Ascending by arrival, not by key: "zweite" sorts before "erste"
	// alphabetically, and a client that trusts UIDs would get the order wrong.
	got := numbered(t, mb, "", "erste", "zweite")
	if got[0] != 1 || got[1] != 2 {
		t.Errorf("numbering is %v - expected the earlier message to get 1", got)
	}
	if next := mb.UIDNext(""); next != 3 {
		t.Errorf("UIDNEXT is %d, expected 3", next)
	}
}

// A second pass must not renumber anything - a UID is stable forever, and a
// client that cached one has to keep finding the same message under it.
func TestASecondRefreshChangesNothing(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	base := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	storeAt(f, "mail/a", base)
	storeAt(f, "mail/b", base.Add(time.Hour))

	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mb.EnsureAllUIDs(ctx); err != nil {
		t.Fatal(err)
	}
	before := numbered(t, mb, "", "a", "b")

	storeAt(f, "mail/c", base.Add(2*time.Hour))
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mb.EnsureAllUIDs(ctx); err != nil {
		t.Fatal(err)
	}
	after := numbered(t, mb, "", "a", "b")
	if before[0] != after[0] || before[1] != after[1] {
		t.Errorf("numbers moved: %v -> %v", before, after)
	}
	if n := numbered(t, mb, "", "c")[0]; n != 3 {
		t.Errorf("the new message got %d, expected 3", n)
	}
	if v := mb.UIDValidity(""); v != 1 {
		t.Errorf("UIDVALIDITY rose to %d without a conflict", v)
	}
}

// Moving takes the number out of the source folder and gives a fresh one in the
// target. The old number stays spent: IMAP never hands one out twice.
func TestMovingRetiresTheOldNumber(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	base := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	storeAt(f, "mail/a", base)
	storeAt(f, "mail/b", base.Add(time.Hour))

	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mb.EnsureAllUIDs(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := mb.Move(ctx, []string{"mail/a"}, "archiv"); err != nil {
		t.Fatal(err)
	}

	if _, ok := mb.UID("", "a"); ok {
		t.Error("the moved message still carries a number in the inbox")
	}
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mb.EnsureAllUIDs(ctx); err != nil {
		t.Fatal(err)
	}
	if n := numbered(t, mb, "archiv", "a")[0]; n != 1 {
		t.Errorf("in the target folder it got %d, expected 1", n)
	}
	// The inbox does not reuse number 1 for the next arrival.
	storeAt(f, "mail/c", base.Add(2*time.Hour))
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mb.EnsureAllUIDs(ctx); err != nil {
		t.Fatal(err)
	}
	if n := numbered(t, mb, "", "c")[0]; n == 1 {
		t.Error("the retired number was handed out again")
	}
}

// The property everything rests on: two machines that index the same bucket
// have to arrive at the same numbering, or a client that talks to both sees a
// message change its UID.
func TestTwoMachinesNumberAlike(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	base := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		storeAt(f, fmt.Sprintf("mail/m%d", i), base.Add(time.Duration(i)*time.Minute))
	}

	first := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := first.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.EnsureAllUIDs(ctx); err != nil {
		t.Fatal(err)
	}
	second := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := second.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := second.EnsureAllUIDs(ctx); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 6; i++ {
		mid := fmt.Sprintf("m%d", i)
		a, oka := first.UID("", mid)
		b, okb := second.UID("", mid)
		if !oka || !okb || a != b {
			t.Errorf("%s: machine one says %d/%v, machine two %d/%v", mid, a, oka, b, okb)
		}
	}
	if first.UIDValidity("") != second.UIDValidity("") {
		t.Error("the machines disagree about UIDVALIDITY")
	}
}

// Folders number independently - inbox 1 and archive 1 are different messages,
// and that is what IMAP expects.
func TestFoldersCountSeparately(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	base := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	storeAt(f, "mail/a", base)
	storeAt(f, "mail/archiv/b", base)

	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mb.EnsureAllUIDs(ctx); err != nil {
		t.Fatal(err)
	}
	if numbered(t, mb, "", "a")[0] != 1 || numbered(t, mb, "archiv", "b")[0] != 1 {
		t.Error("the folders do not number independently")
	}
}

// The property that makes pausing IMAP free: a refresh alone writes no numbers.
//
// Numbers cost about fifteen bytes per message in the state document that every
// machine downloads on every start - five thousand messages produce seventy-four
// kilobytes of pure numbering. A mailbox whose owner never touches IMAP must not
// pay that.
func TestARefreshAloneNumbersNothing(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	storeAt(f, "mail/a", time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC))

	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := mb.UID("", "a"); ok {
		t.Error("a plain refresh handed out a number")
	}
	if folders := mb.State.Data().UIDFolders(); len(folders) != 0 {
		t.Errorf("the state carries numbering for %v without anybody asking", folders)
	}

	// And on request it is there.
	if err := mb.EnsureUIDs(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := mb.UID("", "a"); !ok {
		t.Error("no number even after EnsureUIDs")
	}
}

// Asking for one folder must not number the others - that would give away the
// saving again.
func TestEnsureTouchesOnlyTheFolderAsked(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	base := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	storeAt(f, "mail/a", base)
	storeAt(f, "mail/archiv/b", base)

	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mb.EnsureUIDs(ctx, "archiv"); err != nil {
		t.Fatal(err)
	}
	if _, ok := mb.UID("archiv", "b"); !ok {
		t.Error("the folder that was asked for has no numbering")
	}
	if _, ok := mb.UID("", "a"); ok {
		t.Error("the inbox was numbered although only the archive was asked for")
	}
}
