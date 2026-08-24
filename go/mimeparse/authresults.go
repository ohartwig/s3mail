// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package mimeparse

import (
	"strings"

	"github.com/emersion/go-message"
)

// SPF, DKIM and DMARC - and why only one of these headers may be believed.
//
// Authentication-Results is written by the receiving mail server after it has
// checked the sender. For a mailbox behind SES that server is SES, and its
// header says amazonses.com. So far, so useful: it is the difference between
// "this really came from the bank" and "somebody wrote the bank's address into
// the From line", which is one line of typing.
//
// The catch is that the header is ordinary text in an ordinary message. Anybody
// can put one into the mail they send, with dkim=pass and any authserv-id they
// like. The receiving server prepends its own on top, so the rule is: read the
// topmost one, and only believe it if it names the server we actually receive
// through. A parser that takes the last one - or any one - hands an attacker a
// green tick to write for himself.
//
// Which is why nothing is reported when no header names amazonses.com: an empty
// result is a statement about what is known, and a wrong one is worse than none.

// AuthResults is what SES said about the sender. Empty strings mean "not
// stated" - a message that arrived before the check was set up, or one whose
// header came from somewhere we do not trust.
type AuthResults struct {
	SPF   string `json:"spf,omitempty"`
	DKIM  string `json:"dkim,omitempty"`
	DMARC string `json:"dmarc,omitempty"`
}

// Any says whether there is anything to show at all.
func (a AuthResults) Any() bool { return a.SPF != "" || a.DKIM != "" || a.DMARC != "" }

// Failed is the one case the interface has to make loud: something was checked
// and did not pass. "none" and "neutral" are not failures - they mean the
// sender's domain published nothing to check against.
func (a AuthResults) Failed() bool {
	for _, v := range []string{a.SPF, a.DKIM, a.DMARC} {
		switch v {
		case "fail", "softfail", "permerror", "temperror":
			return true
		}
	}
	return false
}

// trustedAuthserv is the identity SES writes into the header. Everything else
// is somebody else's claim about themselves.
const trustedAuthserv = "amazonses.com"

func authResults(h message.Header) AuthResults {
	fields := h.FieldsByKey("Authentication-Results")
	for fields.Next() {
		line, err := fields.Text()
		if err != nil {
			line = fields.Value()
		}
		// The authserv-id is the first token, up to the first semicolon.
		id, rest, found := strings.Cut(line, ";")
		if !found || !strings.Contains(strings.ToLower(id), trustedAuthserv) {
			continue
		}
		out := AuthResults{
			SPF:   authMethod(rest, "spf"),
			DKIM:  authMethod(rest, "dkim"),
			DMARC: authMethod(rest, "dmarc"),
		}
		if out.Any() {
			return out
		}
	}
	return AuthResults{}
}

// authMethod pulls "spf=pass" out of the rest of the line. The value ends at
// the first space, semicolon or opening bracket - "dkim=pass header.i=@x.de"
// and "spf=pass (google.com: domain of ...)" both occur in the wild.
func authMethod(line, method string) string {
	low := strings.ToLower(line)
	for i := 0; i+len(method)+1 <= len(low); i++ {
		if !strings.HasPrefix(low[i:], method+"=") {
			continue
		}
		// Must stand on its own: "spf=" also occurs inside "header.spf=".
		if i > 0 {
			c := low[i-1]
			if c != ' ' && c != ';' && c != '\t' && c != '\n' {
				continue
			}
		}
		v := low[i+len(method)+1:]
		if end := strings.IndexAny(v, " ;(\t\r\n"); end >= 0 {
			v = v[:end]
		}
		return v
	}
	return ""
}
