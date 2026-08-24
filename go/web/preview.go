// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package web

import "strings"

// Which attachments may be shown instead of only downloaded.
//
// This is a security decision, not a convenience one. An attachment is a file a
// stranger sent, and showing it means the browser parses it inside s3mail's own
// origin - the origin that holds the token cookie. So the list is a whitelist
// of formats that cannot carry code, and it stays short.
//
// SVG is deliberately not on it. It looks like an image, it is XML with script
// support, and it is the classic way to turn "just a picture" into JavaScript
// in somebody else's page. Whoever wants to see one downloads it.
//
// The headers on the answer are the second half of the same decision: a CSP
// with `sandbox` puts the response into an opaque origin, so even a format that
// turns out to carry something cannot reach the cookie or call the API. The
// page shows it in a sandboxed iframe on top of that.
func previewable(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp",
		"application/pdf":
		return true
	}
	return false
}
