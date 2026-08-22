package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"s3mail/s3fake"
	"s3mail/store"
)

// serverMitSperrliste baut einen Server mit Postfach - ohne das antworten alle
// /api/-Routen mit 503 ("noch nicht eingerichtet"), auch diese hier.
func serverMitSperrliste(t *testing.T, l Sperrliste) (*httptest.Server, *Server) {
	t.Helper()
	ctx := context.Background()
	f := s3fake.Neu()
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), true)
	srv := NewServer(mb, testToken, "127.0.0.1", 0,
		map[string]any{"bucket": "test-bucket", "root": "mail/", "can_send": true})
	if l != nil {
		srv.MitSperrliste(l)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	srv.Port = portVon(ts.URL) // sonst schlaegt die Host-Pruefung zu
	return ts, srv
}

type fakeSperrliste struct {
	drin    map[string]bool
	fehler  error
	gelesen int
}

func (f *fakeSperrliste) Sperren(_ context.Context, a string) error {
	if f.fehler != nil {
		return f.fehler
	}
	f.drin[a] = true
	return nil
}
func (f *fakeSperrliste) Freigeben(_ context.Context, a string) error {
	delete(f.drin, a)
	return nil
}
func (f *fakeSperrliste) Lesen(context.Context) ([]Sperreintrag, error) {
	f.gelesen++
	out := []Sperreintrag{}
	for a := range f.drin {
		out = append(out, Sperreintrag{Adresse: a, Grund: "COMPLAINT"})
	}
	return out, nil
}

// TestAdresseAusKopfzeile - der Knopf sitzt an der Mailansicht, und dort steht
// die Adresse als `Name <a@x.de>`. Ginge das ungefiltert an SES, landete der
// Anzeigename auf der Sperrliste oder SES lehnte ab.
func TestAdresseAusKopfzeile(t *testing.T) {
	faelle := map[string]string{
		`Vorname Nachname <a@x.de>`:    "a@x.de",
		`"Nachname, Vorname" <b@y.de>`: "b@y.de",
		`  c@z.de  `:                   "c@z.de",
		`<d@z.de>`:                     "d@z.de",
		`a@x.de, b@y.de`:               "", // zwei auf einmal: lieber nichts
		`kein-at-zeichen`:              "",
		``:                             "",
	}
	for ein, will := range faelle {
		if got := adresseAus(ein); got != will {
			t.Errorf("%q -> %q, erwartet %q", ein, got, will)
		}
	}
}

func TestSperrenUndFreigeben(t *testing.T) {
	f := &fakeSperrliste{drin: map[string]bool{}}
	ts, _ := serverMitSperrliste(t, f)

	ruf := func(pfad, koerper string) *http.Response {
		req, _ := http.NewRequest("POST", ts.URL+pfad, strings.NewReader(koerper))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-S3mail-Token", testToken)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := ruf("/api/block", `{"address":"Kunde <weg@x.de>"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("block: %d", resp.StatusCode)
	}
	var d struct {
		Adresse  string         `json:"address"`
		Gesperrt []Sperreintrag `json:"blocked"`
	}
	json.NewDecoder(resp.Body).Decode(&d)
	if d.Adresse != "weg@x.de" || !f.drin["weg@x.de"] {
		t.Fatalf("nicht gesperrt: %+v", d)
	}
	// Die Antwort traegt die neue Liste, damit der Dialog ohne zweiten Aufruf stimmt.
	if len(d.Gesperrt) != 1 {
		t.Errorf("Liste in der Antwort: %+v", d.Gesperrt)
	}

	if resp := ruf("/api/unblock", `{"address":"weg@x.de"}`); resp.StatusCode != 200 {
		t.Fatalf("unblock: %d", resp.StatusCode)
	}
	if f.drin["weg@x.de"] {
		t.Error("nach dem Freigeben immer noch gesperrt")
	}

	// Ohne brauchbare Adresse: 400, und nichts passiert.
	if resp := ruf("/api/block", `{"address":"quatsch"}`); resp.StatusCode != 400 {
		t.Errorf("kaputte Adresse -> %d, erwartet 400", resp.StatusCode)
	}
}

// TestOhneVersandKeineSperrliste - wer nicht senden darf (--no-send), soll auch
// niemanden vom Senden ausschliessen koennen. Ohne die Pruefung liefe die Route
// in einen Nil-Zeiger.
func TestOhneVersandKeineSperrliste(t *testing.T) {
	ts, _ := serverMitSperrliste(t, nil)
	req, _ := http.NewRequest("GET", ts.URL+"/api/blocked", nil)
	req.Header.Set("X-S3mail-Token", testToken)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("ohne Sperrliste -> %d, erwartet 400", resp.StatusCode)
	}
}

// TestSperrlisteInDerOberflaeche - der Knopf ist der einzige Weg dorthin.
func TestSperrlisteInDerOberflaeche(t *testing.T) {
	for _, teil := range []string{`id="vBlock"`, `id="blockedBtn"`, "/api/unblock"} {
		if !strings.Contains(SeitePostfach, teil) {
			t.Errorf("Postfachseite ohne %s", teil)
		}
	}
}
