// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"strings"
	"testing"

	"s3mail/s3fake"
)

// Which attachments may be rendered inside s3mail's own origin. Every entry is
// a decision to let a stranger's file be parsed by the browser next to the
// token cookie, so the list is a whitelist and it stays short.
func TestPreviewableIsAWhitelist(t *testing.T) {
	yes := []string{
		"image/png", "image/jpeg", "IMAGE/JPEG", "image/gif", "image/webp",
		"application/pdf", "image/png; name=x.png",
	}
	for _, ct := range yes {
		if !previewable(ct) {
			t.Errorf("%q should be previewable", ct)
		}
	}

	// The interesting half. SVG looks like an image and is XML with script
	// support - the classic way to turn "just a picture" into JavaScript in
	// somebody else's page.
	no := []string{
		"image/svg+xml", "text/html", "application/xhtml+xml", "text/xml",
		"application/javascript", "application/ms-tnef", "application/zip",
		"application/octet-stream", "", "text/plain",
	}
	for _, ct := range no {
		if previewable(ct) {
			t.Errorf("%q must not be previewable", ct)
		}
	}
}

// The page has to carry the way to the preview, and the sandbox with it.
func TestThePageCarriesThePreview(t *testing.T) {
	for _, anchor := range []string{
		"showPreview", `id="preview"`, `sandbox=""`, "&view=1",
	} {
		if !strings.Contains(PageMailbox, anchor) {
			t.Errorf("the mailbox page has lost %q", anchor)
		}
	}
	// The whitelist in the page must not be wider than the one on the server.
	if strings.Contains(PageMailbox, "image/svg") {
		t.Error("the page offers a preview for SVG")
	}
}

// The route itself: an image comes back to be shown, locked down; anything
// else comes back as a download no matter what the caller asked for.
func TestTheRouteOnlyShowsWhatItMay(t *testing.T) {
	f := s3fake.New()
	f.Store("mail/m9", []byte("From: a@b.de\r\nTo: post@firma.de\r\nSubject: Anhang\r\n"+
		"Date: Mon, 03 Aug 2026 09:00:00 +0000\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: multipart/mixed; boundary=\"B\"\r\n\r\n"+
		"--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nText.\r\n\r\n"+
		"--B\r\nContent-Type: image/png; name=\"bild.png\"\r\n"+
		"Content-Transfer-Encoding: base64\r\n"+
		"Content-Disposition: attachment; filename=\"bild.png\"\r\n\r\n"+
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAAAAAA6fptVAAAACklEQVR4nGP4DwABAQEAGXreZgAAAABJRU5ErkJggg==\r\n\r\n"+
		"--B\r\nContent-Type: image/svg+xml; name=\"bild.svg\"\r\n"+
		"Content-Disposition: attachment; filename=\"bild.svg\"\r\n\r\n"+
		"<svg xmlns=\"http://www.w3.org/2000/svg\"><script>alert(1)</script></svg>\r\n"+
		"--B--\r\n"))
	ts, _, mb := serverWith(t, f)
	if _, err := mb.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	png := callServer(t, ts, "GET", "/api/attachment?key=mail/m9&index=1&view=1", "", nil)
	if got := png.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "inline") {
		t.Errorf("the image does not come back to be shown: %q", got)
	}
	if csp := png.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Errorf("the image comes back without a sandbox: %q", csp)
	}

	svg := callServer(t, ts, "GET", "/api/attachment?key=mail/m9&index=2&view=1", "", nil)
	if got := svg.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Errorf("an SVG was shown instead of downloaded: %q", got)
	}
}

// Addresses come out of the index, and the field has to be able to ask.
func TestAddressesAreOffered(t *testing.T) {
	ts, _ := buildServer(t)
	d := callServer(t, ts, "GET", "/api/addresses?q=anna", "", nil).json(t)
	list, _ := d["addresses"].([]any)
	if len(list) == 0 {
		t.Fatalf("no address found: %v", d)
	}
	first, _ := list[0].(map[string]any)
	if !strings.Contains(first["label"].(string), "@") {
		t.Errorf("the label is not usable in the field: %v", first)
	}
}

func TestThePageAsksForAddresses(t *testing.T) {
	for _, anchor := range []string{"/api/addresses", `id="addrbook"`, "wireAddresses"} {
		if !strings.Contains(PageMailbox, anchor) {
			t.Errorf("the mailbox page has lost %q", anchor)
		}
	}
}
