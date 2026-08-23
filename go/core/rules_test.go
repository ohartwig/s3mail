package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestRegeltrefferGegenPython: dieselbe Regel, dieselben Mails, dieselben Treffer.
func TestRegeltrefferGegenPython(t *testing.T) {
	index, _ := laden(t)
	blob, err := os.ReadFile(filepath.Join("testdata", "rulehits.json"))
	if err != nil {
		t.Fatal(err)
	}
	var faelle []struct {
		Regel struct {
			Contains string `json:"contains"`
			Field    string `json:"field"`
		} `json:"regel"`
		Trifft []string `json:"trifft"`
	}
	if err := json.Unmarshal(blob, &faelle); err != nil {
		t.Fatal(err)
	}
	for _, f := range faelle {
		r := Rule{Contains: f.Regel.Contains, Field: f.Regel.Field}
		got := []string{}
		for _, m := range index {
			if r.Hits(m) {
				got = append(got, m.Mid)
			}
		}
		want := f.Trifft
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
		t.Fatalf("erste Regel hat nicht gewonnen: %+v", plan)
	}
	if !reflect.DeepEqual(plan[0].AddTags, []string{"A"}) {
		t.Errorf("Tags der zweiten Regel angewandt: %v", plan[0].AddTags)
	}
}

func TestRulesRunOnlyOnce(t *testing.T) {
	d := NewData()
	archiv := Archive
	d.Rules = []Rule{{Contains: "shop", Field: "any", Folder: &archiv, Enabled: true}}
	m := mail("m1", Inbox, "news@shop.io", "Angebot")

	plan := PlanRules([]Message{m}, d, false)
	if len(plan) != 1 || !plan[0].MarkRuled {
		t.Fatal("erster Lauf hat nicht gegriffen")
	}
	Apply(d, Op{T: "ruled", Mids: []string{"m1"}})

	// zurueckgeschoben: die Automatik fasst sie nicht wieder an
	if plan := PlanRules([]Message{m}, d, false); len(plan) != 0 {
		t.Errorf("bereits einsortierte Mail wurde erneut angefasst: %+v", plan)
	}
	// ausser man will es ausdruecklich
	if plan := PlanRules([]Message{m}, d, true); len(plan) != 1 {
		t.Error("force hat die Ruled-Marke nicht ueberstimmt")
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
			t.Errorf("%s wurde angefasst: %+v", folder, plan[0])
		}
		if !plan[0].MarkRuled {
			t.Errorf("%s: Ruled-Marke fehlt, die Mail wird bei jedem Lauf neu geprueft", folder)
		}
	}
}

func TestRulesSwitchedOff(t *testing.T) {
	d := NewData()
	archiv := Archive
	d.Rules = []Rule{{Contains: "shop", Field: "any", Folder: &archiv, Enabled: false}}
	if plan := PlanRules([]Message{mail("m1", Inbox, "news@shop.io", "x")}, d, false); len(plan) != 0 {
		t.Errorf("ausgeschaltete Regel hat gegriffen: %+v", plan)
	}
}

func TestCleanRules(t *testing.T) {
	strich, boese, archiv := "-", "../boese", "archiv"
	rein := []Rule{
		{Contains: "  a  ", Field: "FROM", Folder: &archiv, Enabled: true},
		{Contains: "", Field: "any"},                       // fliegt raus
		{Contains: "b", Field: "quatsch", Folder: &strich}, // Feld -> any, kein Ordner
		{Contains: "c", Field: "to", Tags: []string{"1", "", "2", "3", "4", "5", "6"}},
	}
	got, err := CleanRules(rein)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d Regeln, erwartet 3: %+v", len(got), got)
	}
	if got[0].Contains != "a" || got[0].Name != "a" || got[0].Field != "from" {
		t.Errorf("erste Regel: %+v", got[0])
	}
	if got[1].Field != "any" || got[1].Folder != nil {
		t.Errorf("unbekanntes Feld/Strich nicht normalisiert: %+v", got[1])
	}
	if len(got[2].Tags) != 5 {
		t.Errorf("Tags nicht auf 5 begrenzt: %v", got[2].Tags)
	}
	if _, err := CleanRules([]Rule{{Contains: "x", Field: "any", Folder: &boese}}); err == nil {
		t.Error("ungueltiger Ordner in einer Regel wurde durchgelassen")
	}
}
