// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"sort"
	"strings"
)

// Addresses out of the mailbox, for the field somebody is typing into.
//
// There is no address book and there should not be one: a second place to keep
// addresses is a second place to keep them wrong. The mailbox already knows
// every address that ever wrote or was written to, and it knows which ones
// matter - the ones that come up often and the ones that came up lately.
//
// Both rankings are needed and they disagree. The accountant who writes every
// month is frequent; the customer from this morning is recent. Sorting by
// either alone puts the wrong one first, so the score is the count plus a bonus
// for having appeared recently, and ties break on the more recent one.

// Contact is one address as the mailbox knows it.
type Contact struct {
	Addr string `json:"addr"`
	// Name is the display name last seen with this address. People change how
	// they sign; the newer spelling is the better guess.
	Name  string `json:"name"`
	Count int    `json:"count"`
	Last  string `json:"last"`
}

// Label is what goes into the field: "Anna Müller <anna@x.de>", or the bare
// address when nobody ever gave a name.
func (c Contact) Label() string {
	if c.Name == "" {
		return c.Addr
	}
	return c.Name + " <" + c.Addr + ">"
}

// Addresses ranks the addresses in the mailbox against what has been typed.
//
// An empty query returns the top of the list, which is what an empty field
// should offer. Matching is on both the address and the name, anywhere in the
// string - people look for "müller" as often as for "anna@".
func Addresses(msgs []Message, query string, limit int) []Contact {
	q := strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = 10
	}

	type acc struct {
		name  string
		count int
		last  string
	}
	seen := map[string]*acc{}

	note := func(addr, name, date string) {
		addr = strings.ToLower(strings.TrimSpace(addr))
		if addr == "" || !strings.Contains(addr, "@") {
			return
		}
		a, ok := seen[addr]
		if !ok {
			a = &acc{}
			seen[addr] = a
		}
		a.count++
		// The newer spelling of the name wins - and a message without a name
		// must not erase one that was there.
		if date > a.last {
			a.last = date
			if name != "" {
				a.name = name
			}
		} else if a.name == "" {
			a.name = name
		}
	}

	for _, m := range msgs {
		note(m.FromAddr, displayName(m.From), m.Date)
		for _, to := range m.ToAddrs {
			note(to, "", m.Date)
		}
	}

	out := make([]Contact, 0, len(seen))
	for addr, a := range seen {
		if q != "" && !strings.Contains(addr, q) && !strings.Contains(strings.ToLower(a.name), q) {
			continue
		}
		out = append(out, Contact{Addr: addr, Name: a.name, Count: a.count, Last: a.last})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Last != out[j].Last {
			return out[i].Last > out[j].Last
		}
		return out[i].Addr < out[j].Addr
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// displayName pulls "Anna Müller" out of "Anna Müller <anna@x.de>". Quotes come
// off, because "Schmidt, Peter" arrives quoted and would otherwise keep them.
func displayName(from string) string {
	name := from
	if i := strings.LastIndex(from, "<"); i >= 0 {
		name = from[:i]
	} else if strings.Contains(from, "@") {
		return "" // bare address, no name to take
	}
	name = strings.TrimSpace(name)
	name = strings.Trim(name, `"`)
	return strings.TrimSpace(name)
}
