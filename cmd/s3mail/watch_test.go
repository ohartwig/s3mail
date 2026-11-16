// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ohartwig/s3mail/web"
)

// fakeQueue stands in for SQS: it hands out what the test wants and counts how
// often it was asked.
type fakeQueue struct {
	mu      sync.Mutex
	answers []answer
	calls   int
}

type answer struct {
	n    int
	keys []string
	err  error
}

func (f *fakeQueue) Wait(ctx context.Context) (int, []string, error) {
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
		return 0, nil, nil
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return a.n, a.keys, a.err
}

func (f *fakeQueue) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// look is one call of the sync function, as the watcher made it.
type look struct {
	keys  []string
	inbox bool
}

// recorder is a sync function that writes down what it was asked.
type recorder struct {
	mu    sync.Mutex
	looks []look
	err   error
	seen  chan look
}

func newRecorder() *recorder { return &recorder{seen: make(chan look, 64)} }

func (r *recorder) sync(_ context.Context, keys []string, inbox bool) (bool, error) {
	r.mu.Lock()
	r.looks = append(r.looks, look{keys, inbox})
	err := r.err
	r.mu.Unlock()
	select {
	case r.seen <- look{keys, inbox}:
	default: // nobody is reading any more; do not block the watcher
	}
	return true, err
}

func (r *recorder) all() []look {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.looks)
}

// next waits for the next look that matches.
func (r *recorder) next(t *testing.T, match func(look) bool) look {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case l := <-r.seen:
			if match(l) {
				return l
			}
		case <-deadline:
			t.Fatal("the expected look did not come")
			return look{}
		}
	}
}

// TestANotificationFetchesTheKeyItNames - the whole point of the change: the
// key from the notification is what gets looked at, not the mailbox.
func TestANotificationFetchesTheKeyItNames(t *testing.T) {
	srv := web.NewServer(nil, "t", "127.0.0.1", 0, nil)
	q := &fakeQueue{answers: []answer{{n: 1, keys: []string{"mail/abc"}}}}
	r := newRecorder()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go watchQueue(ctx, srv, "post", q, time.Hour, r.sync)

	l := r.next(t, func(l look) bool { return len(l.keys) > 0 })
	if !slices.Equal(l.keys, []string{"mail/abc"}) || l.inbox {
		t.Errorf("looked at %+v - expected the named key and no inbox listing", l)
	}
}

// TestANotificationWithoutAKeyLooksAtTheInbox - a notification nobody could
// read still means mail came; dropping it would lose the mail until the
// reconcile.
func TestANotificationWithoutAKeyLooksAtTheInbox(t *testing.T) {
	srv := web.NewServer(nil, "t", "127.0.0.1", 0, nil)
	q := &fakeQueue{answers: []answer{{n: 0}, {n: 1}}}
	r := newRecorder()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go watchQueue(ctx, srv, "post", q, time.Hour, r.sync)

	r.next(t, func(l look) bool { return l.inbox })
}

// TestWithAQueueThereIsNoPolling is the one that matters for the bill: between
// notifications the watcher looks once at the start and then only every
// `every` - and never at the inbox while the queue works.
func TestWithAQueueThereIsNoPolling(t *testing.T) {
	srv := web.NewServer(nil, "t", "127.0.0.1", 0, nil)
	q := &fakeQueue{}
	r := newRecorder()
	ctx, cancel := context.WithCancel(t.Context())
	go watchQueue(ctx, srv, "post", q, time.Hour, r.sync)
	time.Sleep(400 * time.Millisecond)
	cancel()

	looks := r.all()
	if len(looks) != 1 {
		t.Fatalf("%d looks in 400 ms of silence - expected only the one at the start", len(looks))
	}
	if looks[0].inbox || len(looks[0].keys) > 0 {
		t.Errorf("the look at the start was %+v - expected the journal only", looks[0])
	}
}

// TestAFailingQueueDoesNotSpin - a missing permission would otherwise produce a
// request per millisecond, and the first anybody would hear of it is the
// invoice.
func TestAFailingQueueDoesNotSpin(t *testing.T) {
	srv := web.NewServer(nil, "t", "127.0.0.1", 0, nil)
	q := &fakeQueue{}
	q.mu.Lock()
	for range 50 {
		q.answers = append(q.answers, answer{err: errors.New("AccessDenied")})
	}
	q.mu.Unlock()

	ctx, cancel := context.WithCancel(t.Context())
	r := newRecorder()
	go watchQueue(ctx, srv, "post", q, time.Hour, r.sync)
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

// TestABrokenQueueFallsBackToTheInbox - without the queue nobody hears of new
// mail, so the periodic look has to list the inbox while it is broken.
func TestABrokenQueueFallsBackToTheInbox(t *testing.T) {
	srv := web.NewServer(nil, "t", "127.0.0.1", 0, nil)
	q := &fakeQueue{answers: []answer{{err: errors.New("AccessDenied")}}}
	r := newRecorder()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go watchQueue(ctx, srv, "post", q, 10*time.Millisecond, r.sync)

	r.next(t, func(l look) bool { return l.inbox })
}

// TestAFailedRefreshIsNotAnnounced - telling the page to reload when the reload
// itself failed only produces a second error in front of the reader.
func TestAFailedRefreshIsNotAnnounced(t *testing.T) {
	srv := web.NewServer(nil, "t", "127.0.0.1", 0, nil)
	heard := srv.Subscribe()
	defer srv.Unsubscribe(heard)

	q := &fakeQueue{answers: []answer{{n: 1, keys: []string{"mail/x"}}}}
	r := newRecorder()
	r.err = errors.New("no permission")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go watchQueue(ctx, srv, "post", q, time.Hour, r.sync)

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
	r := newRecorder()
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan bool)
	go func() {
		watchQueue(ctx, srv, "post", q, time.Hour, r.sync)
		stopped <- true
	}()
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the watcher kept running after the context ended")
	}
}
