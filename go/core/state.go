// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package core carries s3mail's state: tags, read/unread, star and rules -
// plus folder logic, the rule engine and search. Nothing here talks to S3 or to
// HTTP; that keeps the layer testable and is the reason it can be checked one
// to one against the Python version.
package core

import "sort"

// TagColors are handed out in order as new tags appear.
var TagColors = []string{
	"#4c8dff", "#38b48b", "#e0a33e", "#d9534f",
	"#a06ee1", "#3ea8c4", "#e07a5f", "#7f9c3a",
}

// Entry is the state of a single message. The key in the state is the base name
// of the S3 object, not the full key - which is why the entry survives a move
// between folders.
type Entry struct {
	Read  bool     `json:"read"`
	Star  bool     `json:"star"`
	Tags  []string `json:"tags"`
	Ruled bool     `json:"ruled"`
}

// Data is what lies in the bucket as a snapshot. Upto is the watermark: up to
// this op name, everything is already in the snapshot.
type Data struct {
	Messages map[string]*Entry `json:"messages"`
	Tags     map[string]string `json:"tags"`
	Rules    []Rule            `json:"rules"`
	Upto     string            `json:"upto,omitempty"`
}

func NewData() *Data {
	return &Data{Messages: map[string]*Entry{}, Tags: map[string]string{}, Rules: []Rule{}}
}

// Normalize fills in missing fields - a snapshot from an older version, or a
// hand-edited file, would otherwise topple us.
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

// Op is a single state change. These objects are what lands in the bucket, one
// at a time - never a copy of the whole document. Read and Star are pointers
// because "do not touch" is a different thing from "set to false".
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

// Apply applies one change to a state document - pure, without I/O.
//
// Every operation has to stay idempotent: that is what makes an op object left
// behind by an incomplete cleanup harmless.
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
			if _, present := d.Tags[tag]; !present {
				d.Tags[tag] = TagColors[len(d.Tags)%len(TagColors)]
			}
		}
		for _, mid := range op.Mids {
			e := d.entry(mid)
			cur := make([]string, 0, len(e.Tags)+len(op.Add))
			for _, t := range e.Tags {
				if !contains(op.Remove, t) {
					cur = append(cur, t)
				}
			}
			for _, t := range op.Add {
				if !contains(cur, t) {
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
			clone := *e
			clone.Tags = append([]string{}, e.Tags...)
			d.Messages[op.New] = &clone
		}
	case "tagdel":
		delete(d.Tags, op.Name)
		for _, e := range d.Messages {
			if contains(e.Tags, op.Name) {
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
		old, fresh, color := op.Old, op.New, op.Color
		if old != fresh {
			if present, ok := d.Tags[old]; ok {
				delete(d.Tags, old)
				if color != "" {
					d.Tags[fresh] = color
				} else {
					d.Tags[fresh] = present
				}
			} else {
				d.Tags[fresh] = colorOr(d, fresh, color)
			}
			for _, e := range d.Messages {
				for i, t := range e.Tags {
					if t == old {
						e.Tags[i] = fresh
					}
				}
			}
		} else if _, ok := d.Tags[fresh]; !ok || color != "" {
			d.Tags[fresh] = colorOr(d, fresh, color)
		}
	case "rules":
		d.Rules = op.Rules
	}
}

func colorOr(d *Data, tag, color string) string {
	if color != "" {
		return color
	}
	if present, ok := d.Tags[tag]; ok {
		return present
	}
	return TagColors[len(d.Tags)%len(TagColors)]
}

// MergeMissing takes over entries from other that base does not know at all -
// so nothing created offline gets lost.
func MergeMissing(base, other *Data) *Data {
	if other == nil {
		return base
	}
	base.Normalize()
	for mid, e := range other.Messages {
		if _, present := base.Messages[mid]; !present {
			clone := *e
			base.Messages[mid] = &clone
		}
	}
	for tag, color := range other.Tags {
		if _, present := base.Tags[tag]; !present {
			base.Tags[tag] = color
		}
	}
	if len(base.Rules) == 0 && len(other.Rules) > 0 {
		base.Rules = other.Rules
	}
	return base
}

// TagNames returns the tags in a stable order - Go maps have none.
func (d *Data) TagNamen() []string {
	out := make([]string, 0, len(d.Tags))
	for t := range d.Tags {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func Ptr[T any](v T) *T { return &v }
