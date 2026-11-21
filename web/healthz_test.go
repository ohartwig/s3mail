// SPDX-FileCopyrightText: 2026 Lei Wang
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"net/http/httptest"
	"testing"
)

// TestHealthz - the probe answers without a token and gives away nothing; every
// other path still wants one.
func TestHealthz(t *testing.T) {
	srv := NewServer(nil, testToken, "127.0.0.1", 0, map[string]any{})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	srv.Port = portOf(ts.URL)

	noToken := map[string]string{"X-S3mail-Token": ""}
	r := callServer(t, ts, "GET", "/healthz", "", noToken)
	if r.Code != 200 || string(r.Body) != "ok\n" {
		t.Errorf("/healthz: HTTP %d %q", r.Code, r.Body)
	}
	if r := callServer(t, ts, "HEAD", "/healthz", "", noToken); r.Code != 200 {
		t.Errorf("HEAD /healthz: HTTP %d", r.Code)
	}
	for _, path := range []string{"/", "/api/overview", "/healthz/x", "/healthzz"} {
		if r := callServer(t, ts, "GET", path, "", noToken); r.Code != 403 {
			t.Errorf("%s without a token: HTTP %d, want 403", path, r.Code)
		}
	}
	if r := callServer(t, ts, "POST", "/healthz", "{}", noToken); r.Code != 403 {
		t.Errorf("POST /healthz without a token: HTTP %d, want 403", r.Code)
	}
}
