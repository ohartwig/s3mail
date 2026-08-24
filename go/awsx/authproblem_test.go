// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx_test

import (
	"errors"
	"testing"

	"s3mail/awsx"
)

// IsAuthProblem decides whether the interface puts up a banner that stays or a
// toast that fades. Getting it wrong in one direction hides a dead mailbox
// behind a message nobody sees; in the other it declares the access broken
// because one object was missing.
func TestIsAuthProblem(t *testing.T) {
	yes := []struct {
		name string
		err  error
	}{
		{"SSO session over", errors.New("sso session token expired")},
		{"no credentials at all", errors.New("no credentials in the chain")},
		{"refresh failed", errors.New("failed to refresh cached credentials")},
		{"key withdrawn", apiError("InvalidClientTokenId")},
		{"token expired", apiError("ExpiredToken")},
		{"policy says no", apiError("AccessDenied")},
		{"wrong secret", apiError("SignatureDoesNotMatch")},
	}
	for _, c := range yes {
		if !awsx.IsAuthProblem(c.err) {
			t.Errorf("%s: not recognised as an access problem", c.name)
		}
	}

	no := []struct {
		name string
		err  error
	}{
		{"nothing", nil},
		{"object missing", apiError("NoSuchKey")},
		{"bucket missing", apiError("NoSuchBucket")},
		{"wrong region", apiError("PermanentRedirect")},
		{"SES refused the message", apiError("MessageRejected")},
		{"something else entirely", errors.New("connection reset by peer")},
	}
	for _, c := range no {
		if awsx.IsAuthProblem(c.err) {
			t.Errorf("%s: wrongly taken for an access problem", c.name)
		}
	}
}
