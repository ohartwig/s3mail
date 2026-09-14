// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"testing"

	"github.com/ohartwig/s3mail/core"
)

func folder(name string) *string { return &name }

func stateWith(rules []core.Rule, tags map[string]string) *core.Data {
	d := core.NewData()
	d.Rules = rules
	for k, v := range tags {
		d.Tags[k] = v
	}
	return d
}

// The decision this file rests on: a file somebody hands over is an offer, not
// a command. Nothing that is already there may be lost by importing.
func TestImportNeverReplaces(t *testing.T) {
	d := stateWith(
		[]core.Rule{{Name: "Meine", Field: "from", Contains: "chef@", Folder: folder("wichtig")}},
		map[string]string{"wichtig": "#ff0000"})

	rules, tags, rep := core.Merge(d, core.Portable{
		Rules: []core.Rule{{Name: "Fremde", Field: "from", Contains: "news@", Folder: folder("werbung")}},
		Tags:  map[string]string{"werbung": "#00ff00"},
	})

	if len(rules) != 2 {
		t.Fatalf("%d rules after the import, expected 2", len(rules))
	}
	if rules[0].Contains != "chef@" {
		t.Error("the existing rule is gone or no longer first")
	}
	if rep.RulesAdded != 1 || rep.RulesSkipped != 0 {
		t.Errorf("report says added=%d skipped=%d", rep.RulesAdded, rep.RulesSkipped)
	}
	if len(tags) != 1 || tags[0] != "werbung" {
		t.Errorf("tags to add: %v", tags)
	}
}

// Two rules are the same rule when they do the same thing. Everybody calls
// theirs "Newsletter", and nobody wants two of them.
func TestTheSameRuleUnderAnotherNameIsSkipped(t *testing.T) {
	d := stateWith([]core.Rule{
		{Name: "Newsletter", Field: "from", Contains: "news@shop.io",
			Folder: folder("werbung"), Tags: []string{"werbung"}}}, nil)

	rules, _, rep := core.Merge(d, core.Portable{Rules: []core.Rule{
		{Name: "Werbung von Kollegin", Field: "from", Contains: "news@shop.io",
			Folder: folder("werbung"), Tags: []string{"werbung"}}}})

	if len(rules) != 1 {
		t.Errorf("%d rules - the duplicate came in anyway", len(rules))
	}
	if rep.RulesSkipped != 1 {
		t.Errorf("skipped=%d", rep.RulesSkipped)
	}
}

// A rule that differs in what it does is a different rule, even under the same
// name.
func TestTheSameNameWithAnotherEffectComesIn(t *testing.T) {
	d := stateWith([]core.Rule{
		{Name: "Newsletter", Field: "from", Contains: "news@shop.io", Folder: folder("werbung")}}, nil)

	rules, _, _ := core.Merge(d, core.Portable{Rules: []core.Rule{
		{Name: "Newsletter", Field: "from", Contains: "news@andere.io", Folder: folder("werbung")}}})

	if len(rules) != 2 {
		t.Errorf("%d rules - the second one was swallowed", len(rules))
	}
}

// The colour in use wins. An import must not repaint a tag that is already on
// three hundred messages.
func TestAnExistingColourStays(t *testing.T) {
	d := stateWith(nil, map[string]string{"wichtig": "#ff0000"})
	_, tags, rep := core.Merge(d, core.Portable{Tags: map[string]string{"wichtig": "#0000ff"}})
	if len(tags) != 0 {
		t.Errorf("the tag was offered for adding again: %v", tags)
	}
	if len(rep.TagsAdded) != 0 {
		t.Errorf("report says %v", rep.TagsAdded)
	}
	if d.Tags["wichtig"] != "#ff0000" {
		t.Errorf("the colour was overwritten: %s", d.Tags["wichtig"])
	}
}

// Two exports of the same state have to be the same file, or version control
// shows changes that are not there.
func TestExportIsStable(t *testing.T) {
	d := stateWith([]core.Rule{{Name: "A"}, {Name: "B"}},
		map[string]string{"x": "#111111", "y": "#222222"})
	a, b := core.Export(d), core.Export(d)
	if len(a.Rules) != len(b.Rules) || len(a.Tags) != len(b.Tags) {
		t.Fatal("two exports differ in size")
	}
	for i := range a.Rules {
		if a.Rules[i].Name != b.Rules[i].Name {
			t.Errorf("rule order differs at %d", i)
		}
	}
}

// The export must not hand out a slice the state still uses - the caller could
// change the mailbox by editing what it exported.
func TestExportHandsOutACopy(t *testing.T) {
	d := stateWith([]core.Rule{{Name: "A"}}, map[string]string{"x": "#111111"})
	out := core.Export(d)
	out.Rules[0].Name = "verstellt"
	out.Tags["x"] = "#999999"
	if d.Rules[0].Name != "A" {
		t.Error("changing the export changed the state")
	}
	if d.Tags["x"] != "#111111" {
		t.Error("changing the export changed the colours")
	}
}
