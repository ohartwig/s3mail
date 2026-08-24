// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"s3mail/s3fake"
	"s3mail/store"
)

// twoMailboxes builds what the feature is for: two mailboxes in one bucket,
// side by side, each with its own prefix and its own sender.
func twoMailboxes(t *testing.T) (*httptest.Server, []Account) {
	t.Helper()
	ctx := context.Background()
	f := s3fake.New()
	f.Store("mail/info/a", rawMail("Anna <anna@kunde.de>", "Anfrage", "Text.",
		"Mon, 03 Aug 2026 09:00:00 +0000"))
	f.Store("mail/support/b", rawMail("Bert <bert@kunde.de>", "Stoerung", "Text.",
		"Tue, 04 Aug 2026 09:00:00 +0000"))
	f.Store("mail/support/c", rawMail("Cem <cem@kunde.de>", "Nachfrage", "Text.",
		"Wed, 05 Aug 2026 09:00:00 +0000"))

	accounts := []Account{}
	for _, spec := range []struct{ id, prefix, from string }{
		{"info", "mail/info/", "info@firma.de"},
		{"support", "mail/support/", "support@firma.de"},
	} {
		mb := store.NewMailbox(ctx, f, nil, "test-bucket", spec.prefix, t.TempDir(), testCacheKey, true)
		if _, err := mb.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, Account{ID: spec.id, Name: spec.from, Mailbox: mb,
			From: spec.from, Sender: &fakeSender{}, Signature: spec.id + " signature"})
	}
	srv := NewServer(accounts, testToken, "127.0.0.1", 0, nil)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	srv.Port = portOf(ts.URL)
	return ts, accounts
}

func messagesOf(t *testing.T, ts *httptest.Server, query string) []map[string]any {
	t.Helper()
	r := callServer(t, ts, "GET", "/api/messages?folder="+query, "", nil)
	if r.Code != 200 {
		t.Fatalf("HTTP %d: %s", r.Code, r.Body)
	}
	var d struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(r.Body, &d); err != nil {
		t.Fatal(err)
	}
	return d.Messages
}

// TestEachMailboxSeesOnlyItsOwn - the whole point. Two prefixes in one bucket
// must not bleed into each other, or somebody answers from the wrong address.
func TestEachMailboxSeesOnlyItsOwn(t *testing.T) {
	ts, _ := twoMailboxes(t)
	if n := len(messagesOf(t, ts, "&account=info")); n != 1 {
		t.Errorf("info sees %d messages, expected 1", n)
	}
	if n := len(messagesOf(t, ts, "&account=support")); n != 2 {
		t.Errorf("support sees %d messages, expected 2", n)
	}
}

// TestWithoutAChoiceTheFirstAnswers - somebody who never switched must not face
// an error page.
func TestWithoutAChoiceTheFirstAnswers(t *testing.T) {
	ts, _ := twoMailboxes(t)
	if n := len(messagesOf(t, ts, "")); n != 1 {
		t.Errorf("without a choice %d messages arrive, expected those of the first", n)
	}
}

// TestAnUnknownMailboxIsAnError, not silently the first one: a stale bookmark
// would otherwise file mail into a stranger's folder without saying so.
func TestAnUnknownMailboxIsAnError(t *testing.T) {
	ts, _ := twoMailboxes(t)
	r := callServer(t, ts, "GET", "/api/messages?folder=&account=gibtsnicht", "", nil)
	if r.Code != 400 {
		t.Errorf("unknown mailbox -> HTTP %d, expected 400", r.Code)
	}
}

// TestTheChoiceRidesInACookie - so a download link stays a plain link, without
// the mailbox in every URL.
func TestTheChoiceRidesInACookie(t *testing.T) {
	ts, _ := twoMailboxes(t)
	r := callServer(t, ts, "GET", "/api/messages?folder=", "",
		map[string]string{"Cookie": "s3mail_account=support"})
	if r.Code != 200 {
		t.Fatalf("HTTP %d: %s", r.Code, r.Body)
	}
	var d struct {
		Messages []map[string]any `json:"messages"`
		Account  string           `json:"account"`
	}
	_ = json.Unmarshal(r.Body, &d)
	if d.Account != "support" || len(d.Messages) != 2 {
		t.Errorf("the cookie does not decide: %q, %d messages", d.Account, len(d.Messages))
	}
}

// TestTheOverviewCarriesWhatTheMailboxAllows - can_send, the sender address and
// the signature belong to the mailbox, not to the program. They used to sit in
// the page's configuration, where a switch could not reach them.
func TestTheOverviewCarriesWhatTheMailboxAllows(t *testing.T) {
	ts, _ := twoMailboxes(t)
	for _, f := range []struct{ account, from string }{
		{"info", "info@firma.de"}, {"support", "support@firma.de"},
	} {
		r := callServer(t, ts, "GET", "/api/overview?account="+f.account, "", nil)
		var d struct {
			DefaultFrom string `json:"default_from"`
			Signature   string `json:"signature"`
			CanSend     bool   `json:"can_send"`
			Accounts    []struct {
				ID, Name string
			} `json:"accounts"`
		}
		if err := json.Unmarshal(r.Body, &d); err != nil {
			t.Fatal(err)
		}
		if d.DefaultFrom != f.from {
			t.Errorf("%s: sender %q", f.account, d.DefaultFrom)
		}
		if !strings.HasPrefix(d.Signature, f.account) {
			t.Errorf("%s: signature %q", f.account, d.Signature)
		}
		if !d.CanSend {
			t.Errorf("%s: sending is off although a sender is attached", f.account)
		}
		if len(d.Accounts) != 2 {
			t.Errorf("%s: the switcher gets %d mailboxes", f.account, len(d.Accounts))
		}
	}
}

// TestSendingUsesTheMailboxItWasWrittenIn - the sent copy has to land in the
// same mailbox, and the address has to be that mailbox's own.
func TestSendingUsesTheMailboxItWasWrittenIn(t *testing.T) {
	ts, accounts := twoMailboxes(t)
	r := callServer(t, ts, "POST", "/api/send?account=support",
		`{"mode":"new","to":"kunde@x.de","subject":"Antwort","body":"Text"}`, nil)
	if r.Code != 200 {
		t.Fatalf("HTTP %d: %s", r.Code, r.Body)
	}
	sent := accounts[1].Sender.(*fakeSender)
	if len(sent.sent) != 1 {
		t.Fatalf("support handed %d messages to SES", len(sent.sent))
	}
	if sent.sent[0].From != "support@firma.de" {
		t.Errorf("sent as %q", sent.sent[0].From)
	}
	if n := len(accounts[0].Sender.(*fakeSender).sent); n != 0 {
		t.Errorf("info sent %d messages although support was written in", n)
	}
	// And the copy lies in the support mailbox, not in the other one.
	for _, m := range accounts[0].Mailbox.Index() {
		if m.Folder == "sent" {
			t.Errorf("the copy landed in the wrong mailbox: %s", m.Key)
		}
	}
}

// TestQuittingWorksWithoutAMailbox - before the setup there is none, and
// without a console the button is the only way out. Resolving a mailbox for it
// would lock the reader in.
func TestQuittingWorksWithoutAMailbox(t *testing.T) {
	srv := NewServer(nil, testToken, "127.0.0.1", 0, nil)
	called := make(chan bool, 1)
	srv.OnShutdown = func() { called <- true }
	ts := httptest.NewServer(srv)
	defer ts.Close()
	srv.Port = portOf(ts.URL)

	if r := callServer(t, ts, "POST", "/api/quit", "{}", nil); r.Code != 200 {
		t.Fatalf("quit without a mailbox: HTTP %d", r.Code)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Error("OnShutdown was not called")
	}
}

// TestSuggestionsComeOutOfTheIndex - the route reads what somebody already did
// by hand. It changes nothing; the reader decides.
func TestSuggestionsComeOutOfTheIndex(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	// Eleven from one sender, ten of them already filed into "werbung".
	for i := 0; i < 11; i++ {
		key := "mail/n" + string(rune('a'+i))
		if i < 10 {
			key = "mail/werbung/n" + string(rune('a'+i))
		}
		f.Store(key, rawMail("Shop <news@shop.io>", "Angebot", "Text.",
			"Mon, 03 Aug 2026 09:00:00 +0000"))
	}
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(one(mb), testToken, "127.0.0.1", 0, nil)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	srv.Port = portOf(ts.URL)

	r := callServer(t, ts, "GET", "/api/rules/suggest", "", nil)
	if r.Code != 200 {
		t.Fatalf("HTTP %d: %s", r.Code, r.Body)
	}
	var d struct {
		Suggestions []struct {
			Contains, Folder string
			Count, Agree     int
		} `json:"suggestions"`
	}
	if err := json.Unmarshal(r.Body, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Suggestions) != 1 {
		t.Fatalf("%d suggestions: %+v", len(d.Suggestions), d.Suggestions)
	}
	s := d.Suggestions[0]
	if s.Contains != "news@shop.io" || s.Folder != "werbung" || s.Agree != 10 || s.Count != 11 {
		t.Errorf("%+v", s)
	}
}

// TestNoSuggestionsIsAnEmptyListAndNotNull - a nil slice becomes JSON `null`,
// and `null.map(...)` ends the page's script. The same trap as with the buckets
// in the wizard.
func TestNoSuggestionsIsAnEmptyListAndNotNull(t *testing.T) {
	ts, _ := twoMailboxes(t)
	r := callServer(t, ts, "GET", "/api/rules/suggest", "", nil)
	if !strings.Contains(string(r.Body), `"suggestions":[]`) {
		t.Errorf("empty suggestions are not an empty list: %s", r.Body)
	}
}

// TestWaitingAndConversationHaveTheirRoutes - both read the index and decide
// nothing; both have to answer an empty list rather than null, or the page's
// script ends on `null.map(...)`.
func TestWaitingAndConversationHaveTheirRoutes(t *testing.T) {
	ts, _ := twoMailboxes(t)
	for _, path := range []string{"/api/waiting", "/api/conversation?address=niemand@x.de"} {
		r := callServer(t, ts, "GET", path, "", nil)
		if r.Code != 200 {
			t.Errorf("%s: HTTP %d: %s", path, r.Code, r.Body)
		}
		// `:null` and not bare "null": a message may contain the word, a field
		// that is null is what ends the page's script.
		if strings.Contains(string(r.Body), ":null") {
			t.Errorf("%s has a field that is null: %s", path, r.Body)
		}
	}
}

// TestTheWaitingWindowIsBounded - the days come out of a URL somebody can type.
func TestTheWaitingWindowIsBounded(t *testing.T) {
	ts, _ := twoMailboxes(t)
	for _, f := range []struct {
		query string
		want  int
	}{
		{"", 5}, {"?days=30", 30}, {"?days=0", 5}, {"?days=-3", 5},
		{"?days=99999", 5}, {"?days=nonsense", 5},
	} {
		r := callServer(t, ts, "GET", "/api/waiting"+f.query, "", nil)
		var d struct {
			Days int `json:"days"`
		}
		_ = json.Unmarshal(r.Body, &d)
		if d.Days != f.want {
			t.Errorf("%q -> %d days, expected %d", f.query, d.Days, f.want)
		}
	}
}

// TestTheConversationFindsBothDirections through the route, not only in core.
func TestTheConversationFindsBothDirections(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	f.Store("mail/in1", rawMail("Anna <anna@kunde.de>", "Anfrage", "Text.",
		"Mon, 03 Aug 2026 09:00:00 +0000"))
	f.Store("mail/sent/out1", []byte("From: post@firma.de\r\nTo: Anna <anna@kunde.de>\r\n"+
		"Subject: Antwort\r\nDate: Tue, 04 Aug 2026 09:00:00 +0000\r\n\r\nText\r\n"))
	f.Store("mail/in2", rawMail("Bert <bert@anders.de>", "Anderes", "Text.",
		"Wed, 05 Aug 2026 09:00:00 +0000"))
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(one(mb), testToken, "127.0.0.1", 0, nil)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	srv.Port = portOf(ts.URL)

	r := callServer(t, ts, "GET", "/api/conversation?address=anna@kunde.de", "", nil)
	var d struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(r.Body, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Messages) != 2 {
		t.Fatalf("%d messages in the conversation, expected the received and the sent one: %+v",
			len(d.Messages), d.Messages)
	}
}

// testCacheKey encrypts what the tests write to disk, like the real thing.
// Fixed rather than random: a test that wants to look at a cache file has to be
// able to open it.
var testCacheKey = []byte("0123456789abcdef0123456789abcdef")
