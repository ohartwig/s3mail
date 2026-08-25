// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The policy for a second device - a phone, usually.
//
// A phone should not carry the same access key as the desktop. Not because it
// is less trustworthy, but because of what happens when it is lost: with one
// shared key, revoking means rotating everywhere, and nobody does that at half
// past eleven at night. With one IAM user per device it is a single click, and
// the desktop keeps working.
//
// s3mail cannot create that user. Its own access may read its policy and
// nothing else - by design, and that design is worth keeping. So it does the
// part it can: it writes out exactly the policy the new user needs, derived
// from the mailbox it is actually looking at, and a person pastes it into the
// IAM console. One step more, and it buys the ability to revoke one device
// without touching the others.
//
// What the device policy is *not* is a copy of the desktop's. Two things are
// left out on purpose - see DevicePolicy.

// DevicePolicyOpts describes the mailbox the device is being let into.
type DevicePolicyOpts struct {
	Bucket string
	Prefix string
	// KMSKey is the ARN of the key, when the mailbox is client-side encrypted.
	// Empty leaves the KMS part out entirely rather than writing a permission
	// that matches everything.
	KMSKey string
	// AllowSend adds ses:SendRawEmail. Off by default: a device that only reads
	// cannot be talked into sending, and most second devices are read-only in
	// practice long before anybody decides they should be.
	AllowSend bool
}

// DevicePolicy renders the policy document for a device user.
//
// Two things the desktop has and the device does not get:
//
//   - **No SQS.** Push on a phone comes from SNS to APNs, not from a queue the
//     device polls. Handing it queue permissions would be a permission nobody
//     uses, and an unused permission is one nobody notices being abused.
//   - **No sending unless asked.** See AllowSend.
//
// Everything else mirrors the mailbox exactly, including the prefix condition
// on ListBucket - without it the device could list the whole bucket, which is
// the difference between "a mailbox" and "the file store it happens to live in".
func DevicePolicy(o DevicePolicyOpts) (string, error) {
	if o.Bucket == "" {
		return "", fmt.Errorf("no bucket")
	}
	prefix := o.Prefix
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	type stmt struct {
		Effect    string `json:"Effect"`
		Action    any    `json:"Action"`
		Resource  any    `json:"Resource"`
		Condition any    `json:"Condition,omitempty"`
	}

	listCond := any(nil)
	objects := "arn:aws:s3:::" + o.Bucket + "/*"
	if prefix != "" {
		listCond = map[string]any{
			"StringLike": map[string]any{"s3:prefix": []string{prefix + "*"}},
		}
		objects = "arn:aws:s3:::" + o.Bucket + "/" + prefix + "*"
	}

	out := []stmt{
		{Effect: "Allow", Action: "s3:ListBucket",
			Resource: "arn:aws:s3:::" + o.Bucket, Condition: listCond},
		{Effect: "Allow",
			Action:   []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject"},
			Resource: objects},
	}
	if o.AllowSend {
		out = append(out, stmt{Effect: "Allow", Action: "ses:SendRawEmail", Resource: "*"})
	}
	if o.KMSKey != "" {
		out = append(out, stmt{Effect: "Allow",
			Action:   []string{"kms:Decrypt", "kms:GenerateDataKey"},
			Resource: o.KMSKey})
	}

	blob, err := json.MarshalIndent(map[string]any{
		"Version":   "2012-10-17",
		"Statement": out,
	}, "", "  ")
	return string(blob), err
}
