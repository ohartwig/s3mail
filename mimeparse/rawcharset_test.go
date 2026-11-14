// SPDX-FileCopyrightText: 2026 Lei Wang
// SPDX-License-Identifier: Apache-2.0

package mimeparse

import (
	"testing"
	"time"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func gbk(t *testing.T, s string) string {
	t.Helper()
	out, err := simplifiedchinese.GBK.NewEncoder().String(s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A QQ-style message: GBK bytes straight in Subject and From (no RFC 2047),
// the charset only in the top-level Content-Type, declared as gb2312.
func TestRawGBKHeaders(t *testing.T) {
	raw := "From: \"" + gbk(t, "微信团队") + "\" <Team@example.com>\r\n" +
		"To: someone@example.org\r\n" +
		"Subject:" + gbk(t, "激活你的微信公众平台帐号") + "\r\n" +
		"Date: Sat, 14 Nov 2026 05:43:38 +0000\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/html;\r\n\tcharset=\"gb2312\"\r\n" +
		"\r\n<p>hi</p>\r\n"

	v := Read([]byte(raw), time.Time{})
	if v.Subject != "激活你的微信公众平台帐号" {
		t.Errorf("Read subject = %q", v.Subject)
	}
	if v.From != "微信团队 <Team@example.com>" {
		t.Errorf("Read from = %q", v.From)
	}

	s := Summarize([]byte(raw), time.Time{})
	if s.Subject != "激活你的微信公众平台帐号" || s.From != "微信团队 <Team@example.com>" {
		t.Errorf("Summarize = %q / %q", s.Subject, s.From)
	}
	if s.FromAddr != "team@example.com" {
		t.Errorf("FromAddr = %q", s.FromAddr)
	}
}

// The charset may only be declared on a body part of a multipart message.
func TestRawHeaderCharsetFromPart(t *testing.T) {
	raw := "From: a@example.com\r\nSubject: " + gbk(t, "你好") + "\r\nDate: Fri, 13 Nov 2026 01:00:00 +0000\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain; charset=GBK\r\n\r\nx\r\n--b--\r\n"
	if got := Read([]byte(raw), time.Time{}).Subject; got != "你好" {
		t.Errorf("subject = %q", got)
	}
}

// Western mail keeps working: raw Latin-1 bytes with a Latin-1 charset, and
// raw bytes with no declared charset still go through the windows-1252 repair.
func TestRawLatinHeadersUnchanged(t *testing.T) {
	latin, _ := charmap.ISO8859_1.NewEncoder().String("Grüße")
	for _, ct := range []string{"Content-Type: text/plain; charset=iso-8859-1\r\n", ""} {
		raw := "From: a@example.com\r\nSubject: " + latin + "\r\nDate: Fri, 13 Nov 2026 01:00:00 +0000\r\n" +
			ct + "\r\nbody\r\n"
		if got := Read([]byte(raw), time.Time{}).Subject; got != "Grüße" {
			t.Errorf("content-type %q: subject = %q", ct, got)
		}
	}
}

// Encoded words inside a quoted display name are decoded, as mail clients do.
func TestQuotedEncodedWordName(t *testing.T) {
	raw := "From: \"=?utf-8?B?5b6u5L+h5Zui6Zif?=\" <weixinmphelper@tencent.com>\r\n" +
		"To: \"=?utf-8?B?d3guc2VydmljZQ==?=\" <service@example.org>\r\n" +
		"Subject: =?utf-8?B?5b6u5L+h?=\r\nDate: Sat, 13 Jul 2024 18:48:55 +0800\r\n\r\nbody\r\n"
	v := Read([]byte(raw), time.Time{})
	if v.From != "微信团队 <weixinmphelper@tencent.com>" {
		t.Errorf("from = %q", v.From)
	}
	if v.To != "wx.service <service@example.org>" {
		t.Errorf("to = %q", v.To)
	}
	// A literal "=?" in an ordinary quoted name stays as written.
	if got := AddrStr(`"a =? b" <a@example.com>`); got != "a =? b <a@example.com>" {
		t.Errorf("plain name = %q", got)
	}
}
