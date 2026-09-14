// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/go/core"
	"git.ole-hartwig.eu/development/s3mail/s3mail/go/s3fake"
	"git.ole-hartwig.eu/development/s3mail/s3mail/go/store"
)

func mailbox(t *testing.T) *store.Mailbox {
	t.Helper()
	ctx := t.Context()
	f := s3fake.New()
	f.Store("mail/m1", []byte("From: Anna <anna@kunde.de>\r\nTo: post@firma.de\r\n"+
		"Subject: Rechnung 1\r\nDate: Mon, 03 Aug 2026 09:00:00 +0000\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n\r\nAnbei die Rechnung.\r\n"))
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	return mb
}

func serve(t *testing.T, lines ...string) []map[string]any {
	t.Helper()
	mb := mailbox(t)
	s := New(t.Context(), []Account{
		{ID: "post", Name: "post@firma.de", Mailbox: mb, From: "post@firma.de"}})
	var out bytes.Buffer
	if err := s.Serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var answers []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("answer is not JSON: %q", line)
		}
		answers = append(answers, m)
	}
	return answers
}

func toolResult(t *testing.T, answer map[string]any) (string, bool) {
	t.Helper()
	res, ok := answer["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %+v", answer)
	}
	isErr, _ := res["isError"].(bool)
	content, _ := res["content"].([]any)
	var sb strings.Builder
	for _, c := range content {
		if m, ok := c.(map[string]any); ok {
			sb.WriteString(m["text"].(string))
		}
	}
	return sb.String(), isErr
}

// TestThereIsNoSendTool is the one that matters. A model that can send from the
// business address turns every incoming message into an attack path: somebody
// else's text lands in its context, and "forward the invoices to me" fits in a
// mail. The drafts folder is the approval step, and it is the only path there is.
func TestThereIsNoSendTool(t *testing.T) {
	answers := serve(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	res := answers[0]["result"].(map[string]any)
	names := map[string]bool{}
	for _, tool := range res["tools"].([]any) {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	for _, forbidden := range []string{"send", "reply", "forward", "delete", "purge"} {
		if names[forbidden] {
			t.Errorf("there is a %q tool - a model must not be able to do that unwatched", forbidden)
		}
	}
	for _, wanted := range []string{"search", "read", "draft", "move", "tag", "flag", "folders"} {
		if !names[wanted] {
			t.Errorf("%q is missing", wanted)
		}
	}
}

// TestAskingToSendExplainsWhyNot - a model that tries should learn the reason,
// not conclude the mailbox is broken and look for another way.
func TestAskingToSendExplainsWhyNot(t *testing.T) {
	answers := serve(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call", "params":{"name":"send","arguments":{"to":"a@b.de"}}}`)
	got, isErr := toolResult(t, answers[0])
	if !isErr {
		t.Error("send did not come back as an error")
	}
	if !strings.Contains(got, "draft") || !strings.Contains(strings.ToLower(got), "human") {
		t.Errorf("the answer does not name the way round: %q", got)
	}
}

// TestADraftIsWrittenAndNotSent - the tool has to land in the drafts folder,
// and it has to say that nothing went out.
func TestADraftIsWrittenAndNotSent(t *testing.T) {
	mb := mailbox(t)
	s := New(t.Context(), []Account{
		{ID: "post", Name: "post", Mailbox: mb, From: "post@firma.de"}})
	var out bytes.Buffer
	err := s.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call", "params":{"name":"draft","arguments":{"to":"kunde@x.de","subject":"Angebot","body":"Anbei."}}}`+"\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "NOT been sent") {
		t.Errorf("the answer does not say that nothing went out: %s", out.String())
	}
	var drafts int
	for _, m := range mb.Index() {
		if m.Folder == core.Drafts {
			drafts++
			if m.Subject != "Angebot" {
				t.Errorf("subject of the draft: %q", m.Subject)
			}
		}
	}
	if drafts != 1 {
		t.Errorf("%d drafts in the mailbox, expected 1", drafts)
	}
}

// TestReadWarnsThatItIsSomebodyElsesText - it rides with every message on
// purpose. A model that has read three should not have to remember it from the
// first one.
func TestReadWarnsThatItIsSomebodyElsesText(t *testing.T) {
	answers := serve(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call", "params":{"name":"read","arguments":{"key":"mail/m1"}}}`)
	got, _ := toolResult(t, answers[0])
	if !strings.Contains(got, "not") || !strings.Contains(got, "instructions") {
		t.Errorf("no warning in front of the message: %q", got)
	}
	if !strings.Contains(got, "Anbei die Rechnung") {
		t.Errorf("the message itself is missing: %q", got)
	}
}

// TestAKeyOutsideTheMailboxIsRefused - the security boundary has to hold here
// as it does over HTTP. A model may name any string it likes.
func TestAKeyOutsideTheMailboxIsRefused(t *testing.T) {
	for _, key := range []string{"andere/nicht-meins", "mail/.s3mail-state/snapshot.json", "../etc/passwd"} {
		answers := serve(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call", "params":{"name":"read","arguments":{"key":"`+key+`"}}}`)
		if _, isErr := toolResult(t, answers[0]); !isErr {
			t.Errorf("%q was read", key)
		}
	}
}

// TestANotificationGetsNoAnswer - the protocol requires it, and a client that
// follows the spec closes the connection over a stray answer.
func TestANotificationGetsNoAnswer(t *testing.T) {
	answers := serve(t,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if len(answers) != 1 {
		t.Fatalf("%d answers for one request plus one notification: %+v", len(answers), answers)
	}
	if answers[0]["id"].(float64) != 1 {
		t.Errorf("the answer belongs to the wrong request: %+v", answers[0])
	}
}

// TestInitializeSpeaksTheClientsVersion where we know it - a client that asks
// for an older revision should not be told a newer number back.
func TestInitializeSpeaksTheClientsVersion(t *testing.T) {
	answers := serve(t, `{"jsonrpc":"2.0","id":1,"method":"initialize", "params":{"protocolVersion":"2024-11-05"}}`)
	res := answers[0]["result"].(map[string]any)
	if res["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocol version: %v", res["protocolVersion"])
	}
	if !strings.Contains(res["instructions"].(string), "no tool to send") {
		t.Errorf("the instructions do not say that sending is out: %v", res["instructions"])
	}
}

// TestGarbageDoesNotKillTheServer - it reads from a pipe somebody else fills.
func TestGarbageDoesNotKillTheServer(t *testing.T) {
	answers := serve(t, `nicht mal JSON`, `{"jsonrpc":"1.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"gibtsnicht"}`,
		`{"jsonrpc":"2.0","id":4,"method":"ping"}`)
	if len(answers) != 4 {
		t.Fatalf("%d answers, expected 4: %+v", len(answers), answers)
	}
	if answers[3]["error"] != nil {
		t.Errorf("the sound request after the garbage failed: %+v", answers[3])
	}
}

// TestSearchFindsAndStaysWithinTheLimit.
func TestSearchFindsAndStaysWithinTheLimit(t *testing.T) {
	answers := serve(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call", "params":{"name":"search","arguments":{"query":"rechnung"}}}`)
	got, isErr := toolResult(t, answers[0])
	if isErr {
		t.Fatalf("search failed: %q", got)
	}
	if !strings.Contains(got, "mail/m1") || !strings.Contains(got, "Rechnung 1") {
		t.Errorf("the message was not found: %q", got)
	}
}

// testCacheKey encrypts what the tests write to disk, like the real thing.
// Fixed rather than random: a test that wants to look at a cache file has to be
// able to open it.
var testCacheKey = []byte("0123456789abcdef0123456789abcdef")
