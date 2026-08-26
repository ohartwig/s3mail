// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/core"
)

func TestANewDeviceRegisters(t *testing.T) {
	if got := core.DecidePush(core.Endpoint{}, "abc"); got != core.PushCreate {
		t.Errorf("a device that never registered should create, got %v", got)
	}
}

func TestAGoodEndpointIsLeftAlone(t *testing.T) {
	e := core.Endpoint{ARN: "arn:x", Exists: true, Token: "abc", Enabled: true}
	if got := core.DecidePush(e, "abc"); got != core.PushKeep {
		t.Errorf("nothing to do here, got %v", got)
	}
}

// APNs disables an endpoint when it rejects a token, and SNS never enables it
// again by itself. Without the refresh the phone stays silent forever, and
// nothing anywhere reports an error - which is why this case has its own test.
func TestADisabledEndpointIsRefreshed(t *testing.T) {
	e := core.Endpoint{ARN: "arn:x", Exists: true, Token: "abc", Enabled: false}
	if got := core.DecidePush(e, "abc"); got != core.PushRefresh {
		t.Errorf("a disabled endpoint has to be re-enabled, got %v", got)
	}
}

func TestANewTokenIsWrittenBack(t *testing.T) {
	e := core.Endpoint{ARN: "arn:x", Exists: true, Token: "alt", Enabled: true}
	if got := core.DecidePush(e, "neu"); got != core.PushRefresh {
		t.Errorf("the token changed, got %v", got)
	}
}

// A restore onto another phone, or an endpoint somebody deleted: the ARN is
// remembered and resolves to nothing.
func TestAVanishedEndpointIsRecreated(t *testing.T) {
	e := core.Endpoint{ARN: "arn:weg", Exists: false, Token: "abc", Enabled: true}
	if got := core.DecidePush(e, "abc"); got != core.PushCreate {
		t.Errorf("the endpoint is gone, got %v", got)
	}
}

// The app asks before iOS has answered. Creating an endpoint with no token
// would leave a dead entry in SNS that nothing ever cleans up.
func TestWithoutATokenNothingHappens(t *testing.T) {
	if got := core.DecidePush(core.Endpoint{}, ""); got != core.PushKeep {
		t.Errorf("no token, no registration, got %v", got)
	}
	e := core.Endpoint{ARN: "arn:x", Exists: true, Token: "abc", Enabled: true}
	if got := core.DecidePush(e, ""); got != core.PushKeep {
		t.Errorf("an empty token must not overwrite a good one, got %v", got)
	}
}

func TestTheEnvironmentPicksTheApplication(t *testing.T) {
	apps := map[string]string{
		"production":  "arn:aws:sns:eu-north-1:1:app/APNS/prod",
		"development": "arn:aws:sns:eu-north-1:1:app/APNS_SANDBOX/sandbox",
	}
	if got := core.PlatformApp(apps, core.PushSandbox); got != apps["development"] {
		t.Errorf("a sandbox build must not register against production: %s", got)
	}
	if got := core.PlatformApp(apps, core.PushProduction); got != apps["production"] {
		t.Errorf("wrong application for production: %s", got)
	}
}

// A setup that carries only one of the two. Answering with the other one would
// produce an endpoint that looks fine and never receives anything.
func TestAMissingApplicationIsEmptyNotTheOtherOne(t *testing.T) {
	only := map[string]string{"production": "arn:prod"}
	if got := core.PlatformApp(only, core.PushSandbox); got != "" {
		t.Errorf("expected nothing, got %q", got)
	}
}
