package core

import (
	"sort"
	"strings"
)

// Suggestions come out of what somebody already does by hand.
//
// The useful signal is not "many messages from this sender" - that is a guess
// about what they would want. It is "eleven of twelve messages from this sender
// were moved into the same folder": a rule they are already following, one
// message at a time. Proposing to automate that rests on evidence rather than
// on taste, and it can be shown to them in one sentence they can check.
//
// This works on sender, folder and tags - never on the text of a message. No
// key, no network, no model: a frequency count over the index that is already
// on the disk.
type Suggestion struct {
	Field    string   `json:"field"`
	Contains string   `json:"contains"`
	Folder   string   `json:"folder"`
	Tags     []string `json:"tags"`

	// Count is how many messages the evidence rests on, Agree how many of them
	// already sit where the rule would put them. Both go to the interface: a
	// suggestion nobody can check is one nobody should accept.
	Count int `json:"count"`
	Agree int `json:"agree"`
}

const (
	// Below this many messages a pattern is a coincidence. Four is where the
	// same decision three times in a row stops looking like chance.
	minEvidence = 4

	// And it has to be nearly all of them. At two thirds somebody is sorting by
	// something the sender does not determine, and the rule would be wrong for
	// every third message.
	minAgreement = 0.8
)

// Suggest reads the index and proposes rules.
//
// What is already covered by a rule is left out, and so is the trash: a rule
// that throws mail away on a guess is the one mistake here that costs
// something. Sent mail and drafts are our own writing and say nothing about
// where somebody files what they receive.
func Suggest(msgs []Message, existing []Rule) []Suggestion {
	type evidence struct {
		total   int
		folders map[string]int
		tags    map[string]int
	}
	bySender := map[string]*evidence{}

	for _, m := range msgs {
		if m.Folder == Sent || m.Folder == Drafts {
			continue
		}
		sender := strings.ToLower(strings.TrimSpace(m.FromAddr))
		if sender == "" {
			continue
		}
		e := bySender[sender]
		if e == nil {
			e = &evidence{folders: map[string]int{}, tags: map[string]int{}}
			bySender[sender] = e
		}
		e.total++
		e.folders[m.Folder]++
		for _, t := range m.Tags {
			e.tags[t]++
		}
	}

	var out []Suggestion
	for sender, e := range bySender {
		if e.total < minEvidence || covered(existing, sender) {
			continue
		}
		folder, agree := strongest(e.folders)
		// The inbox is where mail lands by itself. That most of it is still
		// there says nothing about anybody's intention.
		if folder == Inbox || folder == Trash {
			continue
		}
		if float64(agree)/float64(e.total) < minAgreement {
			continue
		}
		s := Suggestion{Field: "from", Contains: sender, Folder: folder,
			Count: e.total, Agree: agree, Tags: []string{}}
		// A tag that almost all of them carry belongs in the rule as well -
		// somebody has been putting it there by hand every time.
		for tag, n := range e.tags {
			if float64(n)/float64(e.total) >= minAgreement {
				s.Tags = append(s.Tags, tag)
			}
		}
		sort.Strings(s.Tags)
		out = append(out, s)
	}

	// The best evidence first: how many messages agree, then how many there
	// were. Stable by sender so the list does not reshuffle between two calls.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agree != out[j].Agree {
			return out[i].Agree > out[j].Agree
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Contains < out[j].Contains
	})
	return out
}

// covered says whether a rule already catches this sender. Suggesting what
// exists makes the list noise, and noise is what gets a feature ignored.
func covered(rules []Rule, sender string) bool {
	for _, r := range rules {
		if r.Field != "from" && r.Field != "any" {
			continue
		}
		if c := strings.ToLower(strings.TrimSpace(r.Contains)); c != "" &&
			strings.Contains(sender, c) {
			return true
		}
	}
	return false
}

func strongest(counts map[string]int) (string, int) {
	best, n := "", 0
	for folder, c := range counts {
		// Ties go to the name that sorts first, so two calls agree.
		if c > n || (c == n && folder < best) {
			best, n = folder, c
		}
	}
	return best, n
}

// AsRule turns a suggestion into the rule it proposes.
func (s Suggestion) AsRule() Rule {
	folder := s.Folder
	return Rule{Name: s.Contains, Field: "from", Contains: s.Contains,
		Folder: &folder, Tags: s.Tags, Enabled: true}
}
