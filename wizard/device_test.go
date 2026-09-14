// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package wizard

import (
	"testing"
)

// What is left to test here without AWS.
//
// Device() used to be a pure function that rendered a policy and a payload, and
// it was tested as one. It is not that any more: it creates an IAM user, mints
// a key and looks for platform applications. The parts worth testing moved to
// where they can be exercised - awsx.DevicePolicy, awsx.DeviceUserName,
// core.SealSecret, awsx.fromPolicies - and each of them has its own tests.
//
// Leaving a test here that "checks Device()" by mocking three AWS clients would
// prove that the mocks were written to match the code, which is not the same as
// proving anything.

// The applications reach the policy in a stable order. Two runs of the wizard
// that produce different documents would show up as a diff nobody can explain.
func TestThePolicySeesTheARNsInAStableOrder(t *testing.T) {
	apps := map[string]string{
		"production":  "arn:b",
		"development": "arn:a",
	}
	first := pushARNs(apps)
	for range 20 {
		if got := pushARNs(apps); len(got) != len(first) || got[0] != first[0] || got[1] != first[1] {
			t.Fatalf("order changed between runs: %v then %v", first, got)
		}
	}
	if first[0] != "arn:a" || first[1] != "arn:b" {
		t.Errorf("not sorted: %v", first)
	}
}

// Empty entries would become a policy resource of "", which IAM rejects with a
// message that names neither the wizard nor the empty string.
func TestEmptyARNsAreDropped(t *testing.T) {
	got := pushARNs(map[string]string{"production": "", "development": "  ", "x": "arn:a"})
	if len(got) != 1 || got[0] != "arn:a" {
		t.Errorf("got %v", got)
	}
}

// An absent map travels as {} and not as null: a phone that finds no field
// cannot tell "no push here" from "this code is too old to know about it".
func TestAnAbsentMapTravelsAsEmpty(t *testing.T) {
	if m := notNilMap(nil); m == nil || len(m) != 0 {
		t.Errorf("got %v", m)
	}
}
