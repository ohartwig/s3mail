// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package wizard_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/wizard"
)

func device(t *testing.T, d wizard.Data) map[string]any {
	t.Helper()
	a := &wizard.Wizard{}
	out, err := a.Device(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The code the phone reads carries everything except the key. That is the
// point: a key in a QR code is a key travelling through a screenshot, through
// a photo library, and through whatever backs that up.
func TestTheCodeCarriesNoKey(t *testing.T) {
	out := device(t, wizard.Data{Bucket: "post", Prefix: "mail/ole/",
		Region: "eu-north-1", From: "post@firma.de", Label: "Post"})

	var payload map[string]string
	if err := json.Unmarshal([]byte(out["payload"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["accessKey"] != "" || payload["secret"] != "" {
		t.Errorf("the code carries a key: %v", payload)
	}
	// But everything the phone cannot work out for itself has to be in there.
	for _, field := range []string{"bucket", "prefix", "region", "from"} {
		if payload[field] == "" {
			t.Errorf("%s is missing from the code", field)
		}
	}
	if payload["prefix"] != "mail/ole/" {
		t.Errorf("prefix is %q", payload["prefix"])
	}
}

// Without a verified sender there is nothing to send as, so the device policy
// does not ask for the permission. An unused permission is one nobody watches.
func TestSendingIsOnlyOfferedWithASender(t *testing.T) {
	with := device(t, wizard.Data{Bucket: "post", Prefix: "mail/", From: "post@firma.de"})
	if !strings.Contains(with["policy"].(string), "ses:SendRawEmail") {
		t.Error("a mailbox with a sender got no sending permission")
	}
	without := device(t, wizard.Data{Bucket: "post", Prefix: "mail/"})
	if strings.Contains(without["policy"].(string), "ses:SendRawEmail") {
		t.Error("a mailbox without a sender got sending permission anyway")
	}
}

// The suggested user name has to say which mailbox it belongs to. Whoever
// reads a list of IAM users in a year should not have to open each one.
func TestTheUserNameSaysWhichMailbox(t *testing.T) {
	out := device(t, wizard.Data{Bucket: "koh-post", Prefix: "mail/ole/", Region: "eu-north-1"})
	name := out["user"].(string)
	for _, part := range []string{"s3mail", "koh-post", "mail-ole"} {
		if !strings.Contains(name, part) {
			t.Errorf("%q does not contain %q", name, part)
		}
	}
	if strings.ContainsAny(name, "/. _") {
		t.Errorf("%q contains characters IAM does not take", name)
	}
}

func TestABucketIsRequired(t *testing.T) {
	a := &wizard.Wizard{}
	if _, err := a.Device(context.Background(), wizard.Data{Prefix: "mail/"}); err == nil {
		t.Error("a device without a bucket was accepted")
	}
}
