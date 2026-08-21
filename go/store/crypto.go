package store

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// SES kann eingehende Mail client-seitig mit KMS verschluesseln, bevor sie in S3
// landet. Dann liegt im Bucket kein MIME, sondern ein Umschlag: der Datenschluessel
// steckt (von KMS verpackt) in den Objekt-Metadaten, der Inhalt ist AES-verschluesselt.
// Serverseitige Verschluesselung (SSE-S3, SSE-KMS) braucht davon nichts - die macht
// S3 beim GET selbst rueckgaengig.
const (
	cseKeyV2 = "x-amz-key-v2"
	cseKeyV1 = "x-amz-key"
)

// KMS ist der Ausschnitt, den s3mail braucht. Als Schnittstelle, damit die Tests
// ohne AWS auskommen.
type KMS interface {
	Decrypt(ciphertext []byte, context map[string]string) ([]byte, error)
}

// ErrKeinKMS meldet, dass die Mail verschluesselt ist, aber kein Zugriff besteht.
var ErrKeinKMS = errors.New("diese Mail ist mit KMS verschluesselt, aber es ist kein KMS-Zugriff eingerichtet")

// KleineMeta senkt alle Schluessel auf Kleinschreibung - S3 gibt Metadaten je nach
// Weg unterschiedlich zurueck.
func KleineMeta(meta map[string]string) map[string]string {
	out := make(map[string]string, len(meta))
	for k, v := range meta {
		out[strings.ToLower(k)] = v
	}
	return out
}

// IstUmschlag erkennt ein client-seitig verschluesseltes Objekt an den Metadaten.
func IstUmschlag(meta map[string]string) bool {
	m := KleineMeta(meta)
	return m[cseKeyV2] != "" || m[cseKeyV1] != ""
}

// Entschluesseln macht den Umschlag auf: KMS entpackt den Datenschluessel, damit
// wird der Inhalt entschluesselt - AES-GCM beim aktuellen Format, AES-CBC beim
// aelteren.
//
// Der Encryption Context aus x-amz-matdesc muss an KMS mit, sonst lehnt KMS ab.
func Entschluesseln(body []byte, meta map[string]string, kms KMS) ([]byte, error) {
	m := KleineMeta(meta)
	verpackt := m[cseKeyV2]
	if verpackt == "" {
		verpackt = m[cseKeyV1]
	}
	if verpackt == "" {
		return body, nil // nicht verschluesselt
	}
	if kms == nil {
		return nil, ErrKeinKMS
	}
	roh, err := base64.StdEncoding.DecodeString(verpackt)
	if err != nil {
		return nil, fmt.Errorf("verpackter Schluessel ist kein base64: %w", err)
	}

	kontext := map[string]string{}
	if md := m["x-amz-matdesc"]; md != "" {
		_ = json.Unmarshal([]byte(md), &kontext) // kaputter Context: dann eben leer
	}
	key, err := kms.Decrypt(roh, kontext)
	if err != nil {
		return nil, err
	}
	iv, err := base64.StdEncoding.DecodeString(m["x-amz-iv"])
	if err != nil {
		return nil, fmt.Errorf("iv ist kein base64: %w", err)
	}

	alg := strings.ToUpper(m["x-amz-cek-alg"])
	if alg == "" {
		alg = "AES/CBC/PKCS5PADDING"
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	var klar []byte
	if strings.Contains(alg, "GCM") {
		gcm, err := cipher.NewGCMWithNonceSize(block, len(iv))
		if err != nil {
			return nil, err
		}
		klar, err = gcm.Open(nil, iv, body, nil) // Tag haengt hinten am Chiffrat
		if err != nil {
			return nil, fmt.Errorf("AES-GCM laesst sich nicht oeffnen: %w", err)
		}
	} else {
		if len(body)%aes.BlockSize != 0 || len(body) == 0 {
			return nil, errors.New("AES-CBC: Laenge ist kein Vielfaches der Blockgroesse")
		}
		klar = make([]byte, len(body))
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(klar, body)
		klar, err = entpolstern(klar)
		if err != nil {
			return nil, err
		}
	}

	if want := m["x-amz-unencrypted-content-length"]; want != "" {
		if n, err := strconv.Atoi(want); err == nil && n != len(klar) {
			return nil, fmt.Errorf("entschluesselte Laenge passt nicht (%d != %d)", len(klar), n)
		}
	}
	return klar, nil
}

// entpolstern entfernt die PKCS#7-Polsterung, ohne bei Murks durchzudrehen.
func entpolstern(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}
	n := int(data[len(data)-1])
	if n == 0 || n > aes.BlockSize || n > len(data) {
		return data, nil // sieht nicht nach Polsterung aus - so lassen, wie Python
	}
	for _, b := range data[len(data)-n:] {
		if int(b) != n {
			return data, nil
		}
	}
	return data[:len(data)-n], nil
}
