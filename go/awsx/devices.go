// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
)

// Letting a device in, without anybody opening the AWS console.
//
// The mailbox access may create users under its own device path, and only with
// a permissions boundary attached - see mail/devices.tf in koh-infra, where the
// reasoning lives. Everything here works inside that: it cannot create a user
// with more rights than the boundary allows, and it cannot take the boundary
// off afterwards, because the right to do so was not granted.

// Device is what the wizard needs to know about a device it just let in.
type Device struct {
	User      string
	AccessKey string
	Secret    string
}

// DeviceOpts describes the device being let in.
type DeviceOpts struct {
	// Mailbox is the local part - "ole" - which is also the path segment the
	// device user lives under.
	Mailbox string
	// Name is what a person calls the device. It ends up in the IAM user name,
	// so that whoever later reads a list of users can tell which phone they are
	// about to lock out without opening anything.
	Name string
	// Boundary is the ARN of the permissions boundary. Required: without it the
	// call would be refused anyway, and refusing here says why.
	Boundary string
	// Policy is the document the device gets. Narrower than the boundary or
	// equal to it, never wider - the boundary would silently win, and the
	// difference between "granted" and "effective" is where people get lost.
	Policy string
	// AllowSend was decided further up; it only shows in Policy. Kept here so
	// the user's tags say what the device may do without reading the policy.
	AllowSend bool
}

// Devices is the IAM and SNS side of pairing.
type Devices struct {
	iam *iam.Client
	sns *sns.Client
}

func NewDevices(cfg aws.Config, endpoint string) *Devices {
	return &Devices{
		iam: iam.NewFromConfig(cfg, func(o *iam.Options) {
			if endpoint != "" {
				o.BaseEndpoint = aws.String(endpoint)
			}
		}),
		sns: sns.NewFromConfig(cfg, func(o *sns.Options) {
			if endpoint != "" {
				o.BaseEndpoint = aws.String(endpoint)
			}
		}),
	}
}

// Create makes the device user, attaches its policy and mints one key.
//
// The key is returned and never stored: this is the only moment it exists
// outside AWS, and it goes straight into the setup code. If the pairing is
// abandoned, Remove takes the whole user away again - see the comment there
// about why that matters more than the PIN does.
func (d *Devices) Create(ctx context.Context, o DeviceOpts) (Device, error) {
	if o.Mailbox == "" || o.Boundary == "" || o.Policy == "" {
		return Device{}, errors.New("mailbox, boundary and policy are all required")
	}
	name := DeviceUserName(o.Mailbox, o.Name)
	path := "/s3mail-devices/" + o.Mailbox + "/"

	_, err := d.iam.CreateUser(ctx, &iam.CreateUserInput{
		UserName:            aws.String(name),
		Path:                aws.String(path),
		PermissionsBoundary: aws.String(o.Boundary),
		Tags: []iamtypes.Tag{
			{Key: aws.String("s3mail-device"), Value: aws.String(o.Name)},
			{Key: aws.String("s3mail-mailbox"), Value: aws.String(o.Mailbox)},
			// Written down because a device list without dates is a list
			// nobody dares to prune.
			{Key: aws.String("s3mail-paired"), Value: aws.String(time.Now().UTC().Format(time.RFC3339))},
		},
	})
	var exists *iamtypes.EntityAlreadyExistsException
	if err != nil && !errors.As(err, &exists) {
		return Device{}, err
	}
	// An existing user is not an error worth stopping for: somebody paired this
	// device before and is doing it again, and the policy and key below are
	// replaced rather than added to.

	if _, err := d.iam.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       aws.String(name),
		PolicyName:     aws.String("mailbox"),
		PolicyDocument: aws.String(o.Policy),
	}); err != nil {
		return Device{}, err
	}

	// One key per device. An old one is taken away first: two live keys for one
	// phone means revoking the phone is two actions, and somebody will do one.
	if err := d.dropKeys(ctx, name); err != nil {
		return Device{}, err
	}
	key, err := d.iam.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{
		UserName: aws.String(name),
	})
	if err != nil {
		return Device{}, err
	}
	return Device{
		User:      name,
		AccessKey: aws.ToString(key.AccessKey.AccessKeyId),
		Secret:    aws.ToString(key.AccessKey.SecretAccessKey),
	}, nil
}

// Used says whether the device has ever made a call with its key.
//
// This is what bounds the exposure of a setup code far better than the PIN
// does: the wizard mints the key when the code appears and asks this when the
// dialog closes. Never used means the pairing did not happen, and the key can
// go - so a photograph of an abandoned code is a ciphertext for a key that no
// longer exists.
func (d *Devices) Used(ctx context.Context, user string) (bool, error) {
	keys, err := d.iam.ListAccessKeys(ctx, &iam.ListAccessKeysInput{
		UserName: aws.String(user),
	})
	if err != nil {
		return false, err
	}
	for _, k := range keys.AccessKeyMetadata {
		out, err := d.iam.GetAccessKeyLastUsed(ctx, &iam.GetAccessKeyLastUsedInput{
			AccessKeyId: k.AccessKeyId,
		})
		if err != nil {
			// Cannot tell - and "cannot tell" must not read as "unused", or an
			// abandoned-looking pairing would take a working phone's key away.
			return true, err
		}
		if out.AccessKeyLastUsed != nil && out.AccessKeyLastUsed.LastUsedDate != nil {
			return true, nil
		}
	}
	return false, nil
}

// Remove takes the device out entirely: key, policy, user.
//
// In that order, and the order is the point. The key first, because that is
// what actually opens anything; if the call dies halfway, what is left behind
// is a user that can do nothing rather than a key nobody is watching.
func (d *Devices) Remove(ctx context.Context, user string) error {
	if err := d.dropKeys(ctx, user); err != nil {
		return err
	}
	_, err := d.iam.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{
		UserName: aws.String(user), PolicyName: aws.String("mailbox"),
	})
	var missing *iamtypes.NoSuchEntityException
	if err != nil && !errors.As(err, &missing) {
		return err
	}
	_, err = d.iam.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
	if err != nil && !errors.As(err, &missing) {
		return err
	}
	return nil
}

func (d *Devices) dropKeys(ctx context.Context, user string) error {
	keys, err := d.iam.ListAccessKeys(ctx, &iam.ListAccessKeysInput{
		UserName: aws.String(user),
	})
	var missing *iamtypes.NoSuchEntityException
	if errors.As(err, &missing) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, k := range keys.AccessKeyMetadata {
		if _, err := d.iam.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{
			UserName: aws.String(user), AccessKeyId: k.AccessKeyId,
		}); err != nil {
			return err
		}
	}
	return nil
}

// PushTargets finds the platform applications instead of asking for their ARNs.
//
// Two calls, and they replace two fields nobody could have filled in: the ARN
// of an SNS platform application is not something a person knows, and a wizard
// that asks for it has given up on the promise it makes everywhere else.
//
// Keyed by Apple's environment names, because that is what the device compares
// against its own provisioning profile.
//
// The second answer - own - says whether every application found belongs to
// this mailbox alone, and it decides a permission rather than a preference.
// SNS authorises GetEndpointAttributes and SetEndpointAttributes against the
// *application*, never the endpoint. On an application shared by several
// mailboxes, handing a device those two would let it retarget or silence a
// different mailbox's phone - so the right may only be granted where the
// application is named for one mailbox. See DevicePolicyOpts.RefreshApps.
//
// Both generations are accepted on purpose. The shared pair came first and is
// still what most accounts have; a device paired against it registers exactly
// as before and simply does not get the refresh right.
func (d *Devices) PushTargets(ctx context.Context, bundleID, mailbox string) (map[string]string, bool, error) {
	out, err := d.sns.ListPlatformApplications(ctx, &sns.ListPlatformApplicationsInput{})
	if err != nil {
		return nil, false, err
	}
	apps := map[string]string{}
	own := map[string]bool{}
	for _, a := range out.PlatformApplications {
		arn := aws.ToString(a.PlatformApplicationArn)
		// The ARN carries the platform: .../app/APNS/name or
		// .../app/APNS_SANDBOX/name. Matching on the attribute would need
		// another call per application.
		env := ""
		switch {
		case strings.Contains(arn, ":app/APNS_SANDBOX/"):
			env = "development"
		case strings.Contains(arn, ":app/APNS/"):
			env = "production"
		default:
			continue
		}
		if !matchesBundle(a.Attributes, bundleID) {
			continue
		}
		// The mailbox's own application wins over the shared one whatever
		// order SNS listed them in.
		mine := ownsApp(arn, mailbox)
		if _, seen := apps[env]; seen && !mine {
			continue
		}
		apps[env] = arn
		own[env] = mine
	}
	all := len(apps) > 0
	for _, mine := range own {
		if !mine {
			all = false
		}
	}
	return apps, all, nil
}

// ownsApp says whether an application is named for this mailbox rather than
// shared by all of them - "s3mail-ios-sandbox-ole" against "s3mail-ios-sandbox".
func ownsApp(arn, mailbox string) bool {
	if mailbox == "" {
		return false
	}
	name := arn[strings.LastIndex(arn, "/")+1:]
	return strings.HasSuffix(name, "-"+mailbox)
}

// matchesBundle keeps another app's notifications out of this mailbox. An
// account may hold platform applications for several apps, and picking the
// wrong one produces an endpoint that looks fine and receives nothing.
func matchesBundle(attrs map[string]string, bundleID string) bool {
	if bundleID == "" {
		return true
	}
	got, present := attrs["ApplePlatformBundleID"]
	return !present || got == bundleID
}

// DeviceUserName builds a name that says which mailbox and which device.
//
// Whoever reads a list of IAM users after losing a phone should be able to tell
// which line to delete without opening anything.
func DeviceUserName(mailbox, name string) string {
	clean := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				return r
			case r == ' ', r == '-', r == '_', r == '.':
				return '-'
			}
			return -1
		}, s)
		return strings.Trim(s, "-")
	}
	device := clean(name)
	if device == "" {
		device = "device"
	}
	if len(device) > 32 {
		device = device[:32]
	}
	return fmt.Sprintf("s3mail-%s-%s", clean(mailbox), device)
}
