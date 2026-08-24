// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"

	"s3mail/mailer"
)

// SES sends replies and forwards.
type SES struct{ c *ses.Client }

func NewSES(cfg aws.Config, endpoint string) *SES {
	return &SES{c: ses.NewFromConfig(cfg, func(o *ses.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})}
}

// Send hands the finished message to SES and returns its message ID.
func (s *SES) Send(ctx context.Context, n mailer.Message) (string, error) {
	resp, err := s.c.SendRawEmail(ctx, &ses.SendRawEmailInput{
		Source:       aws.String(n.From),
		Destinations: n.To,
		RawMessage:   &types.RawMessage{Data: n.Raw},
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(resp.MessageId), nil
}

// Identities returns the sender addresses verified in SES. Without the right,
// an empty list comes back instead of an error - the wizard then lets it be
// typed in.
func (s *SES) Identities(ctx context.Context) []string {
	resp, err := s.c.ListIdentities(ctx, &ses.ListIdentitiesInput{
		IdentityType: types.IdentityTypeEmailAddress})
	if err != nil || len(resp.Identities) == 0 {
		return []string{} // nie nil - siehe Buckets()
	}
	attrs, err := s.c.GetIdentityVerificationAttributes(ctx,
		&ses.GetIdentityVerificationAttributesInput{Identities: resp.Identities})
	if err != nil {
		return resp.Identities
	}
	out := []string{}
	for _, id := range resp.Identities {
		if a, present := attrs.VerificationAttributes[id]; present &&
			a.VerificationStatus == types.VerificationStatusSuccess {
			out = append(out, id)
		}
	}
	return out
}

// VerifiedDomains returns the domains that are cleared.
//
// Separate from Identities(), because the two mean different things: a verified
// address is one address, a verified domain allows EVERY address below it.
// Whoever asks only for address identities - the way this file did at first -
// gets an empty list from a setup cleanly verified over the domain, and takes
// it for an error.
func (s *SES) VerifiedDomains(ctx context.Context) []string {
	resp, err := s.c.ListIdentities(ctx, &ses.ListIdentitiesInput{
		IdentityType: types.IdentityTypeDomain})
	if err != nil || len(resp.Identities) == 0 {
		return []string{}
	}
	attrs, err := s.c.GetIdentityVerificationAttributes(ctx,
		&ses.GetIdentityVerificationAttributesInput{Identities: resp.Identities})
	if err != nil {
		return resp.Identities
	}
	out := []string{}
	for _, id := range resp.Identities {
		if a, present := attrs.VerificationAttributes[id]; present &&
			a.VerificationStatus == types.VerificationStatusSuccess {
			out = append(out, id)
		}
	}
	return out
}

// Verified says whether address or domain are cleared in SES.
func (s *SES) Verified(ctx context.Context, address, domain string) ([]string, error) {
	resp, err := s.c.GetIdentityVerificationAttributes(ctx,
		&ses.GetIdentityVerificationAttributesInput{Identities: []string{address, domain}})
	if err != nil {
		return nil, err
	}
	var good []string
	for _, id := range []string{address, domain} {
		if a, present := resp.VerificationAttributes[id]; present &&
			a.VerificationStatus == types.VerificationStatusSuccess {
			good = append(good, id)
		}
	}
	return good, nil
}
