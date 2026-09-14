// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// Pairing a device: the secret travels in the code, the PIN does not.
//
// The setup code used to carry no key at all, and somebody typed forty
// characters on a phone. That was defensible and nobody enjoyed it. Now the
// desktop creates the device's IAM user itself, so the key has to reach the
// phone somehow - and without a server in the middle, the only channel is the
// code on the screen.
//
// A code that carried the key in the clear would make a photograph of the
// screen equal to that device's access. So the secret is sealed with a PIN the
// desktop shows *beside* the code and never puts into it. A photograph alone is
// then a ciphertext.
//
// **What this is and is not.** Six digits is a million possibilities. The
// iteration count below makes one attempt cost about a second on a laptop,
// which is eleven days of single-core work for the whole space - and far less
// on a machine built for it, because PBKDF2-SHA256 is exactly what such
// machines are good at. This raises the bar from "a photograph is access" to "a
// photograph plus real work is access". It is not a passphrase and must not be
// described as one.
//
// What actually bounds the damage is elsewhere and cheaper: the key is minted
// when the dialog opens and revoked when it closes unused, and a device can be
// removed with one click. The PIN buys the window; the revocation closes it.

const (
	// pairingRounds: about a second on a current laptop. Deliberately not
	// higher - the phone does this too, on a colder CPU, and a person is
	// waiting.
	pairingRounds = 600_000
	pairingSalt   = 16
	pairingKey    = 32
)

// ErrWrongPIN is what a mistyped PIN produces. It says nothing about which part
// was wrong, because there is nothing useful to say: AES-GCM either
// authenticates or it does not.
var ErrWrongPIN = errors.New("wrong PIN")

// NewPIN draws six digits.
//
// crypto/rand, not math/rand: a PIN somebody could predict from the time of day
// is not a PIN. Leading zeros are kept - "004711" is a fine PIN and dropping
// the zeros would quietly shrink the space.
func NewPIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n), nil
}

// SealSecret encrypts the secret access key with the PIN. Returns the sealed
// box and the salt, both base64.
func SealSecret(secret, pin string) (sealed, salt string, err error) {
	if secret == "" {
		return "", "", errors.New("nothing to seal")
	}
	if err := checkPIN(pin); err != nil {
		return "", "", err
	}

	raw := make([]byte, pairingSalt)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	gcm, err := pairingCipher(pin, raw)
	if err != nil {
		return "", "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", "", err
	}
	// The nonce travels in front of the box. It is not secret; it must only
	// never repeat, and it does not, because it is drawn fresh for every seal.
	box := gcm.Seal(nonce, nonce, []byte(secret), nil)
	return base64.StdEncoding.EncodeToString(box),
		base64.StdEncoding.EncodeToString(raw), nil
}

// OpenSecret is the phone's side.
func OpenSecret(sealed, salt, pin string) (string, error) {
	if err := checkPIN(pin); err != nil {
		return "", err
	}
	box, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", errors.New("the code is damaged")
	}
	raw, err := base64.StdEncoding.DecodeString(salt)
	if err != nil || len(raw) != pairingSalt {
		return "", errors.New("the code is damaged")
	}
	gcm, err := pairingCipher(pin, raw)
	if err != nil {
		return "", err
	}
	if len(box) < gcm.NonceSize() {
		return "", errors.New("the code is damaged")
	}
	secret, err := gcm.Open(nil, box[:gcm.NonceSize()], box[gcm.NonceSize():], nil)
	if err != nil {
		// Every failure here is the same failure as far as anybody outside can
		// tell, and that is the point: a wrong PIN and a tampered box must not
		// be distinguishable.
		return "", ErrWrongPIN
	}
	return string(secret), nil
}

func pairingCipher(pin string, salt []byte) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, pin, salt, pairingRounds, pairingKey)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// checkPIN insists on exactly six digits.
//
// Not politeness: a PIN of a different length would derive a different key and
// fail with "wrong PIN", which sends whoever typed it looking for a typo rather
// than at the field that trimmed their input.
func checkPIN(pin string) error {
	if len(pin) != 6 {
		return errors.New("the PIN has six digits")
	}
	if strings.TrimLeft(pin, "0123456789") != "" {
		return errors.New("the PIN is digits only")
	}
	return nil
}

// What closing the setup dialog should do about the keys.
//
// The decision sits here and not in the wizard for the same reason
// DecidePush does: it has four branches, three of them are about not
// destroying something, and it was previously buried between AWS calls where
// nothing could reach it. Every branch below was a way somebody could lose
// access to their mailbox.
type PairingOutcome int

const (
	// PairingUnknown: leave everything alone. No key was handed out, or AWS
	// would not say whether it was used. Doing nothing is always safe here;
	// a leftover key is visible and fixable, a phone that went silent is not.
	PairingUnknown PairingOutcome = iota
	// PairingDone: the phone took the new key. Whatever it replaced can go,
	// which restores one key per device - the property that makes locking out
	// a lost phone a single click rather than two.
	PairingDone
	// PairingDropDevice: the key was never used and there was nothing before
	// it. The device was created for a pairing that did not happen, so it goes
	// with it - and the code on screen becomes a ciphertext for a key that no
	// longer exists.
	PairingDropDevice
	// PairingKeepPrevious: never used, but a phone was already paired. Only
	// the new key goes. This is the case that did not exist before: opening
	// the dialog used to take the working phone's key away before anybody had
	// scanned anything.
	PairingKeepPrevious
)

// Pairing is what the wizard knows when the dialog closes.
type Pairing struct {
	// Minted is the key the dialog handed out. Empty means none was.
	Minted string
	// Used is whether the phone has made a call with that key.
	Used bool
	// CanTell is false when AWS would not answer. Then Used means nothing.
	CanTell bool
	// Others counts the device's remaining keys - what it had before this
	// dialog opened.
	Others int
}

// DecidePairing answers what to do with the keys.
func DecidePairing(p Pairing) PairingOutcome {
	if p.Minted == "" || !p.CanTell {
		return PairingUnknown
	}
	if p.Used {
		return PairingDone
	}
	if p.Others == 0 {
		return PairingDropDevice
	}
	return PairingKeepPrevious
}
