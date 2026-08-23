package store_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"s3mail/store"
)

type fakeKMS struct {
	key      []byte
	contexts []map[string]string
	err      error
}

func (f *fakeKMS) Decrypt(ct []byte, encContext map[string]string) ([]byte, error) {
	f.contexts = append(f.contexts, encContext)
	if f.err != nil {
		return nil, f.err
	}
	return f.key, nil
}

type envelopeCase struct {
	Body string            `json:"body"`
	Meta map[string]string `json:"meta"`
	Key  string            `json:"key"`
}

func loadEnvelopes(t *testing.T) ([]byte, map[string]envelopeCase) {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join("testdata", "envelopes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Plain string                  `json:"plain"`
		Cases map[string]envelopeCase `json:"faelle"`
	}
	if err := json.Unmarshal(blob, &d); err != nil {
		t.Fatal(err)
	}
	plain, err := base64.StdEncoding.DecodeString(d.Plain)
	if err != nil {
		t.Fatal(err)
	}
	return plain, d.Cases
}

func unpack(t *testing.T, f envelopeCase) ([]byte, []byte) {
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

// TestEnvelopeAgainstPython entschluesselt Chiffrate, die die Python-Testsuite
// erzeugt hat - GCM (aktuelles Format) und CBC (aelteres).
func TestEnvelopeAgainstPython(t *testing.T) {
	plain, cases := loadEnvelopes(t)
	for name, f := range cases {
		body, key := unpack(t, f)
		got, err := store.Decrypt(body, f.Meta, &fakeKMS{key: key})
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

// TestEncryptionContext - fehlt der Context aus x-amz-matdesc, lehnt store.KMS ab.
// Der Fehler waere im Betrieb schwer zu finden, deshalb hier festgenagelt.
func TestEncryptionContext(t *testing.T) {
	_, cases := loadEnvelopes(t)
	f := cases["cbc"]
	body, key := unpack(t, f)
	kms := &fakeKMS{key: key}
	if _, err := store.Decrypt(body, f.Meta, kms); err != nil {
		t.Fatal(err)
	}
	if len(kms.contexts) != 1 {
		t.Fatalf("%d store.KMS-Aufrufe", len(kms.contexts))
	}
	if kms.contexts[0]["kms_cmk_id"] == "" {
		t.Errorf("Encryption Context nicht durchgereicht: %v", kms.contexts[0])
	}
}

func TestWithoutKMSPermissions(t *testing.T) {
	_, cases := loadEnvelopes(t)
	f := cases["gcm"]
	body, _ := unpack(t, f)

	if _, err := store.Decrypt(body, f.Meta, nil); !errors.Is(err, store.ErrNoKMS) {
		t.Errorf("ohne store.KMS-Client: %v", err)
	}
	_, err := store.Decrypt(body, f.Meta, &fakeKMS{err: errors.New("AccessDenied")})
	if err == nil {
		t.Error("fehlendes kms:Decrypt wurde verschluckt")
	}
}

func TestUnencryptedPassesThrough(t *testing.T) {
	raw := []byte("From: a@b.de\r\n\r\nKlartext")
	got, err := store.Decrypt(raw, map[string]string{"foo": "bar"}, nil)
	if err != nil || !bytes.Equal(got, raw) {
		t.Errorf("unverschluesseltes Objekt wurde angefasst: %q, %v", got, err)
	}
	if store.IsEnvelope(map[string]string{"foo": "bar"}) {
		t.Error("IstUmschlag meldet falschen Alarm")
	}
}

func TestMetadataCaseInsensitive(t *testing.T) {
	_, cases := loadEnvelopes(t)
	f := cases["gcm"]
	body, key := unpack(t, f)
	big := map[string]string{}
	for k, v := range f.Meta {
		big[toUpperFirst(k)] = v
	}
	if !store.IsEnvelope(big) {
		t.Fatal("Umschlag mit anders geschriebenen Metadaten nicht erkannt")
	}
	if _, err := store.Decrypt(body, big, &fakeKMS{key: key}); err != nil {
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
