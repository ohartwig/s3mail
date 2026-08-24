// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package keyring keeps one secret: the key the local cache is encrypted with.
//
// The cache holds message bodies, and s3mail decrypts client-side encrypted
// mail before writing them - so a plain cache quietly undoes the encryption the
// bucket was set up with. File permissions do not help against the actual
// threat, which is a backup, a synced folder, or a disk without full-disk
// encryption: all three copy the whole home directory, permissions included.
//
// Hence a key that does not live in the home directory. Every platform has one
// place for that, and they are all different:
//
//	macOS    the login keychain, through /usr/bin/security
//	Windows  DPAPI - the key is stored encrypted to the Windows account, and a
//	         copy of the file is useless on another machine or for another user
//	Linux    the Secret Service, through secret-tool, when one is installed
//
// Where none of that exists, this package says so and says nothing else. What
// happens then is a decision for the caller, and s3mail makes it a decision for
// the reader rather than quietly writing plain text.
package keyring

import (
	"crypto/rand"
	"errors"
)

// ErrUnavailable means there is no place to keep a key on this machine. It is
// not a failure - a headless Linux box without a keyring is a normal machine -
// but it is also not something to paper over.
var ErrUnavailable = errors.New("no keyring on this system")

// KeySize is the AES-256 key length.
const KeySize = 32

// Service is the name the key is filed under. One key per machine, not per
// mailbox: the cache directory is shared, and a second key would only mean a
// second thing to lose.
const Service = "s3mail-cache"

// Key returns the cache key, creating one on first use.
func Key() ([]byte, error) {
	if existing, err := load(); err == nil && len(existing) == KeySize {
		return existing, nil
	} else if err != nil && !errors.Is(err, errNotFound) {
		return nil, err
	}
	fresh := make([]byte, KeySize)
	if _, err := rand.Read(fresh); err != nil {
		return nil, err
	}
	if err := store(fresh); err != nil {
		return nil, err
	}
	return fresh, nil
}

// errNotFound separates "there is no key yet" from "there is no keyring".
var errNotFound = errors.New("no key stored yet")
