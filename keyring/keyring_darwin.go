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

// storeCommand is the add-generic-password call, written for security's
// interactive mode: the command goes to standard input, the only argument is
// -i. That keeps the key out of argv. The process list is readable by every
// user of the machine - ps shows everybody's arguments - and the earlier
// version put the key there for the duration of the call. Short, once, and
// useless without the cache files; but THR-S3MAIL-008 promises another user
// gets nothing, and this was something (AUD-008-F03).
//
// -U updates an existing item instead of refusing; without it a second run
// after a manual deletion would fail with "already exists". security's
// interactive parser splits on white space; account, service and a base64
// key contain none.
func storeCommand(key []byte) (args []string, stdin string) {
	return []string{"-i"}, "add-generic-password -a " + account() + " -s " + Service +
		" -w " + base64.StdEncoding.EncodeToString(key) + " -U\n"
}

func store(key []byte) error {
	args, stdin := storeCommand(key)
	cmd := exec.Command("/usr/bin/security", args...)
	cmd.Stdin = strings.NewReader(stdin)
	// In interactive mode a failed command still ends the run with a non-zero
	// status - measured, not assumed: a bare add-generic-password exits 2.
	if err := cmd.Run(); err != nil {
		return ErrUnavailable
	}
	return nil
}
