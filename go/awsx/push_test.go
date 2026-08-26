// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"
)

// The one piece of string handling in this file, and the one that would fail
// silently: if the ARN cannot be read out, the device makes a new endpoint on
// every launch, each of them a subscriber on the topic, and the phone buzzes
// once per launch it has ever had.
func TestTheExistingEndpointIsReadOutOfTheError(t *testing.T) {
	const arn = "arn:aws:sns:eu-north-1:714476370938:endpoint/APNS_SANDBOX/s3mail-ios-sandbox/abc-123"
	err := &types.InvalidParameterException{
		Message: aws.String("Invalid parameter: Token Reason: Endpoint " + arn +
			" already exists with the same Token, but different attributes."),
	}
	got, ok := existingEndpoint(err)
	if !ok {
		t.Fatal("the ARN was not recognised - every launch would create another endpoint")
	}
	if got != arn {
		t.Errorf("got %q, want %q", got, arn)
	}
}

// An ARN at the very end of the message, with no trailing space.
func TestAnARNAtTheEndOfTheMessage(t *testing.T) {
	const arn = "arn:aws:sns:eu-north-1:1:endpoint/APNS/app/x"
	err := &types.InvalidParameterException{
		Message: aws.String("Endpoint " + arn + " already exists with the same Token"),
	}
	if got, ok := existingEndpoint(err); !ok || got != arn {
		t.Errorf("got %q %v", got, ok)
	}
}

// Any other InvalidParameterException must not be mistaken for this one -
// otherwise a real error turns into an empty ARN and a confusing follow-up.
func TestAnotherInvalidParameterIsNotAnEndpoint(t *testing.T) {
	err := &types.InvalidParameterException{
		Message: aws.String("Invalid parameter: PlatformApplicationArn"),
	}
	if _, ok := existingEndpoint(err); ok {
		t.Error("an unrelated error was read as an existing endpoint")
	}
}

func TestSomethingElseEntirelyIsNotAnEndpoint(t *testing.T) {
	if _, ok := existingEndpoint(&types.NotFoundException{}); ok {
		t.Error("NotFound is not an existing endpoint")
	}
}
