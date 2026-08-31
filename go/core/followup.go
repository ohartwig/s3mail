// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"cmp"
	"slices"
	"time"
)

// Waiting answers the question a business mailbox asks every Monday: what did I
// write that nobody answered?
//
// It exists because the sent copy exists. Before there was a Sent folder there
// was nothing to compare against - the reply was in the mailbox and what it
// replied to was gone.
//
// Answered means: something came back from one of the recipients after the
// message went out. Deliberately by address and not by thread: an answer often
// arrives as a fresh message, from a colleague of the person written to, or
// with the quoted thread stripped by a helpdesk system. Threading would be
// exact about the wrong question.
type Waiting struct {
	Key     string   `json:"key"`
	Subject string   `json:"subject"`
	To      string   `json:"to"`
	Date    string   `json:"date"`
	Days    int      `json:"days"`
	Addrs   []string `json:"addrs"`
}

// Unanswered walks the sent mail and returns what has been waiting for at least
// `after`.
//
// now is passed in rather than read: a function that asks the clock cannot be
// checked, and this one decides what somebody sees on a Monday morning.
func Unanswered(msgs []Message, now time.Time, after time.Duration) []Waiting {
	// When each address last wrote to us. One pass, so a big mailbox does not
	// turn this into a loop inside a loop.
	lastIn := map[string]time.Time{}
	for _, m := range msgs {
		if m.Folder == Sent || m.Folder == Drafts || m.FromAddr == "" {
			continue
		}
		when, err := time.Parse(time.RFC3339, m.Date)
		if err != nil {
			continue
		}
		if when.After(lastIn[m.FromAddr]) {
			lastIn[m.FromAddr] = when
		}
	}

	var out []Waiting
	for _, m := range msgs {
		if m.Folder != Sent || len(m.ToAddrs) == 0 {
			continue
		}
		sent, err := time.Parse(time.RFC3339, m.Date)
		if err != nil {
			continue
		}
		waited := now.Sub(sent)
		if waited < after {
			continue
		}
		answered := false
		for _, addr := range m.ToAddrs {
			if last, ok := lastIn[addr]; ok && last.After(sent) {
				answered = true
				break
			}
		}
		if answered {
			continue
		}
		out = append(out, Waiting{Key: m.Key, Subject: m.Subject, To: m.To,
			Date: m.Date, Days: int(waited.Hours() / 24), Addrs: m.ToAddrs})
	}

	// The longest wait first - that is the one worth a second message.
	slices.SortFunc(out, func(a, b Waiting) int {
		return cmp.Or(
			cmp.Compare(b.Days, a.Days),
			cmp.Compare(a.Key, b.Key),
		)
	})
	return out
}

// Conversation is every message to and from one address, newest first - what
// somebody wants in front of them before they pick up the phone.
func Conversation(msgs []Message, address string) []Message {
	if address == "" {
		return nil
	}
	var out []Message
	for _, m := range msgs {
		if m.Folder == Drafts {
			continue
		}
		if m.FromAddr == address || slices.Contains(m.ToAddrs, address) {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b Message) int { return cmp.Compare(b.Date, a.Date) })
	return out
}
