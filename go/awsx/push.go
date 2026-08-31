// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx

import (
	"context"
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"

	"git.ole-hartwig.eu/development/s3mail/s3mail/core"
)

// Registering a phone for push. The rules are in core/push.go; this is the
// SDK.
//
// Nothing on the desktop uses it. It lives here anyway rather than in the
// mobile bridge, because this package is where the AWS calls are - all of them,
// so that "which SDK calls does s3mail make" has one answer and not two.
type Push struct {
	c *sns.Client
}

func NewPush(cfg aws.Config, endpoint string) *Push {
	return &Push{c: sns.NewFromConfig(cfg, func(o *sns.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})}
}

// Register makes sure this device has a usable endpoint and returns its ARN.
//
// Idempotent on purpose: the app calls it on every launch. A token can change
// between launches, and an endpoint can be disabled between them without
// anybody being told, so "already registered" is not a state worth trusting.
//
// storedARN is what the device wrote down last time, empty on the first run.
func (p *Push) Register(ctx context.Context, appARN, storedARN, token string) (string, error) {
	if appARN == "" {
		return "", errors.New("no platform application for this build")
	}
	if token == "" {
		return storedARN, nil
	}

	stored := core.Endpoint{ARN: storedARN}
	if storedARN != "" {
		attrs, err := p.c.GetEndpointAttributes(ctx,
			&sns.GetEndpointAttributesInput{EndpointArn: aws.String(storedARN)})
		switch {
		case err == nil:
			stored.Exists = true
			stored.Token = attrs.Attributes["Token"]
			stored.Enabled = attrs.Attributes["Enabled"] == "true"
		case isNotFound(err):
			// The endpoint is gone - deleted, or this is a restore onto
			// another phone. Not an error: it is the reason to make a new one.
		case isNotAllowed(err):
			// No right to look. Also not an error, and this is the common case
			// rather than an exotic one: SNS authorises reading an endpoint
			// against the *platform application*, so on an application shared
			// by several mailboxes the right cannot be granted without letting
			// one device reach another's endpoints - see DevicePolicyOpts.
			//
			// Registering does not need it. CreatePlatformEndpoint below is
			// idempotent for a token it already knows, so the device still
			// ends up with its endpoint. What is lost is repair: an endpoint
			// APNs disabled while the token stayed the same will not come
			// back. Rare - a token that goes stale is usually replaced, and a
			// new token makes a new endpoint - and far better than refusing to
			// register at all, which is what this used to do.
		default:
			return "", err
		}
	}

	switch core.DecidePush(stored, token) {
	case core.PushKeep:
		return storedARN, nil

	case core.PushRefresh:
		_, err := p.c.SetEndpointAttributes(ctx, &sns.SetEndpointAttributesInput{
			EndpointArn: aws.String(storedARN),
			Attributes:  map[string]string{"Token": token, "Enabled": "true"},
		})
		if isNotAllowed(err) {
			// The endpoint is there and this is the ARN for it. Saying so is
			// more truthful than an error: what failed is the repair, not the
			// registration, and a device that reports failure here would stop
			// receiving the notifications it can still receive.
			return storedARN, nil
		}
		return storedARN, err
	}

	out, err := p.c.CreatePlatformEndpoint(ctx, &sns.CreatePlatformEndpointInput{
		PlatformApplicationArn: aws.String(appARN),
		Token:                  aws.String(token),
	})
	if err == nil {
		return aws.ToString(out.EndpointArn), nil
	}
	// SNS refuses a second endpoint for a token it already knows - and puts the
	// existing ARN in the error text, which is the only place it can be read
	// from. Ugly, and the documented way: the alternative is listing every
	// endpoint of the application on every launch.
	if arn, ok := existingEndpoint(err); ok {
		_, seterr := p.c.SetEndpointAttributes(ctx, &sns.SetEndpointAttributesInput{
			EndpointArn: aws.String(arn),
			Attributes:  map[string]string{"Token": token, "Enabled": "true"},
		})
		if isNotAllowed(seterr) {
			return arn, nil
		}
		return arn, seterr
	}
	return "", err
}

// Subscribe hangs the endpoint on the topic and answers with the subscription.
//
// Also idempotent: SNS answers a repeat subscription of the same endpoint to
// the same topic with the subscription that is already there. So the device
// need not remember whether it subscribed - which is good, because the one
// thing it cannot check cheaply is the topic's subscriber list.
func (p *Push) Subscribe(ctx context.Context, topicARN, endpointARN string) (string, error) {
	if topicARN == "" || endpointARN == "" {
		return "", errors.New("no topic or no endpoint")
	}
	out, err := p.c.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: aws.String(topicARN),
		Protocol: aws.String("application"),
		Endpoint: aws.String(endpointARN),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.SubscriptionArn), nil
}

func isNotFound(err error) bool {
	_, ok := errors.AsType[*types.NotFoundException](err)
	return ok
}

// isNotAllowed reports the one refusal that is a design decision rather than a
// fault: the device has no right to read or change an endpoint.
//
// SNS answers with AuthorizationError and not AccessDenied, which is worth
// writing down - it is the reason this was mistaken for an S3 problem once.
func isNotAllowed(err error) bool {
	_, ok := errors.AsType[*types.AuthorizationErrorException](err)
	return ok
}

// existingEndpoint digs the ARN out of "Endpoint arn:aws:sns:... already exists
// with the same Token, but different attributes."
func existingEndpoint(err error) (string, bool) {
	invalid, ok := errors.AsType[*types.InvalidParameterException](err)
	if !ok {
		return "", false
	}
	text := aws.ToString(invalid.Message)
	if !strings.Contains(text, "already exists with the same Token") {
		return "", false
	}
	i := strings.Index(text, "arn:aws:sns:")
	if i < 0 {
		return "", false
	}
	arn := text[i:]
	// The message continues after the ARN; cut at the first space.
	if j := strings.IndexAny(arn, " "); j > 0 {
		arn = arn[:j]
	}
	return arn, true
}
