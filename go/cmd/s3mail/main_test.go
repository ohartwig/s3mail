// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/config"
	"git.ole-hartwig.eu/development/s3mail/s3mail/web"
)

// TestTheConfiguredLanguageReachesThePage pins down what the smoke test found:
// the server takes the language out of the configuration map it hands to the
// page, and that map was built without it. A fresh browser without a cookie
// then got the mailbox in its own language instead of the chosen one - the
// setting was stored, used for the console, and ignored by the very page it was
// made on.
//
// The test goes through a real request on purpose. Checking the map alone would
// pass a renamed key on both sides while the page still came up wrong.
func TestTheConfiguredLanguageReachesThePage(t *testing.T) {
	k := config.Config{Language: "es",
		Accounts: []config.Account{{Bucket: "b", From: "post@firma.de"}}}
	srv := web.NewServer(nil, "t", "127.0.0.1", 0, serverConfig(k, 60))
	ts := httptest.NewServer(srv)
	defer ts.Close()
	srv.Port = portOfURL(ts.URL)

	body := get(t, ts, "/", "de-DE,de;q=0.9")
	if !strings.Contains(body, `lang="es"`) {
		t.Error("the page does not come up in the configured language although the browser asks for another")
	}
}

// TestTheWizardIsNotLeftOut - before the setup there is no mailbox and thus no
// mailbox configuration. If the language is missing there, the wizard of all
// things comes up in the wrong one: the single place where the reader has not
// chosen anything yet.
func TestTheWizardIsNotLeftOut(t *testing.T) {
	srv := web.NewServer(nil, "t", "127.0.0.1", 0, map[string]any{"language": "es"})
	ts := httptest.NewServer(srv)
	defer ts.Close()
	srv.Port = portOfURL(ts.URL)

	if !strings.Contains(get(t, ts, "/", "de-DE,de;q=0.9"), `lang="es"`) {
		t.Error("the wizard ignores the configured language")
	}
}

func get(t *testing.T, ts *httptest.Server, path, acceptLanguage string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	req.Header.Set("X-S3mail-Token", "t")
	req.Header.Set("Accept-Language", acceptLanguage)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64<<10)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}

func portOfURL(u string) int {
	parts := strings.Split(u, ":")
	p := 0
	for _, c := range parts[len(parts)-1] {
		p = p*10 + int(c-'0')
	}
	return p
}
