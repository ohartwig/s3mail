// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strings"
	"testing"
)

// Saved searches and tags, seen from the browser's side.

func TestAFilterSurvivesTheRoute(t *testing.T) {
	ts, _ := buildServer(t)

	d := callServer(t, ts, "POST", "/api/filters",
		`{"filters":[{"name":"Rechnungen","query":"rechnung has:anhang"},{"name":"","query":"x"}]}`, nil).json(t)
	list, _ := d["filters"].([]any)
	if len(list) != 1 {
		t.Fatalf("%d filters came back, expected 1: %v", len(list), d)
	}

	// And it has to be in the overview afterwards, or the sidebar cannot show it.
	over := callServer(t, ts, "GET", "/api/overview", "", nil).json(t)
	got, _ := over["filters"].([]any)
	if len(got) != 1 {
		t.Errorf("the filter is not in the overview: %v", over["filters"])
	}
}

// Never nil: the page does filters.map(...), and null.map ends the script.
func TestFiltersAreNeverNull(t *testing.T) {
	ts, _ := buildServer(t)
	body := string(callServer(t, ts, "GET", "/api/overview", "", nil).Body)
	if strings.Contains(body, `"filters":null`) {
		t.Errorf("filters came back as null: %s", body)
	}
	if !strings.Contains(body, `"filters"`) {
		t.Errorf("the overview does not carry filters at all: %s", body)
	}
}

// A tag can be created from the sidebar, without going through a message first.
func TestATagCanBeCreatedOnItsOwn(t *testing.T) {
	ts, _ := buildServer(t)
	d := callServer(t, ts, "POST", "/api/tags", `{"action":"create","name":"Wichtig"}`, nil).json(t)
	tags, _ := d["tags"].(map[string]any)
	if _, ok := tags["Wichtig"]; !ok {
		t.Errorf("the tag was not created: %v", tags)
	}
}

// The page has to carry both ways in, and the calls have to be shaped the way
// api() expects - a wrong call shape is invisible in a server test.
func TestThePageCarriesTagAndFilterCreation(t *testing.T) {
	for _, anchor := range []string{
		`id="addTag"`, `id="newTagName"`, `action: "create"`,
		`id="addFilter"`, `id="newFilterName"`, "/api/filters", "data-fq",
	} {
		if !strings.Contains(PageMailbox, anchor) {
			t.Errorf("the mailbox page has lost %q", anchor)
		}
	}
	// api(path, body) takes a plain object. Passing fetch options instead sends
	// {"method":"POST","body":"..."} and the server sees an empty request - a
	// mistake that no server test can catch.
	if strings.Contains(PageMailbox, `method: "POST"`) ||
		strings.Contains(PageMailbox, `method:"POST", body:`) {
		t.Error("an api() call passes fetch options instead of a body object")
	}
}
