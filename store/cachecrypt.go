// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

// The cache on disk, encrypted.
//
// What lies there is not metadata: the body cache holds whole messages, and
// s3mail decrypts client-side encrypted mail before writing them. A plain cache
// therefore quietly undoes the encryption the bucket was set up with, and file
// permissions do not help - a backup, a synced folder and an unencrypted disk
// all copy the home directory with its permissions intact.
//
// AES-256-GCM, one random nonce per file, prepended. GCM and not CTR because a
// cache that can be edited by whoever can write the file is a cache that can be
// used to feed the parser something - authentication is not a luxury here.
//
// A key that no longer fits is not an error worth reporting upwards: it means
// the keyring gave out a new key, or the file came from another machine. The
// cache is a cache. It gets thrown away and rebuilt, exactly as it is when the
// index version has moved on.

var errCacheKey = errors.New("cache cannot be decrypted with this key")

func seal(key, plain []byte) ([]byte, error) {
	if len(key) == 0 {
		return plain, nil // plain text was asked for, deliberately
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

func open(key, blob []byte) ([]byte, error) {
	if len(key) == 0 {
		return blob, nil
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(blob) < gcm.NonceSize() {
		return nil, errCacheKey
	}
	out, err := gcm.Open(nil, blob[:gcm.NonceSize()], blob[gcm.NonceSize():], nil)
	if err != nil {
		return nil, errCacheKey
	}
	return out, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
