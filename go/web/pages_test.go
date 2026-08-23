package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"s3mail/i18n"
)

// The token page is the one a stranger sees, so it is the one that has to be
// readable in their language.
func TestTokenPageIsTranslated(t *testing.T) {
	fuer := map[string]string{
		"de": "Token fehlt",
		"en": "Token missing",
		"es": "Falta el token",
	}
	for code, satz := range fuer {
		out := page("token", SeiteToken, code, nil)
		if !strings.Contains(out, satz) {
			t.Errorf("%s: %q fehlt in der Seite", code, satz)
		}
		if !strings.Contains(out, `lang="`+code+`"`) {
			t.Errorf("%s: lang-Attribut nicht gesetzt", code)
		}
	}
}

// Ein Sprachwechsel darf nicht mehr aendern als die Sprache: derselbe Aufbau,
// dieselben Ankerpunkte fuer das Skript.
func TestSwitchingLanguageKeepsTheMarkup(t *testing.T) {
	de := page("inbox", SeitePostfach, "de", map[string]any{"bucket": "b"})
	es := page("inbox", SeitePostfach, "es", map[string]any{"bucket": "b"})

	for _, anker := range []string{`id="side"`, `id="list"`, `id="view"`, `id="q"`} {
		if !strings.Contains(de, anker) || !strings.Contains(es, anker) {
			t.Errorf("%s fehlt in einer der beiden Fassungen", anker)
		}
	}
	if de == es {
		t.Error("beide Fassungen sind gleich - wird ueberhaupt uebersetzt?")
	}
}

// Der Katalog muss auch im Skript ankommen: die Saetze, die beim Klicken
// entstehen, kann keine Vorlage vorher erzeugen.
func TestCatalogReachesTheScript(t *testing.T) {
	out := page("inbox", SeitePostfach, "es", map[string]any{"bucket": "b"})

	if strings.Contains(out, "__I18N__") {
		t.Error("__I18N__ steht noch als Platzhalter in der Seite")
	}
	if !strings.Contains(out, `"nav.refresh"`) {
		t.Error("der Katalog fehlt im ausgelieferten Skript")
	}
}

// Die gewaehlte Sprache schlaegt die des Browsers. Wer in s3mail eine Sprache
// gewaehlt hat, hat etwas Bestimmteres gesagt als seine Browsereinstellung.
func TestChoiceBeatsBrowser(t *testing.T) {
	srv := NewServer(nil, testToken, "127.0.0.1", 0, map[string]any{"language": "de"})

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Language", "es-ES,es;q=0.9")
	if got := srv.language(r); got != "de" {
		t.Errorf("Konfiguration schlaegt Browser nicht: %q", got)
	}

	r.AddCookie(&http.Cookie{Name: "s3mail_lang", Value: "es"})
	if got := srv.language(r); got != "es" {
		t.Errorf("Auswahl schlaegt Konfiguration nicht: %q", got)
	}

	// Ohne alles entscheidet der Browser.
	leer := NewServer(nil, testToken, "127.0.0.1", 0, nil)
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.4")
	if got := leer.language(r2); got != "es" {
		t.Errorf("Browser wird nicht gehoert: %q", got)
	}
}

func TestLanguageRouteSetsACookie(t *testing.T) {
	srv := NewServer(nil, testToken, "127.0.0.1", 0, nil)
	srv.WithWizard(nil)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	srv.Port = portVon(ts.URL)

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
	var gesetzt string
	for _, c := range resp.Cookies() {
		if c.Name == "s3mail_lang" {
			gesetzt = c.Value
		}
	}
	if gesetzt != "es" {
		t.Errorf("Cookie = %q", gesetzt)
	}

	// Eine Sprache, die es nicht gibt, darf nicht als Wunsch haengenbleiben.
	req2, _ := http.NewRequest("GET", ts.URL+"/language/fr", nil)
	req2.Header.Set("X-S3mail-Token", testToken)
	resp2, _ := client.Do(req2)
	for _, c := range resp2.Cookies() {
		if c.Name == "s3mail_lang" && c.Value != i18n.Fallback {
			t.Errorf("unbekannte Sprache gespeichert: %q", c.Value)
		}
	}
}

// Every key the pages ask for must exist in the catalogue. A typo does not
// crash anything: it puts "compose.subjekt" on screen, and only the person who
// opens that dialog in that language ever sees it.
func TestPagesUseOnlyKnownKeys(t *testing.T) {
	keys := map[string]bool{}
	for _, seite := range []string{SeitePostfach, SeiteAssistent, SeiteToken} {
		for _, m := range regexp.MustCompile(`T\["([^"]+)"\]`).FindAllStringSubmatch(seite, -1) {
			keys[m[1]] = true
		}
		for _, m := range regexp.MustCompile(`\{\{t "([^"]+)"\}\}`).FindAllStringSubmatch(seite, -1) {
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
