package core

import "strings"

// Rule sortiert eingehende Mail automatisch ein: "enthaelt X im Feld Y -> Ordner
// und Tags". Folder ist ein Zeiger, weil "nicht verschieben" etwas anderes ist
// als "in den Posteingang verschieben".
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

var rulefelder = map[string]bool{"from": true, "to": true, "subject": true, "any": true}

// CleanRules validiert und normalisiert, was aus der Oberflaeche kommt. Regeln
// ohne Suchbegriff fliegen raus, unbekannte Felder werden zu "any".
func CleanRules(rules []Rule) ([]Rule, error) {
	out := make([]Rule, 0, len(rules))
	for _, r := range rules {
		feld := strings.ToLower(strings.TrimSpace(r.Field))
		if !rulefelder[feld] {
			feld = "any"
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
		out = append(out, Rule{name, feld, contains, folder, tags, r.Star, r.Read, r.Enabled})
	}
	return out, nil
}

// Hits sagt, ob eine Regel auf eine Mail passt.
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

// RuleAction ist, was fuer eine Mail zu tun waere. Das Ausfuehren - Verschieben in
// S3 - macht die Schicht darueber; hier bleibt die Entscheidung fuer sich testbar.
type RuleAction struct {
	Mid       string
	Key       string
	MarkRuled bool
	AddTags   []string
	SetRead   *bool
	SetStar   *bool
	MoveTo    *string
}

// PlanRules laeuft die Regeln durch und liefert, was zu tun ist.
//
// Erste passende Regel gewinnt. Eine Mail wird nur einmal automatisch einsortiert
// (Ruled-Marke) - schiebt jemand sie zurueck, bleibt sie liegen. Papierkorb und
// Spam fasst die Automatik nicht an. force laesst beides ausser Kraft, das ist
// "auf alle bestehenden Mails anwenden".
func PlanRules(pool []Message, d *Data, force bool) []RuleAction {
	aktiv := make([]Rule, 0, len(d.Rules))
	for _, r := range d.Rules {
		if r.Enabled {
			aktiv = append(aktiv, r)
		}
	}
	if len(aktiv) == 0 {
		return nil
	}
	out := make([]RuleAction, 0, len(pool))
	for _, m := range pool {
		if d.Get(m.Mid).Ruled && !force {
			continue
		}
		if (m.Folder == Trash || m.Folder == Spam) && !force {
			out = append(out, RuleAction{Mid: m.Mid, Key: m.Key, MarkRuled: true})
			continue
		}
		aktion := RuleAction{Mid: m.Mid, Key: m.Key, MarkRuled: true}
		for _, r := range aktiv {
			if !r.Hits(m) {
				continue
			}
			if len(r.Tags) > 0 {
				aktion.AddTags = r.Tags
			}
			if r.Read {
				aktion.SetRead = Ptr(true)
			}
			if r.Star {
				aktion.SetStar = Ptr(true)
			}
			if r.Folder != nil && *r.Folder != m.Folder {
				target := *r.Folder
				aktion.MoveTo = &target
			}
			break // erste passende Regel gewinnt
		}
		out = append(out, aktion)
	}
	return out
}
