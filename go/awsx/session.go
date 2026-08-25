// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package awsx connects s3mail to the real AWS: the implementations of store.S3
// and store.KMS, plus the translation of AWS errors into sentences somebody can
// act on.
package awsx

import (
	"context"
	"errors"
	"git.ole-hartwig.eu/development/s3mail/s3mail/i18n"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/smithy-go"
)

// SharedDir is where credentials and config live. Empty means: whatever the SDK
// takes by itself (~/.aws).
//
// The wizard writes the credentials somewhere - the SDK has to look for them in
// the same place, or somebody creates a profile that is never found.
var SharedDir string

// Session builds the AWS configuration from profile and region. Both may be
// empty; then whatever the environment holds applies.
func Session(ctx context.Context, profile, region string) (aws.Config, error) {
	var opts []func(*config.LoadOptions) error
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	if SharedDir != "" {
		opts = append(opts,
			config.WithSharedCredentialsFiles([]string{
				filepath.Join(SharedDir, "credentials")}),
			config.WithSharedConfigFiles([]string{
				filepath.Join(SharedDir, "config")}))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Static builds a configuration from credentials handed in, without touching a
// file.
//
// Session looks for a profile in ~/.aws - right on a machine where the wizard
// put one there, and impossible on a phone: there is no home directory to keep
// it in, and an app holds its key in the system keychain instead.
//
// So the credentials come as arguments, and nothing here reaches for the
// environment or the disk. A caller that passes nothing gets an error rather
// than somebody else's long-forgotten default profile - on a desktop that would
// be a surprise, and on a phone it would be a lie.
func Static(_ context.Context, accessKey, secret, region string) (aws.Config, error) {
	if accessKey == "" || secret == "" {
		return aws.Config{}, errors.New("no credentials given")
	}
	if region == "" {
		return aws.Config{}, errors.New("no region given")
	}
	// Built by hand rather than through LoadDefaultConfig, and the test says
	// why: even with the shared files taken away, LoadDefaultConfig still reads
	// AWS_PROFILE from the environment and fails on a profile that does not
	// exist. A configuration that is supposed to depend on nothing must not be
	// assembled by the machinery whose whole job is to look around.
	//
	// The service clients fill in their own defaults for what is left empty -
	// retries, timeouts, the HTTP client - so nothing is lost with them.
	return aws.Config{
		Region: region,
		Credentials: credentials.NewStaticCredentialsProvider(
			accessKey, secret, ""),
	}, nil
}

// CheckAccess fetches the credentials once. config.LoadDefaultConfig reports no
// error yet when none are stored at all - that would otherwise surface at the
// first call, in the middle of some other operation.
func CheckAccess(ctx context.Context, cfg aws.Config) error {
	if cfg.Credentials == nil {
		return errors.New("no credentials")
	}
	_, err := cfg.Credentials.Retrieve(ctx)
	return err
}

// PlainText translates an AWS exception into a sentence that says what to do.
//
// The AWS SDK reports missing or unusable credentials in half a dozen shapes,
// all of them English and none of them hinting that the wizard two fields
// further up would solve exactly that.
func PlainText(err error, profile string, cat i18n.Catalog) string {
	if err == nil {
		return ""
	}
	wo := cat.T("aws.defaultProfile")
	if profile != "" {
		wo = cat.Tf("aws.namedProfile", profile)
	}
	keys := cat.T("aws.enterKeys")
	text := err.Error()

	// no such profile
	var missingProfile config.SharedConfigProfileNotExistError
	if errors.As(err, &missingProfile) {
		return cat.Tf("aws.noSuchProfile", profile, keys)
	}

	// SSO: the login is missing, not the credentials
	if strings.Contains(text, "sso") || strings.Contains(text, "SSO") ||
		strings.Contains(text, "token") && strings.Contains(text, "expired") {
		cmd := "aws sso login"
		if profile != "" {
			cmd += " --profile " + profile
		}
		return cat.Tf("aws.ssoLoginNeeded", wo, cmd)
	}

	// a missing region otherwise surfaces as a cryptic endpoint error
	if strings.Contains(text, "no region") || strings.Contains(text, "region is required") ||
		strings.Contains(text, "MissingRegion") {
		return cat.T("aws.noRegion")
	}

	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "InvalidClientTokenId", "UnrecognizedClientException", "AuthFailure",
			"InvalidAccessKeyId":
			return cat.T("aws.badKeyId")
		case "SignatureDoesNotMatch":
			return cat.T("aws.badSecret")
		case "ExpiredToken", "ExpiredTokenException", "TokenRefreshRequired":
			return cat.Tf("aws.expired", wo, keys)
		case "AccessDenied", "AccessDeniedException":
			return cat.T("aws.accessDenied")
		case "NoSuchBucket":
			return cat.T("aws.noSuchBucket")
		case "PermanentRedirect", "IllegalLocationConstraintException",
			"AuthorizationHeaderMalformed":
			return cat.T("aws.wrongRegion")
		}
	}

	// No credentials - the SDK lets this through as CredentialRequiresARNError, as
	// an empty provider or plainly as text.
	if strings.Contains(text, "failed to refresh cached credentials") ||
		strings.Contains(text, "no EC2 IMDS role found") ||
		strings.Contains(text, "failed to retrieve credentials") ||
		strings.Contains(text, "no credentials") ||
		strings.Contains(text, "EmptyStaticCreds") {
		return cat.Tf("aws.noCredentials", wo, keys)
	}

	// network
	if strings.Contains(text, "no such host") || strings.Contains(text, "dial tcp") ||
		strings.Contains(text, "connection refused") || strings.Contains(text, "timeout") {
		return cat.T("aws.noConnection")
	}

	return text // do not swallow the unknown
}

// IsAuthProblem says whether an error is about the access itself rather than
// about what was asked for.
//
// The distinction matters upwards: a message that cannot be read is one
// message, and the reader tries another. An access that has expired is the
// whole mailbox, and no amount of clicking fixes it - the answer is a command
// in a terminal. The interface has to tell those apart, or it shows a toast
// that fades while nothing works any more.
func IsAuthProblem(err error) bool {
	if err == nil {
		return false
	}
	var missingProfile config.SharedConfigProfileNotExistError
	if errors.As(err, &missingProfile) {
		return true
	}
	text := err.Error()
	if strings.Contains(text, "sso") || strings.Contains(text, "SSO") ||
		strings.Contains(text, "token") && strings.Contains(text, "expired") ||
		strings.Contains(text, "no credentials") ||
		strings.Contains(text, "failed to refresh cached credentials") {
		return true
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "InvalidClientTokenId", "UnrecognizedClientException", "AuthFailure",
			"InvalidAccessKeyId", "SignatureDoesNotMatch", "ExpiredToken",
			"ExpiredTokenException", "TokenRefreshRequired", "AccessDenied",
			"AccessDeniedException", "InvalidIdentityToken":
			return true
		}
	}
	return false
}
