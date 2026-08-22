package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"s3mail/assistent"
	"s3mail/s3fake"
	"s3mail/store"
)

const testToken = "test-token-123"

func serverBauen(t *testing.T) (*httptest.Server, *store.Mailbox) {
	t.Helper()
	ctx := context.Background()
	f := s3fake.Neu()
	f.Setzen("mail/m1", mailRoh("Anna <anna@kunde.de>", "Rechnung 1", "Anbei die Rechnung.",
		"Mon, 03 Aug 2026 09:00:00 +0000"))
	f.Setzen("mail/m2", mailRoh("Shop <news@shop.io>", "Angebot", "Neu im Sortiment.",
		"Tue, 04 Aug 2026 09:00:00 +0000"))
	f.Setzen("mail/archiv/alt1", mailRoh("Alt <alt@firma.de>", "Altes", "Alter Text.",
		"Wed, 01 Jul 2026 08:00:00 +0000"))

	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(mb, testToken, "127.0.0.1", 0, map[string]any{
		"bucket": "test-bucket", "root": "mail/", "can_send": false})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	// Port aus der Testadresse uebernehmen, sonst schlaegt die Host-Pruefung zu
	srv.Port = portVon(ts.URL)
	return ts, mb
}

func mailRoh(from, subject, body, date string) []byte {
	return []byte("From: " + from + "\r\nTo: post@firma.de\r\nSubject: " + subject +
		"\r\nDate: " + date + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
}

func portVon(u string) int {
	teile := strings.Split(u, ":")
	p := 0
	for _, c := range teile[len(teile)-1] {
		p = p*10 + int(c-'0')
	}
	return p
}

type ruf struct {
	Code int
	Body []byte
}

func (r ruf) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("keine JSON-Antwort (%d): %s", r.Code, r.Body)
	}
	return m
}

func rufen(t *testing.T, ts *httptest.Server, methode, pfad string, body string,
	header map[string]string) ruf {
	t.Helper()
	var leser io.Reader
	if body != "" {
		leser = strings.NewReader(body)
	}
	req, err := http.NewRequest(methode, ts.URL+pfad, leser)
	if err != nil {
		t.Fatal(err)
	}
	if _, gesetzt := header["X-S3mail-Token"]; !gesetzt {
		req.Header.Set("X-S3mail-Token", testToken)
	}
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	blob, _ := io.ReadAll(resp.Body)
	return ruf{resp.StatusCode, blob}
}

func TestPostfachRouten(t *testing.T) {
	ts, mb := serverBauen(t)

	d := rufen(t, ts, "GET", "/api/messages?folder=", "", nil).json(t)
	msgs, _ := d["messages"].([]any)
	if len(msgs) != 2 {
		t.Errorf("%d Mails im Posteingang, erwartet 2", len(msgs))
	}
	if d["allow_delete"] != true || d["folders"] == nil {
		t.Errorf("Uebersicht fehlt in der Antwort: %v", d)
	}

	rufen(t, ts, "POST", "/api/flag", `{"keys":["mail/m1"],"star":true}`, nil)
	d = rufen(t, ts, "GET", "/api/messages?folder=&star=1", "", nil).json(t)
	if msgs, _ := d["messages"].([]any); len(msgs) != 1 {
		t.Errorf("Stern-Filter: %d Treffer", len(msgs))
	}

	rufen(t, ts, "POST", "/api/tag", `{"keys":["mail/m1"],"add":["wichtig"]}`, nil)
	d = rufen(t, ts, "GET", "/api/messages?folder=*&tag=wichtig", "", nil).json(t)
	if msgs, _ := d["messages"].([]any); len(msgs) != 1 {
		t.Errorf("Tag-Filter: %d Treffer", len(msgs))
	}

	d = rufen(t, ts, "POST", "/api/move", `{"keys":["mail/m2"],"folder":"spam"}`, nil).json(t)
	moved, _ := d["moved"].([]any)
	if len(moved) != 1 {
		t.Fatalf("Verschieben: %v", d)
	}

	// endgueltig loeschen nur aus dem Papierkorb
	if r := rufen(t, ts, "POST", "/api/delete", `{"keys":["mail/spam/m2"]}`, nil); r.Code != 403 {
		t.Errorf("Loeschen ausserhalb des Papierkorbs: HTTP %d", r.Code)
	}
	rufen(t, ts, "POST", "/api/move", `{"keys":["mail/spam/m2"],"folder":"trash"}`, nil)
	d = rufen(t, ts, "POST", "/api/delete", `{"keys":["mail/trash/m2"]}`, nil).json(t)
	if d["deleted"] != float64(1) {
		t.Errorf("Loeschen aus dem Papierkorb: %v", d)
	}
	_ = mb
}

func TestFehlercodes(t *testing.T) {
	ts, _ := serverBauen(t)
	faelle := []struct {
		name, methode, pfad, body string
		code                      int
	}{
		{"ungueltiger Ordner", "POST", "/api/move", `{"keys":["mail/m1"],"folder":"../boese"}`, 400},
		{"fremder Key", "GET", "/api/message?key=andere/nicht-meins", "", 400},
		{"interner Key", "GET", "/api/message?key=mail/.s3mail-state.json", "", 400},
		{"unbekannte Route", "GET", "/api/gibtsnicht", "", 404},
		{"kaputtes JSON", "POST", "/api/flag", `{kaputt`, 400},
		{"unbekannte Tag-Aktion", "POST", "/api/tags", `{"action":"quatsch","name":"x"}`, 400},
	}
	for _, f := range faelle {
		if r := rufen(t, ts, f.methode, f.pfad, f.body, nil); r.Code != f.code {
			t.Errorf("%s: HTTP %d, erwartet %d (%s)", f.name, r.Code, f.code, r.Body)
		}
	}
}

func TestMailLesenUndAnhang(t *testing.T) {
	ctx := context.Background()
	f := s3fake.Neu()
	f.Setzen("mail/m1", []byte("From: a@b.de\r\nTo: c@d.de\r\nSubject: Mit Anhang\r\n"+
		"Date: Mon, 03 Aug 2026 09:00:00 +0000\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: multipart/mixed; boundary=\"B\"\r\n\r\n"+
		"--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHallo mit Ümlaut.\r\n"+
		"--B\r\nContent-Type: application/pdf\r\n"+
		"Content-Disposition: attachment; filename=\"Rechnung.pdf\"\r\n\r\n"+
		"%PDF-fake\r\n--B--\r\n"))
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(mb, testToken, "127.0.0.1", 0, nil)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	srv.Port = portVon(ts.URL)

	d := rufen(t, ts, "GET", "/api/message?key=mail/m1", "", nil).json(t)
	if !strings.Contains(d["text"].(string), "Ümlaut") {
		t.Errorf("Fliesstext: %v", d["text"])
	}
	anh, _ := d["attachments"].([]any)
	if len(anh) != 1 {
		t.Fatalf("Anhaenge: %v", anh)
	}
	if d["read"] != true || !mb.State.Get("m1").Read {
		t.Error("Oeffnen hat nicht als gelesen markiert")
	}

	r := rufen(t, ts, "GET", "/api/attachment?key=mail/m1&index=1", "", nil)
	if r.Code != 200 || !strings.HasPrefix(string(r.Body), "%PDF") {
		t.Errorf("Anhang: HTTP %d %q", r.Code, r.Body)
	}
	r = rufen(t, ts, "GET", "/api/raw?key=mail/m1", "", nil)
	if r.Code != 200 || !strings.HasPrefix(string(r.Body), "From:") {
		t.Errorf("Rohmail: HTTP %d", r.Code)
	}
}

// TestZugangskontrolle - dieselben drei Pruefungen wie in der Python-Fassung.
func TestZugangskontrolle(t *testing.T) {
	ts, _ := serverBauen(t)
	ohne := map[string]string{"X-S3mail-Token": ""}

	if r := rufen(t, ts, "GET", "/api/overview", "", ohne); r.Code != 403 {
		t.Errorf("ohne Token: HTTP %d", r.Code)
	}
	if r := rufen(t, ts, "GET", "/api/overview", "", map[string]string{"X-S3mail-Token": "falsch"}); r.Code != 403 {
		t.Errorf("falsches Token: HTTP %d", r.Code)
	}
	if r := rufen(t, ts, "GET", "/", "", ohne); r.Code != 403 {
		t.Errorf("Postfachseite ohne Token: HTTP %d", r.Code)
	}
	if r := rufen(t, ts, "GET", "/api/overview", "", map[string]string{"Cookie": "s3mail=" + testToken,
		"X-S3mail-Token": ""}); r.Code != 200 {
		t.Errorf("Token per Cookie: HTTP %d", r.Code)
	}

	// CSRF: fremde Seite schickt einen POST, das Token-Cookie faehrt mit
	if r := rufen(t, ts, "POST", "/api/empty-trash", "{}",
		map[string]string{"Origin": "https://boese.example"}); r.Code != 403 {
		t.Errorf("CSRF-POST von fremder Herkunft: HTTP %d", r.Code)
	}
	if r := rufen(t, ts, "POST", "/api/empty-trash", "{}",
		map[string]string{"Origin": "http://localhost:1234"}); r.Code != 403 {
		t.Errorf("fremder Port als Origin: HTTP %d", r.Code)
	}
	// DNS-Rebinding: fremder Name im Host-Header
	if r := rufen(t, ts, "GET", "/api/messages?folder=", "",
		map[string]string{"Host": "boese.example"}); r.Code != 403 {
		t.Errorf("fremder Host: HTTP %d", r.Code)
	}
}

// TestSeiteSetztCookie - daran haengen die Download-Links.
func TestSeiteSetztCookie(t *testing.T) {
	ts, _ := serverBauen(t)
	req, _ := http.NewRequest("GET", ts.URL+"/?t="+testToken, nil)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	c := resp.Header.Get("Set-Cookie")
	if !strings.Contains(c, "s3mail="+testToken) || !strings.Contains(c, "SameSite=Strict") {
		t.Errorf("Cookie: %q", c)
	}
	blob, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(blob), "__CONFIG__") {
		t.Error("Platzhalter __CONFIG__ nicht ersetzt")
	}
	if !strings.Contains(string(blob), "test-bucket") {
		t.Error("Konfiguration nicht in die Seite eingesetzt")
	}
}

func TestOberflaecheIstVollstaendig(t *testing.T) {
	for name, seite := range map[string]string{
		"Postfach":  SeitePostfach,
		"Assistent": SeiteAssistent,
		"Token":     SeiteToken,
	} {
		if len(seite) < 500 {
			t.Errorf("%s: nur %d Zeichen - eingebettet?", name, len(seite))
		}
		if !strings.Contains(seite, "<!doctype html>") && !strings.Contains(seite, "<!DOCTYPE html>") {
			t.Errorf("%s: kein Dokumentkopf", name)
		}
	}
	if !strings.Contains(SeitePostfach, "__CONFIG__") {
		t.Error("Postfachseite hat keinen Platzhalter fuer die Konfiguration")
	}
}

// TestOhnePostfach - vor der Einrichtung gibt es kein Postfach. Die Routen
// duerfen dann nicht in einen Nil-Zeiger laufen.
func TestOhnePostfach(t *testing.T) {
	srv := NewServer(nil, testToken, "127.0.0.1", 0, nil)
	srv.MitAssistent(&assistent.Assistent{})
	ts := httptest.NewServer(srv)
	defer ts.Close()
	srv.Port = portVon(ts.URL)

	for _, pfad := range []string{"/api/messages?folder=", "/api/overview",
		"/api/message?key=mail/m1", "/api/raw?key=mail/m1"} {
		r := rufen(t, ts, "GET", pfad, "", nil)
		if r.Code != 503 {
			t.Errorf("%s: HTTP %d, erwartet 503", pfad, r.Code)
		}
	}
	if r := rufen(t, ts, "POST", "/api/refresh", "{}", nil); r.Code != 503 {
		t.Errorf("refresh: HTTP %d", r.Code)
	}
	// Der Assistent muss erreichbar bleiben
	if r := rufen(t, ts, "POST", "/api/setup/info", "{}", nil); r.Code == 503 {
		t.Error("Assistent gesperrt, obwohl er gebraucht wird")
	}
	// Beenden muss gerade hier gehen - das ist der Zustand, in dem ein
	// Erstnutzer steckt, und ohne Konsole ist der Knopf der einzige Ausweg.
	beendet := make(chan struct{}, 1)
	srv.BeimBeenden = func() { beendet <- struct{}{} }
	if r := rufen(t, ts, "POST", "/api/quit", "{}", nil); r.Code != 200 {
		t.Errorf("Beenden im Assistenten: HTTP %d", r.Code)
	}
	select {
	case <-beendet:
	case <-time.After(2 * time.Second):
		t.Error("BeimBeenden wurde nicht gerufen")
	}

	// und die Startseite zeigt ihn
	r := rufen(t, ts, "GET", "/", "", nil)
	if r.Code != 200 || !strings.Contains(string(r.Body), "s3mail einrichten") {
		t.Errorf("Startseite ohne Postfach: HTTP %d", r.Code)
	}
}

// TestAutoAbgleichWirdAusgeliefert - der Takt kommt aus der Konfiguration; ohne
// ihn stünde die Seite still und niemand saehe, dass es den Abgleich gibt.
func TestAutoAbgleichWirdAusgeliefert(t *testing.T) {
	ctx := context.Background()
	f := s3fake.Neu()
	f.Setzen("mail/m1", mailRoh("a@b.de", "x", "y", "Mon, 03 Aug 2026 09:00:00 +0000"))
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(mb, testToken, "127.0.0.1", 0, map[string]any{
		"bucket": "test-bucket", "refresh_seconds": 45})
	ts := httptest.NewServer(srv)
	defer ts.Close()
	srv.Port = portVon(ts.URL)

	seite := string(rufen(t, ts, "GET", "/?t="+testToken, "", nil).Body)
	if !strings.Contains(seite, "autoAbgleichPlanen") {
		t.Error("der automatische Abgleich fehlt in der ausgelieferten Seite")
	}
	if !strings.Contains(seite, `"refresh_seconds":45`) {
		t.Error("das Intervall kommt nicht in der Seite an")
	}
	// Ohne Intervall muss der Takt ausbleiben, nicht auf einen Standardwert fallen
	srv.Config = map[string]any{"bucket": "test-bucket", "refresh_seconds": 0}
	seite = string(rufen(t, ts, "GET", "/?t="+testToken, "", nil).Body)
	if !strings.Contains(seite, `"refresh_seconds":0`) {
		t.Error("abgeschalteter Abgleich wird nicht als 0 ausgeliefert")
	}
}

// TestSchreibenOhneVorlage - Antworten und Weiterleiten gab es von Anfang an,
// eine Mail ohne Vorlage nicht: der Server konnte es (mode "new"), nur fuehrte
// kein Weg dorthin. Das faellt niemandem auf, der ein volles Postfach testet.
func TestSchreibenOhneVorlage(t *testing.T) {
	for _, teil := range []string{`id="new"`, `compose("new")`, `mode === "new" ? ""`} {
		if !strings.Contains(SeitePostfach, teil) {
			t.Errorf("Postfachseite ohne %s - neue Nachricht nicht erreichbar", teil)
		}
	}
	// Der Schluessel der offenen Mail darf nicht mitgehen, sonst haengt die neue
	// Nachricht am Faden einer fremden.
	if strings.Contains(SeitePostfach, `{mode, key: current`) {
		t.Error("neue Nachricht schickt den Schluessel der offenen Mail mit")
	}
}
