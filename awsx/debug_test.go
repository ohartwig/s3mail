// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ohartwig/s3mail/awsx"
	"github.com/ohartwig/s3mail/s3fake"
)

// The one property of the debug log: it says what was called and never what was
// in it. Somebody debugging a 403 does not need anybody's mail in a file, and
// the moment they are debugging is the moment they least expect it there.
func TestTheDebugLogCarriesNoMail(t *testing.T) {
	geheim := "Vertraulich: die Bankverbindung lautet DE12"
	f := s3fake.New()
	f.Store("mail/m1", []byte("From: a@b.de\r\nTo: post@firma.de\r\n"+
		"Subject: Kontodaten\r\n\r\n"+geheim+"\r\n"))

	var log bytes.Buffer
	awsx.SetDebug(&log)
	t.Cleanup(func() { awsx.SetDebug(nil) })

	ctx := t.Context()
	client := awsx.WrapForDebug(f)
	if _, err := client.List(ctx, "test-bucket", "mail/"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(ctx, "test-bucket", "mail/m1", ""); err != nil {
		t.Fatal(err)
	}
	if err := client.Put(ctx, "test-bucket", "mail/drafts/d1", []byte(geheim), "message/rfc822"); err != nil {
		t.Fatal(err)
	}

	out := log.String()
	if strings.Contains(out, geheim) {
		t.Errorf("the message body is in the log:\n%s", out)
	}
	if strings.Contains(out, "Kontodaten") {
		t.Errorf("the subject is in the log:\n%s", out)
	}
	// And it has to be useful: the call and the key are the whole point.
	for _, want := range []string{"s3 List", "s3 Get", "s3 Put", "mail/m1"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log does not mention %q:\n%s", want, out)
		}
	}
}

// Switched off it writes nothing at all - not even an empty line per call.
func TestOffMeansOff(t *testing.T) {
	var log bytes.Buffer
	awsx.SetDebug(&log)
	awsx.SetDebug(nil)
	if awsx.Debugging() {
		t.Fatal("still on after being switched off")
	}
	f := s3fake.New()
	if _, err := awsx.WrapForDebug(f).List(t.Context(), "b", "p"); err != nil {
		t.Fatal(err)
	}
	if log.Len() != 0 {
		t.Errorf("wrote %d bytes while switched off: %q", log.Len(), log.String())
	}
}
