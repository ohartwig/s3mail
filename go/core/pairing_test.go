// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"errors"
	"strings"
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/core"
)

const secret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"

func TestWhatIsSealedComesBack(t *testing.T) {
	sealed, salt, err := core.SealSecret(secret, "004711")
	if err != nil {
		t.Fatal(err)
	}
	got, err := core.OpenSecret(sealed, salt, "004711")
	if err != nil {
		t.Fatal(err)
	}
	if got != secret {
		t.Errorf("got %q", got)
	}
}

// The whole point of the exercise: the code alone is not the key.
func TestTheSealedBoxDoesNotContainTheSecret(t *testing.T) {
	sealed, salt, err := core.SealSecret(secret, "123456")
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{sealed, salt} {
		if strings.Contains(part, secret) {
			t.Fatal("the secret is legible in the code")
		}
		// Also not in fragments long enough to matter.
		if strings.Contains(part, secret[:12]) {
			t.Fatal("part of the secret is legible in the code")
		}
	}
}

func TestAWrongPINDoesNotOpenIt(t *testing.T) {
	sealed, salt, _ := core.SealSecret(secret, "123456")
	if _, err := core.OpenSecret(sealed, salt, "123457"); !errors.Is(err, core.ErrWrongPIN) {
		t.Errorf("expected ErrWrongPIN, got %v", err)
	}
}

// A tampered box and a wrong PIN have to look the same from outside. If they
// did not, an attacker could tell "right PIN, damaged box" from "wrong PIN" and
// would have a way to test PINs faster than by trying them.
func TestTamperingLooksLikeAWrongPIN(t *testing.T) {
	sealed, salt, _ := core.SealSecret(secret, "123456")
	// Flip one character in the middle of the box.
	broken := []byte(sealed)
	i := len(broken) / 2
	if broken[i] == 'A' {
		broken[i] = 'B'
	} else {
		broken[i] = 'A'
	}
	_, err := core.OpenSecret(string(broken), salt, "123456")
	if err == nil {
		t.Fatal("a tampered box opened")
	}
	if !errors.Is(err, core.ErrWrongPIN) {
		t.Errorf("tampering is distinguishable from a wrong PIN: %v", err)
	}
}

// Two seals of the same secret with the same PIN must differ - otherwise the
// salt or the nonce is not doing its job, and two codes side by side would
// betray that they carry the same key.
func TestTwoSealsAreNotAlike(t *testing.T) {
	a, saltA, _ := core.SealSecret(secret, "123456")
	b, saltB, _ := core.SealSecret(secret, "123456")
	if a == b {
		t.Error("two seals are identical - the nonce repeats")
	}
	if saltA == saltB {
		t.Error("two salts are identical")
	}
}

// Leading zeros are a fifth of the PIN space. Dropping them would shrink it
// quietly.
func TestAPINWithLeadingZerosWorks(t *testing.T) {
	sealed, salt, err := core.SealSecret(secret, "000042")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.OpenSecret(sealed, salt, "000042"); err != nil {
		t.Errorf("a PIN with leading zeros did not open its own box: %v", err)
	}
}

func TestAPINHasSixDigits(t *testing.T) {
	for _, bad := range []string{"12345", "1234567", "", "12345a", "12 456"} {
		if _, _, err := core.SealSecret(secret, bad); err == nil {
			t.Errorf("%q was accepted as a PIN", bad)
		}
	}
}

// The generated PIN has to be six characters every time, including the runs
// that land below 100000.
func TestGeneratedPINsAreAlwaysSixDigits(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		pin, err := core.NewPIN()
		if err != nil {
			t.Fatal(err)
		}
		if len(pin) != 6 || strings.TrimLeft(pin, "0123456789") != "" {
			t.Fatalf("%q is not six digits", pin)
		}
		seen[pin] = true
	}
	// Not a randomness test - just the mistake of a constant PIN.
	if len(seen) < 150 {
		t.Errorf("only %d different PINs in 200 draws", len(seen))
	}
}
