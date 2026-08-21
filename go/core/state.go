// Package core traegt den Zustand von s3mail: Tags, gelesen/ungelesen, Stern und
// Regeln - dazu Ordnerlogik, Regel-Engine und Suche. Nichts hier spricht mit S3
// oder mit HTTP; das haelt die Schicht testbar und ist der Grund, warum sie sich
// eins zu eins gegen die Python-Fassung pruefen laesst.
package core

import "sort"

// TagColors wird der Reihe nach vergeben, wenn ein neuer Tag auftaucht.
var TagColors = []string{
	"#4c8dff", "#38b48b", "#e0a33e", "#d9534f",
	"#a06ee1", "#3ea8c4", "#e07a5f", "#7f9c3a",
}

// Entry ist der Zustand einer einzelnen Mail. Schluessel im Zustand ist der
// Basename des S3-Objekts, nicht der volle Key - deshalb ueberlebt der Eintrag
// das Verschieben zwischen Ordnern.
type Entry struct {
	Read  bool     `json:"read"`
	Star  bool     `json:"star"`
	Tags  []string `json:"tags"`
	Ruled bool     `json:"ruled"`
}

// Data ist das, was als Snapshot im Bucket liegt. Upto ist der Wasserstand: bis
// zu diesem Op-Namen steckt alles bereits im Snapshot.
type Data struct {
	Messages map[string]*Entry `json:"messages"`
	Tags     map[string]string `json:"tags"`
	Rules    []Rule            `json:"rules"`
	Upto     string            `json:"upto,omitempty"`
}

func NewData() *Data {
	return &Data{Messages: map[string]*Entry{}, Tags: map[string]string{}, Rules: []Rule{}}
}

// Normalize fuellt fehlende Felder auf - ein Snapshot aus einer aelteren Fassung
// oder eine von Hand editierte Datei kippt uns sonst.
func (d *Data) Normalize() *Data {
	if d.Messages == nil {
		d.Messages = map[string]*Entry{}
	}
	if d.Tags == nil {
		d.Tags = map[string]string{}
	}
	if d.Rules == nil {
		d.Rules = []Rule{}
	}
	return d
}

func (d *Data) Get(mid string) Entry {
	if e, ok := d.Messages[mid]; ok && e != nil {
		return *e
	}
	return Entry{Tags: []string{}}
}

func (d *Data) entry(mid string) *Entry {
	if e, ok := d.Messages[mid]; ok && e != nil {
		return e
	}
	e := &Entry{Tags: []string{}}
	d.Messages[mid] = e
	return e
}

// Op ist eine einzelne Zustandsaenderung. Genau diese Objekte landen einzeln im
// Bucket - nie ein Abzug des ganzen Dokuments. Read und Star sind Zeiger, weil
// "nicht anfassen" etwas anderes ist als "auf false setzen".
type Op struct {
	T      string   `json:"t"`
	Mids   []string `json:"mids,omitempty"`
	Read   *bool    `json:"read,omitempty"`
	Star   *bool    `json:"star,omitempty"`
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
	Old    string   `json:"old,omitempty"`
	New    string   `json:"new,omitempty"`
	Name   string   `json:"name,omitempty"`
	Color  string   `json:"color,omitempty"`
	Rules  []Rule   `json:"rules,omitempty"`
}

// Apply wendet eine Aenderung auf ein Zustands-Dokument an - rein, ohne I/O.
//
// Jede Operation muss idempotent bleiben: darauf beruht, dass ein Op-Objekt, das
// beim Aufraeumen liegengeblieben ist, nichts kaputtmacht.
func Apply(d *Data, op Op) {
	d.Normalize()
	switch op.T {
	case "flags":
		for _, mid := range op.Mids {
			e := d.entry(mid)
			if op.Read != nil {
				e.Read = *op.Read
			}
			if op.Star != nil {
				e.Star = *op.Star
			}
		}
	case "tags":
		for _, tag := range op.Add {
			if _, da := d.Tags[tag]; !da {
				d.Tags[tag] = TagColors[len(d.Tags)%len(TagColors)]
			}
		}
		for _, mid := range op.Mids {
			e := d.entry(mid)
			cur := make([]string, 0, len(e.Tags)+len(op.Add))
			for _, t := range e.Tags {
				if !enthaelt(op.Remove, t) {
					cur = append(cur, t)
				}
			}
			for _, t := range op.Add {
				if !enthaelt(cur, t) {
					cur = append(cur, t)
				}
			}
			e.Tags = cur
		}
	case "ruled":
		for _, mid := range op.Mids {
			d.entry(mid).Ruled = true
		}
	case "drop":
		for _, mid := range op.Mids {
			delete(d.Messages, mid)
		}
	case "rekey":
		if e, ok := d.Messages[op.Old]; ok && e != nil {
			kopie := *e
			kopie.Tags = append([]string{}, e.Tags...)
			d.Messages[op.New] = &kopie
		}
	case "tagdel":
		delete(d.Tags, op.Name)
		for _, e := range d.Messages {
			if enthaelt(e.Tags, op.Name) {
				rest := make([]string, 0, len(e.Tags))
				for _, t := range e.Tags {
					if t != op.Name {
						rest = append(rest, t)
					}
				}
				e.Tags = rest
			}
		}
	case "tagren":
		alt, neu, farbe := op.Old, op.New, op.Color
		if alt != neu {
			if vorhanden, ok := d.Tags[alt]; ok {
				delete(d.Tags, alt)
				if farbe != "" {
					d.Tags[neu] = farbe
				} else {
					d.Tags[neu] = vorhanden
				}
			} else {
				d.Tags[neu] = farbeOder(d, neu, farbe)
			}
			for _, e := range d.Messages {
				for i, t := range e.Tags {
					if t == alt {
						e.Tags[i] = neu
					}
				}
			}
		} else if _, ok := d.Tags[neu]; !ok || farbe != "" {
			d.Tags[neu] = farbeOder(d, neu, farbe)
		}
	case "rules":
		d.Rules = op.Rules
	}
}

func farbeOder(d *Data, tag, farbe string) string {
	if farbe != "" {
		return farbe
	}
	if vorhanden, ok := d.Tags[tag]; ok {
		return vorhanden
	}
	return TagColors[len(d.Tags)%len(TagColors)]
}

// MergeMissing uebernimmt Eintraege aus other, die base gar nicht kennt - so geht
// nichts verloren, was offline entstanden ist.
func MergeMissing(base, other *Data) *Data {
	if other == nil {
		return base
	}
	base.Normalize()
	for mid, e := range other.Messages {
		if _, da := base.Messages[mid]; !da {
			kopie := *e
			base.Messages[mid] = &kopie
		}
	}
	for tag, farbe := range other.Tags {
		if _, da := base.Tags[tag]; !da {
			base.Tags[tag] = farbe
		}
	}
	if len(base.Rules) == 0 && len(other.Rules) > 0 {
		base.Rules = other.Rules
	}
	return base
}

// TagNamen liefert die Tags in stabiler Reihenfolge - Go-Maps haben keine.
func (d *Data) TagNamen() []string {
	out := make([]string, 0, len(d.Tags))
	for t := range d.Tags {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func enthaelt(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func Ptr[T any](v T) *T { return &v }
