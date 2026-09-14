// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ohartwig/s3mail/awsx"
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

// Push is two actions and no more.
//
// The interesting assertions here are the negative ones. A phone that could
// Publish to the topic could send a notification to every other device on the
// mailbox; one that could DeleteEndpoint could silence them. Neither is needed
// to receive a notification, and both are things a stolen phone must not do.
func TestPushIsTwoActionsAndNoMore(t *testing.T) {
	const app = "arn:aws:sns:eu-north-1:123456789012:app/APNS/s3mail-ios"
	const topic = "arn:aws:sns:eu-north-1:123456789012:s3mail-neue-mail"
	got := actions(t, policy(t, awsx.DevicePolicyOpts{
		Bucket: "post", Prefix: "mail/ole/",
		PushApps: []string{app}, PushTopic: topic}))

	joined := strings.Join(got, " ")
	for _, want := range []string{"sns:CreatePlatformEndpoint", "sns:Subscribe"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s missing, the device cannot register itself: %v", want, got)
		}
	}
	// The two that repair an endpoint are absent here on purpose: nothing said
	// the application belongs to this mailbox alone, and on a shared one they
	// reach every other mailbox's endpoints.
	for _, forbidden := range []string{"sns:Publish", "sns:DeleteEndpoint",
		"sns:Unsubscribe", "sns:*",
		"sns:SetEndpointAttributes", "sns:GetEndpointAttributes"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("%s granted - a lost phone could use it: %v", forbidden, got)
		}
	}
}

// Both Apple environments, because a device cannot be told which one it is.
func TestBothPlatformApplicationsCanBeAllowed(t *testing.T) {
	const prod = "arn:aws:sns:eu-north-1:123456789012:app/APNS/s3mail-ios"
	const sandbox = "arn:aws:sns:eu-north-1:123456789012:app/APNS_SANDBOX/s3mail-ios"
	d := policy(t, awsx.DevicePolicyOpts{Bucket: "post",
		PushApps: []string{prod, sandbox}, PushTopic: "arn:aws:sns:x:1:t"})

	var found []string
	for _, s := range d["Statement"].([]any) {
		m := s.(map[string]any)
		if m["Action"] == "sns:CreatePlatformEndpoint" {
			for _, r := range m["Resource"].([]any) {
				found = append(found, r.(string))
			}
		}
	}
	if len(found) != 2 {
		t.Fatalf("expected both platform applications, got %v", found)
	}
	// Named individually, not as a wildcard: "every platform application in
	// the account" would include ones that have nothing to do with this
	// mailbox.
	for _, arn := range found {
		if strings.HasSuffix(arn, "*") {
			t.Errorf("wildcard resource %q - that is every app in the account", arn)
		}
	}
}

// No push asked for, no push permissions. An unused permission is one nobody
// notices being abused.
func TestWithoutPushThereAreNoSNSPermissions(t *testing.T) {
	got := strings.Join(actions(t, policy(t, awsx.DevicePolicyOpts{Bucket: "post"})), " ")
	if strings.Contains(got, "sns:") {
		t.Errorf("SNS permissions without push being asked for: %s", got)
	}
}

// A topic without applications, or the other way round, must not produce half
// a permission set that looks like it works.
func TestPushNeedsBothTheAppsAndTheTopic(t *testing.T) {
	onlyApps := strings.Join(actions(t, policy(t, awsx.DevicePolicyOpts{
		Bucket: "post", PushApps: []string{"arn:aws:sns:x:1:app/APNS/a"}})), " ")
	if strings.Contains(onlyApps, "sns:Subscribe") {
		t.Error("Subscribe without a topic to subscribe to")
	}
	onlyTopic := strings.Join(actions(t, policy(t, awsx.DevicePolicyOpts{
		Bucket: "post", PushTopic: "arn:aws:sns:x:1:t"})), " ")
	if strings.Contains(onlyTopic, "sns:") {
		t.Error("a topic alone is not push - the device cannot register")
	}
}

// A device that cannot repair its own endpoint loses push for good.
//
// APNs disables an endpoint whose token went stale and nothing re-enables it on
// its own, so the recovery core.DecidePush describes needs both actions. The
// price is that SNS authorises them against the application, which is why they
// arrive through their own field and only for an application this mailbox owns.
func TestOwnApplicationsMayBeRepaired(t *testing.T) {
	const own = "arn:aws:sns:eu-north-1:123456789012:app/APNS/s3mail-ios-ole"
	d := policy(t, awsx.DevicePolicyOpts{
		Bucket: "post", Prefix: "mail/ole/",
		PushApps: []string{own}, PushTopic: "arn:aws:sns:x:1:t",
		RefreshApps: []string{own}})

	joined := strings.Join(actions(t, d), " ")
	for _, want := range []string{"sns:GetEndpointAttributes", "sns:SetEndpointAttributes"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s missing - the device cannot recover a disabled endpoint", want)
		}
	}

	// And on that application only. A wildcard here would be every app in the
	// account, which is exactly the reach the field exists to avoid.
	for _, st := range d["Statement"].([]any) {
		m := st.(map[string]any)
		acts, ok := m["Action"].([]any)
		if !ok || len(acts) == 0 || acts[0].(string) != "sns:GetEndpointAttributes" {
			continue
		}
		for _, r := range m["Resource"].([]any) {
			if r.(string) != own {
				t.Errorf("repair allowed on %q, which is not this mailbox's application", r)
			}
		}
	}
}

// The shared application is the dangerous case, and it is the default.
func TestASharedApplicationIsNotRepairable(t *testing.T) {
	const shared = "arn:aws:sns:eu-north-1:123456789012:app/APNS/s3mail-ios"
	got := strings.Join(actions(t, policy(t, awsx.DevicePolicyOpts{
		Bucket: "post", PushApps: []string{shared},
		PushTopic: "arn:aws:sns:x:1:t"})), " ")

	if strings.Contains(got, "EndpointAttributes") {
		t.Errorf("a device may change endpoints on a shared application: %s", got)
	}
}
