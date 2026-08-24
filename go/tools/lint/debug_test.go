// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The debug log must never carry mail.
//
// awsx/debug.go is careful about it, but care is not a guarantee - the next
// person adding a line there has to be stopped by something other than a
// comment. Two things are checked, and both are about the same mistake:
//
//   - The SDK's own request logging (ClientLogMode with LogRequestWithBody, or
//     LogResponseWithBody) dumps whole requests. For this program a request body
//     is somebody's mail, and the response body is somebody else's.
//   - A debug line that formats a body or a raw message.
func TestTheDebugLogCannotCarryMail(t *testing.T) {
	root := moduleRoot(t)

	// Assembled rather than written out: this file would otherwise trip its own
	// check, and a guard that has to exclude itself by name is one bad rename
	// away from excluding something else.
	forbidden := []string{"LogRequest" + "WithBody", "LogResponse" + "WithBody"}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		blob, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(blob)
		for _, bad := range forbidden {
			// The comment in debug.go names them on purpose, to say why they are
			// not used. A comment is not a call.
			for _, line := range strings.Split(text, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") {
					continue
				}
				if strings.Contains(line, bad) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s uses %s - that writes mail into the log", rel, bad)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// And the debug lines themselves: none of them may format something that is
	// a body.
	blob, err := os.ReadFile(filepath.Join(root, "awsx", "debug.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(blob), "\n") {
		if !strings.Contains(line, "debugf(") {
			continue
		}
		for _, bad := range []string{"body", "Body", "raw", "Raw", "out.Body"} {
			if strings.Contains(line, bad) && !strings.Contains(line, "len(") {
				t.Errorf("a debug line passes %s: %s", bad, strings.TrimSpace(line))
			}
		}
	}
}
