// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ohartwig/s3mail/deploy"
)

// The clamp between two ways of building the same thing.
//
// One mailbox is set up by OpenTofu in a private repository, another by the
// CloudFormation template anybody can launch. Both describe the same contract -
// which bucket, which prefix, which sender, which boundary - and neither is the
// authority. The authority is fromPolicies: whatever s3mail can read out of an
// access's own policy is what a setup has to produce.
//
// Without this test the two would drift, and the drift would be invisible. The
// template keeps deploying, the stack keeps succeeding, and a stranger ends up
// with a mailbox s3mail cannot make sense of - while it works perfectly for
// whoever wrote the Terraform.
func TestTheTemplateBuildsAMailboxThisCodeUnderstands(t *testing.T) {
	found := discoverFromTemplate(t)

	if found.Bucket != "example-mail" {
		t.Errorf("bucket read as %q - the wizard would ask for it", found.Bucket)
	}
	if found.Prefix != "mail/ole/" {
		t.Errorf("prefix read as %q", found.Prefix)
	}
	if found.From != "ole@example.org" {
		t.Errorf("sender read as %q - sending would be off", found.From)
	}
	if found.Mailbox != "ole" {
		t.Errorf("mailbox read as %q - device users would land under the wrong path", found.Mailbox)
	}
	if found.DeviceBoundary == "" {
		t.Error("no permissions boundary found - the wizard would refuse to pair a phone")
	}
	if !strings.Contains(found.DeviceBoundary, "device-boundary") {
		t.Errorf("boundary read as %q", found.DeviceBoundary)
	}
}

// The bucket policy is the part that made pairing useless once: it denied
// reading to every principal it did not know, and it did not know device users.
// The template has to know them from the start.
func TestTheTemplateLetsPairedDevicesRead(t *testing.T) {
	policy := resource(t, "MailBucketPolicy")["PolicyDocument"]
	blob, err := json.Marshal(resolve(policy))
	if err != nil {
		t.Fatal(err)
	}
	text := string(blob)

	for _, sid := range []string{"OnlyMailboxesReadContents", "ListingMailboxesAndManagersOnly"} {
		if !strings.Contains(text, sid) {
			t.Fatalf("%s is gone - anybody in the account could read the mail", sid)
		}
	}
	// Both denies exempt the device path, or a paired phone gets AccessDenied
	// with a perfectly correct policy of its own.
	if n := strings.Count(text, "user/s3mail-devices/ole/*"); n < 2 {
		t.Errorf("device users are exempt from %d of the two denies", n)
	}
}

// Deleting a stack must not delete somebody's mail.
func TestTheBucketOutlivesTheStack(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(deploy.Template()), &doc); err != nil {
		t.Fatal(err)
	}
	bucket := doc["Resources"].(map[string]any)["MailBucketResource"].(map[string]any)
	if bucket["DeletionPolicy"] != "Retain" {
		t.Error("the bucket goes when the stack goes, and takes the mail with it")
	}
}

// --- the bits that make a template testable ---------------------------------

// values stands in for what CloudFormation knows at deploy time.
var values = map[string]string{
	"MailBucket":       "example-mail",
	"MailboxLocalPart": "ole",
	"MailDomain":       "example.org",
	"AWS::Partition":   "aws",
	"AWS::AccountId":   "123456789012",
	"AWS::Region":      "eu-north-1",
	"MailboxUser":      "s3mail-ole",
	"DeviceBoundary":   "arn:aws:iam::123456789012:policy/s3mail/s3mail-ole-device-boundary",
	// GetAtt, written the way resolve looks it up.
	"MailboxUser.Arn": "arn:aws:iam::123456789012:user/s3mail/s3mail-ole",
}

// resolve walks a decoded template and replaces the intrinsics with what a
// deployment would put there. Three of them are enough for this template, and a
// fourth appearing should fail loudly rather than resolve to something wrong.
func resolve(node any) any {
	switch v := node.(type) {
	case map[string]any:
		if len(v) == 1 {
			for key, arg := range v {
				switch key {
				case "Ref":
					return values[arg.(string)]
				case "Fn::Sub":
					out := arg.(string)
					for name, value := range values {
						out = strings.ReplaceAll(out, "${"+name+"}", value)
					}
					return out
				case "Fn::GetAtt":
					parts := arg.([]any)
					return values[parts[0].(string)+"."+parts[1].(string)]
				}
			}
		}
		out := make(map[string]any, len(v))
		for key, value := range v {
			out[key] = resolve(value)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, value := range v {
			out[i] = resolve(value)
		}
		return out
	}
	return node
}

func resource(t *testing.T, name string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(deploy.Template()), &doc); err != nil {
		t.Fatalf("the template is not valid JSON: %v", err)
	}
	res, ok := doc["Resources"].(map[string]any)[name].(map[string]any)
	if !ok {
		t.Fatalf("the template has no %s", name)
	}
	return res["Properties"].(map[string]any)
}

// discoverFromTemplate is what awsx.Discover does against a live account, with
// the template standing in for the account: the inline policy of the user and
// the managed policy attached to it.
func discoverFromTemplate(t *testing.T) Finding {
	t.Helper()

	user := resource(t, "MailboxUser")
	inline := user["Policies"].([]any)[0].(map[string]any)["PolicyDocument"]
	attached := resource(t, "DeviceAdmin")["PolicyDocument"]

	documents := make([]string, 0, 2)
	for _, doc := range []any{inline, attached} {
		blob, err := json.Marshal(resolve(doc))
		if err != nil {
			t.Fatal(err)
		}
		documents = append(documents, string(blob))
	}
	return fromPolicies(documents)
}
