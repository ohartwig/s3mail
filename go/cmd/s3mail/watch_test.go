// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"git.ole-hartwig.eu/development/s3mail/s3mail/web"
)

// fakeQueue stands in for SQS: it hands out what the test wants and counts how
// often it was asked.
type fakeQueue struct {
	mu      sync.Mutex
	answers []answer
	calls   int
}

type answer struct {
	n   int
	err error
}

func (f *fakeQueue) Wait(ctx context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.answers) == 0 {
		// Nothing more planned: behave like a long poll that runs out, so the
		// loop keeps turning without spinning.
		f.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
		f.mu.Lock()
		return 0, nil
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return a.n, a.err
}

func (f *fakeQueue) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestAMessageTriggersARefreshAndTellsThePage - the whole chain in one: SQS
// says something came, s3mail looks, and the page learns of it.
func TestAMessageTriggersARefreshAndTellsThePage(t *testing.T) {
	srv := web.NewServer(nil, "t", "127.0.0.1", 0, nil)
	q := &fakeQueue{answers: []answer{{n: 1}}}
	refreshed := make(chan bool, 4)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchQueue(ctx, srv, "post", q, func(context.Context) error {
		refreshed <- true
		return nil
	})

	select {
	case <-refreshed:
	case <-time.After(2 * time.Second):
		t.Fatal("a message did not trigger a refresh")
	}
}

// TestAFailingQueueDoesNotSpin is the one that matters for the bill: a missing
// permission would otherwise produce a request per millisecond, and the first
// anybody would hear of it is the invoice.
func TestAFailingQueueDoesNotSpin(t *testing.T) {
	srv := web.NewServer(nil, "t", "127.0.0.1", 0, nil)
	q := &fakeQueue{}
	q.mu.Lock()
	for i := 0; i < 50; i++ {
		q.answers = append(q.answers, answer{err: errors.New("AccessDenied")})
	}
	q.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	go watchQueue(ctx, srv, "post", q, func(context.Context) error { return nil })
	time.Sleep(300 * time.Millisecond)
	cancel()

	// With a backoff starting at a second, at most a handful of attempts fit
	// into 300 ms. Without one there would be thousands.
	if n := q.count(); n > 5 {
		t.Errorf("%d attempts in 300 ms - the backoff is not holding", n)
	}
	if q.count() == 0 {
		t.Error("not tried at all")
	}
}

// TestAFailedRefreshIsNotAnnounced - telling the page to reload when the reload
// itself failed only produces a second error in front of the reader.
func TestAFailedRefreshIsNotAnnounced(t *testing.T) {
	srv := web.NewServer(nil, "t", "127.0.0.1", 0, nil)
	heard := srv.Subscribe()
	defer srv.Unsubscribe(heard)

	q := &fakeQueue{answers: []answer{{n: 1}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchQueue(ctx, srv, "post", q, func(context.Context) error {
		return errors.New("no permission")
	})

	select {
	case msg := <-heard:
		t.Errorf("announced although the refresh failed: %q", msg)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestTheWatcherStopsWithTheProgram - a goroutine that outlives the context
// keeps polling an AWS queue after the window is closed.
func TestTheWatcherStopsWithTheProgram(t *testing.T) {
	srv := web.NewServer(nil, "t", "127.0.0.1", 0, nil)
	q := &fakeQueue{}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan bool)
	go func() {
		watchQueue(ctx, srv, "post", q, func(context.Context) error { return nil })
		stopped <- true
	}()
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the watcher kept running after the context ended")
	}
}
