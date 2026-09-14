// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/go/core"
	"git.ole-hartwig.eu/development/s3mail/s3mail/go/i18n"
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
		`id="devName"`, `id="devQR"`, `id="devPin"`, `id="devDone"`} {
		if !strings.Contains(out, anchor) {
			t.Errorf("%s missing - the dialog has no place to put its answer", anchor)
		}
	}
	if !strings.Contains(out, "/api/setup/device") {
		t.Error("nothing calls /api/setup/device - the endpoint stays unreachable")
	}
}

// One field, one button. Everything that used to be copied out of here - the
// user name, the policy, the payload - is now done by the program, and a field
// for it would be homework nobody asked for.
func TestPairingAsksForOneThingOnly(t *testing.T) {
	out := page("setup", PageWizard, "de", nil)
	for _, gone := range []string{`id="devPolicy"`, `id="devUser"`,
		`id="devPayload"`, `id="devPolicyCopy"`} {
		if strings.Contains(out, gone) {
			t.Errorf("%s is still there - the console work was not actually removed", gone)
		}
	}
}

// A code that is abandoned has to take its key with it. Without this the wizard
// would leave a working key behind every time somebody looked at the dialog and
// changed their mind.
func TestAbandoningTheDialogRevokesTheKey(t *testing.T) {
	out := page("setup", PageWizard, "de", nil)
	if !strings.Contains(out, "/api/setup/device/abandon") {
		t.Error("nothing revokes the key when the dialog closes")
	}
	if !strings.Contains(out, "device_user:") {
		t.Error("the abandon call does not say which pairing it means")
	}
}

// Links in a mail have to open - and the mail has to stay powerless while they
// do.
//
// The empty sandbox forbade both: scripts (wanted) and navigation (not
// wanted). Beside it stood a <base target="_blank"> attempting exactly that and
// getting nowhere. Found in use, not by a test.
func TestMailLinksCanBeOpened(t *testing.T) {
	out := page("inbox", PageMailbox, "de", map[string]any{"bucket": "b"})

	if !strings.Contains(out, "allow-popups") {
		t.Error("the iframe forbids navigation - links do nothing")
	}
	if !strings.Contains(out, "allow-popups-to-escape-sandbox") {
		t.Error("an opened page would inherit the sandbox and look broken")
	}
	// And the absences that carry the whole arrangement. An allow-scripts here
	// would undo the decision that makes showing a stranger's HTML defensible
	// at all.
	for _, forbidden := range []string{"allow-scripts", "allow-same-origin",
		"allow-forms", "allow-top-navigation"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("%s is granted - the mail could act", forbidden)
		}
	}
}

// Otherwise a URL in a text mail is something to retype by hand.
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
	// Only the two schemes. A javascript: out of somebody else's mail does not
	// become a link.
	if !strings.Contains(out, `https?:\/\/|mailto:`) {
		t.Error("the scheme list is not what it should be")
	}
}

// Icons come from the sprite, not from the font.
//
// Emoji were the previous answer and a poor one: they render differently on
// every machine, cannot take a colour, and put a decision about what a mailbox
// looks like into core/folder.go - a package that is meant to hold logic
// without side effects.
func TestTheIconsAreDrawnNotTyped(t *testing.T) {
	out := page("inbox", PageMailbox, "de", map[string]any{"bucket": "b"})

	if !strings.Contains(out, `id="i-inbox"`) {
		t.Error("no sprite - the icons have nowhere to come from")
	}
	if !strings.Contains(out, "const ic = ") {
		t.Error("no helper - nothing turns a name into a glyph")
	}
	// The emoji this replaced. A test on absence, because the way they come
	// back is somebody adding one line in a hurry.
	for _, glyph := range []string{"📥", "📝", "📤", "📦", "🗑", "📎", "★", "☆", "⬇︎"} {
		if strings.Contains(out, glyph) {
			t.Errorf("%s is back in the page", glyph)
		}
	}
}

// An SVG without a viewBox does not scale - it crops.
//
// The sprite is drawn in a 24x24 grid and .ic sizes the element at about an em.
// Without a viewBox those 24 user units map 1:1 onto ~18 pixels, so the browser
// shows the top left corner of every glyph and nothing warns anybody: the page
// is valid, the icons are simply cut off. That is a whole class of bug that
// costs one attribute to close, so the attribute is worth a test.
func TestTheIconsScaleRatherThanCrop(t *testing.T) {
	out := page("inbox", PageMailbox, "de", map[string]any{"bucket": "b"})

	idx := strings.Index(out, "const ic = ")
	if idx < 0 {
		t.Fatal("no ic helper to check")
	}
	// A window rather than the line: the template sits on the next one, and a
	// reformat should not be able to move the attribute out of sight.
	window := out[idx:min(idx+400, len(out))]
	if !strings.Contains(window, `viewBox="0 0 24 24"`) {
		t.Error("ic() builds an svg without a viewBox - every glyph renders cropped")
	}
}

// Every folder icon goes through ic(), including the one in the move menu.
//
// That menu was the last place still interpolating the raw value, which was
// invisible while the value was an emoji and became the literal word
// "folder.inbox" the day it turned into a name.
func TestTheMoveMenuDrawsItsIconsToo(t *testing.T) {
	out := page("inbox", PageMailbox, "de", map[string]any{"bucket": "b"})

	if !strings.Contains(out, `${ic(f.icon || "folder")}`) {
		t.Error("the move menu does not send its icons through ic()")
	}
	if strings.Contains(out, `">${f.icon} `) {
		t.Error("the move menu still prints the icon name as text")
	}
}

// A placeholder that arrives as "${T[...]}" is a quoting mistake, not a
// translation: single quotes make a JS string, and only backticks interpolate.
// The catalogue is wired up correctly and the field still showed the source.
func TestTheNewFolderFieldAsksInTheReadersLanguage(t *testing.T) {
	out := page("inbox", PageMailbox, "de", map[string]any{"bucket": "b"})

	if strings.Contains(out, `'<div class="opt" style="gap:4px">`) {
		t.Error("the new-folder row is a single-quoted string - its placeholder cannot interpolate")
	}
}

// The icon travels as a name so that one word can be an SVG here and an SF
// Symbol on the phone. A glyph in the core would have decided for both.
func TestTheCoreNamesIconsRatherThanDrawingThem(t *testing.T) {
	for _, f := range core.SystemFolders {
		if f.Icon == "" {
			t.Errorf("%s has no icon name", f.Name)
		}
		for _, r := range f.Icon {
			if r > 127 {
				t.Errorf("%s carries a glyph (%q) rather than a name", f.Name, f.Icon)
				break
			}
		}
	}
}

// Closing the pairing dialog has to say which key it handed out.
//
// Without it the wizard cannot tell the two outcomes apart - phone took the
// key, or nobody scanned anything - and it used to guess by taking every key
// away. Opening the dialog was then enough to lock out a phone that had been
// working a moment earlier, which is exactly the bug this line prevents.
func TestClosingThePairingSaysWhichKeyItHandedOut(t *testing.T) {
	out := page("setup", PageWizard, "de", nil)

	if !strings.Contains(out, "device_key:") {
		t.Error("the dialog closes without naming its key - the wizard has to guess")
	}
	if !strings.Contains(out, "pairedKey = d.key") {
		t.Error("the key from the pairing is never kept, so there is nothing to send back")
	}
	// The outcome where a previous pairing survives is the new one, and the
	// only one that has something reassuring to say.
	if !strings.Contains(out, "setup.devicePairingKept") {
		t.Error("nothing tells anybody that their paired phone is still fine")
	}
}

// TestTheFolderBadgeCountsUnread - the number beside a folder is what every mail
// program promises in that spot: what is still unread. It used to fall back to
// the total as soon as nothing was unread, so a folder read to the end kept
// wearing a number, and a fresh mailbox - where everything is unread - showed
// the same figure twice as "17/17". The tag row one line below always did it
// right; this is the folder row catching up.
func TestTheFolderBadgeCountsUnread(t *testing.T) {
	out := page("inbox", PageMailbox, "de", map[string]any{"bucket": "b"})

	i := strings.Index(out, `<span class="cnt">`)
	if i < 0 {
		t.Fatal("no badge in the sidebar at all")
	}
	badge := out[i:min(i+60, len(out))]
	if !strings.Contains(badge, "f.unread") {
		t.Errorf("the folder badge does not read f.unread: %q", badge)
	}
	if strings.Contains(badge, "f.count") {
		t.Errorf("the folder badge falls back to f.count - that is the total: %q", badge)
	}
	// The total is not lost, it moved into the tooltip.
	if !strings.Contains(out, "side.folderTotal") {
		t.Error("no tooltip carrying the total - the number is simply gone")
	}
}
