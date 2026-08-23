package check_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"s3mail/i18n"

	"s3mail/check"
	"s3mail/s3fake"
	"s3mail/store"
)

func byName(items []check.Item) map[string]check.Item {
	out := map[string]check.Item{}
	for _, p := range items {
		out[p.Name] = p
	}
	return out
}

func TestEverythingOK(t *testing.T) {
	f := s3fake.New()
	f.Store("mail/m1", []byte("From: a@b.de\r\nSubject: x\r\n\r\nText\r\n"))
	p := check.Run(context.Background(), f, nil, nil, "test-bucket", "mail/", "", i18n.Get("de"))
	if !check.AllOK(p) {
		t.Errorf("nicht alles gruen: %+v", p)
	}
	k := byName(p)
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
	if f.Has("mail/.s3mail-probe") {
		t.Error("Testobjekt liegen gelassen")
	}
}

// TestMissingPermissionNamesTheAction - das ist der ganze Zweck der Liste.
func TestMissingPermissionNamesTheAction(t *testing.T) {
	f := s3fake.New()
	f.Store("mail/m1", []byte("From: a@b.de\r\n\r\nText\r\n"))
	f.PutErr = errors.New("AccessDenied")

	p := check.Run(context.Background(), f, nil, nil, "test-bucket", "mail/", "", i18n.Get("de"))
	k := byName(p)
	if k["Schreiben"].OK {
		t.Fatal("Schreibfehler nicht bemerkt")
	}
	if !strings.Contains(k["Schreiben"].Hint, "s3:PutObject") {
		t.Errorf("Hinweis nennt die IAM-Aktion nicht: %q", k["Schreiben"].Hint)
	}
	if check.AllOK(p) {
		t.Error("Gesamturteil trotz Fehler gruen")
	}
}

func TestEmptyMailboxIsNoError(t *testing.T) {
	f := s3fake.New()
	p := check.Run(context.Background(), f, nil, nil, "test-bucket", "mail/", "", i18n.Get("de"))
	k := byName(p)
	if !k["Bucket lesen"].OK || !strings.Contains(k["Bucket lesen"].Detail, "noch nichts") {
		t.Errorf("%+v", k["Bucket lesen"])
	}
	if !k["Mail lesen"].Skipped || !k["Verschlüsselung"].Skipped {
		t.Error("leeres Postfach muss uebersprungen werden, nicht rot sein")
	}
	if !check.AllOK(p) {
		t.Errorf("leeres Postfach als Fehler gewertet: %+v", p)
	}
}

// TestInternalObjectsAreNotMail - sonst prueft der Test den Zustand statt einer Mail.
func TestInternalObjectsAreNotMail(t *testing.T) {
	f := s3fake.New()
	f.Store("mail/"+store.StateObject, []byte(`{"messages":{}}`))
	f.Store("mail/"+store.StateOps+"x.json", []byte(`{"ops":[]}`))
	p := byName(check.Run(context.Background(), f, nil, nil, "test-bucket", "mail/", "", i18n.Get("de")))
	if !p["Mail lesen"].Skipped {
		t.Errorf("Zustandsdatei als Mail geprueft: %+v", p["Mail lesen"])
	}
}

type kmsFake struct {
	key []byte
	err error
}

func (k *kmsFake) Decrypt(_ []byte, _ map[string]string) ([]byte, error) {
	if k.err != nil {
		return nil, k.err
	}
	return k.key, nil
}

func envelope(t *testing.T) ([]byte, map[string]string, []byte) {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join("..", "store", "testdata", "envelopes.json"))
	if err != nil {
		t.Skip("keine Umschlaege vorhanden")
	}
	var d struct {
		Cases map[string]struct {
			Body string            `json:"body"`
			Meta map[string]string `json:"meta"`
			Key  string            `json:"key"`
		} `json:"faelle"`
	}
	if err := json.Unmarshal(blob, &d); err != nil {
		t.Fatal(err)
	}
	f := d.Cases["gcm"]
	body, _ := base64.StdEncoding.DecodeString(f.Body)
	key, _ := base64.StdEncoding.DecodeString(f.Key)
	return body, f.Meta, key
}

// TestEncryptionIsDetected - der Assistent soll sagen, womit man es zu tun
// hat, statt den Nutzer raten zu lassen.
func TestEncryptionIsDetected(t *testing.T) {
	body, meta, key := envelope(t)

	f := s3fake.New()
	f.Store("mail/enc", body)
	f.SetMeta("mail/enc", meta)
	p := byName(check.Run(context.Background(), f, &kmsFake{key: key}, nil,
		"test-bucket", "mail/", "", i18n.Get("de")))
	if !p["Verschlüsselung"].OK || !strings.Contains(p["Verschlüsselung"].Detail, "klappt") {
		t.Errorf("%+v", p["Verschlüsselung"])
	}

	// ohne kms:Decrypt muss der Punkt rot sein und die Aktion nennen
	p = byName(check.Run(context.Background(), f,
		&kmsFake{err: errors.New("AccessDenied")}, nil, "test-bucket", "mail/", "", i18n.Get("de")))
	if p["Verschlüsselung"].OK {
		t.Error("fehlendes kms:Decrypt nicht bemerkt")
	}
	if !strings.Contains(p["Verschlüsselung"].Hint, "kms:Decrypt") {
		t.Errorf("Hinweis: %q", p["Verschlüsselung"].Hint)
	}

	// serverseitig verschluesselt: nur ein Hinweis, kein Fehler
	g := s3fake.New()
	g.Store("mail/m1", []byte("From: a@b.de\r\n\r\nText\r\n"))
	g.SetSSE("mail/m1", store.CopyOpts{ServerSideEncryption: "aws:kms"})
	p = byName(check.Run(context.Background(), g, nil, nil, "test-bucket", "mail/", "", i18n.Get("de")))
	if !p["Verschlüsselung"].OK || !strings.Contains(p["Verschlüsselung"].Detail, "serverseitig") {
		t.Errorf("%+v", p["Verschlüsselung"])
	}
	if !strings.Contains(p["Verschlüsselung"].Hint, "kms:GenerateDataKey") {
		t.Errorf("Hinweis zu SSE-KMS fehlt: %q", p["Verschlüsselung"].Hint)
	}
}

type sesFake struct {
	good []string
	err  error
}

func (s *sesFake) Verified(_ context.Context, _, _ string) ([]string, error) {
	return s.good, s.err
}

func TestSESSender(t *testing.T) {
	f := s3fake.New()
	f.Store("mail/m1", []byte("From: a@b.de\r\n\r\nText\r\n"))
	ctx := context.Background()

	p := byName(check.Run(ctx, f, nil, &sesFake{good: []string{"support@firma.de"}},
		"test-bucket", "mail/", "support@firma.de", i18n.Get("de")))
	if !p["SES-Absender"].OK {
		t.Errorf("%+v", p["SES-Absender"])
	}
	p = byName(check.Run(ctx, f, nil, &sesFake{}, "test-bucket", "mail/", "x@y.de", i18n.Get("de")))
	if p["SES-Absender"].OK {
		t.Error("unverifizierte Adresse als in Ordnung gemeldet")
	}
	if !strings.Contains(p["SES-Absender"].Hint, "verifizieren") {
		t.Errorf("Hinweis: %q", p["SES-Absender"].Hint)
	}
	// ohne Absender: uebersprungen, nicht rot
	p = byName(check.Run(ctx, f, nil, &sesFake{}, "test-bucket", "mail/", "", i18n.Get("de")))
	if !p["SES-Absender"].Skipped {
		t.Errorf("%+v", p["SES-Absender"])
	}
}

// TestHintNamesThePrefixFirst - der haeufigste Grund fuer ein 403 beim
// Auflisten ist bei prefix-beschraenkten Zugaengen nicht das fehlende Recht,
// sondern ein Prefix eine Ebene zu weit oben. Die alte Fassung nannte genau das
// nicht und schickte den Nutzer zur IAM-Konsole statt ins Feld darueber.
func TestHintNamesThePrefixFirst(t *testing.T) {
	f := s3fake.New()
	f.ListErr = errors.New("AccessDenied")
	p := byName(check.Run(context.Background(), f, nil, nil,
		"test-bucket", "mail/ole/", "", i18n.Get("de")))

	h := p["Bucket lesen"].Hint
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
