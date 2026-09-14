// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestRuleHitsAgainstPython: the same rule, the same mail, the same hits.
func TestRuleHitsAgainstPython(t *testing.T) {
	index, _ := load(t)
	blob, err := os.ReadFile(filepath.Join("testdata", "rulehits.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Rule struct {
			Contains string `json:"contains"`
			Field    string `json:"field"`
		} `json:"regel"`
		Hits []string `json:"trifft"`
	}
	if err := json.Unmarshal(blob, &cases); err != nil {
		t.Fatal(err)
	}
	for _, f := range cases {
		r := Rule{Contains: f.Rule.Contains, Field: f.Rule.Field}
		got := []string{}
		for _, m := range index {
			if r.Hits(m) {
				got = append(got, m.Mid)
			}
		}
		want := f.Hits
		if len(want) == 0 {
			want = []string{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:%q\n  python: %v\n  go:     %v", r.Field, r.Contains, want, got)
		}
	}
}

func mail(mid, folder, from, subject string) Message {
	return Message{Mid: mid, Key: "mail/" + mid, Folder: folder, From: from, Subject: subject}
}

func TestRulesFirstMatchWins(t *testing.T) {
	d := NewData()
	archiv, spam := Archive, Spam
	d.Rules = []Rule{
		{Contains: "shop", Field: "any", Folder: &archiv, Tags: []string{"A"}, Enabled: true},
		{Contains: "shop", Field: "any", Folder: &spam, Tags: []string{"B"}, Enabled: true},
	}
	plan := PlanRules([]Message{mail("m1", Inbox, "news@shop.io", "Angebot")}, d, false)
	if len(plan) != 1 || plan[0].MoveTo == nil || *plan[0].MoveTo != Archive {
		t.Fatalf("the first rule did not win: %+v", plan)
	}
	if !reflect.DeepEqual(plan[0].AddTags, []string{"A"}) {
		t.Errorf("tags of the second rule applied: %v", plan[0].AddTags)
	}
}

func TestRulesRunOnlyOnce(t *testing.T) {
	d := NewData()
	archiv := Archive
	d.Rules = []Rule{{Contains: "shop", Field: "any", Folder: &archiv, Enabled: true}}
	m := mail("m1", Inbox, "news@shop.io", "Angebot")

	plan := PlanRules([]Message{m}, d, false)
	if len(plan) != 1 || !plan[0].MarkRuled {
		t.Fatal("the first run did not take hold")
	}
	Apply(d, Op{T: "ruled", Mids: []string{"m1"}})

	// moved back: the automation does not touch it again
	if plan := PlanRules([]Message{m}, d, false); len(plan) != 0 {
		t.Errorf("an already filed message was touched again: %+v", plan)
	}
	// unless somebody explicitly wants it
	if plan := PlanRules([]Message{m}, d, true); len(plan) != 1 {
		t.Error("force did not override the Ruled mark")
	}
}

func TestRulesLeaveTrashAndSpamAlone(t *testing.T) {
	d := NewData()
	archiv := Archive
	d.Rules = []Rule{{Contains: "shop", Field: "any", Folder: &archiv, Tags: []string{"A"}, Enabled: true}}
	for _, folder := range []string{Trash, Spam} {
		plan := PlanRules([]Message{mail("m1", folder, "news@shop.io", "x")}, d, false)
		if len(plan) != 1 {
			t.Fatalf("%s: %+v", folder, plan)
		}
		if plan[0].MoveTo != nil || len(plan[0].AddTags) > 0 {
			t.Errorf("%s was touched: %+v", folder, plan[0])
		}
		if !plan[0].MarkRuled {
			t.Errorf("%s: Ruled mark missing, the message is checked again on every run", folder)
		}
	}
}

func TestRulesSwitchedOff(t *testing.T) {
	d := NewData()
	archiv := Archive
	d.Rules = []Rule{{Contains: "shop", Field: "any", Folder: &archiv, Enabled: false}}
	if plan := PlanRules([]Message{mail("m1", Inbox, "news@shop.io", "x")}, d, false); len(plan) != 0 {
		t.Errorf("a switched-off rule took hold: %+v", plan)
	}
}

func TestCleanRules(t *testing.T) {
	dash, hostile, archiv := "-", "../boese", "archiv"
	in := []Rule{
		{Contains: "  a  ", Field: "FROM", Folder: &archiv, Enabled: true},
		{Contains: "", Field: "any"},                     // fliegt raus
		{Contains: "b", Field: "quatsch", Folder: &dash}, // field -> any, no folder
		{Contains: "c", Field: "to", Tags: []string{"1", "", "2", "3", "4", "5", "6"}},
	}
	got, err := CleanRules(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d rules, expected 3: %+v", len(got), got)
	}
	if got[0].Contains != "a" || got[0].Name != "a" || got[0].Field != "from" {
		t.Errorf("first rule: %+v", got[0])
	}
	if got[1].Field != "any" || got[1].Folder != nil {
		t.Errorf("unknown field/dash not normalised: %+v", got[1])
	}
	if len(got[2].Tags) != 5 {
		t.Errorf("tags not capped at 5: %v", got[2].Tags)
	}
	if _, err := CleanRules([]Rule{{Contains: "x", Field: "any", Folder: &hostile}}); err == nil {
		t.Error("an invalid folder in a rule was let through")
	}
}
