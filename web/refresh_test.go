// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/ohartwig/s3mail/s3fake"
	"github.com/ohartwig/s3mail/store"
)

// refreshServer is a mailbox behind a server, already listed once, with the
// fake handed back so a test can count what a refresh costs.
func refreshServer(t *testing.T, push bool) (*httptest.Server, *s3fake.Fake) {
	t.Helper()
	ctx := t.Context()
	f := s3fake.New()
	f.Store("mail/m1", rawMail("a@b.de", "x", "y", "Mon, 03 Aug 2026 09:00:00 +0000"))
	f.Store("mail/archiv/a1", rawMail("a@b.de", "old", "y", "Mon, 03 Aug 2026 09:00:00 +0000"))
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	accounts := one(mb)
	accounts[0].Push = push
	srv := NewServer(accounts, testToken, "127.0.0.1", 0, map[string]any{"refresh_seconds": 60})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	srv.Port = portOf(ts.URL)
	f.ClearCalls()
	return ts, f
}

func listsRoot(f *s3fake.Fake) bool {
	for _, l := range f.Listings {
		if l.Prefix == "mail/" && !l.Dir {
			return true
		}
	}
	return false
}

// TestTheQuickRefreshDoesNotListTheMailbox - the page's timer sends quick, and
// quick must never become the full listing: that was the bill.
func TestTheQuickRefreshDoesNotListTheMailbox(t *testing.T) {
	ts, f := refreshServer(t, false)
	if r := callServer(t, ts, "POST", "/api/refresh", `{"quick":true}`, nil); r.Code != 200 {
		t.Fatalf("HTTP %d: %s", r.Code, r.Body)
	}
	if listsRoot(f) {
		t.Error("the quick refresh listed the whole mailbox")
	}
	dir := false
	for _, l := range f.Listings {
		dir = dir || l.Dir && l.Prefix == "mail/"
	}
	if !dir {
		t.Error("without a queue the quick refresh has to look at the inbox")
	}
}

// TestWithPushTheQuickRefreshListsNoMail - with a queue, new mail comes through
// the watcher; the page's quick refresh reads the journal and nothing else.
func TestWithPushTheQuickRefreshListsNoMail(t *testing.T) {
	ts, f := refreshServer(t, true)
	d := callServer(t, ts, "POST", "/api/refresh", `{"quick":true}`, nil).json(t)
	for _, l := range f.Listings {
		if l.Prefix != "mail/"+store.StateOps {
			t.Errorf("listed %q with a queue configured", l.Prefix)
		}
	}
	if d["push"] != true {
		t.Error("the overview does not tell the page that a queue looks after the mailbox")
	}
}

// TestTheButtonStillListsEverything - the reader's explicit refresh is the way
// to the full listing, and has to stay one.
func TestTheButtonStillListsEverything(t *testing.T) {
	ts, f := refreshServer(t, true)
	callServer(t, ts, "POST", "/api/refresh", "", nil)
	if !listsRoot(f) {
		t.Error("the explicit refresh did not list the mailbox")
	}
}

// TestThePageTimerAsksForTheQuickSync - the page is a string; what the timer
// sends is only visible in it. Every automatic call has to carry quick, and
// the timer has to stand down when a queue looks after the mailbox.
func TestThePageTimerAsksForTheQuickSync(t *testing.T) {
	start := strings.Index(PageMailbox, "function scheduleAutoRefresh")
	end := strings.Index(PageMailbox, `$("#quit").onclick`)
	if start < 0 || end < start {
		t.Fatal("the automatic refresh is no longer where this test looks")
	}
	auto := PageMailbox[start:end]
	calls := regexp.MustCompile(`api\("/api/refresh", \{([^}]*)\}\)`).FindAllStringSubmatch(auto, -1)
	if len(calls) < 2 {
		t.Fatalf("expected the timer and the visibility handler, found %d calls", len(calls))
	}
	for _, c := range calls {
		if !strings.Contains(c[1], "quick: true") {
			t.Errorf("an automatic refresh asks for the full listing: %s", c[0])
		}
	}
	if strings.Count(auto, "over.push") < 2 {
		t.Error("the timer or the visibility handler runs although a queue looks after the mailbox")
	}
}
