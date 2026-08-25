// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/core"
)

func mails() []core.Message {
	return []core.Message{
		// The accountant: writes often, last a while ago.
		{From: "Buchhaltung <buch@kunde.de>", FromAddr: "buch@kunde.de",
			ToAddrs: []string{"post@firma.de"}, Date: "2026-06-01T09:00:00Z"},
		{From: "Buchhaltung <buch@kunde.de>", FromAddr: "buch@kunde.de",
			ToAddrs: []string{"post@firma.de"}, Date: "2026-06-02T09:00:00Z"},
		{From: "buch@kunde.de", FromAddr: "buch@kunde.de",
			ToAddrs: []string{"post@firma.de"}, Date: "2026-06-03T09:00:00Z"},
		// This morning's customer: once, but just now.
		{From: `"Schmidt, Peter" <p.schmidt@neu.de>`, FromAddr: "p.schmidt@neu.de",
			ToAddrs: []string{"post@firma.de"}, Date: "2026-08-24T08:00:00Z"},
		// Somebody written to, never seen as a sender.
		{From: "post@firma.de", FromAddr: "post@firma.de",
			ToAddrs: []string{"anna@ziel.de"}, Date: "2026-08-20T08:00:00Z"},
	}
}

func TestFrequentComesFirst(t *testing.T) {
	got := core.Addresses(mails(), "", 10)
	if len(got) == 0 {
		t.Fatal("nothing found")
	}
	if got[0].Addr != "buch@kunde.de" && got[0].Addr != "post@firma.de" {
		t.Errorf("first is %q - neither of the two frequent ones", got[0].Addr)
	}
}

// A name is picked up even when the address only ever appeared as a recipient -
// there it has no display name at all, and the field still has to offer it.
func TestARecipientIsOffered(t *testing.T) {
	got := core.Addresses(mails(), "anna", 10)
	if len(got) != 1 || got[0].Addr != "anna@ziel.de" {
		t.Fatalf("recipient not found: %+v", got)
	}
	if got[0].Label() != "anna@ziel.de" {
		t.Errorf("label %q - without a name it is the bare address", got[0].Label())
	}
}

// People change how they sign. The newer spelling is the better guess, and a
// message without a name must not erase one that was there.
func TestTheNewerNameWins(t *testing.T) {
	msgs := []core.Message{
		{From: "A. Mueller <a@x.de>", FromAddr: "a@x.de", Date: "2026-01-01T00:00:00Z"},
		{From: "Anna Müller <a@x.de>", FromAddr: "a@x.de", Date: "2026-08-01T00:00:00Z"},
		{From: "a@x.de", FromAddr: "a@x.de", Date: "2026-08-02T00:00:00Z"},
	}
	got := core.Addresses(msgs, "a@x", 5)
	if len(got) != 1 {
		t.Fatalf("%d entries for one address", len(got))
	}
	if got[0].Name != "Anna Müller" {
		t.Errorf("name is %q - expected the newest one that had a name", got[0].Name)
	}
	if got[0].Count != 3 {
		t.Errorf("counted %d, expected 3", got[0].Count)
	}
}

// Quotes come off: "Schmidt, Peter" arrives quoted from Outlook and would
// otherwise be offered with them.
func TestQuotesComeOff(t *testing.T) {
	got := core.Addresses(mails(), "schmidt", 5)
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if got[0].Name != "Schmidt, Peter" {
		t.Errorf("name is %q", got[0].Name)
	}
	if got[0].Label() != "Schmidt, Peter <p.schmidt@neu.de>" {
		t.Errorf("label is %q", got[0].Label())
	}
}

// Searching by name is as common as searching by address.
func TestSearchingByName(t *testing.T) {
	if got := core.Addresses(mails(), "buchhaltung", 5); len(got) != 1 {
		t.Errorf("name search found %d", len(got))
	}
	if got := core.Addresses(mails(), "kunde.de", 5); len(got) != 1 {
		t.Errorf("domain search found %d", len(got))
	}
}

func TestTheLimitHolds(t *testing.T) {
	if got := core.Addresses(mails(), "", 2); len(got) != 2 {
		t.Errorf("%d entries although two were asked for", len(got))
	}
}

// Nothing that is not an address may come out - a broken header must not turn
// into a suggestion.
func TestOnlyAddressesComeOut(t *testing.T) {
	msgs := []core.Message{
		{From: "kaputt", FromAddr: "kaputt", Date: "2026-08-01T00:00:00Z"},
		{From: "", FromAddr: "", ToAddrs: []string{"", "  ", "auch-kaputt"},
			Date: "2026-08-01T00:00:00Z"},
	}
	if got := core.Addresses(msgs, "", 10); len(got) != 0 {
		t.Errorf("something without an @ was offered: %+v", got)
	}
}
