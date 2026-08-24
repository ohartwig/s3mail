// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package mimeparse

import "testing"

func TestUnsubscribeReadsBothWays(t *testing.T) {
	u := Read(corpusFile(t, "31-unsubscribe-both"), epoch).Unsub
	if u.Mail != "abmelden@shop.io" {
		t.Errorf("address is %q", u.Mail)
	}
	// The subject is what some lists insist on; without it the mail is ignored.
	if u.Subject != "unsubscribe%20post@firma.de" {
		t.Errorf("subject is %q", u.Subject)
	}
	if u.Link != "https://shop.io/abmelden?id=123" {
		t.Errorf("link is %q", u.Link)
	}
	if !u.OneClick {
		t.Error("the sender offers one-click and it was not noticed")
	}
	if !u.Any() {
		t.Error("Any() says there is nothing")
	}
}

// The comma inside the mailto: query must not split the entry - that is what
// makes splitting on brackets rather than commas necessary.
func TestACommaInTheQueryDoesNotSplitIt(t *testing.T) {
	u := Read([]byte("From: a@b.de\r\n"+
		"List-Unsubscribe: <mailto:off@x.de?subject=stop&body=a,b,c>\r\n\r\nx"), epoch).Unsub
	if u.Mail != "off@x.de" {
		t.Errorf("address is %q", u.Mail)
	}
}

func TestOnlyALinkIsAlsoFine(t *testing.T) {
	u := Read(corpusFile(t, "32-unsubscribe-link-only"), epoch).Unsub
	if u.Mail != "" {
		t.Errorf("an address appeared out of nowhere: %q", u.Mail)
	}
	if u.Link != "https://andere.io/off" {
		t.Errorf("link is %q", u.Link)
	}
	if u.OneClick {
		t.Error("one-click was assumed although the header does not say so")
	}
}

// A plain http: link is not taken. Opening one is a request in the clear that
// says "this address is read", and the sender chose to offer it that way.
func TestPlainHTTPIsNotOffered(t *testing.T) {
	u := Read([]byte("From: a@b.de\r\n"+
		"List-Unsubscribe: <http://x.de/off>\r\n\r\nx"), epoch).Unsub
	if u.Any() {
		t.Errorf("an unencrypted link was offered: %+v", u)
	}
}

func TestNoHeaderNoOffer(t *testing.T) {
	if u := Read(corpusFile(t, "23-no-message-id"), epoch).Unsub; u.Any() {
		t.Errorf("something was offered without a header: %+v", u)
	}
}
