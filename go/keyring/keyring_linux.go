// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package keyring

import (
	"encoding/base64"
	"errors"
	"os/exec"
	"strings"
)

// Linux: the Secret Service, through secret-tool.
//
// Unlike macOS and Windows there is no place every system has. A desktop with
// GNOME or KDE runs a keyring daemon; a server does not, and neither does a
// container. secret-tool is the common front end to the ones that exist, and
// its absence is the honest signal that this machine has nowhere to keep a key.
//
// Talking D-Bus directly would work where a daemon runs and would need a
// dependency for the case where none does. The tool is the smaller answer.

func load() ([]byte, error) {
	if _, err := exec.LookPath("secret-tool"); err != nil {
		return nil, ErrUnavailable
	}
	out, err := exec.Command("secret-tool", "lookup", "service", Service).Output()
	if err != nil {
		if _, exit := errors.AsType[*exec.ExitError](err); exit {
			return nil, errNotFound
		}
		return nil, ErrUnavailable
	}
	if strings.TrimSpace(string(out)) == "" {
		return nil, errNotFound
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
}

func store(key []byte) error {
	if _, err := exec.LookPath("secret-tool"); err != nil {
		return ErrUnavailable
	}
	cmd := exec.Command("secret-tool", "store", "--label=s3mail cache key",
		"service", Service)
	cmd.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString(key))
	if err := cmd.Run(); err != nil {
		return ErrUnavailable
	}
	return nil
}
