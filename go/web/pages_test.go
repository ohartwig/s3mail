// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/core"
	"git.ole-hartwig.eu/development/s3mail/s3mail/i18n"
)

// The token page is the one a stranger sees, so it is the one that has to be
// readable in their language.
func TestTokenPageIsTranslated(t *testing.T) {
	fuer := map[string]string{
		"de": "Token fehlt",
		"en": "Token missing",
		"es": "Falta el token",
	}
	for code, phrase := range fuer {
		out := page("token", PageToken, code, nil)
		if !strings.Contains(out, phrase) {
			t.Errorf("%s: %q missing from the page", code, phrase)
		}
		if !strings.Contains(out, `lang="`+code+`"`) {
			t.Errorf("%s: lang attribute not set", code)
		}
	}
}

// A language switch may change no more than the language: the same structure,
// the same anchors for the script.
func TestSwitchingLanguageKeepsTheMarkup(t *testing.T) {
	de := page("inbox", PageMailbox, "de", map[string]any{"bucket": "b"})
	es := page("inbox", PageMailbox, "es", map[string]any{"bucket": "b"})

	for _, anchor := range []string{`id="side"`, `id="list"`, `id="view"`, `id="q"`} {
		if !strings.Contains(de, anchor) || !strings.Contains(es, anchor) {
			t.Errorf("%s missing from one of the two versions", anchor)
		}
	}
	if de == es {
		t.Error("both versions are identical - is anything translated at all?")
	}
}

// The catalogue has to reach the script as well: the sentences that appear on
// a click cannot be produced by a template beforehand.
func TestCatalogReachesTheScript(t *testing.T) {
	out := page("inbox", PageMailbox, "es", map[string]any{"bucket": "b"})

	if strings.Contains(out, "__I18N__") {
		t.Error("__I18N__ still sits in the page as a placeholder")
	}
	if !strings.Contains(out, `"nav.refresh"`) {
		t.Error("the catalogue is missing from the shipped script")
	}
}

// The chosen language beats the browser. Whoever picked a language in s3mail
// has said something more specific than their browser setting.
func TestChoiceBeatsBrowser(t *testing.T) {
	srv := NewServer(nil, testToken, "127.0.0.1", 0, map[string]any{"language": "de"})

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Language", "es-ES,es;q=0.9")
	if got := srv.language(r); got != "de" {
		t.Errorf("configuration does not beat the browser: %q", got)
	}

	r.AddCookie(&http.Cookie{Name: "s3mail_lang", Value: "es"})
	if got := srv.language(r); got != "es" {
		t.Errorf("the choice does not beat the configuration: %q", got)
	}

	// With neither, the browser decides.
	empty := NewServer(nil, testToken, "127.0.0.1", 0, nil)
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.4")
	if got := empty.language(r2); got != "es" {
		t.Errorf("the browser is not heard: %q", got)
	}
}

func TestLanguageRouteSetsACookie(t *testing.T) {
	srv := NewServer(nil, testToken, "127.0.0.1", 0, nil)
	srv.WithWizard(nil)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	srv.Port = portOf(ts.URL)

	client := ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequest("GET", ts.URL+"/language/es", nil)
	req.Header.Set("X-S3mail-Token", testToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("Status %d", resp.StatusCode)
	}
	var set string
	for _, c := range resp.Cookies() {
		if c.Name == "s3mail_lang" {
			set = c.Value
		}
	}
	if set != "es" {
		t.Errorf("Cookie = %q", set)
	}

	// A language that does not exist must not stick as a wish.
	req2, _ := http.NewRequest("GET", ts.URL+"/language/fr", nil)
	req2.Header.Set("X-S3mail-Token", testToken)
	resp2, _ := client.Do(req2)
	for _, c := range resp2.Cookies() {
		if c.Name == "s3mail_lang" && c.Value != i18n.Fallback {
			t.Errorf("unknown language stored: %q", c.Value)
		}
	}
}

// Every key the pages ask for must exist in the catalogue. A typo does not
// crash anything: it puts "compose.subjekt" on screen, and only the person who
// opens that dialog in that language ever sees it.
func TestPagesUseOnlyKnownKeys(t *testing.T) {
	keys := map[string]bool{}
	for _, page := range []string{PageMailbox, PageWizard, PageToken} {
		for _, m := range regexp.MustCompile(`T\["([^"]+)"\]`).FindAllStringSubmatch(page, -1) {
			keys[m[1]] = true
		}
		for _, m := range regexp.MustCompile(`\{\{t "([^"]+)"\}\}`).FindAllStringSubmatch(page, -1) {
			keys[m[1]] = true
		}
	}
	if len(keys) < 20 {
		t.Fatalf("only %d keys found - is the extraction still right?", len(keys))
	}

	cat := i18n.Get("en")
	for key := range keys {
		if cat.T(key) == key {
			t.Errorf("%q has no text in the catalogue", key)
		}
	}
}

// TestComposeCarriesTheNewFields - the pages are strings; a lost field is
// noticed by nobody otherwise. Checked are the anchors, not the sentences.
func TestComposeCarriesTheNewFields(t *testing.T) {
	out := page("inbox", PageMailbox, "de", map[string]any{"bucket": "b"})
	for _, anchor := range []string{"c_bcc", "c_files", "c_attach", "c_atts", "c_draft",
		"/api/draft", "draft_key", "attachments"} {
		if !strings.Contains(out, anchor) {
			t.Errorf("%q missing from the mailbox page - the way there is gone", anchor)
		}
	}
}

// TestSentAndDraftsAreInTheSidebar - both are system folders and have to be
// visible even while they are empty; otherwise nobody finds their own mail again.
func TestSentAndDraftsAreInTheSidebar(t *testing.T) {
	for _, key := range []string{"folder.sent", "folder.drafts"} {
		for _, lang := range []string{"de", "en", "es"} {
			if i18n.Get(lang).T(key) == key {
				t.Errorf("%s: %q has no label", lang, key)
			}
		}
	}
	names := map[string]bool{}
	for _, f := range core.SystemFolders {
		names[f.Name] = true
	}
	for _, want := range []string{core.Sent, core.Drafts} {
		if !names[want] {
			t.Errorf("%q is not a system folder - then it disappears when empty", want)
		}
	}
}

// TestSignatureReachesBothPages - the signature is set in the wizard and used in
// the composer. Two pages, one setting; if the way there breaks on either side,
// it is stored and never seen or seen and never stored.
func TestSignatureReachesBothPages(t *testing.T) {
	setup := page("setup", PageWizard, "de", nil)
	for _, anchor := range []string{`id="signature"`, "setup.signature"} {
		if !strings.Contains(setup, anchor) {
			t.Errorf("%q missing from the wizard - the signature cannot be set", anchor)
		}
	}
	inbox := page("inbox", PageMailbox, "de", map[string]any{"signature": "Kai Ole Hartwig"})
	if !strings.Contains(inbox, "function signature()") {
		t.Error("the composer does not put the signature into the message")
	}
	if !strings.Contains(inbox, "Kai Ole Hartwig") {
		t.Error("the signature does not arrive in the page")
	}
}

// TestThePageListensForEvents - the way there is a string in a page; without a
// test a lost line would leave the mailbox back on its timer and nobody would
// notice, because the timer still works.
func TestThePageListensForEvents(t *testing.T) {
	out := page("inbox", PageMailbox, "de", map[string]any{"bucket": "b"})
	for _, anchor := range []string{"EventSource", "/api/events", `addEventListener("mail"`} {
		if !strings.Contains(out, anchor) {
			t.Errorf("%q missing from the page - it is back on polling", anchor)
		}
	}
	// And the timer has to stay: it is the fallback for a broken stream.
	if !strings.Contains(out, "scheduleAutoRefresh") {
		t.Error("the timer is gone - a broken stream would stop the mailbox for good")
	}
}

// TestTheRulesDialogAsksForSuggestions - the way there is a string in a page.
func TestTheRulesDialogAsksForSuggestions(t *testing.T) {
	out := page("inbox", PageMailbox, "de", map[string]any{"bucket": "b"})
	for _, anchor := range []string{"/api/rules/suggest", "ruleTips", "showSuggestions"} {
		if !strings.Contains(out, anchor) {
			t.Errorf("%q missing - the suggestions never reach the reader", anchor)
		}
	}
}

// The device dialog has to be reachable from the wizard.
//
// The endpoint /api/setup/device existed for a while with nothing calling it -
// built, tested and unreachable, which is the same as absent for whoever is
// trying to set up a phone. A test that only checked the handler would not have
// noticed, so this one checks the way there.
func TestTheWizardCanPrepareADevice(t *testing.T) {
	out := page("setup", PageWizard, "de", nil)

	for _, anchor := range []string{`id="device"`, `id="deviceBox"`,
		`id="devUser"`, `id="devPolicy"`, `id="devPayload"`} {
		if !strings.Contains(out, anchor) {
			t.Errorf("%s missing - the dialog has no place to put its answer", anchor)
		}
	}
	if !strings.Contains(out, "/api/setup/device") {
		t.Error("nothing calls /api/setup/device - the endpoint stays unreachable")
	}
}

// Three copy buttons and three fields, because all three are pasted somewhere
// else: the user name and the policy into the IAM console, the code into the
// phone. A field somebody has to retype by hand is a field somebody retypes
// wrongly.
func TestEverythingTheDeviceNeedsCanBeCopied(t *testing.T) {
	out := page("setup", PageWizard, "de", nil)
	for _, id := range []string{"devUserCopy", "devPolicyCopy", "devPayloadCopy"} {
		if !strings.Contains(out, `id="`+id+`"`) {
			t.Errorf("%s missing", id)
		}
	}
}

// Links in einer Mail muessen sich oeffnen lassen - und die Mail muss dabei
// machtlos bleiben.
//
// Der leere Sandkasten verbot beides: Skripte (gewollt) und das Navigieren
// (nicht gewollt). Daneben stand ein <base target="_blank">, das genau das
// versuchte und ins Leere lief. Aufgefallen im Betrieb.
func TestMailLinksCanBeOpened(t *testing.T) {
	out := page("inbox", PageMailbox, "de", map[string]any{"bucket": "b"})

	if !strings.Contains(out, "allow-popups") {
		t.Error("the iframe forbids navigation - links do nothing")
	}
	if !strings.Contains(out, "allow-popups-to-escape-sandbox") {
		t.Error("an opened page would inherit the sandbox and look broken")
	}
	// Und die Abwesenheiten, die das Ganze tragen. Ein allow-scripts hier
	// waere die Ruecknahme der Entscheidung, fremdes HTML ueberhaupt
	// anzuzeigen.
	for _, forbidden := range []string{"allow-scripts", "allow-same-origin",
		"allow-forms", "allow-top-navigation"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("%s is granted - the mail could act", forbidden)
		}
	}
}

// URLs in einer Textmail sind sonst Text zum Abtippen.
func TestPlainTextURLsBecomeLinks(t *testing.T) {
	out := page("inbox", PageMailbox, "de", map[string]any{"bucket": "b"})

	if !strings.Contains(out, "const linkify") {
		t.Fatal("no linkify - URLs in a text mail stay text")
	}
	if !strings.Contains(out, "linkify(esc(") {
		t.Error("escaping has to come first: linkify(esc(...)), never the other way round")
	}
	if !strings.Contains(out, `rel="noopener noreferrer"`) {
		t.Error("an opened page would get a handle on this window and the referrer")
	}
	// Nur die drei Schemata. javascript: aus einer fremden Mail wird kein Link.
	if !strings.Contains(out, `https?:\/\/|mailto:`) {
		t.Error("the scheme list is not what it should be")
	}
}
