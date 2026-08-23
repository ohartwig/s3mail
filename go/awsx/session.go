// Package awsx connects s3mail to the real AWS: the implementations of store.S3
// and store.KMS, plus the translation of AWS errors into sentences somebody can
// act on.
package awsx

import (
	"context"
	"errors"
	"path/filepath"
	"s3mail/i18n"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
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

// CheckAccess fetches the credentials once. config.LoadDefaultConfig reports no
// error yet when none are stored at all - that would otherwise surface at the
// first call, in the middle of some other operation.
func CheckAccess(ctx context.Context, cfg aws.Config) error {
	if cfg.Credentials == nil {
		return errors.New("keine Zugangsdaten")
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
		strings.Contains(text, "keine Zugangsdaten") ||
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
