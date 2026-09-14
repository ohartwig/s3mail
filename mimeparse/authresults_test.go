// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package mimeparse

import "testing"

func TestAuthResultsArePassedThrough(t *testing.T) {
	s := Summarize(corpusFile(t, "27-auth-pass"), epoch)
	if s.Auth.SPF != "pass" || s.Auth.DKIM != "pass" || s.Auth.DMARC != "pass" {
		t.Errorf("not read: %+v", s.Auth)
	}
	if s.Auth.Failed() {
		t.Error("three times pass counted as a failure")
	}
	if !s.Auth.Any() {
		t.Error("Any() says there is nothing to show")
	}
}

func TestAFailureIsRecognised(t *testing.T) {
	s := Summarize(corpusFile(t, "28-auth-fail"), epoch)
	if s.Auth.SPF != "fail" || s.Auth.DMARC != "fail" {
		t.Errorf("not read: %+v", s.Auth)
	}
	if s.Auth.DKIM != "none" {
		t.Errorf("dkim=none was turned into %q", s.Auth.DKIM)
	}
	if !s.Auth.Failed() {
		t.Error("spf=fail did not count as a failure")
	}
}

// The one that matters. Authentication-Results is ordinary text in an ordinary
// message: anybody can write one with dkim=pass. Only the header the receiving
// server wrote may be believed, and for a mailbox behind SES that is the one
// naming amazonses.com.
func TestAForeignHeaderIsNotBelieved(t *testing.T) {
	s := Summarize(corpusFile(t, "29-auth-forged"), epoch)
	if s.Auth.Any() {
		t.Errorf("a header the sender wrote himself was believed: %+v", s.Auth)
	}
}

// And when the sender forges one and SES puts its own on top: the topmost wins,
// and it says fail.
func TestTheTopmostHeaderWins(t *testing.T) {
	s := Summarize(corpusFile(t, "30-auth-forged-below"), epoch)
	if s.Auth.SPF != "fail" {
		t.Errorf("the lower, forged header won: %+v", s.Auth)
	}
	if !s.Auth.Failed() {
		t.Error("the forgery below turned the message green")
	}
}

// A message from before any of this was set up says nothing, and nothing is
// what has to be shown - a green tick for "not checked" would be a lie.
func TestNoHeaderMeansNoStatement(t *testing.T) {
	s := Summarize(corpusFile(t, "23-no-message-id"), epoch)
	if s.Auth.Any() || s.Auth.Failed() {
		t.Errorf("something was claimed although no header says anything: %+v", s.Auth)
	}
}

// "spf=" also occurs inside "header.spf=" - a substring match would read the
// wrong value.
func TestTheValueIsNotTakenFromASubstring(t *testing.T) {
	if got := authMethod("dkim=pass header.spf=fail header.i=@x.de", "spf"); got == "fail" {
		t.Errorf("header.spf= was read as spf=: %q", got)
	}
}
