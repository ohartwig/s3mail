// SPDX-FileCopyrightText: 2026 Lei Wang
// SPDX-License-Identifier: Apache-2.0

package mimeparse

import (
	"testing"
	"time"
)

// Without a Date header the topmost Received time is used, not the fallback.
func TestDateFromReceived(t *testing.T) {
	raw := "Received: from mx.example.com by mx.example.org with SMTP id abc;\r\n" +
		" Sat, 14 Nov 2026 05:43:38 +0000 (UTC)\r\n" +
		"Received: from sender.example.net by mx.example.com;\r\n" +
		" Fri, 13 Nov 2026 01:00:00 +0000\r\n" +
		"From: team@example.com\r\nTo: someone@example.org\r\nSubject: hi\r\n\r\nbody\r\n"
	zero := time.Unix(0, 0).UTC()
	if got := Read([]byte(raw), zero).Date; got != "2026-11-14T05:43:38Z" {
		t.Errorf("Read date = %q, want the topmost Received time", got)
	}
	if got := Summarize([]byte(raw), zero).Date; got != "2026-11-14T05:43:38Z" {
		t.Errorf("Summarize date = %q, want the topmost Received time", got)
	}
}

// A Date header wins over Received.
func TestDateHeaderPreferred(t *testing.T) {
	raw := "Received: by mx.example.org; Sat, 14 Nov 2026 05:43:38 +0000\r\n" +
		"From: a@example.com\r\nDate: Fri, 13 Nov 2026 01:00:00 +0000\r\nSubject: x\r\n\r\nbody\r\n"
	if got := Read([]byte(raw), time.Time{}).Date; got != "2026-11-13T01:00:00Z" {
		t.Errorf("date = %q", got)
	}
}

// Neither Date nor a usable Received: the fallback, as before.
func TestDateFallback(t *testing.T) {
	raw := "Received: by mx.example.org with no date\r\nFrom: a@example.com\r\nSubject: x\r\n\r\nbody\r\n"
	fb := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := Read([]byte(raw), fb).Date; got != "2000-01-01T00:00:00Z" {
		t.Errorf("date = %q", got)
	}
}
