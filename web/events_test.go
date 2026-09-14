// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/s3mail/s3fake"
	"github.com/ohartwig/s3mail/store"
)

func serverForEvents(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	f := s3fake.New()
	mb := store.NewMailbox(t.Context(), f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	srv := NewServer(one(mb), testToken, "127.0.0.1", 0, nil)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	srv.Port = portOf(ts.URL)
	return ts, srv
}

// openStream hangs on /api/events and returns the lines as they arrive.
func openStream(t *testing.T, ts *httptest.Server, srv *Server) (<-chan string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	req.Header.Set("X-S3mail-Token", testToken)
	resp, err := ts.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		cancel()
		t.Fatalf("content type %q - no browser treats that as a stream", ct)
	}
	lines := make(chan string, 32)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	// The subscription is registered inside the handler; without waiting for it
	// the first event would be sent into the void and the test would flake.
	waitFor(t, func() bool { return subscribers(srv) == 1 })
	return lines, func() { cancel(); resp.Body.Close() }
}

func subscribers(srv *Server) int {
	srv.events.mu.Lock()
	defer srv.events.mu.Unlock()
	return len(srv.events.subs)
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the condition did not come true within two seconds")
}

// TestAnEventReachesThePage is the whole point: the program says something and
// the page hears it, instead of asking every sixty seconds.
func TestAnEventReachesThePage(t *testing.T) {
	ts, srv := serverForEvents(t)
	lines, closeStream := openStream(t, ts, srv)
	defer closeStream()

	srv.Notify("post")

	var got []string
	deadline := time.After(2 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended, seen: %v", got)
			}
			got = append(got, line)
			if strings.HasPrefix(line, "data:") {
				if !strings.Contains(line, `"account":"post"`) {
					t.Errorf("event without the mailbox: %q", line)
				}
				if !slices.Contains(got, "event: mail") {
					t.Errorf("no event name in front of the data: %v", got)
				}
				return
			}
		case <-deadline:
			t.Fatalf("no event within two seconds, seen: %v", got)
		}
	}
}

// TestAStreamWithoutATokenIsRefused - it is a route like any other, and the
// three checks have to hold here too.
func TestAStreamWithoutATokenIsRefused(t *testing.T) {
	ts, _ := serverForEvents(t)
	req, _ := http.NewRequest("GET", ts.URL+"/api/events", nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("stream without a token: HTTP %d, expected 403", resp.StatusCode)
	}
}

// TestAClosedPageIsForgotten - otherwise every reload leaves a channel behind
// and the program leaks one per visit.
func TestAClosedPageIsForgotten(t *testing.T) {
	ts, srv := serverForEvents(t)
	_, closeStream := openStream(t, ts, srv)
	closeStream()
	waitFor(t, func() bool { return subscribers(srv) == 0 })
}

// TestAPageThatDoesNotReadDoesNotBlockTheOthers. The channels are small on
// purpose; a page that stopped reading must not hold up the mailbox trying to
// tell it something.
func TestAPageThatDoesNotReadDoesNotBlockTheOthers(t *testing.T) {
	srv := NewServer(nil, testToken, "127.0.0.1", 0, nil)
	deaf := srv.events.subscribe()
	defer srv.events.unsubscribe(deaf)

	done := make(chan bool, 1)
	go func() {
		for range 100 {
			srv.Notify("post")
		}
		done <- true
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a page that does not read blocks the sender")
	}
}

// TestShutdownReleasesTheStreams - http.Server.Shutdown waits for handlers to
// return and does not cancel their requests. A stream that is meant to stay
// open would hold up quitting for as long as the deadline allows.
func TestShutdownReleasesTheStreams(t *testing.T) {
	ts, srv := serverForEvents(t)
	lines, closeStream := openStream(t, ts, srv)
	defer closeStream()

	srv.StopEvents()
	select {
	case _, ok := <-lines:
		if ok {
			// A line may still be in flight; what matters is that it ends.
			for range lines {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stream stayed open after StopEvents")
	}
}
