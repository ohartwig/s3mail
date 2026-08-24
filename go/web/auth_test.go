// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"s3mail/s3fake"
	"s3mail/store"
)

// An expired access used to arrive in the browser as the raw SDK sentence, with
// HTTP 502 and nothing to tell it apart from a broken object. The wizard has
// translated these errors into plain language from the start; the running
// mailbox did not use that translation.

func authServer(t *testing.T, f *s3fake.Fake) *httptest.Server {
	t.Helper()
	mb := store.NewMailbox(context.Background(), f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	accounts := one(mb)
	accounts[0].Profile = "arbeit"
	srv := NewServer(accounts, testToken, "127.0.0.1", 0, nil)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	srv.Port = portOf(ts.URL)
	return ts
}

func TestAnExpiredSessionSaysWhatToDo(t *testing.T) {
	f := s3fake.New()
	ts := authServer(t, f)
	f.ListErr = errors.New("sso session token expired")

	r := callServer(t, ts, "POST", "/api/refresh", "", nil)
	var body map[string]any
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["reauth"] != true {
		t.Errorf("the answer is not marked as an access problem: %s", r.Body)
	}
	text, _ := body["error"].(string)
	if !strings.Contains(text, "aws sso login") {
		t.Errorf("the answer does not name the command: %q", text)
	}
	// Without the profile the command is the wrong one on a machine with more
	// than one mailbox.
	if !strings.Contains(text, "arbeit") {
		t.Errorf("the answer does not name the profile: %q", text)
	}
}

// The other direction: an ordinary failure must not raise the banner, or every
// missing object would declare the mailbox dead.
func TestAnOrdinaryFailureIsNotAnAccessProblem(t *testing.T) {
	f := s3fake.New()
	ts := authServer(t, f)
	f.ListErr = errors.New("connection reset by peer")

	r := callServer(t, ts, "POST", "/api/refresh", "", nil)
	var body map[string]any
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["reauth"] == true {
		t.Errorf("an ordinary failure was marked as an access problem: %s", r.Body)
	}
}

// The page has to carry the way to the banner - it is a string, and a lost
// element falls to nobody's notice.
func TestThePageCarriesTheAccessBanner(t *testing.T) {
	for _, anchor := range []string{
		`id="authbar"`, "renderAuth", "d.reauth",
		`T["auth.gone"]`, `T["auth.retry"]`,
	} {
		if !strings.Contains(PageMailbox, anchor) {
			t.Errorf("the mailbox page has lost %q", anchor)
		}
	}
}

// SPF, DKIM and DMARC have to be reachable from the page - the list carries the
// failure, the open message carries the detail.
func TestThePageCarriesTheSenderChecks(t *testing.T) {
	for _, anchor := range []string{
		"m.auth_failed",           // the mark in the list
		"authFlags",               // the renderer for the open message
		`T["view.authFailShort"]`, // its text
	} {
		if !strings.Contains(PageMailbox, anchor) {
			t.Errorf("the mailbox page has lost %q", anchor)
		}
	}
}
