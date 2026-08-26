// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package wizard

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"git.ole-hartwig.eu/development/s3mail/s3mail/awsx"
	"git.ole-hartwig.eu/development/s3mail/s3mail/config"
	"git.ole-hartwig.eu/development/s3mail/s3mail/i18n"
	"git.ole-hartwig.eu/development/s3mail/s3mail/qr"
)

// Letting a second device into the mailbox - a phone, in practice.
//
// The wizard already works out bucket, prefix and sender from the IAM policy of
// the access. Asking somebody to retype that on a phone would undo the one
// thing this setup is proud of, so the phone reads a code instead.
//
// What it cannot do is create the IAM user for that device. The mailbox access
// may read its own policy and nothing more - which is the right shape, and
// worth keeping. So the wizard hands out two things: the policy to paste into
// the IAM console, and the code that carries what the device needs once the key
// exists.
//
// Why a separate user at all: with one shared key, a lost phone means rotating
// everywhere, and nobody does that at half past eleven at night. With one user
// per device it is a single click, and the desktop keeps working.

// Device answers /api/setup/device.
func (a *Wizard) Device(_ context.Context, d Data) (map[string]any, error) {
	cat := i18n.Get(d.Language)
	if strings.TrimSpace(d.Bucket) == "" {
		return nil, inputError("%s", cat.T("setup.error.noBucket"))
	}
	prefix := config.NormalizePrefix(d.Prefix)

	policy, err := awsx.DevicePolicy(awsx.DevicePolicyOpts{
		Bucket: d.Bucket, Prefix: prefix,
		// Sending is off unless the mailbox has a verified sender at all -
		// without one there is nothing to send as, and a permission that
		// cannot be used is one nobody watches.
		AllowSend: strings.TrimSpace(d.From) != "",
		// Push only when both halves are there. One without the other is a
		// permission that cannot be used, and an unused permission is one
		// nobody notices being abused.
		// The policy only needs the ARNs; which environment each belongs to is
		// the device's problem, not IAM's.
		PushApps:  pushARNs(d.PushApps),
		PushTopic: strings.TrimSpace(d.PushTopic),
	})
	if err != nil {
		return nil, err
	}

	// The payload the phone reads. Without the key: that one belongs to the
	// device and is pasted in after the IAM user exists. A code that already
	// carried a key would be a key travelling through a screenshot.
	// The ARNs travel with the code because the device cannot work them out.
	// They are not secret - an ARN names a resource, it does not open it, and
	// the policy above decides what the device may do with it.
	payload, err := json.Marshal(map[string]any{
		"bucket": d.Bucket, "prefix": prefix, "region": d.Region,
		"from": strings.TrimSpace(d.From), "label": strings.TrimSpace(d.Label),
		"accessKey": "", "secret": "",
		"pushApps": notNilMap(d.PushApps), "pushTopic": strings.TrimSpace(d.PushTopic),
	})
	if err != nil {
		return nil, err
	}

	// The code as an image, so nobody types four hundred characters into a
	// phone. Rendered here and not in the browser: the encoder is Go, it is
	// tested, and one place that produces the code is one place to get it
	// wrong.
	//
	// A payload too large for a symbol is not an error worth stopping for - the
	// text below it can still be copied, and refusing the whole dialog over a
	// missing picture would be the wrong trade.
	image := ""
	if code, err := qr.Encode(string(payload)); err == nil {
		image = code.SVG(4)
	}

	return map[string]any{
		"policy":  policy,
		"payload": string(payload),
		"qr":      image,
		"user":    suggestedUserName(d.Bucket, prefix),
	}, nil
}

// suggestedUserName proposes a name that says which mailbox and which device -
// "s3mail-post-mail-ole-geraet". Whoever later reads a list of IAM users should
// be able to tell what a name is for without opening it.
func suggestedUserName(bucket, prefix string) string {
	clean := func(s string) string {
		s = strings.ToLower(s)
		s = strings.NewReplacer("/", "-", ".", "-", "_", "-", " ", "-").Replace(s)
		return strings.Trim(s, "-")
	}
	parts := []string{"s3mail", clean(bucket)}
	if p := clean(prefix); p != "" {
		parts = append(parts, p)
	}
	return strings.Join(append(parts, "geraet"), "-")
}

// pushARNs is the policy's view of the applications: the ARNs, in a stable
// order so two runs produce the same document.
func pushARNs(apps map[string]string) []string {
	out := make([]string, 0, len(apps))
	for _, arn := range apps {
		if arn = strings.TrimSpace(arn); arn != "" {
			out = append(out, arn)
		}
	}
	sort.Strings(out)
	return out
}

// notNilMap keeps an absent map out of the code as {} rather than null. A phone
// that finds no field cannot tell "no push here" from "this code is too old to
// know about it".
func notNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
