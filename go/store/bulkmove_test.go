// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/s3fake"
	"git.ole-hartwig.eu/development/s3mail/s3mail/store"
)

// Moving a whole search result is the normal case, not the exception. What
// happens to the rest when one message will not move decides whether somebody
// can trust the answer.

func filledMailbox(t *testing.T, n int) (*s3fake.Fake, *store.Mailbox, []string) {
	t.Helper()
	ctx := context.Background()
	f := s3fake.New()
	var keys []string
	for i := range n {
		key := fmt.Sprintf("mail/m%02d", i)
		f.Store(key, []byte(fmt.Sprintf(
			"From: a@b.de\r\nTo: post@firma.de\r\nSubject: Nr %d\r\n"+
				"Date: Mon, 03 Aug 2026 09:00:00 +0000\r\n\r\nText.\r\n", i)))
		keys = append(keys, key)
	}
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	return f, mb, keys
}

// One message that will not move must not take the other nineteen with it.
func TestOneStuckMessageDoesNotStopTheRest(t *testing.T) {
	ctx := context.Background()
	f, mb, keys := filledMailbox(t, 20)
	f.CopyErrFor = map[string]error{keys[7]: errors.New("object locked")}

	res, err := mb.Move(ctx, keys, "archiv")
	if err != nil {
		t.Fatalf("the whole move failed over one message: %v", err)
	}
	moved, failed := 0, 0
	for _, r := range res {
		if r.Err != "" {
			failed++
			continue
		}
		moved++
	}
	if moved != 19 || failed != 1 {
		t.Errorf("%d moved, %d failed - expected 19 and 1", moved, failed)
	}
	// And the failure has to name the message, or nobody can go and look.
	for _, r := range res {
		if r.Err != "" && r.Key != keys[7] {
			t.Errorf("the wrong message is reported as failed: %s", r.Key)
		}
	}
}

// The other extreme: if everything fails, the answer must not be five thousand
// identical sentences. Ten in a row is enough to know something systemic is
// wrong.
func TestManyFailuresInARowStopTheMove(t *testing.T) {
	ctx := context.Background()
	f, mb, keys := filledMailbox(t, 40)
	stuck := map[string]error{}
	for _, k := range keys {
		stuck[k] = errors.New("access denied")
	}
	f.CopyErrFor = stuck

	res, err := mb.Move(ctx, keys, "archiv")
	if err == nil {
		t.Error("a move in which nothing worked came back without an error")
	}
	if len(res) > 12 {
		t.Errorf("%d results - it kept going long after it was hopeless", len(res))
	}
}

// A failure in the middle must not lose what came before it.
func TestWhatMovedBeforeTheFailureStaysMoved(t *testing.T) {
	ctx := context.Background()
	f, mb, keys := filledMailbox(t, 5)
	f.CopyErrFor = map[string]error{keys[0]: errors.New("object locked")}

	res, _ := mb.Move(ctx, keys, "archiv")
	if len(res) != 5 {
		t.Fatalf("%d results for five messages", len(res))
	}
	for i := 1; i < 5; i++ {
		if _, ok := f.Objs["mail/archiv/"+keys[i][len("mail/"):]]; !ok {
			t.Errorf("%s did not arrive in the target folder", keys[i])
		}
	}
}

// testCacheKey encrypts what the tests write to disk, like the real thing.
// Fixed rather than random: a test that wants to look at a cache file has to be
// able to open it.
var testCacheKey = []byte("0123456789abcdef0123456789abcdef")
