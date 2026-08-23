package mimeparse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func korpus(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "corpus", name+".eml"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReadTextAndHTML(t *testing.T) {
	v := Read(korpus(t, "04-alternative"), time.Unix(0, 0).UTC())
	if !strings.Contains(v.Text, "Nur-Text-Fassung mit Ümlaut") {
		t.Errorf("Fliesstext: %q", v.Text)
	}
	if !strings.Contains(v.HTML, "<b>Ümlaut</b>") {
		t.Errorf("HTML: %q", v.HTML)
	}
	if len(v.Anhaenge) != 0 {
		t.Errorf("alternative hat keine Anhaenge: %v", v.Anhaenge)
	}
}

// TestReadAttachmentsWithContent - die Detailansicht muss den Anhang gleich
// mitliefern, sonst wird die Mail zum Herunterladen ein zweites Mal geparst.
func TestReadAttachmentsWithContent(t *testing.T) {
	v := Read(korpus(t, "05-nested-mixed"), time.Unix(0, 0).UTC())
	if len(v.Anhaenge) != 1 {
		t.Fatalf("%d Anhaenge", len(v.Anhaenge))
	}
	a := v.Anhaenge[0]
	if a.Filename != "Rechnung.pdf" || a.ContentType != "application/pdf" {
		t.Errorf("%+v", a)
	}
	if !strings.HasPrefix(string(a.Inhalt), "%PDF") || a.Size != len(a.Inhalt) {
		t.Errorf("Inhalt fehlt oder Groesse stimmt nicht: %d Byte", a.Size)
	}
	if !strings.Contains(v.Text, "Anbei die Rechnung") {
		t.Errorf("Fliesstext aus dem verschachtelten Teil fehlt: %q", v.Text)
	}
}

func TestReadFilenames(t *testing.T) {
	for _, f := range []struct{ datei, name string }{
		{"06-rfc2231-filename", "Bescheid Übersicht.pdf"},
		{"07-raw-filename", "Übersicht Größe.xlsx"},
	} {
		v := Read(korpus(t, f.datei), time.Unix(0, 0).UTC())
		if len(v.Anhaenge) != 1 || v.Anhaenge[0].Filename != f.name {
			t.Errorf("%s: %v, erwartet %q", f.datei, v.Anhaenge, f.name)
		}
	}
}

// TestReadInlineStaysOut - das Logo aus der Signatur ist kein Anhang und
// darf auch nicht als Binaermuell im Text landen.
func TestReadInlineStaysOut(t *testing.T) {
	v := Read(korpus(t, "08-inline-cid"), time.Unix(0, 0).UTC())
	if len(v.Anhaenge) != 0 {
		t.Errorf("inline-Bild als Anhang gezaehlt: %v", v.Anhaenge)
	}
	if strings.Contains(v.Text, "PNGFAKE") || strings.Contains(v.HTML, "PNGFAKE") {
		t.Errorf("Bildinhalt im Text gelandet: %q / %q", v.Text, v.HTML)
	}
	if !strings.Contains(v.Text, "Hallo") {
		t.Errorf("Text aus dem HTML fehlt: %q", v.Text)
	}
}

func TestReadHeaders(t *testing.T) {
	v := Read(korpus(t, "13-address-commas"), time.Unix(0, 0).UTC())
	if !strings.Contains(v.From, "Firma GmbH, Abteilung Vertrieb") {
		t.Errorf("Anzeigename mit Komma zerlegt: %q", v.From)
	}
	if strings.Count(v.To, "@") != 3 {
		t.Errorf("Empfaengerliste: %q", v.To)
	}
}

func TestReadSESVerdicts(t *testing.T) {
	v := Read(korpus(t, "15-ses-verdicts"), time.Unix(0, 0).UTC())
	if !v.Spam || v.Virus {
		t.Errorf("Verdicts: spam=%v virus=%v", v.Spam, v.Virus)
	}
}

func TestReadBrokenMail(t *testing.T) {
	v := Read(korpus(t, "11-broken"), time.Unix(0, 0).UTC())
	if v.Subject != "(kein Betreff)" && v.Subject != "(nicht lesbar)" {
		t.Errorf("Betreff: %q", v.Subject)
	}
	if !strings.Contains(v.Text, "kein gueltiges mime") {
		t.Errorf("Rohtext nicht als Rueckfall gezeigt: %q", v.Text)
	}
}
