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

// SES can encrypt incoming mail client-side with KMS before it lands in S3. What
// lies in the bucket then is not MIME but an envelope: the data key sits (wrapped
// by KMS) in the object metadata, the content is AES encrypted. Server-side
// encryption (SSE-S3, SSE-KMS) needs none of this - S3 undoes that itself on GET.
const (
	cseKeyV2 = "x-amz-key-v2"
	cseKeyV1 = "x-amz-key"
)

// KMS is the slice of it s3mail needs. An interface, so the tests get by without
// an account.
// ohne AWS auskommen.
type KMS interface {
	Decrypt(ciphertext []byte, context map[string]string) ([]byte, error)
}

// ErrNoKMS reports that the message is encrypted but no access exists.
var ErrNoKMS = errors.New("diese Mail ist mit KMS verschluesselt, aber es ist kein KMS-Zugriff eingerichtet")

// LowerMeta senkt alle Schluessel auf Kleinschreibung - S3 gibt Metadaten je nach
// Weg unterschiedlich zurueck.
func LowerMeta(meta map[string]string) map[string]string {
	out := make(map[string]string, len(meta))
	for k, v := range meta {
		out[strings.ToLower(k)] = v
	}
	return out
}

// IsEnvelope recognises a client-side encrypted object by its metadata.
func IsEnvelope(meta map[string]string) bool {
	m := LowerMeta(meta)
	return m[cseKeyV2] != "" || m[cseKeyV1] != ""
}

// Decrypt opens the envelope: KMS unwraps the data key, and with it the content
// is decrypted - AES-GCM for the current format, AES-CBC for the older one.
//
// The encryption context from x-amz-matdesc has to go to KMS, or KMS refuses.
func Decrypt(body []byte, meta map[string]string, kms KMS) ([]byte, error) {
	m := LowerMeta(meta)
	wrapped := m[cseKeyV2]
	if wrapped == "" {
		wrapped = m[cseKeyV1]
	}
	if wrapped == "" {
		return body, nil // not encrypted
	}
	if kms == nil {
		return nil, ErrNoKMS
	}
	raw, err := base64.StdEncoding.DecodeString(wrapped)
	if err != nil {
		return nil, fmt.Errorf("verpackter Schluessel ist kein base64: %w", err)
	}

	encContext := map[string]string{}
	if md := m["x-amz-matdesc"]; md != "" {
		_ = json.Unmarshal([]byte(md), &encContext) // a broken context: then an empty one
	}
	key, err := kms.Decrypt(raw, encContext)
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

	var plain []byte
	if strings.Contains(alg, "GCM") {
		gcm, err := cipher.NewGCMWithNonceSize(block, len(iv))
		if err != nil {
			return nil, err
		}
		plain, err = gcm.Open(nil, iv, body, nil) // Tag haengt hinten am Chiffrat
		if err != nil {
			return nil, fmt.Errorf("AES-GCM laesst sich nicht oeffnen: %w", err)
		}
	} else {
		if len(body)%aes.BlockSize != 0 || len(body) == 0 {
			return nil, errors.New("AES-CBC: Laenge ist kein Vielfaches der Blockgroesse")
		}
		plain = make([]byte, len(body))
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, body)
		plain, err = unpad(plain)
		if err != nil {
			return nil, err
		}
	}

	if want := m["x-amz-unencrypted-content-length"]; want != "" {
		if n, err := strconv.Atoi(want); err == nil && n != len(plain) {
			return nil, fmt.Errorf("entschluesselte Laenge passt nicht (%d != %d)", len(plain), n)
		}
	}
	return plain, nil
}

// unpad removes the PKCS#7 padding without losing its mind over garbage.
func unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}
	n := int(data[len(data)-1])
	if n == 0 || n > aes.BlockSize || n > len(data) {
		return data, nil // does not look like padding - leave it, the way Python does
	}
	for _, b := range data[len(data)-n:] {
		if int(b) != n {
			return data, nil
		}
	}
	return data[:len(data)-n], nil
}
