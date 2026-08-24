// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package keyring

import (
	"bytes"
	"errors"
	"testing"
)

// This test touches the real keyring of the machine it runs on. That is the
// point: a mocked keyring would prove that the mock works.
//
// Where there is none - a container, a build runner, a headless Linux box - it
// skips. Skipping is the honest outcome there, and ErrUnavailable is exactly
// what the caller has to handle.
func TestTheKeyComesBackTheSame(t *testing.T) {
	first, err := Key()
	if errors.Is(err, ErrUnavailable) {
		t.Skip("no keyring on this machine - that is a supported state")
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != KeySize {
		t.Fatalf("key is %d bytes, expected %d", len(first), KeySize)
	}

	// A second call must not mint a new key: every new key throws away the whole
	// cache, and a program that does that on every start has no cache.
	second, err := Key()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Error("the second call returned a different key")
	}
}

// A key of zeros would be a key that looks like a key.
func TestTheKeyIsNotEmpty(t *testing.T) {
	key, err := Key()
	if errors.Is(err, ErrUnavailable) {
		t.Skip("no keyring on this machine")
	}
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(key, make([]byte, KeySize)) {
		t.Error("the key is all zeros")
	}
}
