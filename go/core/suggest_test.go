// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package core

import "testing"

// from builds n messages from one sender, of which moved sit in folder and the
// rest in the inbox.
func from(sender, folder string, n, moved int, tags ...string) []Message {
	out := make([]Message, 0, n)
	for i := 0; i < n; i++ {
		m := Message{FromAddr: sender, Folder: Inbox, Tags: []string{}}
		if i < moved {
			m.Folder = folder
			m.Tags = append([]string{}, tags...)
		}
		out = append(out, m)
	}
	return out
}

// TestASuggestionRestsOnWhatSomebodyAlreadyDid - the whole idea. Eleven of
// twelve messages moved into the same folder is a rule being followed by hand.
func TestASuggestionRestsOnWhatSomebodyAlreadyDid(t *testing.T) {
	msgs := from("news@shop.io", "werbung", 12, 11)
	got := Suggest(msgs, nil)
	if len(got) != 1 {
		t.Fatalf("%d suggestions, expected 1: %+v", len(got), got)
	}
	s := got[0]
	if s.Contains != "news@shop.io" || s.Folder != "werbung" {
		t.Errorf("%+v", s)
	}
	// The numbers go to the interface: a suggestion nobody can check is one
	// nobody should accept.
	if s.Count != 12 || s.Agree != 11 {
		t.Errorf("evidence %d/%d, expected 11/12", s.Agree, s.Count)
	}
}

// TestTooFewIsCoincidence - three of three is not a habit yet.
func TestTooFewIsCoincidence(t *testing.T) {
	if got := Suggest(from("a@x.de", "archiv", 3, 3), nil); len(got) != 0 {
		t.Errorf("suggested on three messages: %+v", got)
	}
}

// TestHalfAndHalfIsNoRule - somebody sorting by something the sender does not
// determine. A rule would be wrong for every second message.
func TestHalfAndHalfIsNoRule(t *testing.T) {
	if got := Suggest(from("a@x.de", "archiv", 10, 5), nil); len(got) != 0 {
		t.Errorf("suggested on half the evidence: %+v", got)
	}
}

// TestTheInboxIsNoDecision - mail lands there by itself. That most of it is
// still there says nothing about anybody's intention.
func TestTheInboxIsNoDecision(t *testing.T) {
	msgs := make([]Message, 0, 20)
	for i := 0; i < 20; i++ {
		msgs = append(msgs, Message{FromAddr: "a@x.de", Folder: Inbox})
	}
	if got := Suggest(msgs, nil); len(got) != 0 {
		t.Errorf("suggested moving to the inbox: %+v", got)
	}
}

// TestTheTrashIsNeverSuggested - a rule that throws mail away on a guess is the
// one mistake here that costs something. Everything else is reversible.
func TestTheTrashIsNeverSuggested(t *testing.T) {
	if got := Suggest(from("a@x.de", Trash, 20, 20), nil); len(got) != 0 {
		t.Errorf("suggested a rule into the trash: %+v", got)
	}
}

// TestOwnMailSaysNothing - sent copies and drafts are our own writing and tell
// nothing about where somebody files what they receive.
func TestOwnMailSaysNothing(t *testing.T) {
	msgs := append(from("kunde@x.de", Sent, 10, 10), from("kunde@x.de", Drafts, 10, 10)...)
	if got := Suggest(msgs, nil); len(got) != 0 {
		t.Errorf("suggested from our own mail: %+v", got)
	}
}

// TestWhatARuleAlreadyCatchesIsNotSuggested - a list that repeats what exists
// is noise, and noise is what gets a feature ignored.
func TestWhatARuleAlreadyCatchesIsNotSuggested(t *testing.T) {
	msgs := from("news@shop.io", "werbung", 12, 12)
	folder := "werbung"
	rules := []Rule{{Field: "from", Contains: "shop.io", Folder: &folder, Enabled: true}}
	if got := Suggest(msgs, rules); len(got) != 0 {
		t.Errorf("suggested what a rule already catches: %+v", got)
	}
}

// TestATagThatAlwaysComesAlongBelongsInTheRule - somebody has been putting it
// there by hand every time.
func TestATagThatAlwaysComesAlongBelongsInTheRule(t *testing.T) {
	msgs := from("buchhaltung@lieferant.de", "rechnungen", 10, 10, "wichtig")
	got := Suggest(msgs, nil)
	if len(got) != 1 {
		t.Fatalf("%d suggestions", len(got))
	}
	if len(got[0].Tags) != 1 || got[0].Tags[0] != "wichtig" {
		t.Errorf("tags: %v", got[0].Tags)
	}
	// And a tag on a minority does not.
	sparse := from("a@x.de", "archiv", 10, 10)
	sparse[0].Tags = []string{"zufall"}
	if s := Suggest(sparse, nil); len(s) != 1 || len(s[0].Tags) != 0 {
		t.Errorf("a tag on one message out of ten made it into the rule: %+v", s)
	}
}

// TestTheOrderIsStable - two calls on the same index must produce the same
// list, or the interface reshuffles under the reader's hand.
func TestTheOrderIsStable(t *testing.T) {
	var msgs []Message
	msgs = append(msgs, from("b@x.de", "archiv", 8, 8)...)
	msgs = append(msgs, from("a@x.de", "archiv", 8, 8)...)
	msgs = append(msgs, from("c@x.de", "werbung", 20, 20)...)

	first := Suggest(msgs, nil)
	for i := 0; i < 20; i++ {
		again := Suggest(msgs, nil)
		for j := range first {
			if first[j].Contains != again[j].Contains {
				t.Fatalf("the order moves between calls: %v vs %v", first, again)
			}
		}
	}
	// The best evidence first.
	if first[0].Contains != "c@x.de" {
		t.Errorf("the strongest suggestion is not at the top: %+v", first)
	}
}

// TestTheSuggestionBecomesAWorkingRule - it is offered to be accepted, and what
// comes out has to survive CleanRules.
func TestTheSuggestionBecomesAWorkingRule(t *testing.T) {
	got := Suggest(from("news@shop.io", "werbung", 12, 12, "werbung"), nil)
	clean, err := CleanRules([]Rule{got[0].AsRule()})
	if err != nil {
		t.Fatalf("the suggested rule does not survive the check: %v", err)
	}
	if len(clean) != 1 || clean[0].Contains != "news@shop.io" || *clean[0].Folder != "werbung" {
		t.Errorf("%+v", clean)
	}
	// And it has to hit the messages it came from.
	if !clean[0].Hits(Message{FromAddr: "news@shop.io", From: "Shop <news@shop.io>"}) {
		t.Error("the suggested rule does not catch its own evidence")
	}
}
