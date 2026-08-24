// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"s3mail/s3fake"
	"s3mail/store"
)

// serverWithSuppressions builds a server with a mailbox - without one every
// /api/ route answers 503 ("not set up yet"), these here included.
func serverWithSuppressions(t *testing.T, l SuppressionList) (*httptest.Server, *Server) {
	t.Helper()
	ctx := context.Background()
	f := s3fake.New()
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	accounts := one(mb)
	accounts[0].Blocked = l
	srv := NewServer(accounts, testToken, "127.0.0.1", 0, nil)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	srv.Port = portOf(ts.URL) // or the host check kicks in
	return ts, srv
}

type fakeSuppressions struct {
	blocked map[string]bool
	err     error
	reads   int
}

func (f *fakeSuppressions) Block(_ context.Context, a string) error {
	if f.err != nil {
		return f.err
	}
	f.blocked[a] = true
	return nil
}
func (f *fakeSuppressions) Unblock(_ context.Context, a string) error {
	delete(f.blocked, a)
	return nil
}
func (f *fakeSuppressions) List(context.Context) ([]SuppressionEntry, error) {
	f.reads++
	out := []SuppressionEntry{}
	for a := range f.blocked {
		out = append(out, SuppressionEntry{Address: a, Reason: "COMPLAINT"})
	}
	return out, nil
}

// TestAddressFromHeader - the button sits on the message view, and there the
// address reads `Name <a@x.com>`. Passed to SES unfiltered, the display name
// would land on the suppression list or SES would refuse.
func TestAddressFromHeader(t *testing.T) {
	cases := map[string]string{
		`Vorname Nachname <a@x.de>`:    "a@x.de",
		`"Nachname, Vorname" <b@y.de>`: "b@y.de",
		`  c@z.de  `:                   "c@z.de",
		`<d@z.de>`:                     "d@z.de",
		`a@x.de, b@y.de`:               "", // zwei auf einmal: lieber nichts
		`no-at-sign`:                   "",
		``:                             "",
	}
	for in, want := range cases {
		if got := addressFrom(in); got != want {
			t.Errorf("%q -> %q, expected %q", in, got, want)
		}
	}
}

func TestBlockAndUnblock(t *testing.T) {
	f := &fakeSuppressions{blocked: map[string]bool{}}
	ts, _ := serverWithSuppressions(t, f)

	call1 := func(path, body string) *http.Response {
		req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-S3mail-Token", testToken)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := call1("/api/block", `{"address":"Kunde <weg@x.de>"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("block: %d", resp.StatusCode)
	}
	var d struct {
		Address string             `json:"address"`
		Blocked []SuppressionEntry `json:"blocked"`
	}
	json.NewDecoder(resp.Body).Decode(&d)
	if d.Address != "weg@x.de" || !f.blocked["weg@x.de"] {
		t.Fatalf("not blocked: %+v", d)
	}
	// The answer carries the new list, so the dialog is right without a second call.
	if len(d.Blocked) != 1 {
		t.Errorf("list in the answer: %+v", d.Blocked)
	}

	if resp := call1("/api/unblock", `{"address":"weg@x.de"}`); resp.StatusCode != 200 {
		t.Fatalf("unblock: %d", resp.StatusCode)
	}
	if f.blocked["weg@x.de"] {
		t.Error("still blocked after unblocking")
	}

	// Without a usable address: 400, and nothing happens.
	if resp := call1("/api/block", `{"address":"quatsch"}`); resp.StatusCode != 400 {
		t.Errorf("broken address -> %d, expected 400", resp.StatusCode)
	}
}

// TestNoSendingNoSuppressionList - whoever may not send (--no-send) should not
// be able to exclude anyone from being sent to either. Without the check the
// in einen Nil-Zeiger.
func TestNoSendingNoSuppressionList(t *testing.T) {
	ts, _ := serverWithSuppressions(t, nil)
	req, _ := http.NewRequest("GET", ts.URL+"/api/blocked", nil)
	req.Header.Set("X-S3mail-Token", testToken)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("without a suppression list -> %d, expected 400", resp.StatusCode)
	}
}

// TestSuppressionListInTheInterface - the button is the only way there.
func TestSuppressionListInTheInterface(t *testing.T) {
	for _, part := range []string{`id="vBlock"`, `id="blockedBtn"`, "/api/unblock"} {
		if !strings.Contains(PageMailbox, part) {
			t.Errorf("Postfachseite ohne %s", part)
		}
	}
}
