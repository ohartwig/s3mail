// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx

import (
	"strings"
	"testing"
)

// The name is what somebody reads when they have lost a phone and want to lock
// it out. It has to say which mailbox and which device, and it has to be a
// legal IAM user name whatever was typed into the field.
func TestTheDeviceNameSaysWhichMailboxAndWhichDevice(t *testing.T) {
	got := DeviceUserName("ole", "Oles iPhone 17 Pro")
	if got != "s3mail-ole-oles-iphone-17-pro" {
		t.Errorf("got %q", got)
	}
}

func TestAwkwardNamesBecomeLegalOnes(t *testing.T) {
	cases := map[string]string{
		"Oles iPhone":     "s3mail-ole-oles-iphone",
		"iPad (Küche)":    "s3mail-ole-ipad-kche",
		"  Test  ":        "s3mail-ole-test",
		"!!!":             "s3mail-ole-geraet",
		"":                "s3mail-ole-geraet",
		"Ole_Test.Device": "s3mail-ole-ole-test-device",
	}
	for in, want := range cases {
		if got := DeviceUserName("ole", in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// IAM user names are capped at 64 characters. A device somebody named after
// their whole family must not produce a call that fails at AWS.
func TestALongNameIsCutBeforeAWSComplains(t *testing.T) {
	got := DeviceUserName("ole", strings.Repeat("iphone", 20))
	if len(got) > 64 {
		t.Errorf("%d characters: %q", len(got), got)
	}
}

// An account may hold platform applications for more than one app. Picking the
// wrong one produces an endpoint that looks fine and never receives anything -
// the failure mode with no error message.
func TestOnlyThisAppsPlatformApplicationsCount(t *testing.T) {
	ours := map[string]string{"ApplePlatformBundleID": "eu.ole-hartwig.s3mail"}
	theirs := map[string]string{"ApplePlatformBundleID": "com.example.other"}

	if !matchesBundle(ours, "eu.ole-hartwig.s3mail") {
		t.Error("our own application was rejected")
	}
	if matchesBundle(theirs, "eu.ole-hartwig.s3mail") {
		t.Error("another app's application was accepted")
	}
	// An application without the attribute is not excluded: it may predate the
	// attribute, and refusing it would be worse than including it.
	if !matchesBundle(map[string]string{}, "eu.ole-hartwig.s3mail") {
		t.Error("an application without the attribute was rejected")
	}
}
