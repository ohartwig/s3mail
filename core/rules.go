// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package core

import "strings"

// Rule files incoming mail automatically: "contains X in field Y -> folder and
// tags". Folder is a pointer because "do not move" is a different thing from
// "move to the inbox".
type Rule struct {
	Name     string   `json:"name"`
	Field    string   `json:"field"`
	Contains string   `json:"contains"`
	Folder   *string  `json:"folder"`
	Tags     []string `json:"tags"`
	Star     bool     `json:"star"`
	Read     bool     `json:"read"`
	Enabled  bool     `json:"enabled"`
}

var ruleFields = map[string]bool{"from": true, "to": true, "subject": true, "any": true}

// CleanRules validates and normalises what comes from the interface. Rules
// without a search term are dropped, unknown fields become "any".
func CleanRules(rules []Rule) ([]Rule, error) {
	out := make([]Rule, 0, len(rules))
	for _, r := range rules {
		field := strings.ToLower(strings.TrimSpace(r.Field))
		if !ruleFields[field] {
			field = "any"
		}
		contains := strings.TrimSpace(r.Contains)
		if contains == "" {
			continue
		}
		name := strings.TrimSpace(r.Name)
		if name == "" {
			name = contains
		}
		if rn := []rune(name); len(rn) > 60 {
			name = string(rn[:60])
		}
		var folder *string
		if r.Folder != nil && *r.Folder != "-" {
			f, err := ValidFolder(*r.Folder)
			if err != nil {
				return nil, err
			}
			folder = &f
		}
		tags := make([]string, 0, 5)
		for _, t := range r.Tags {
			if t = strings.TrimSpace(t); t != "" && len(tags) < 5 {
				tags = append(tags, t)
			}
		}
		out = append(out, Rule{name, field, contains, folder, tags, r.Star, r.Read, r.Enabled})
	}
	return out, nil
}

// Hits says whether a rule matches a message.
func (r Rule) Hits(m Message) bool {
	needle := strings.ToLower(r.Contains)
	var hay string
	switch r.Field {
	case "from":
		hay = m.From
	case "to":
		hay = m.To + " " + m.Cc
	case "subject":
		hay = m.Subject
	default:
		hay = strings.Join([]string{m.From, m.To, m.Cc, m.Subject, m.Snippet}, " ")
	}
	return strings.Contains(strings.ToLower(hay), needle)
}

// RuleAction is what would have to be done for a message. Carrying it out -
// moving in S3 - is the layer above; here the decision stays testable on its own.
type RuleAction struct {
	Mid       string
	Key       string
	MarkRuled bool
	AddTags   []string
	SetRead   *bool
	SetStar   *bool
	MoveTo    *string
}

// skipsRules names the folders the automation leaves alone.
func skipsRules(folder string) bool {
	return folder == Trash || folder == Spam || folder == Sent || folder == Drafts
}

// PlanRules walks the rules and returns what to do.
//
// The first matching rule wins. A message is filed automatically only once (the
// Ruled mark) - if somebody moves it back, it stays where they put it. Trash and
// spam are left alone. force overrides both; that is "apply to all existing
// mail".
func PlanRules(pool []Message, d *Data, force bool) []RuleAction {
	active := make([]Rule, 0, len(d.Rules))
	for _, r := range d.Rules {
		if r.Enabled {
			active = append(active, r)
		}
	}
	if len(active) == 0 {
		return nil
	}
	out := make([]RuleAction, 0, len(pool))
	for _, m := range pool {
		if d.Get(m.Mid).Ruled && !force {
			continue
		}
		// Trash and spam are somebody's decision, sent mail and drafts are our
		// own writing - the automation has no business in any of the four.
		if skipsRules(m.Folder) && !force {
			out = append(out, RuleAction{Mid: m.Mid, Key: m.Key, MarkRuled: true})
			continue
		}
		action := RuleAction{Mid: m.Mid, Key: m.Key, MarkRuled: true}
		for _, r := range active {
			if !r.Hits(m) {
				continue
			}
			if len(r.Tags) > 0 {
				action.AddTags = r.Tags
			}
			if r.Read {
				action.SetRead = Ptr(true)
			}
			if r.Star {
				action.SetStar = Ptr(true)
			}
			if r.Folder != nil && *r.Folder != m.Folder {
				target := *r.Folder
				action.MoveTo = &target
			}
			break // erste passende Regel gewinnt
		}
		out = append(out, action)
	}
	return out
}
