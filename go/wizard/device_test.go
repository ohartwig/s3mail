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

	// map[string]any and not map[string]string: the payload carries the push
	// ARNs as a list.
	var payload map[string]any
	if err := json.Unmarshal([]byte(out["payload"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["accessKey"] != "" || payload["secret"] != "" {
		t.Errorf("the code carries a key: %v", payload)
	}
	// But everything the phone cannot work out for itself has to be in there.
	for _, field := range []string{"bucket", "prefix", "region", "from"} {
		if payload[field] == "" || payload[field] == nil {
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

// The ARNs travel with the code, because the device cannot work them out.
//
// They are not secret: an ARN names a resource, it does not open it, and the
// policy decides what the device may do with it. What would be a mistake is
// leaving them out - then the phone knows it should register somewhere and not
// where.
func TestTheCodeCarriesThePushTargets(t *testing.T) {
	const prod = "arn:aws:sns:eu-north-1:123456789012:app/APNS/s3mail-ios"
	const sandbox = "arn:aws:sns:eu-north-1:123456789012:app/APNS_SANDBOX/s3mail-ios"
	const topic = "arn:aws:sns:eu-north-1:123456789012:s3mail-neue-mail"

	out := device(t, wizard.Data{Bucket: "post", Prefix: "mail/ole/",
		Region: "eu-north-1", PushApps: []string{prod, sandbox}, PushTopic: topic})

	var payload map[string]any
	if err := json.Unmarshal([]byte(out["payload"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	apps, ok := payload["pushApps"].([]any)
	if !ok || len(apps) != 2 {
		t.Fatalf("both platform applications should be in the code: %v", payload["pushApps"])
	}
	if payload["pushTopic"] != topic {
		t.Errorf("topic missing from the code: %v", payload["pushTopic"])
	}
	// And the policy has to match what the code promises.
	if !strings.Contains(out["policy"].(string), "sns:CreatePlatformEndpoint") {
		t.Error("the code names a place to register, the policy does not allow it")
	}
}

// No push asked for: an empty list, not a missing field. A phone that finds no
// key at all cannot tell "no push here" from "this code is too old to know".
func TestWithoutPushTheListIsEmptyNotAbsent(t *testing.T) {
	out := device(t, wizard.Data{Bucket: "post", Region: "eu-north-1"})
	var payload map[string]any
	if err := json.Unmarshal([]byte(out["payload"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	apps, present := payload["pushApps"]
	if !present {
		t.Fatal("pushApps is missing entirely")
	}
	if list, ok := apps.([]any); !ok || len(list) != 0 {
		t.Errorf("expected an empty list, got %v", apps)
	}
	if strings.Contains(out["policy"].(string), "sns:") {
		t.Error("SNS permissions without push being asked for")
	}
}
