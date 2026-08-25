// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx_test

import (
	"encoding/json"
	"strings"
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/awsx"
)

func policy(t *testing.T, o awsx.DevicePolicyOpts) map[string]any {
	t.Helper()
	text, err := awsx.DevicePolicy(o)
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(text), &d); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, text)
	}
	return d
}

func actions(t *testing.T, d map[string]any) []string {
	t.Helper()
	var out []string
	for _, s := range d["Statement"].([]any) {
		switch a := s.(map[string]any)["Action"].(type) {
		case string:
			out = append(out, a)
		case []any:
			for _, one := range a {
				out = append(out, one.(string))
			}
		}
	}
	return out
}

// The whole point of a device policy: it is narrower than the desktop's.
func TestADevicePolicyIsNarrowerThanTheDesktops(t *testing.T) {
	got := actions(t, policy(t, awsx.DevicePolicyOpts{Bucket: "post", Prefix: "mail/ole/"}))
	joined := strings.Join(got, " ")

	// No queue: push on a phone comes from SNS to APNs. A permission nobody
	// uses is one nobody notices being abused.
	if strings.Contains(joined, "sqs:") {
		t.Errorf("the device gets queue permissions: %v", got)
	}
	// No sending unless asked for.
	if strings.Contains(joined, "ses:") {
		t.Errorf("the device may send without being asked: %v", got)
	}
	// But it has to be able to read and file mail, or it is not a mailbox.
	for _, needed := range []string{"s3:ListBucket", "s3:GetObject", "s3:PutObject"} {
		if !strings.Contains(joined, needed) {
			t.Errorf("%s is missing: %v", needed, got)
		}
	}
}

func TestSendingIsAddedOnRequest(t *testing.T) {
	got := strings.Join(actions(t, policy(t, awsx.DevicePolicyOpts{
		Bucket: "post", Prefix: "mail/", AllowSend: true})), " ")
	if !strings.Contains(got, "ses:SendRawEmail") {
		t.Errorf("sending was asked for and is missing: %s", got)
	}
}

// The prefix condition is the difference between "a mailbox" and "the file
// store it happens to live in". Without it a device could list everything.
func TestTheListingIsBoundToThePrefix(t *testing.T) {
	d := policy(t, awsx.DevicePolicyOpts{Bucket: "post", Prefix: "mail/ole/"})
	first := d["Statement"].([]any)[0].(map[string]any)
	cond, ok := first["Condition"].(map[string]any)
	if !ok {
		t.Fatalf("ListBucket has no condition: %+v", first)
	}
	like := cond["StringLike"].(map[string]any)["s3:prefix"].([]any)
	if like[0] != "mail/ole/*" {
		t.Errorf("the condition reads %v", like)
	}
	// And the object permissions must not reach outside the prefix either.
	second := d["Statement"].([]any)[1].(map[string]any)
	if second["Resource"] != "arn:aws:s3:::post/mail/ole/*" {
		t.Errorf("objects are not bound to the prefix: %v", second["Resource"])
	}
}

// A prefix somebody typed without a slash would let the device see
// "mail-alt/" as well. Repaired rather than refused: the wizard knows what was
// meant, and a policy is not the place to teach S3 semantics.
func TestAPrefixWithoutASlashIsRepaired(t *testing.T) {
	d := policy(t, awsx.DevicePolicyOpts{Bucket: "post", Prefix: "mail"})
	second := d["Statement"].([]any)[1].(map[string]any)
	if second["Resource"] != "arn:aws:s3:::post/mail/*" {
		t.Errorf("resource is %v", second["Resource"])
	}
}

// A whole bucket without a prefix is a legitimate mailbox - then there is
// nothing to condition on, and writing a condition that matches everything
// would only look like a restriction.
func TestWithoutAPrefixThereIsNoCondition(t *testing.T) {
	d := policy(t, awsx.DevicePolicyOpts{Bucket: "post"})
	first := d["Statement"].([]any)[0].(map[string]any)
	if _, has := first["Condition"]; has {
		t.Errorf("a condition appeared without a prefix: %+v", first)
	}
}

// The KMS part is left out entirely rather than written with a wildcard: a
// permission on every key in the account is not the same as a permission on
// the one key this mailbox uses.
func TestKMSOnlyWhenThereIsAKey(t *testing.T) {
	without := strings.Join(actions(t, policy(t, awsx.DevicePolicyOpts{Bucket: "post"})), " ")
	if strings.Contains(without, "kms:") {
		t.Errorf("KMS permissions without a key: %s", without)
	}
	with := policy(t, awsx.DevicePolicyOpts{Bucket: "post",
		KMSKey: "arn:aws:kms:eu-north-1:1:key/abc"})
	last := with["Statement"].([]any)[len(with["Statement"].([]any))-1].(map[string]any)
	if last["Resource"] != "arn:aws:kms:eu-north-1:1:key/abc" {
		t.Errorf("the key is not named: %v", last["Resource"])
	}
}

func TestABucketIsRequired(t *testing.T) {
	if _, err := awsx.DevicePolicy(awsx.DevicePolicyOpts{}); err == nil {
		t.Error("a policy without a bucket was accepted")
	}
}
