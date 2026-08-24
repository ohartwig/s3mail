// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// LifecycleRuleID is the identifier of our trash rule. Anything else in the
// bucket's lifecycle configuration is left alone.
const LifecycleRuleID = "s3mail-trash"

// Buckets lists every bucket of the account. Without s3:ListAllMyBuckets an
// empty list comes back - the wizard then lets the name be typed in.
func (a *S3) Buckets(ctx context.Context) ([]string, error) {
	resp, err := a.c.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && (api.ErrorCode() == "AccessDenied" ||
			api.ErrorCode() == "AccessDeniedException") {
			// An empty slice, not nil: a nil slice becomes JSON `null`, and the
			// interface calls .map() on it. A missing s3:ListAllMyBuckets is the normal
			// case - mailbox users deliberately do not get it.
			return []string{}, nil
		}
		return []string{}, err
	}
	out := make([]string, 0, len(resp.Buckets))
	for _, b := range resp.Buckets {
		out = append(out, aws.ToString(b.Name))
	}
	return out, nil
}

// LifecycleDays reads after how many days the trash is emptied. 0 means
// "no rule of ours".
func (a *S3) LifecycleDays(ctx context.Context, bucket string) int {
	resp, err := a.c.GetBucketLifecycleConfiguration(ctx,
		&s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(bucket)})
	if err != nil {
		return 0
	}
	for _, r := range resp.Rules {
		if aws.ToString(r.ID) == LifecycleRuleID && r.Expiration != nil {
			return int(aws.ToInt32(r.Expiration.Days))
		}
	}
	return 0
}

// SetLifecycle creates the trash rule, changes it, or takes it out again.
// tage=0 wieder heraus. Fremde Regeln im Bucket bleiben stehen.
func (a *S3) SetLifecycle(ctx context.Context, bucket, prefix string, days int) error {
	var existing []types.LifecycleRule
	if resp, err := a.c.GetBucketLifecycleConfiguration(ctx,
		&s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(bucket)}); err == nil {
		for _, r := range resp.Rules {
			if aws.ToString(r.ID) != LifecycleRuleID {
				existing = append(existing, r)
			}
		}
	}
	if days > 0 {
		existing = append(existing, types.LifecycleRule{
			ID:         aws.String(LifecycleRuleID),
			Status:     types.ExpirationStatusEnabled,
			Filter:     &types.LifecycleRuleFilter{Prefix: aws.String(prefix + "trash/")},
			Expiration: &types.LifecycleExpiration{Days: aws.Int32(int32(days))},
		})
	}
	if len(existing) == 0 {
		if _, err := a.c.DeleteBucketLifecycle(ctx,
			&s3.DeleteBucketLifecycleInput{Bucket: aws.String(bucket)}); err != nil {
			return err
		}
		return nil
	}
	_, err := a.c.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket:                 aws.String(bucket),
		LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: existing},
	})
	if err != nil {
		return err
	}
	if days == 0 {
		return nil
	}
	return nil
}
