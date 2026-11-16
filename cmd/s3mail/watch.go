// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"time"

	"github.com/ohartwig/s3mail/web"
)

// queue is the ear the watcher hangs on. An interface, so the loop below can be
// checked without an AWS account - and the properties worth checking are all in
// the loop, not in the SDK call.
type queue interface {
	// Wait returns how many notifications came and the object keys they name.
	Wait(ctx context.Context) (int, []string, error)
}

// syncFunc looks at the mailbox: at keys when the queue named some, at the
// inbox listing when inbox is set, and at the journal always. changed says
// whether the page has anything new to show.
type syncFunc func(ctx context.Context, keys []string, inbox bool) (changed bool, err error)

// watchQueue hangs on the mailbox's queue and is, for a mailbox that has one,
// the only thing that looks at the bucket: the page runs no timer then.
//
// What it does, and what each costs:
//
//   - a notification names the key SES wrote; that key is fetched with a HEAD
//     and a range GET. No listing.
//   - every `every` (the -refresh interval), the journal: one listing of the
//     op prefix, which holds a few dozen objects, so other machines' moves,
//     flags and announced arrivals show up. No listing of the mail.
//   - the full listing only when the mailbox says it is due (start, or
//     ReconcileEvery since the last one) - Sync decides that, not this loop.
//
// Until 1.6.33 every notification triggered a full listing, and the page timer
// ran beside it every minute regardless of the queue. With 227,000 objects that
// was 227 requests and 90 MB per look, 40 to 60 GB a day.
//
// It runs per mailbox, because the queue is a property of the mailbox: one
// topic each, so an account that may read one mailbox does not learn who wrote
// to another. The SES notification carries sender, recipient and subject.
func watchQueue(ctx context.Context, srv *web.Server, id string, q queue,
	every time.Duration, sync syncFunc) {
	look := func(keys []string, inbox bool) {
		changed, err := sync(ctx, keys, inbox)
		if err == nil && changed {
			srv.Notify(id)
		}
	}
	// A failure must not turn into a hot loop. Twice as long each time up to a
	// minute: a missing permission would otherwise produce a request per
	// millisecond, and the first anybody would hear of it is the AWS bill.
	wait := time.Second
	var last time.Time // zero: look once at the start
	queueOK := true
	for {
		if ctx.Err() != nil {
			return
		}
		if every > 0 && time.Since(last) >= every || last.IsZero() {
			// While the queue fails, nobody hears of new mail through it - the
			// inbox listing stands in, at the same pace and no faster.
			look(nil, !queueOK)
			last = time.Now()
		}
		n, keys, err := q.Wait(ctx)
		if err != nil && n == 0 {
			if ctx.Err() != nil {
				return
			}
			queueOK = false
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
			if wait < time.Minute {
				wait *= 2
			}
			continue
		}
		// Messages taken but not deleted still name their keys: look at them,
		// they come back once more and cost a HEAD each.
		queueOK, wait = true, time.Second
		if n == 0 {
			continue // the long poll ran out, nothing came - ask again
		}
		// A notification whose key could not be read still means "something
		// arrived" - look at the inbox for it.
		look(keys, len(keys) < n)
		last = time.Now()
	}
}
