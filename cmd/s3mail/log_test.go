// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestASecondStartKeepsTheFirstOnesLog - a second start that finds the port
// taken used to empty start.log before it knew why it could not start, and the
// file of the instance that held the port was gone. It has to append.
func TestASecondStartKeepsTheFirstOnesLog(t *testing.T) {
	t.Setenv("S3MAIL_CONFIG_DIR", t.TempDir())

	first := startLog()
	report(first, "first instance line")
	first.Close()

	second := appendLog()
	report(second, "second instance line")
	second.Close()

	blob, err := os.ReadFile(filepath.Join(os.Getenv("S3MAIL_CONFIG_DIR"), "start.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first instance line", "second instance line"} {
		if !strings.Contains(string(blob), want) {
			t.Errorf("start.log lost %q:\n%s", want, blob)
		}
	}
}
