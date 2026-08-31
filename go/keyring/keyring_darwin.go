// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package keyring

import (
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

// macOS: the login keychain, through the security tool that ships with the
// system. Shelling out rather than binding the Security framework, because the
// framework needs CGO - and CGO would end cross compilation, which is the
// reason this program can be built for four platforms from one machine.
//
// The keychain is part of a Time Machine backup, but encrypted with the login
// password. That is the difference that matters here: a copied home directory
// hands over the cache, not the key.

// account is the second half of the item's identity. security insists on it for
// add-generic-password, and an item filed under the wrong account is an item
// that cannot be found again.
func account() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	return "s3mail"
}

func load() ([]byte, error) {
	out, err := exec.Command("/usr/bin/security", "find-generic-password",
		"-a", account(), "-s", Service, "-w").Output()
	if err != nil {
		if _, exit := errors.AsType[*exec.ExitError](err); exit {
			return nil, errNotFound // item is simply not there yet
		}
		return nil, ErrUnavailable
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
}

func store(key []byte) error {
	// -U updates an existing item instead of refusing; without it a second run
	// after a manual deletion would fail with "already exists".
	//
	// The key stands in argv for the moment this runs, and security says so
	// itself ("Use of the -p or -w options is insecure"). The alternative is a
	// prompt on a terminal, which a program that starts by double click does not
	// have. It is the same exposure the threat model already concedes under
	// assumption 1: a process of the same user can read the start address out of
	// the configuration directory anyway. Against another user, against a backup
	// and against a synced folder - the cases this key exists for - it changes
	// nothing.
	err := exec.Command("/usr/bin/security", "add-generic-password",
		"-a", account(), "-s", Service,
		"-w", base64.StdEncoding.EncodeToString(key), "-U").Run()
	if err != nil {
		return ErrUnavailable
	}
	return nil
}
