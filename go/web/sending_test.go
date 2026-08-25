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

	"git.ole-hartwig.eu/development/s3mail/s3mail/s3fake"
	"git.ole-hartwig.eu/development/s3mail/s3mail/store"
)

// The send path, seen from the outside: what the browser gets, and what stays
// behind in the bucket.

func serverWith(t *testing.T, f *s3fake.Fake) (*httptest.Server, *Server, *store.Mailbox) {
	t.Helper()
	ctx := context.Background()
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	accounts := one(mb)
	accounts[0].Sender = &fakeSender{}
	srv := NewServer(accounts, testToken, "127.0.0.1", 0, nil)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	srv.Port = portOf(ts.URL)
	return ts, srv, mb
}

func markers(f *s3fake.Fake) []string {
	var out []string
	for k := range f.Objs {
		if strings.HasSuffix(k, ".sending") {
			out = append(out, k)
		}
	}
	return out
}

// A send that runs through leaves nothing behind. The marker is bookkeeping,
// not a record - the sent folder is the record.
func TestASuccessfulSendLeavesNoMarker(t *testing.T) {
	f := s3fake.New()
	ts, _, _ := serverWith(t, f)

	r := callServer(t, ts, "POST", "/api/send",
		`{"mode":"new","to":"kunde@x.de","subject":"Rechnung","body":"Anbei."}`, nil)
	if r.Code != 200 {
		t.Fatalf("HTTP %d: %s", r.Code, r.Body)
	}
	if m := markers(f); len(m) != 0 {
		t.Errorf("marker left behind: %v", m)
	}
}

// SES refused. Nothing went out, so the marker has to go - otherwise the next
// start asks a question whose answer is "no" and always was.
func TestARefusedSendLeavesNoQuestion(t *testing.T) {
	f := s3fake.New()
	ts, srv, _ := serverWith(t, f)
	srv.accounts[0].Sender = &fakeSender{err: errors.New("MessageRejected")}

	r := callServer(t, ts, "POST", "/api/send",
		`{"mode":"new","to":"kunde@x.de","subject":"x","body":"y"}`, nil)
	if r.Code == 200 {
		t.Fatal("a refused send was reported as success")
	}
	if m := markers(f); len(m) != 0 {
		t.Errorf("a refused send left a marker: %v", m)
	}
}

// The marker is a safety net, not a permission. If the bucket refuses the
// write, the mail still goes out - refusing here would turn a storage problem
// into "you cannot send mail" - and the answer says what was lost.
func TestSendWorksEvenIfTheMarkerCannotBeWritten(t *testing.T) {
	f := s3fake.New()
	ts, _, _ := serverWith(t, f)
	f.PutErr = errors.New("no write permission")

	r := callServer(t, ts, "POST", "/api/send",
		`{"mode":"new","to":"kunde@x.de","subject":"x","body":"y"}`, nil)
	if r.Code != 200 {
		t.Fatalf("HTTP %d - a marker that cannot be written blocked the send", r.Code)
	}
	if !strings.Contains(string(r.Body), "warning") {
		t.Errorf("no warning although the send was not recorded: %s", r.Body)
	}
}

// An unfinished send reaches the page through the overview, so the banner is
// there on the first paint rather than after a second request.
func TestTheOpenQuestionReachesThePage(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	ts, srv, mb := serverWith(t, f)

	if _, err := mb.BeginSend(ctx, store.Sending{To: "kunde@x.de",
		Subject: "Rechnung 7", MessageID: "<r7@firma.de>", Raw: []byte("From: a\r\n\r\nx")}); err != nil {
		t.Fatal(err)
	}
	srv.RecoverSends(ctx)

	r := callServer(t, ts, "GET", "/api/overview", "", nil)
	var got struct {
		Pending []struct{ Key, To, Subject string } `json:"pending_sends"`
	}
	if err := json.Unmarshal(r.Body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Pending) != 1 {
		t.Fatalf("%d open sends in the overview, expected 1: %s", len(got.Pending), r.Body)
	}
	if got.Pending[0].Subject != "Rechnung 7" {
		t.Errorf("the question does not say what it is about: %+v", got.Pending[0])
	}
}

// Answering takes the question off the list and does what the answer implies.
func TestAnsweringClearsTheQuestion(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	ts, srv, mb := serverWith(t, f)

	key, err := mb.BeginSend(ctx, store.Sending{To: "kunde@x.de", Subject: "Rechnung 7",
		MessageID: "<r7@firma.de>", Raw: []byte("From: a\r\nTo: b\r\n\r\nx")})
	if err != nil {
		t.Fatal(err)
	}
	srv.RecoverSends(ctx)

	r := callServer(t, ts, "POST", "/api/sending/resolve",
		`{"key":"`+key+`","sent":true}`, nil)
	if r.Code != 200 {
		t.Fatalf("HTTP %d: %s", r.Code, r.Body)
	}
	if m := markers(f); len(m) != 0 {
		t.Errorf("the marker survived the answer: %v", m)
	}
	if len(srv.pendingSends(&srv.accounts[0])) != 0 {
		t.Error("the question is still on the list")
	}
}

// The route takes a key from the browser. It must only accept one this server
// actually asked about - otherwise it is a way to have any object treated as a
// send marker and deleted.
func TestOnlyAKnownKeyIsAccepted(t *testing.T) {
	f := s3fake.New()
	ts, _, _ := serverWith(t, f)

	r := callServer(t, ts, "POST", "/api/sending/resolve",
		`{"key":"mail/drafts/fremd.eml.sending","sent":true}`, nil)
	if r.Code == 200 {
		t.Error("an unknown key was accepted")
	}
}

// The mailbox page has to carry the way to the answer. The page is a string;
// a lost button falls to nobody's notice otherwise.
func TestThePageCarriesTheQuestion(t *testing.T) {
	for _, anchor := range []string{
		`id="pending"`,         // where the banner lands
		"renderPending",        // the renderer
		"/api/sending/resolve", // the route it calls
		`T["sending.wentOut"]`, // both answers
		`T["sending.didNot"]`,
	} {
		if !strings.Contains(PageMailbox, anchor) {
			t.Errorf("the mailbox page has lost %q", anchor)
		}
	}
}
