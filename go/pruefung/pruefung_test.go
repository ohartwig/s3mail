package pruefung_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"s3mail/pruefung"
	"s3mail/s3fake"
	"s3mail/store"
)

func nach(punkte []pruefung.Punkt) map[string]pruefung.Punkt {
	out := map[string]pruefung.Punkt{}
	for _, p := range punkte {
		out[p.Name] = p
	}
	return out
}

func TestAllesInOrdnung(t *testing.T) {
	f := s3fake.Neu()
	f.Setzen("mail/m1", []byte("From: a@b.de\r\nSubject: x\r\n\r\nText\r\n"))
	p := pruefung.Ausfuehren(context.Background(), f, nil, nil, "test-bucket", "mail/", "")
	if !pruefung.Alles(p) {
		t.Errorf("nicht alles gruen: %+v", p)
	}
	k := nach(p)
	if !strings.Contains(k["Bucket lesen"].Detail, "Objekt") {
		t.Errorf("%+v", k["Bucket lesen"])
	}
	if k["Mail lesen"].Detail != "m1" {
		t.Errorf("%+v", k["Mail lesen"])
	}
	if k["Verschlüsselung"].Detail != "keine – die Mails liegen im Klartext" {
		t.Errorf("%+v", k["Verschlüsselung"])
	}
	if !k["Schreiben"].OK || !k["Löschen"].OK {
		t.Errorf("Schreiben/Loeschen: %+v %+v", k["Schreiben"], k["Löschen"])
	}
	// Das Testobjekt muss wieder weg sein
	if f.Hat("mail/.s3mail-probe") {
		t.Error("Testobjekt liegen gelassen")
	}
}

// TestFehlendesRechtNenntDieAktion - das ist der ganze Zweck der Liste.
func TestFehlendesRechtNenntDieAktion(t *testing.T) {
	f := s3fake.Neu()
	f.Setzen("mail/m1", []byte("From: a@b.de\r\n\r\nText\r\n"))
	f.PutErr = errors.New("AccessDenied")

	p := pruefung.Ausfuehren(context.Background(), f, nil, nil, "test-bucket", "mail/", "")
	k := nach(p)
	if k["Schreiben"].OK {
		t.Fatal("Schreibfehler nicht bemerkt")
	}
	if !strings.Contains(k["Schreiben"].Hinweis, "s3:PutObject") {
		t.Errorf("Hinweis nennt die IAM-Aktion nicht: %q", k["Schreiben"].Hinweis)
	}
	if pruefung.Alles(p) {
		t.Error("Gesamturteil trotz Fehler gruen")
	}
}

func TestLeeresPostfachIstKeinFehler(t *testing.T) {
	f := s3fake.Neu()
	p := pruefung.Ausfuehren(context.Background(), f, nil, nil, "test-bucket", "mail/", "")
	k := nach(p)
	if !k["Bucket lesen"].OK || !strings.Contains(k["Bucket lesen"].Detail, "noch nichts") {
		t.Errorf("%+v", k["Bucket lesen"])
	}
	if !k["Mail lesen"].Uebergangen || !k["Verschlüsselung"].Uebergangen {
		t.Error("leeres Postfach muss uebersprungen werden, nicht rot sein")
	}
	if !pruefung.Alles(p) {
		t.Errorf("leeres Postfach als Fehler gewertet: %+v", p)
	}
}

// TestInterneObjekteSindKeineMail - sonst prueft der Test den Zustand statt einer Mail.
func TestInterneObjekteSindKeineMail(t *testing.T) {
	f := s3fake.Neu()
	f.Setzen("mail/"+store.StateObject, []byte(`{"messages":{}}`))
	f.Setzen("mail/"+store.StateOps+"x.json", []byte(`{"ops":[]}`))
	p := nach(pruefung.Ausfuehren(context.Background(), f, nil, nil, "test-bucket", "mail/", ""))
	if !p["Mail lesen"].Uebergangen {
		t.Errorf("Zustandsdatei als Mail geprueft: %+v", p["Mail lesen"])
	}
}

type kmsFake struct {
	key    []byte
	fehler error
}

func (k *kmsFake) Decrypt(_ []byte, _ map[string]string) ([]byte, error) {
	if k.fehler != nil {
		return nil, k.fehler
	}
	return k.key, nil
}

func umschlag(t *testing.T) ([]byte, map[string]string, []byte) {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join("..", "store", "testdata", "envelopes.json"))
	if err != nil {
		t.Skip("keine Umschlaege vorhanden")
	}
	var d struct {
		Faelle map[string]struct {
			Body string            `json:"body"`
			Meta map[string]string `json:"meta"`
			Key  string            `json:"key"`
		} `json:"faelle"`
	}
	if err := json.Unmarshal(blob, &d); err != nil {
		t.Fatal(err)
	}
	f := d.Faelle["gcm"]
	body, _ := base64.StdEncoding.DecodeString(f.Body)
	key, _ := base64.StdEncoding.DecodeString(f.Key)
	return body, f.Meta, key
}

// TestVerschluesselungWirdErkannt - der Assistent soll sagen, womit man es zu tun
// hat, statt den Nutzer raten zu lassen.
func TestVerschluesselungWirdErkannt(t *testing.T) {
	body, meta, key := umschlag(t)

	f := s3fake.Neu()
	f.Setzen("mail/enc", body)
	f.MetaSetzen("mail/enc", meta)
	p := nach(pruefung.Ausfuehren(context.Background(), f, &kmsFake{key: key}, nil,
		"test-bucket", "mail/", ""))
	if !p["Verschlüsselung"].OK || !strings.Contains(p["Verschlüsselung"].Detail, "klappt") {
		t.Errorf("%+v", p["Verschlüsselung"])
	}

	// ohne kms:Decrypt muss der Punkt rot sein und die Aktion nennen
	p = nach(pruefung.Ausfuehren(context.Background(), f,
		&kmsFake{fehler: errors.New("AccessDenied")}, nil, "test-bucket", "mail/", ""))
	if p["Verschlüsselung"].OK {
		t.Error("fehlendes kms:Decrypt nicht bemerkt")
	}
	if !strings.Contains(p["Verschlüsselung"].Hinweis, "kms:Decrypt") {
		t.Errorf("Hinweis: %q", p["Verschlüsselung"].Hinweis)
	}

	// serverseitig verschluesselt: nur ein Hinweis, kein Fehler
	g := s3fake.Neu()
	g.Setzen("mail/m1", []byte("From: a@b.de\r\n\r\nText\r\n"))
	g.SSESetzen("mail/m1", store.CopyOpts{ServerSideEncryption: "aws:kms"})
	p = nach(pruefung.Ausfuehren(context.Background(), g, nil, nil, "test-bucket", "mail/", ""))
	if !p["Verschlüsselung"].OK || !strings.Contains(p["Verschlüsselung"].Detail, "serverseitig") {
		t.Errorf("%+v", p["Verschlüsselung"])
	}
	if !strings.Contains(p["Verschlüsselung"].Hinweis, "kms:GenerateDataKey") {
		t.Errorf("Hinweis zu SSE-KMS fehlt: %q", p["Verschlüsselung"].Hinweis)
	}
}

type sesFake struct {
	gut    []string
	fehler error
}

func (s *sesFake) Verifiziert(_ context.Context, _, _ string) ([]string, error) {
	return s.gut, s.fehler
}

func TestSESAbsender(t *testing.T) {
	f := s3fake.Neu()
	f.Setzen("mail/m1", []byte("From: a@b.de\r\n\r\nText\r\n"))
	ctx := context.Background()

	p := nach(pruefung.Ausfuehren(ctx, f, nil, &sesFake{gut: []string{"support@firma.de"}},
		"test-bucket", "mail/", "support@firma.de"))
	if !p["SES-Absender"].OK {
		t.Errorf("%+v", p["SES-Absender"])
	}
	p = nach(pruefung.Ausfuehren(ctx, f, nil, &sesFake{}, "test-bucket", "mail/", "x@y.de"))
	if p["SES-Absender"].OK {
		t.Error("unverifizierte Adresse als in Ordnung gemeldet")
	}
	if !strings.Contains(p["SES-Absender"].Hinweis, "verifizieren") {
		t.Errorf("Hinweis: %q", p["SES-Absender"].Hinweis)
	}
	// ohne Absender: uebersprungen, nicht rot
	p = nach(pruefung.Ausfuehren(ctx, f, nil, &sesFake{}, "test-bucket", "mail/", ""))
	if !p["SES-Absender"].Uebergangen {
		t.Errorf("%+v", p["SES-Absender"])
	}
}

// TestHinweisNenntDasPrefixZuerst - der haeufigste Grund fuer ein 403 beim
// Auflisten ist bei prefix-beschraenkten Zugaengen nicht das fehlende Recht,
// sondern ein Prefix eine Ebene zu weit oben. Die alte Fassung nannte genau das
// nicht und schickte den Nutzer zur IAM-Konsole statt ins Feld darueber.
func TestHinweisNenntDasPrefixZuerst(t *testing.T) {
	f := s3fake.Neu()
	f.ListErr = errors.New("AccessDenied")
	p := nach(pruefung.Ausfuehren(context.Background(), f, nil, nil,
		"test-bucket", "mail/ole/", ""))

	h := p["Bucket lesen"].Hinweis
	if !strings.Contains(h, "mail/ole/") {
		t.Errorf("das erwartete Prefix wird nicht genannt: %q", h)
	}
	if !strings.Contains(h, "Prefix") {
		t.Errorf("Prefix kommt im Hinweis nicht vor: %q", h)
	}
	if strings.Index(h, "Prefix") > strings.Index(h, "s3:ListBucket") {
		t.Errorf("die unwahrscheinlichere Ursache steht zuerst: %q", h)
	}
}
