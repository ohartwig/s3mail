// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/s3fake"
	"git.ole-hartwig.eu/development/s3mail/s3mail/store"
)

const vertraulich = "Die Bankverbindung lautet DE12 3456 7890"

func mailboxWithKey(t *testing.T, dir string, key []byte) (*s3fake.Fake, *store.Mailbox) {
	t.Helper()
	ctx := t.Context()
	f := s3fake.New()
	f.Store("mail/m1", []byte("From: kunde@x.de\r\nTo: post@firma.de\r\n"+
		"Subject: Kontodaten\r\nDate: Mon, 03 Aug 2026 09:00:00 +0000\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n\r\n"+vertraulich+"\r\n"))
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", dir, key, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := mb.Fetch(ctx, "mail/m1", 0); err != nil {
		t.Fatal(err)
	}
	return f, mb
}

// scanDisk reads every file below dir and returns them joined - what somebody
// with a backup of the home directory would have in front of them.
func scanDisk(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		blob, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sb.Write(blob)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

// The point of the whole exercise: a copied cache directory gives up nothing.
func TestNothingReadableLandsOnDisk(t *testing.T) {
	dir := t.TempDir()
	mailboxWithKey(t, dir, testCacheKey)

	onDisk := scanDisk(t, dir)
	if onDisk == "" {
		t.Fatal("nothing was written at all - then this test proves nothing")
	}
	for _, secret := range []string{vertraulich, "Kontodaten", "kunde@x.de"} {
		if strings.Contains(onDisk, secret) {
			t.Errorf("%q is readable in the cache", secret)
		}
	}
}

// And the other direction, so the test above cannot pass for the wrong reason.
func TestWithoutAKeyItIsReadable(t *testing.T) {
	dir := t.TempDir()
	mailboxWithKey(t, dir, nil)

	if !strings.Contains(scanDisk(t, dir), vertraulich) {
		t.Error("even without a key nothing was readable - the test scans the wrong place")
	}
}

// A cache from another machine, or from before the key changed: it is a cache,
// it gets rebuilt. Nothing may fall over, and nothing may come back wrong.
func TestAForeignCacheIsIgnored(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	f, _ := mailboxWithKey(t, dir, testCacheKey)

	other := []byte("ffffffffffffffffffffffffffffffff")
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", dir, other, true)
	if n := len(mb.Index()); n != 0 {
		t.Errorf("%d entries were taken from a cache written with another key", n)
	}
	// It has to work afterwards - a wrong key is not a broken mailbox.
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mb.Index()) != 1 {
		t.Error("the mailbox did not rebuild its index")
	}
	body, err := mb.Fetch(ctx, "mail/m1", 0)
	if err != nil || !strings.Contains(string(body), vertraulich) {
		t.Errorf("the message could not be read after the key changed: %v", err)
	}
}

// The index survives a restart with the same key - otherwise encryption would
// cost a full pass over the headers at every start.
func TestTheCacheStillWorks(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	f, _ := mailboxWithKey(t, dir, testCacheKey)

	again := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", dir, testCacheKey, true)
	if n := len(again.Index()); n != 1 {
		t.Errorf("%d entries came back from the cache, expected 1", n)
	}
	// And the body cache too: no Refresh, so this can only come from disk.
	body, err := again.Fetch(ctx, "mail/m1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), vertraulich) {
		t.Error("the body did not come back out of the cache")
	}
}

// A file somebody edited must not be handed to the parser as if it were ours.
// GCM and not a stream cipher is the reason this test can exist.
func TestATamperedCacheIsRefused(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	f, mb := mailboxWithKey(t, dir, testCacheKey)
	_ = f

	blob, err := os.ReadFile(mb.CacheFile)
	if err != nil {
		t.Fatal(err)
	}
	blob[len(blob)-1] ^= 0xff // one flipped bit at the end
	if err := os.WriteFile(mb.CacheFile, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	again := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", dir, testCacheKey, true)
	if n := len(again.Index()); n != 0 {
		t.Errorf("%d entries were taken from a tampered cache", n)
	}
}
