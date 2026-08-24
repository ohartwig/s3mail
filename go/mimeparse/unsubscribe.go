// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package mimeparse

import (
	"strings"

	"github.com/emersion/go-message"
)

// List-Unsubscribe, and the part of it s3mail deliberately does not use.
//
// The header carries one or more ways to get off a list: a mailto: address, an
// https: link, or both. RFC 8058 adds one-click unsubscribe - the client POSTs
// to the link by itself, no page, no confirmation.
//
// s3mail does not do the POST. A mail client that fires requests at URLs a
// stranger put in a header has a back channel: the mere request confirms the
// address is read, and it happens without anybody deciding to. The mailto: way
// is offered instead - it opens the compose dialog with everything filled in,
// and a person presses send, which is the same approval step that exists
// everywhere else here.
//
// The https: link is shown, not followed. Clicking it is a decision.

// Unsubscribe is what the header offers.
type Unsubscribe struct {
	// Mail is the address to write to, already stripped of <> and mailto:.
	Mail string `json:"mail,omitempty"`
	// Subject is what the sender wants in the subject line, if the mailto:
	// carried one. Some lists only accept "unsubscribe".
	Subject string `json:"subject,omitempty"`
	// Link is an https: URL to open. Never called by s3mail itself.
	Link string `json:"link,omitempty"`
	// OneClick says the sender offers RFC 8058. Recorded so the interface can
	// stay honest about what it is not doing, not because it is used.
	OneClick bool `json:"one_click,omitempty"`
}

func (u Unsubscribe) Any() bool { return u.Mail != "" || u.Link != "" }

func unsubscribe(h message.Header) Unsubscribe {
	raw := h.Get("List-Unsubscribe")
	if raw == "" {
		return Unsubscribe{}
	}
	out := Unsubscribe{
		OneClick: strings.Contains(strings.ToLower(h.Get("List-Unsubscribe-Post")), "one-click"),
	}
	// The header is a comma-separated list of <...> entries. Commas also occur
	// inside a mailto: query, so split on the brackets rather than on commas.
	for _, part := range strings.Split(raw, "<") {
		entry, ok := strings.CutSuffix(strings.TrimSpace(part), ">")
		if !ok {
			if i := strings.Index(part, ">"); i >= 0 {
				entry = strings.TrimSpace(part[:i])
			} else {
				continue
			}
		}
		low := strings.ToLower(entry)
		switch {
		case strings.HasPrefix(low, "mailto:") && out.Mail == "":
			addr, query, _ := strings.Cut(entry[len("mailto:"):], "?")
			out.Mail = strings.TrimSpace(addr)
			for _, kv := range strings.Split(query, "&") {
				if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, "subject") {
					out.Subject = strings.TrimSpace(v)
				}
			}
		case strings.HasPrefix(low, "https://") && out.Link == "":
			out.Link = entry
		}
	}
	return out
}
