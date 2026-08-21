package store

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeKMS struct {
	key      []byte
	kontexte []map[string]string
	fehler   error
}

func (f *fakeKMS) Decrypt(ct []byte, kontext map[string]string) ([]byte, error) {
	f.kontexte = append(f.kontexte, kontext)
	if f.fehler != nil {
		return nil, f.fehler
	}
	return f.key, nil
}

type umschlagFall struct {
	Body string            `json:"body"`
	Meta map[string]string `json:"meta"`
	Key  string            `json:"key"`
}

func umschlaegeLaden(t *testing.T) ([]byte, map[string]umschlagFall) {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join("testdata", "envelopes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Plain  string                  `json:"plain"`
		Faelle map[string]umschlagFall `json:"faelle"`
	}
	if err := json.Unmarshal(blob, &d); err != nil {
		t.Fatal(err)
	}
	plain, err := base64.StdEncoding.DecodeString(d.Plain)
	if err != nil {
		t.Fatal(err)
	}
	return plain, d.Faelle
}

func entpacken(t *testing.T, f umschlagFall) ([]byte, []byte) {
	t.Helper()
	body, err := base64.StdEncoding.DecodeString(f.Body)
	if err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(f.Key)
	if err != nil {
		t.Fatal(err)
	}
	return body, key
}

// TestUmschlagGegenPython entschluesselt Chiffrate, die die Python-Testsuite
// erzeugt hat - GCM (aktuelles Format) und CBC (aelteres).
func TestUmschlagGegenPython(t *testing.T) {
	plain, faelle := umschlaegeLaden(t)
	for name, f := range faelle {
		body, key := entpacken(t, f)
		got, err := Entschluesseln(body, f.Meta, &fakeKMS{key: key})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !bytes.Equal(got, plain) {
			t.Errorf("%s: Klartext weicht ab\n  erwartet: %q\n  bekommen: %q",
				name, plain[:60], got[:min(60, len(got))])
		}
	}
}

// TestEncryptionContext - fehlt der Context aus x-amz-matdesc, lehnt KMS ab.
// Der Fehler waere im Betrieb schwer zu finden, deshalb hier festgenagelt.
func TestEncryptionContext(t *testing.T) {
	_, faelle := umschlaegeLaden(t)
	f := faelle["cbc"]
	body, key := entpacken(t, f)
	kms := &fakeKMS{key: key}
	if _, err := Entschluesseln(body, f.Meta, kms); err != nil {
		t.Fatal(err)
	}
	if len(kms.kontexte) != 1 {
		t.Fatalf("%d KMS-Aufrufe", len(kms.kontexte))
	}
	if kms.kontexte[0]["kms_cmk_id"] == "" {
		t.Errorf("Encryption Context nicht durchgereicht: %v", kms.kontexte[0])
	}
}

func TestOhneKMSRechte(t *testing.T) {
	_, faelle := umschlaegeLaden(t)
	f := faelle["gcm"]
	body, _ := entpacken(t, f)

	if _, err := Entschluesseln(body, f.Meta, nil); !errors.Is(err, ErrKeinKMS) {
		t.Errorf("ohne KMS-Client: %v", err)
	}
	_, err := Entschluesseln(body, f.Meta, &fakeKMS{fehler: errors.New("AccessDenied")})
	if err == nil {
		t.Error("fehlendes kms:Decrypt wurde verschluckt")
	}
}

func TestUnverschluesseltGehtDurch(t *testing.T) {
	roh := []byte("From: a@b.de\r\n\r\nKlartext")
	got, err := Entschluesseln(roh, map[string]string{"foo": "bar"}, nil)
	if err != nil || !bytes.Equal(got, roh) {
		t.Errorf("unverschluesseltes Objekt wurde angefasst: %q, %v", got, err)
	}
	if IstUmschlag(map[string]string{"foo": "bar"}) {
		t.Error("IstUmschlag meldet falschen Alarm")
	}
}

func TestMetadatenGrossKleinEgal(t *testing.T) {
	_, faelle := umschlaegeLaden(t)
	f := faelle["gcm"]
	body, key := entpacken(t, f)
	gross := map[string]string{}
	for k, v := range f.Meta {
		gross[toUpperFirst(k)] = v
	}
	if !IstUmschlag(gross) {
		t.Fatal("Umschlag mit anders geschriebenen Metadaten nicht erkannt")
	}
	if _, err := Entschluesseln(body, gross, &fakeKMS{key: key}); err != nil {
		t.Errorf("Metadaten mit Grossbuchstaben: %v", err)
	}
}

func toUpperFirst(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-32) + s[1:]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
