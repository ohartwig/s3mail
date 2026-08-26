// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Finding is what s3mail has found out about itself.
type Finding struct {
	User   string `json:"user"`
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix"`
	From   string `json:"from"`
	Region string `json:"region"`

	// Queue is the SQS queue new mail is announced through. Empty means: no
	// push, the timer does it - which is what every mailbox did until now.
	Queue string `json:"queue"`

	// PushTopic is the SNS topic a phone subscribes to. Read out of the
	// sns:Subscribe statement: whoever may subscribe to it was meant to.
	PushTopic string `json:"push_topic"`

	// Mailbox is the local part - "ole" - which is also the path segment device
	// users live under. Derived from the prefix rather than asked for.
	Mailbox string `json:"mailbox"`

	// DeviceBoundary is the permissions boundary every device user must carry.
	// It stands in the access's own policy, in the condition that makes
	// creating a device user safe at all - so if it is absent, this access may
	// not create devices, and the wizard can say so instead of finding out
	// three IAM calls later.
	DeviceBoundary string `json:"device_boundary"`
}

// Complete says whether the wizard can go on without asking.
func (f Finding) Complete() bool { return f.Bucket != "" && f.Prefix != "" }

// Discover reads the settings out of the access itself instead of asking for
// them.
//
// A mailbox access carries everything needed in its own IAM policy: the right
// to s3:GetObject names bucket and prefix, the condition ses:FromAddress names
// the sender address. This is no guesswork - it is exactly the statement AWS
// measures the access against later. Whoever types it in by hand can only
// differ from it, not improve on it.
//
// Without the right to read one's own policy an empty finding comes back and
// the wizard asks as it did before. That is not an error.
func Discover(ctx context.Context, cfg aws.Config) (Finding, error) {
	id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return Finding{}, err
	}
	name := userFromArn(aws.ToString(id.Arn))
	if name == "" {
		// A role, root or another identity type - has no user policy.
		return Finding{}, nil
	}
	f := fromPolicies(policyDocuments(ctx, iam.NewFromConfig(cfg), name))
	f.User = name
	if f.Bucket != "" {
		f.Region = BucketRegion(ctx, cfg, f.Bucket)
	}
	return f, nil
}

// userFromArn pulls the user name out of arn:aws:iam::123:user/path/name.
// For anything else (assumed role, root, a service) "" comes back.
func userFromArn(arn string) string {
	i := strings.Index(arn, ":user/")
	if i < 0 {
		return ""
	}
	rest := arn[i+len(":user/"):]
	if j := strings.LastIndex(rest, "/"); j >= 0 { // Pfad abschneiden
		rest = rest[j+1:]
	}
	return rest
}

// policyDocuments fetches the user's policy documents - the inline ones first,
// then the attached ones. Every step may fail without dragging the rest down:
// most accesses may read only part of this, some nothing at all.
func policyDocuments(ctx context.Context, c *iam.Client, user string) []string {
	var out []string
	if l, err := c.ListUserPolicies(ctx, &iam.ListUserPoliciesInput{UserName: &user}); err == nil {
		for _, n := range l.PolicyNames {
			p, err := c.GetUserPolicy(ctx, &iam.GetUserPolicyInput{UserName: &user, PolicyName: &n})
			if err == nil {
				out = append(out, aws.ToString(p.PolicyDocument))
			}
		}
	}
	if l, err := c.ListAttachedUserPolicies(ctx,
		&iam.ListAttachedUserPoliciesInput{UserName: &user}); err == nil {
		for _, a := range l.AttachedPolicies {
			m, err := c.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: a.PolicyArn})
			if err != nil {
				continue
			}
			v, err := c.GetPolicyVersion(ctx, &iam.GetPolicyVersionInput{
				PolicyArn: a.PolicyArn, VersionId: m.Policy.DefaultVersionId})
			if err == nil {
				out = append(out, aws.ToString(v.PolicyVersion.Document))
			}
		}
	}
	return out
}

// BucketRegion says which region a bucket sits in.
//
// S3 tells this even to somebody who may not touch the bucket: the header
// x-amz-bucket-region comes with a 403 or 301 answer as well. So a HeadBucket
// is enough here, although a mailbox access may not run one at all - we do not
// need the answer, we need the letterhead.
func BucketRegion(ctx context.Context, cfg aws.Config, bucket string) string {
	out, err := s3.NewFromConfig(cfg).HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &bucket})
	if err == nil {
		if r := aws.ToString(out.BucketRegion); r != "" {
			return r
		}
		return cfg.Region
	}
	var response interface{ HTTPResponse() *http.Response }
	if errors.As(err, &response) {
		if r := response.HTTPResponse().Header.Get("x-amz-bucket-region"); r != "" {
			return r
		}
	}
	return ""
}
