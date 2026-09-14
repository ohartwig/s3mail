// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.ole-hartwig.eu/development/s3mail/s3mail/go/config"
)

// runningInstance checks whether an s3mail already answers at the remembered
// address, and returns it.
//
// The address lives in adresse.txt, token included - the same file that exists
// for the case where somebody closed the window. It is still verified: the file
// survives a crash, the port is then taken by something else, and "already
// running" would simply be a lie.
func runningInstance() (string, bool) {
	blob, err := os.ReadFile(filepath.Join(config.Dir(), "adresse.txt"))
	if err != nil {
		return "", false
	}
	url := strings.TrimSpace(strings.SplitN(string(blob), "\n", 2)[0])
	if !strings.HasPrefix(url, "http://") {
		return "", false
	}

	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	// 200 means: this is our instance and the token still matches. Anything else
	// - a 403 from a foreign service on the same port included - does not count.
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	return url, true
}
