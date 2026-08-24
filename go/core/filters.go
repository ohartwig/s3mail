// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package core

import "strings"

// Saved searches, in the bucket rather than in the browser.
//
// The search line already does the work: `from:kunde@x.de has:anhang
// after:2026-01-01` is a filter. What it cannot do is survive being typed once,
// and a filter somebody uses every Monday is one they should not have to
// remember the syntax of every Monday.
//
// They live in the state next to tags and rules, for the same reason: a second
// machine has to see them, and a browser's local storage is where work quietly
// disappears when somebody clears their cache.

// Filter is a saved search.
type Filter struct {
	Name  string `json:"name"`
	Query string `json:"query"`
}

// Limits, so that a filter list stays a filter list. Not defences against an
// attacker - whoever can write here owns the mailbox anyway - but against the
// state document growing without anybody noticing.
const (
	MaxFilters     = 30
	MaxFilterName  = 40
	MaxFilterQuery = 200
)

// CleanFilters is what the route runs before writing. Empty entries fall out
// rather than being rejected: the interface writes the whole list back, and a
// half-filled row somebody abandoned should not block saving the rest.
func CleanFilters(in []Filter) []Filter {
	out := make([]Filter, 0, len(in))
	seen := map[string]bool{}
	for _, f := range in {
		f.Name = strings.TrimSpace(f.Name)
		f.Query = strings.TrimSpace(f.Query)
		if f.Name == "" || f.Query == "" {
			continue
		}
		if len(f.Name) > MaxFilterName {
			f.Name = strings.TrimSpace(f.Name[:MaxFilterName])
		}
		if len(f.Query) > MaxFilterQuery {
			f.Query = strings.TrimSpace(f.Query[:MaxFilterQuery])
		}
		key := strings.ToLower(f.Name)
		if seen[key] {
			// The first one under a name wins. Two entries with the same name
			// are a mistake, not a choice - the sidebar would show them twice
			// and nobody could tell which is which.
			continue
		}
		seen[key] = true
		out = append(out, f)
		if len(out) >= MaxFilters {
			break
		}
	}
	return out
}
