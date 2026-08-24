// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"s3mail/s3fake"
	"s3mail/store"
)

// --no-cache hands an empty directory down. Nothing may then reach the disk:
// not the index, not the message bodies, not the local copy of the state.
//
// The bodies are the reason this switch exists. They lie there in plain text -
// including the ones that arrived client-side encrypted, because s3mail has to
// decrypt them to show them. Whoever encrypts mail with KMS does not expect to
// find it readable in a cache directory afterwards.
func TestWithoutACacheNothingReachesTheDisk(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	f := s3fake.New()
	f.Store("mail/m1", []byte("From: a@b.de\r\nTo: post@firma.de\r\nSubject: Geheim\r\n"+
		"Date: Mon, 03 Aug 2026 09:00:00 +0000\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n\r\nVertraulich.\r\n"))

	// Empty cache directory: the mailbox is told to keep nothing.
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", "", true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := mb.Fetch(ctx, "mail/m1", 0); err != nil {
		t.Fatal(err)
	}
	if mb.CacheFile != "" {
		t.Errorf("an index file was set up anyway: %s", mb.CacheFile)
	}
	// The temp dir stands in for the home directory: nothing may appear in it.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%d files written although the cache is off: %v", len(entries), entries)
	}
}

// The other direction, so the test above cannot pass for the wrong reason: with
// a directory the files do appear.
func TestWithACacheTheFilesAppear(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	f := s3fake.New()
	f.Store("mail/m1", []byte("From: a@b.de\r\nSubject: x\r\n\r\ny\r\n"))

	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", dir, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if mb.CacheFile == "" {
		t.Fatal("no index file although a directory was given")
	}
	if _, err := os.Stat(mb.CacheFile); err != nil {
		t.Errorf("the index was not written: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(matches) == 0 {
		t.Error("nothing in the cache directory")
	}
}
