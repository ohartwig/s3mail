// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package wizard

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ohartwig/s3mail/awsx"
	"github.com/ohartwig/s3mail/config"
	"github.com/ohartwig/s3mail/core"
	"github.com/ohartwig/s3mail/i18n"
	"github.com/ohartwig/s3mail/qr"
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

// Device answers /api/setup/device: it lets a phone in.
//
// What it used to do was hand out homework - create this IAM user, paste this
// policy, mint a key, type forty characters on a phone. That is a reasonable
// thing to ask of whoever runs the infrastructure and an unreasonable thing to
// ask of whoever reads the mail, and s3mail is for the second person.
//
// Now it does the work: creates the device user under the mailbox's device
// path with the permissions boundary attached, mints one key, finds the push
// targets by looking rather than asking, and seals the key with a PIN it shows
// beside the code instead of inside it.
//
// The boundary is what makes this safe rather than trusting - see
// mail/devices.tf in koh-infra. Whatever policy is written here, a device can
// never exceed it, so this call cannot grant more than the access it runs
// under already has.
func (a *Wizard) Device(ctx context.Context, d Data) (map[string]any, error) {
	cat := i18n.Get(d.Language)
	if strings.TrimSpace(d.Bucket) == "" {
		return nil, inputError("%s", cat.T("setup.error.noBucket"))
	}
	if strings.TrimSpace(d.DeviceName) == "" {
		return nil, inputError("%s", cat.T("setup.error.noDeviceName"))
	}
	prefix := config.NormalizePrefix(d.Prefix)

	cfg, err := awsx.Session(ctx, d.Profile, d.Region)
	if err != nil {
		return nil, fmt.Errorf("%s", awsx.PlainText(err, d.Profile, cat))
	}
	found, err := awsx.Discover(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("%s", awsx.PlainText(err, d.Profile, cat))
	}
	if found.Mailbox == "" || found.DeviceBoundary == "" {
		// The mailbox has no device path or no boundary, which means the
		// infrastructure predates this. Saying so beats a 403 from IAM three
		// calls later that names neither.
		return nil, inputError("%s", cat.T("setup.error.noDeviceSupport"))
	}

	devices := awsx.NewDevices(cfg, "")

	// Look for the platform applications rather than asking. Failing to find
	// them is not a reason to stop: a mailbox without push is still a mailbox,
	// and the device simply will not register.
	// ownApps says whether the platform applications belong to this mailbox
	// alone. It decides one permission and nothing else - see
	// awsx.DevicePolicyOpts.RefreshApps.
	apps, ownApps, _ := devices.PushTargets(ctx, appleBundleID, found.Mailbox)
	topic := strings.TrimSpace(d.PushTopic)
	if topic == "" && found.PushTopic != "" {
		topic = found.PushTopic
	}

	policy, err := awsx.DevicePolicy(awsx.DevicePolicyOpts{
		Bucket: d.Bucket, Prefix: prefix,
		// Sending is off unless the mailbox has a verified sender at all -
		// without one there is nothing to send as, and a permission that
		// cannot be used is one nobody watches.
		AllowSend: strings.TrimSpace(d.From) != "",
		PushApps:  pushARNs(apps),
		// Only where the application is this mailbox's own. On the shared pair
		// the right would reach other mailboxes' endpoints, so a device paired
		// against those registers and simply cannot refresh - which is what
		// every device did before this existed.
		RefreshApps: refreshARNs(apps, ownApps),
		PushTopic:   topic,
	})
	if err != nil {
		return nil, err
	}

	device, err := devices.Create(ctx, awsx.DeviceOpts{
		Mailbox:   found.Mailbox,
		Name:      d.DeviceName,
		Boundary:  found.DeviceBoundary,
		Policy:    policy,
		AllowSend: strings.TrimSpace(d.From) != "",
	})
	if err != nil {
		return nil, fmt.Errorf("%s", awsx.PlainText(err, d.Profile, cat))
	}

	pin, err := core.NewPIN()
	if err != nil {
		return nil, err
	}
	sealed, salt, err := core.SealSecret(device.Secret, pin)
	if err != nil {
		return nil, err
	}

	// The code carries the sealed box and never the PIN. That separation is
	// the whole reason a photograph of the screen is not the device's access.
	payload, err := json.Marshal(map[string]any{
		"bucket": d.Bucket, "prefix": prefix, "region": d.Region,
		"from": strings.TrimSpace(d.From), "label": strings.TrimSpace(d.Label),
		"accessKey": device.AccessKey,
		"secret":    "",
		"sealed":    sealed,
		"salt":      salt,
		"pushApps":  notNilMap(apps), "pushTopic": topic,
	})
	if err != nil {
		return nil, err
	}

	image := ""
	if code, err := qr.Encode(string(payload)); err == nil {
		image = code.SVG(4)
	}

	return map[string]any{
		"user": device.User,
		// Handed back on close so the dialog can be settled - see
		// DeviceAbandon. An id, not a secret.
		"key":     device.AccessKey,
		"pin":     pin,
		"payload": string(payload),
		"qr":      image,
		"push":    len(apps) > 0 && topic != "",
	}, nil
}

// DeviceAbandon answers /api/setup/device/abandon: the dialog closed without
// the phone ever using its key.
//
// This is what actually bounds the exposure of a setup code, and it does more
// than the PIN does. A photograph of an abandoned code becomes a ciphertext for
// a key that no longer exists.
//
// It asks first whether the key was used, and treats "cannot tell" as used: a
// network hiccup here must not take a working phone's key away. Leaving one key
// too many is visible and fixable; a mailbox that went silent is neither.
func (a *Wizard) DeviceAbandon(ctx context.Context, d Data) (map[string]any, error) {
	cat := i18n.Get(d.Language)
	user := strings.TrimSpace(d.DeviceUser)
	if user == "" {
		return map[string]any{"removed": false}, nil
	}
	cfg, err := awsx.Session(ctx, d.Profile, d.Region)
	if err != nil {
		return nil, fmt.Errorf("%s", awsx.PlainText(err, d.Profile, cat))
	}
	devices := awsx.NewDevices(cfg, "")

	minted := strings.TrimSpace(d.DeviceKey)
	state := core.Pairing{Minted: minted}
	if minted != "" {
		used, err := devices.KeyUsed(ctx, minted)
		others, oerr := devices.OtherKeys(ctx, user, minted)
		// Either question going unanswered means the same thing: decide
		// nothing. See core.DecidePairing.
		state.Used, state.Others = used, len(others)
		state.CanTell = err == nil && oerr == nil
	}

	switch core.DecidePairing(state) {
	case core.PairingDone:
		// The phone took it, so the key it replaced is dead weight.
		others, err := devices.OtherKeys(ctx, user, minted)
		if err != nil {
			return map[string]any{"removed": false}, nil
		}
		for _, old := range others {
			if err := devices.DropKey(ctx, user, old); err != nil {
				return nil, fmt.Errorf("%s", awsx.PlainText(err, d.Profile, cat))
			}
		}
		return map[string]any{"removed": false, "paired": true}, nil

	case core.PairingDropDevice:
		if err := devices.Remove(ctx, user); err != nil {
			return nil, fmt.Errorf("%s", awsx.PlainText(err, d.Profile, cat))
		}
		return map[string]any{"removed": true}, nil

	case core.PairingKeepPrevious:
		if err := devices.DropKey(ctx, user, minted); err != nil {
			return nil, fmt.Errorf("%s", awsx.PlainText(err, d.Profile, cat))
		}
		return map[string]any{"removed": false, "kept": true}, nil
	}
	return map[string]any{"removed": false}, nil
}

// appleBundleID is the identifier of the iOS app, used to tell this app's
// platform applications from any other in the account. Picking the wrong one
// produces an endpoint that looks fine and receives nothing.
const appleBundleID = "eu.ole-hartwig.s3mail"

// refreshARNs is pushARNs, or nothing at all when the applications are shared.
//
// Written as its own function so the condition has a name: the difference
// between "may repair its own endpoint" and "may reach into another mailbox's"
// is one boolean, and a boolean inlined in a struct literal is a boolean nobody
// reads twice.
func refreshARNs(apps map[string]string, own bool) []string {
	if !own {
		return nil
	}
	return pushARNs(apps)
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
	slices.Sort(out)
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
