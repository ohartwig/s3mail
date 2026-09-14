// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package mimeparse

import (
	"io"

	"github.com/emersion/go-message/charset"
)

// charsetReader passes through to go-message/charset and, for an unknown
// encoding, returns the raw stream instead of blowing the message up - the same
// way part_text() in Python falls back to utf-8 with errors="replace".
func charsetReader(name string, input io.Reader) (io.Reader, error) {
	r, err := charset.Reader(name, input)
	if err != nil {
		return input, nil
	}
	return r, nil
}
