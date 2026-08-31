// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// serveWith is `serve` with a server that can be configured first - read-only
// is not something a client asks for, it is how the server was started.
func serveWith(t *testing.T, prepare func(*Server), lines ...string) []map[string]any {
	t.Helper()
	mb := mailbox(t)
	s := New(t.Context(), []Account{
		{ID: "post", Name: "post@firma.de", Mailbox: mb, From: "post@firma.de"}})
	prepare(s)

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

func call(id int, tool, args string) string {
	return `{"jsonrpc":"2.0","id":` + itoa(id) + `,"method":"tools/call","params":{"name":"` +
		tool + `","arguments":` + args + `}}`
}

func itoa(i int) string { return string(rune('0' + i)) }

// The one this file exists for.
//
// The MCP surface is defended with the argument that everything it can do is
// reversible. A move into the trash is not: the setup assistant offers a
// lifecycle rule that empties it after 7, 30 or 90 days. "Move everything from
// rechnung@ to the trash" fits in a mail, and a mail is text an attacker
// writes. So the tool has to refuse, not the documentation.
func TestMoveToTrashIsRefused(t *testing.T) {
	for _, folder := range []string{"trash", "spam"} {
		answers := serveWith(t, func(*Server) {},
			call(1, "search", `{"query":"Rechnung"}`),
			call(2, "move", `{"keys":["mail/m1"],"folder":"`+folder+`"}`))

		body, isErr := toolResult(t, answers[len(answers)-1])
		if !isErr {
			t.Fatalf("move to %s went through: %s", folder, body)
		}
		if !strings.Contains(body, "lifecycle") {
			t.Errorf("the refusal for %s does not say why: %s", folder, body)
		}
	}
}

// A refusal has to teach, not just say no - the same reasoning `send` gets.
// Otherwise a model concludes the mailbox is broken and tries something worse.
func TestTheRefusalExplainsItself(t *testing.T) {
	answers := serveWith(t, func(*Server) {},
		call(1, "move", `{"keys":["mail/m1"],"folder":"trash"}`))
	body, _ := toolResult(t, answers[len(answers)-1])

	for _, want := range []string{"delet", "human"} {
		if !strings.Contains(strings.ToLower(body), want) {
			t.Errorf("the refusal misses %q: %s", want, body)
		}
	}
}

// Filing somewhere a message can be found again stays allowed - the point is
// not to make the model harmless, it is to keep it from losing mail.
func TestMovingIntoAnOrdinaryFolderStillWorks(t *testing.T) {
	answers := serveWith(t, func(*Server) {},
		call(1, "move", `{"keys":["mail/m1"],"folder":"archiv"}`))
	body, isErr := toolResult(t, answers[len(answers)-1])
	if isErr {
		t.Fatalf("an ordinary move was refused: %s", body)
	}
}

func TestReadOnlyHidesTheChangingTools(t *testing.T) {
	answers := serveWith(t, func(s *Server) { s.ReadOnly = true },
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	blob, err := json.Marshal(answers[0])
	if err != nil {
		t.Fatal(err)
	}
	listed := string(blob)
	for _, gone := range []string{`"move"`, `"tag"`, `"flag"`, `"draft"`} {
		if strings.Contains(listed, gone) {
			t.Errorf("%s is still offered read-only", gone)
		}
	}
	for _, kept := range []string{`"search"`, `"read"`, `"folders"`} {
		if !strings.Contains(listed, kept) {
			t.Errorf("%s disappeared, but reading has to stay", kept)
		}
	}
}

// Hidden is not enough: a tool that is not listed can still be called.
func TestReadOnlyRefusesTheCallAsWell(t *testing.T) {
	answers := serveWith(t, func(s *Server) { s.ReadOnly = true },
		call(1, "move", `{"keys":["mail/m1"],"folder":"archiv"}`))
	body, isErr := toolResult(t, answers[len(answers)-1])
	if !isErr {
		t.Fatalf("read-only let a move through: %s", body)
	}
	if !strings.Contains(body, "read-only") {
		t.Errorf("the refusal does not name the reason: %s", body)
	}
}

// What the model is told up front has to match what the server does, in both
// modes - a rule it learns late reads as a broken mailbox.
func TestInstructionsMatchTheMode(t *testing.T) {
	open := instructions(false)
	if !strings.Contains(open, "trash and spam are not") {
		t.Errorf("the instructions do not mention the trash rule: %s", open)
	}
	if !strings.Contains(open, "no tool to send") {
		t.Errorf("the instructions dropped the send rule: %s", open)
	}
	locked := instructions(true)
	if !strings.Contains(locked, "read-only") {
		t.Errorf("read-only is not announced: %s", locked)
	}
}
