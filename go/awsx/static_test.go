// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx_test

import (
	"os"
	"path/filepath"
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/go/awsx"
)

// Static exists for the case where there is no ~/.aws to read: a phone. So the
// one property that matters is that it reads nothing - not a profile, not the
// environment, not a file somebody left behind.
func TestStaticReadsNothingFromTheSurroundings(t *testing.T) {
	// A credentials file that would win if anything looked at it.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "credentials"),
		[]byte("[default]\naws_access_key_id = AUS_DER_DATEI\naws_secret_access_key = falsch\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_ACCESS_KEY_ID", "AUS_DER_UMGEBUNG")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "auch falsch")
	t.Setenv("AWS_PROFILE", "gibt-es-nicht")

	cfg, err := awsx.Static(t.Context(), "UEBERGEBEN", "geheim", "eu-north-1")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := cfg.Credentials.Retrieve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessKeyID != "UEBERGEBEN" {
		t.Errorf("used %q - something from the surroundings won", creds.AccessKeyID)
	}
	if cfg.Region != "eu-north-1" {
		t.Errorf("region is %q", cfg.Region)
	}
}

// Nothing passed means an error, not a silent fallback to whatever is lying
// around. On a phone there is nothing to fall back to, and on a desktop falling
// back would be the surprise.
func TestStaticRefusesEmptyCredentials(t *testing.T) {
	cases := []struct{ name, key, secret, region string }{
		{"no key", "", "geheim", "eu-north-1"},
		{"no secret", "AKIA", "", "eu-north-1"},
		{"no region", "AKIA", "geheim", ""},
	}
	for _, c := range cases {
		if _, err := awsx.Static(t.Context(), c.key, c.secret, c.region); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}
