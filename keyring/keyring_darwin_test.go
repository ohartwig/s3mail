// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package keyring

import (
	"encoding/base64"
	"strings"
	"testing"
)

// The key must not stand in argv: ps shows every user's process arguments on
// macOS, and the threat model promises another user of the machine nothing.
// It goes to security's standard input instead (AUD-008-F03).
func TestTheKeyIsNotInTheArguments(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	encoded := base64.StdEncoding.EncodeToString(key)

	args, stdin := storeCommand(key)

	for _, a := range args {
		if strings.Contains(a, encoded) || strings.Contains(a, string(key)) {
			t.Fatalf("the key is in argv: %q", args)
		}
	}
	if len(args) != 1 || args[0] != "-i" {
		t.Errorf("not the interactive mode: %q", args)
	}
	if !strings.Contains(stdin, "add-generic-password") || !strings.Contains(stdin, encoded) {
		t.Errorf("the command on stdin is incomplete: %q", stdin)
	}
	if !strings.Contains(stdin, " -s "+Service+" ") || !strings.Contains(stdin, " -U") {
		t.Errorf("service or -U missing: %q", stdin)
	}
	if !strings.HasSuffix(stdin, "\n") {
		t.Errorf("security -i reads lines; the command has no line end: %q", stdin)
	}
}
