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

// The refusal that is a decision, told apart from the ones that are faults.
//
// This mattered enough to cost an evening: a device could not read its own
// endpoint, Register treated that as fatal, and push died on the second launch
// of every phone. The right cannot simply be granted - SNS authorises it
// against the platform application, so on a shared one it would reach other
// mailboxes' endpoints - so the refusal is permanent by design and has to be
// survivable.
func TestBeingRefusedTheRepairIsNotAFault(t *testing.T) {
	denied := &types.AuthorizationErrorException{
		Message: aws.String("User: arn:aws:iam::1:user/s3mail-devices/ole/phone is not " +
			"authorized to perform: SNS:GetEndpointAttributes"),
	}
	if !isNotAllowed(denied) {
		t.Error("the refusal was not recognised - registering would abort on it")
	}

	// Everything else must stay a fault. Swallowing a real failure here would
	// hand back an endpoint ARN for an endpoint that is not there.
	for name, err := range map[string]error{
		"not found":         &types.NotFoundException{Message: aws.String("gone")},
		"invalid parameter": &types.InvalidParameterException{Message: aws.String("nope")},
		"nothing at all":    nil,
	} {
		if isNotAllowed(err) {
			t.Errorf("%s was taken for a refusal", name)
		}
	}
}

// SNS says AuthorizationError where S3 and IAM say AccessDenied, and the
// difference is not cosmetic: it is what told a 403 from the bucket apart from
// a 403 from push while both were broken at once.
func TestTheRefusalIsNotAnAccessDenied(t *testing.T) {
	var denied *types.AuthorizationErrorException
	if _, ok := any(denied).(interface{ ErrorCode() string }); !ok {
		t.Skip("the SDK type no longer reports a code")
	}
	if got := (&types.AuthorizationErrorException{}).ErrorCode(); got != "AuthorizationError" {
		t.Errorf("SNS now answers %q - the note in isNotAllowed is out of date", got)
	}
}
