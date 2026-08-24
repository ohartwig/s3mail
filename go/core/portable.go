// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package core

import "sort"

// Rules and tags as a file, so that a second mailbox does not start empty.
//
// The interesting half is the import, and the decision in it is: merge, never
// replace. A file somebody hands over is an offer, not a command. Replacing
// would destroy work in one click that took months to accumulate, and the
// button that does that has to be pressed on purpose, not by accident - so it
// does not exist here at all.
//
// Which makes duplicates the thing to solve. Two rules are the same rule when
// they do the same thing, not when they carry the same name: everybody calls
// their newsletter rule "Newsletter", and nobody wants two of them.

// Portable is what a file carries.
type Portable struct {
	Rules []Rule            `json:"rules"`
	Tags  map[string]string `json:"tags"`
}

// Export takes what the state holds. Sorted, so two exports of the same state
// are the same file - otherwise a version control system sees changes that are
// not there.
func Export(d *Data) Portable {
	d.Normalize()
	out := Portable{Rules: append([]Rule{}, d.Rules...), Tags: map[string]string{}}
	for name, color := range d.Tags {
		out.Tags[name] = color
	}
	return out
}

// ImportReport says what an import did, so the answer can be specific rather
// than "imported".
type ImportReport struct {
	RulesAdded   int      `json:"rules_added"`
	RulesSkipped int      `json:"rules_skipped"`
	TagsAdded    []string `json:"tags_added"`
}

// Merge folds a file into the state and reports what happened. Pure: it returns
// the new rule list and the tags to add, it writes nothing.
func Merge(d *Data, in Portable) (rules []Rule, tags []string, rep ImportReport) {
	d.Normalize()
	rules = append([]Rule{}, d.Rules...)

	have := map[string]bool{}
	for _, r := range rules {
		have[ruleKey(r)] = true
	}
	for _, r := range in.Rules {
		if have[ruleKey(r)] {
			rep.RulesSkipped++
			continue
		}
		have[ruleKey(r)] = true
		rules = append(rules, r)
		rep.RulesAdded++
	}

	for name := range in.Tags {
		if name == "" {
			continue
		}
		if _, present := d.Tags[name]; present {
			continue // the colour that is already here stays - it is the one in use
		}
		tags = append(tags, name)
	}
	sort.Strings(tags)
	rep.TagsAdded = tags
	return rules, tags, rep
}

// ruleKey is what makes two rules the same rule: what they look at, what they
// look for, and what they do. Not the name - everybody calls it "Newsletter".
func ruleKey(r Rule) string {
	folder := ""
	if r.Folder != nil {
		folder = *r.Folder
	}
	tags := append([]string{}, r.Tags...)
	sort.Strings(tags)
	key := r.Field + "\x00" + r.Contains + "\x00" + folder
	for _, t := range tags {
		key += "\x00" + t
	}
	if r.Star {
		key += "\x00star"
	}
	if r.Read {
		key += "\x00read"
	}
	return key
}
