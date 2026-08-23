package main

import (
	"context"
	"time"

	"s3mail/web"
)

// queue is the ear the watcher hangs on. An interface, so the loop below can be
// checked without an AWS account - and the properties worth checking are all in
// the loop, not in the SDK call.
type queue interface {
	Wait(ctx context.Context) (int, error)
}

// watchQueue hangs on the mailbox's queue and tells the pages when something
// arrived. Without it the interface asks every sixty seconds, and a message can
// sit that long before anybody sees it.
//
// It runs per mailbox, because the queue is a property of the mailbox: one
// topic each, so an account that may read one mailbox does not learn who wrote
// to another. The SES notification carries sender, recipient and subject.
func watchQueue(ctx context.Context, srv *web.Server, id string, q queue,
	refresh func(context.Context) error) {
	// A failure must not turn into a hot loop. Twice as long each time up to a
	// minute: a missing permission would otherwise produce a request per
	// millisecond, and the first anybody would hear of it is the AWS bill.
	wait := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := q.Wait(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
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
		wait = time.Second
		if n == 0 {
			continue // the long poll ran out, nothing came - ask again
		}
		// What the message says does not matter; it means "go and look". The
		// refresh compares ETags and fetches only what really changed.
		if err := refresh(ctx); err != nil {
			continue
		}
		srv.Notify(id)
	}
}
