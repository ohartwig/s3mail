// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package core

// What to do about a device's push registration, decided without touching AWS.
//
// The rules live here and the calls live in awsx, the same split as uid.go and
// store/uids.go. Not for symmetry: registering a device is a small decision
// tree with four inputs and exactly one wrong answer per branch, and a decision
// tree that can only be exercised against a real SNS endpoint is a decision
// tree nobody exercises.
//
// What makes it fiddly is that the endpoint outlives the app. It sits in SNS
// after the app is deleted, after the phone is wiped, after the token changes -
// and APNs disables it the moment it decides the token is stale. So "have I
// registered?" is never answered by local state alone.

// PushAction is what the device should do next.
type PushAction int

const (
	// PushKeep: the endpoint is there, enabled, and holds this token.
	PushKeep PushAction = iota
	// PushCreate: there is no usable endpoint - make one.
	PushCreate
	// PushRefresh: the endpoint exists but holds an old token or was disabled.
	// APNs disables an endpoint when it rejects a token, and SNS never enables
	// it again on its own: without this the phone stays silent forever, and
	// nothing anywhere reports an error.
	PushRefresh
)

// Endpoint is what SNS says about the endpoint this device stored last time.
type Endpoint struct {
	// ARN is empty when the device has never registered.
	ARN string
	// Exists is false when the stored ARN no longer resolves - the endpoint was
	// deleted, or this is a restore onto a different phone.
	Exists bool
	// Token is what SNS has on file, which is not necessarily what iOS just
	// handed the app: tokens change on reinstall, on restore, and sometimes for
	// no reason anybody outside Apple can see.
	Token   string
	Enabled bool
}

// DecidePush answers what to do, given what SNS says and the token iOS just
// handed over.
//
// An empty token is PushKeep and not PushCreate: the app is asked to register
// before iOS has answered, and creating an endpoint with no token would put a
// dead entry in SNS that nothing ever cleans up.
func DecidePush(stored Endpoint, token string) PushAction {
	if token == "" {
		return PushKeep
	}
	if stored.ARN == "" || !stored.Exists {
		return PushCreate
	}
	if stored.Token != token || !stored.Enabled {
		return PushRefresh
	}
	return PushKeep
}

// PushEnvironment names which of Apple's two worlds a build belongs to.
//
// It is read from the build's own provisioning profile, never guessed from the
// build configuration: a TestFlight build is a Release build and talks to
// production, an Ad Hoc build is a Release build and talks to sandbox, and a
// Debug build talks to sandbox. `#if DEBUG` gets the middle case wrong, and the
// symptom is a notification that is never delivered and never reports an error.
type PushEnvironment string

const (
	PushProduction PushEnvironment = "production"
	PushSandbox    PushEnvironment = "development"
)

// PlatformApp picks the platform application for an environment.
//
// The device is handed both ARNs at setup because it cannot work them out, and
// picks here. An environment with no ARN gives an empty string rather than the
// other one: registering a sandbox build against the production application
// produces an endpoint that looks fine and never receives anything.
func PlatformApp(apps map[string]string, env PushEnvironment) string {
	return apps[string(env)]
}
