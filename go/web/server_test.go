// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.ole-hartwig.eu/development/s3mail/s3mail/s3fake"
	"git.ole-hartwig.eu/development/s3mail/s3mail/store"
	"git.ole-hartwig.eu/development/s3mail/s3mail/wizard"
)

const testToken = "test-token-123"

// one is the usual case in the tests: a single mailbox, as it was before there
// could be several. The ID is fixed so a test can name it in a URL.
func one(mb *store.Mailbox) []Account {
	return []Account{{ID: "post", Name: "post@firma.de", Mailbox: mb, From: "post@firma.de"}}
}

func buildServer(t *testing.T) (*httptest.Server, *store.Mailbox) {
	t.Helper()
	ctx := context.Background()
	f := s3fake.New()
	f.Store("mail/m1", rawMail("Anna <anna@kunde.de>", "Rechnung 1", "Anbei die Rechnung.",
		"Mon, 03 Aug 2026 09:00:00 +0000"))
	f.Store("mail/m2", rawMail("Shop <news@shop.io>", "Angebot", "Neu im Sortiment.",
		"Tue, 04 Aug 2026 09:00:00 +0000"))
	f.Store("mail/archiv/alt1", rawMail("Alt <alt@firma.de>", "Altes", "Alter Text.",
		"Wed, 01 Jul 2026 08:00:00 +0000"))

	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(one(mb), testToken, "127.0.0.1", 0, map[string]any{
		"bucket": "test-bucket", "root": "mail/", "can_send": false})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	// Take the port from the test address, or the host check kicks in
	srv.Port = portOf(ts.URL)
	return ts, mb
}

func rawMail(from, subject, body, date string) []byte {
	return []byte("From: " + from + "\r\nTo: post@firma.de\r\nSubject: " + subject +
		"\r\nDate: " + date + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
}

func portOf(u string) int {
	parts1 := strings.Split(u, ":")
	p := 0
	for _, c := range parts1[len(parts1)-1] {
		p = p*10 + int(c-'0')
	}
	return p
}

type call struct {
	Code int
	Body []byte
	// Header matters wherever the answer is not JSON: a preview stands or falls
	// on Content-Disposition and the CSP, not on its body.
	Header http.Header
}

func (r call) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("no JSON answer (%d): %s", r.Code, r.Body)
	}
	return m
}

func callServer(t *testing.T, ts *httptest.Server, method, path string, body string,
	header map[string]string) call {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, set := header["X-S3mail-Token"]; !set {
		req.Header.Set("X-S3mail-Token", testToken)
	}
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	blob, _ := io.ReadAll(resp.Body)
	return call{resp.StatusCode, blob, resp.Header}
}

func TestMailboxRoutes(t *testing.T) {
	ts, mb := buildServer(t)

	d := callServer(t, ts, "GET", "/api/messages?folder=", "", nil).json(t)
	msgs, _ := d["messages"].([]any)
	if len(msgs) != 2 {
		t.Errorf("%d messages in the inbox, expected 2", len(msgs))
	}
	if d["allow_delete"] != true || d["folders"] == nil {
		t.Errorf("overview missing from the answer: %v", d)
	}

	callServer(t, ts, "POST", "/api/flag", `{"keys":["mail/m1"],"star":true}`, nil)
	d = callServer(t, ts, "GET", "/api/messages?folder=&star=1", "", nil).json(t)
	if msgs, _ := d["messages"].([]any); len(msgs) != 1 {
		t.Errorf("Stern-Filter: %d Treffer", len(msgs))
	}

	callServer(t, ts, "POST", "/api/tag", `{"keys":["mail/m1"],"add":["wichtig"]}`, nil)
	d = callServer(t, ts, "GET", "/api/messages?folder=*&tag=wichtig", "", nil).json(t)
	if msgs, _ := d["messages"].([]any); len(msgs) != 1 {
		t.Errorf("tag filter: %d hits", len(msgs))
	}

	d = callServer(t, ts, "POST", "/api/move", `{"keys":["mail/m2"],"folder":"spam"}`, nil).json(t)
	moved, _ := d["moved"].([]any)
	if len(moved) != 1 {
		t.Fatalf("Verschieben: %v", d)
	}

	// deleting for good only from the trash
	if r := callServer(t, ts, "POST", "/api/delete", `{"keys":["mail/spam/m2"]}`, nil); r.Code != 403 {
		t.Errorf("Loeschen ausserhalb des Papierkorbs: HTTP %d", r.Code)
	}
	callServer(t, ts, "POST", "/api/move", `{"keys":["mail/spam/m2"],"folder":"trash"}`, nil)
	d = callServer(t, ts, "POST", "/api/delete", `{"keys":["mail/trash/m2"]}`, nil).json(t)
	if d["deleted"] != float64(1) {
		t.Errorf("delete from the trash: %v", d)
	}
	_ = mb
}

func TestErrorCodes(t *testing.T) {
	ts, _ := buildServer(t)
	cases := []struct {
		name, method, path, body string
		code                     int
	}{
		{"invalid folder", "POST", "/api/move", `{"keys":["mail/m1"],"folder":"../boese"}`, 400},
		{"foreign key", "GET", "/api/message?key=andere/nicht-meins", "", 400},
		{"interner Key", "GET", "/api/message?key=mail/.s3mail-state.json", "", 400},
		{"unbekannte Route", "GET", "/api/gibtsnicht", "", 404},
		{"broken JSON", "POST", "/api/flag", `{kaputt`, 400},
		{"unknown tag action", "POST", "/api/tags", `{"action":"quatsch","name":"x"}`, 400},
	}
	for _, f := range cases {
		if r := callServer(t, ts, f.method, f.path, f.body, nil); r.Code != f.code {
			t.Errorf("%s: HTTP %d, expected %d (%s)", f.name, r.Code, f.code, r.Body)
		}
	}
}

func TestReadMailAndAttachment(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	f.Store("mail/m1", []byte("From: a@b.de\r\nTo: c@d.de\r\nSubject: Mit Anhang\r\n"+
		"Date: Mon, 03 Aug 2026 09:00:00 +0000\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: multipart/mixed; boundary=\"B\"\r\n\r\n"+
		"--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHallo mit Ümlaut.\r\n"+
		"--B\r\nContent-Type: application/pdf\r\n"+
		"Content-Disposition: attachment; filename=\"Rechnung.pdf\"\r\n\r\n"+
		"%PDF-fake\r\n--B--\r\n"))
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(one(mb), testToken, "127.0.0.1", 0, nil)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	srv.Port = portOf(ts.URL)

	d := callServer(t, ts, "GET", "/api/message?key=mail/m1", "", nil).json(t)
	if !strings.Contains(d["text"].(string), "Ümlaut") {
		t.Errorf("Fliesstext: %v", d["text"])
	}
	att, _ := d["attachments"].([]any)
	if len(att) != 1 {
		t.Fatalf("Anhaenge: %v", att)
	}
	if d["read"] != true || !mb.State.Get("m1").Read {
		t.Error("opening did not mark it read")
	}

	r := callServer(t, ts, "GET", "/api/attachment?key=mail/m1&index=1", "", nil)
	if r.Code != 200 || !strings.HasPrefix(string(r.Body), "%PDF") {
		t.Errorf("Anhang: HTTP %d %q", r.Code, r.Body)
	}
	r = callServer(t, ts, "GET", "/api/raw?key=mail/m1", "", nil)
	if r.Code != 200 || !strings.HasPrefix(string(r.Body), "From:") {
		t.Errorf("Rohmail: HTTP %d", r.Code)
	}
}

// TestAccessControl - the same three checks as in the Python version.
func TestAccessControl(t *testing.T) {
	ts, _ := buildServer(t)
	without := map[string]string{"X-S3mail-Token": ""}

	if r := callServer(t, ts, "GET", "/api/overview", "", without); r.Code != 403 {
		t.Errorf("without a token: HTTP %d", r.Code)
	}
	if r := callServer(t, ts, "GET", "/api/overview", "", map[string]string{"X-S3mail-Token": "falsch"}); r.Code != 403 {
		t.Errorf("wrong token: HTTP %d", r.Code)
	}
	if r := callServer(t, ts, "GET", "/", "", without); r.Code != 403 {
		t.Errorf("mailbox page without a token: HTTP %d", r.Code)
	}
	if r := callServer(t, ts, "GET", "/api/overview", "", map[string]string{"Cookie": "s3mail=" + testToken,
		"X-S3mail-Token": ""}); r.Code != 200 {
		t.Errorf("token by cookie: HTTP %d", r.Code)
	}

	// CSRF: a foreign page sends a POST, the token cookie rides along
	if r := callServer(t, ts, "POST", "/api/empty-trash", "{}",
		map[string]string{"Origin": "https://boese.example"}); r.Code != 403 {
		t.Errorf("CSRF POST from a foreign origin: HTTP %d", r.Code)
	}
	if r := callServer(t, ts, "POST", "/api/empty-trash", "{}",
		map[string]string{"Origin": "http://localhost:1234"}); r.Code != 403 {
		t.Errorf("foreign port as origin: HTTP %d", r.Code)
	}
	// DNS rebinding: a foreign name in the Host header
	if r := callServer(t, ts, "GET", "/api/messages?folder=", "",
		map[string]string{"Host": "boese.example"}); r.Code != 403 {
		t.Errorf("fremder Host: HTTP %d", r.Code)
	}
}

// TestPageSetsCookie - the download links hang on this.
func TestPageSetsCookie(t *testing.T) {
	ts, _ := buildServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/?t="+testToken, nil)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	c := resp.Header.Get("Set-Cookie")
	if !strings.Contains(c, "s3mail="+testToken) || !strings.Contains(c, "SameSite=Strict") {
		t.Errorf("Cookie: %q", c)
	}
	blob, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(blob), "__CONFIG__") {
		t.Error("placeholder __CONFIG__ not replaced")
	}
	if !strings.Contains(string(blob), "test-bucket") {
		t.Error("configuration not put into the page")
	}
}

func TestInterfaceIsComplete(t *testing.T) {
	// Since the translation the token page is a skeleton: its sentences live
	// in the catalogues, not in the file. What counts for it is that the
	// template is there, not how long it is.
	for name, page := range map[string]string{
		"Postfach":  PageMailbox,
		"Assistent": PageWizard,
		"Token":     PageToken,
	} {
		atLeast := 500
		if name == "Token" {
			atLeast = 200
		}
		if len(page) < atLeast {
			t.Errorf("%s: only %d characters - embedded?", name, len(page))
		}
		if !strings.Contains(page, "<!doctype html>") && !strings.Contains(page, "<!DOCTYPE html>") {
			t.Errorf("%s: no document head", name)
		}
	}
	if !strings.Contains(PageMailbox, "__CONFIG__") {
		t.Error("the mailbox page has no placeholder for the configuration")
	}
}

// TestWithoutMailbox - before the setup there is no mailbox. The routes
// must not run into a nil pointer then.
func TestWithoutMailbox(t *testing.T) {
	srv := NewServer(nil, testToken, "127.0.0.1", 0, nil)
	srv.WithWizard(&wizard.Wizard{})
	ts := httptest.NewServer(srv)
	defer ts.Close()
	srv.Port = portOf(ts.URL)

	for _, path := range []string{"/api/messages?folder=", "/api/overview",
		"/api/message?key=mail/m1", "/api/raw?key=mail/m1"} {
		r := callServer(t, ts, "GET", path, "", nil)
		if r.Code != 503 {
			t.Errorf("%s: HTTP %d, expected 503", path, r.Code)
		}
	}
	if r := callServer(t, ts, "POST", "/api/refresh", "{}", nil); r.Code != 503 {
		t.Errorf("refresh: HTTP %d", r.Code)
	}
	// The wizard has to stay reachable
	if r := callServer(t, ts, "POST", "/api/setup/info", "{}", nil); r.Code == 503 {
		t.Error("wizard blocked although it is what is needed")
	}
	// Quitting has to work right here - this is the state a first-time reader
	// is in, and without a console the button is the only way out.
	stopped := make(chan struct{}, 1)
	srv.OnShutdown = func() { stopped <- struct{}{} }
	if r := callServer(t, ts, "POST", "/api/quit", "{}", nil); r.Code != 200 {
		t.Errorf("quitting inside the wizard: HTTP %d", r.Code)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Error("OnShutdown was not called")
	}

	// and the start page shows it. What is checked is an anchor from the markup
	// and not a sentence: since the translation the page comes in the language
	// of the asker, and a test hanging on one word breaks at the next language
	// switch without anything being broken.
	r := callServer(t, ts, "GET", "/", "", nil)
	if r.Code != 200 || !strings.Contains(string(r.Body), `id="saveCreds"`) {
		t.Errorf("Startseite ohne Postfach: HTTP %d", r.Code)
	}
}

// TestAutoRefreshIsShipped - the interval comes from the configuration;
// without it the page would stand still and nobody would see the refresh exists.
func TestAutoRefreshIsShipped(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	f.Store("mail/m1", rawMail("a@b.de", "x", "y", "Mon, 03 Aug 2026 09:00:00 +0000"))
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(one(mb), testToken, "127.0.0.1", 0, map[string]any{
		"bucket": "test-bucket", "refresh_seconds": 45})
	ts := httptest.NewServer(srv)
	defer ts.Close()
	srv.Port = portOf(ts.URL)

	page := string(callServer(t, ts, "GET", "/?t="+testToken, "", nil).Body)
	if !strings.Contains(page, "scheduleAutoRefresh") {
		t.Error("the automatic refresh is missing from the shipped page")
	}
	if !strings.Contains(page, `"refresh_seconds":45`) {
		t.Error("the interval does not arrive in the page")
	}
	// Without an interval the refresh must stay away, not fall back to a default
	srv.Config = map[string]any{"bucket": "test-bucket", "refresh_seconds": 0}
	page = string(callServer(t, ts, "GET", "/?t="+testToken, "", nil).Body)
	if !strings.Contains(page, `"refresh_seconds":0`) {
		t.Error("a switched-off refresh is not shipped as 0")
	}
}

// TestComposeWithoutATemplate - replying and forwarding existed from the start,
// a message without a template did not: the server could do it (mode "new"),
// only no path led there. Nobody testing a full mailbox notices that.
func TestComposeWithoutATemplate(t *testing.T) {
	for _, part := range []string{`id="new"`, `compose("new")`, `mode === "new" ? ""`} {
		if !strings.Contains(PageMailbox, part) {
			t.Errorf("mailbox page without %s - new message unreachable", part)
		}
	}
	// The key of the open message must not ride along, or a new message would
	// hang on the thread of somebody else's.
	if strings.Contains(PageMailbox, `{mode, key: current`) {
		t.Error("a new message carries the key of the open message")
	}
}
