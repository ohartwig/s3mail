// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package wizard serves the /api/setup/* calls: set up AWS access, pick a
// bucket, test the connection, set the trash lifecycle rule, save.
package wizard

import (
	"context"
	"fmt"
	"strings"

	"git.ole-hartwig.eu/development/s3mail/s3mail/awsx"
	"git.ole-hartwig.eu/development/s3mail/s3mail/check"
	"git.ole-hartwig.eu/development/s3mail/s3mail/config"
	"git.ole-hartwig.eu/development/s3mail/s3mail/i18n"
)

// Regions in which SES accepts incoming mail. Offering others would be a trap -
// the bucket can be created anywhere, the receipt rule cannot.
//
// The names are English and are not translated: that is what they are called at
// AWS, and somebody looking for a region in the console searches for exactly
// that word.
var Regions = [][2]string{
	{"eu-central-1", "Europe (Frankfurt)"}, {"eu-west-1", "Europe (Ireland)"},
	{"eu-west-2", "Europe (London)"}, {"eu-west-3", "Europe (Paris)"},
	{"eu-north-1", "Europe (Stockholm)"}, {"eu-south-1", "Europe (Milan)"},
	{"us-east-1", "US East (N. Virginia)"}, {"us-east-2", "US East (Ohio)"},
	{"us-west-1", "US West (N. California)"}, {"us-west-2", "US West (Oregon)"},
	{"ca-central-1", "Canada (Central)"}, {"sa-east-1", "South America (São Paulo)"},
	{"ap-northeast-1", "Asia Pacific (Tokyo)"}, {"ap-northeast-2", "Asia Pacific (Seoul)"},
	{"ap-southeast-1", "Asia Pacific (Singapore)"}, {"ap-southeast-2", "Asia Pacific (Sydney)"},
	{"ap-south-1", "Asia Pacific (Mumbai)"}, {"il-central-1", "Israel (Tel Aviv)"},
	{"af-south-1", "Africa (Cape Town)"}, {"me-south-1", "Middle East (Bahrain)"},
}

// Data is what the wizard page sends.
type Data struct {
	Profile     string `json:"profile"`
	NewProfile  string `json:"new_profile"`
	KeyID       string `json:"key_id"`
	Secret      string `json:"secret"`
	Region      string `json:"region"`
	Bucket      string `json:"bucket"`
	Prefix      string `json:"prefix"`
	From        string `json:"from"`
	AllowDelete *bool  `json:"allow_delete"`
	Signature   string `json:"signature"`
	Label       string `json:"label"`
	Queue       string `json:"queue"`

	// PushApps and PushTopic describe where a device registers for push.
	//
	// Keyed by Apple's own environment names - "production" and "development" -
	// and not a plain list. A list would travel just as well and the device
	// could not use it: both ARNs look alike, and picking the wrong one gives
	// an endpoint that looks fine and never receives anything. Which one a
	// build needs cannot be decided here; the device reads that from its own
	// provisioning profile.
	PushApps  map[string]string `json:"push_apps"`
	PushTopic string            `json:"push_topic"`

	// DeviceName is what a person calls the phone - "Oles iPhone". It ends up
	// in the IAM user name, so whoever later has to lock a device out can tell
	// which line to delete without opening anything.
	DeviceName string `json:"device_name"`
	// DeviceUser identifies a pairing that is being finished or abandoned. The
	// wizard sends back what Device() gave it.
	DeviceUser string `json:"device_user"`
	// DeviceKey is the access key that pairing handed out. It decides what
	// closing the dialog means: taken by the phone, or never used at all.
	//
	// The key *id*, not the secret - it names a key, it does not open one, and
	// the browser has to be able to hand it back.
	DeviceKey string `json:"device_key"`

	// Account is the mailbox being edited, by its ID. Empty means the first one,
	// "new" means: add one. Without it a second mailbox would overwrite the
	// first, because the wizard only ever knew one.
	Account string `json:"account"`
	Days    int    `json:"days"`

	// Language is set by the HTTP layer from the request, not by the browser
	// from the form: it is already in the cookie there, and two sources for
	// the same fact drift apart eventually.
	Language string `json:"-"`
}

// Activator is called after saving, so the running process switches into the
// mailbox without a restart.
type Activator func(k config.Config) error

type Wizard struct {
	Activate Activator
}

// InputError is a fault the user can fix themselves - the HTTP layer turns it
// into a 400 rather than a 500.
type InputError struct{ Text string }

func (e InputError) Error() string { return e.Text }

func inputError(format string, a ...any) error {
	return InputError{fmt.Sprintf(format, a...)}
}

// Info returns everything the page needs while loading.
func (a *Wizard) Info(_ context.Context, d Data) (map[string]any, error) {
	regions := make([]map[string]string, 0, len(Regions))
	for _, r := range Regions {
		regions = append(regions, map[string]string{"id": r[0], "label": r[1] + " · " + r[0]})
	}
	k := config.Load()
	boxes := make([]map[string]string, 0, len(k.Accounts))
	for _, acc := range k.Accounts {
		boxes = append(boxes, map[string]string{"id": acc.ID(), "name": acc.Name()})
	}
	return map[string]any{
		"config":      flatten(k, editing(k, d.Account)),
		"accounts":    boxes,
		"profiles":    config.Profiles(),
		"regions":     regions,
		"config_file": config.File(),
	}, nil
}

// editing picks the mailbox the wizard is working on: the one named, "new" for
// another one, otherwise the first. A wizard that always edited the first would
// silently overwrite it the moment somebody adds a second.
func editing(k config.Config, id string) config.Account {
	if id == "new" {
		return config.DefaultAccount()
	}
	if id != "" {
		if acc, ok := k.Account(id); ok {
			return acc
		}
	}
	if len(k.Accounts) == 0 {
		return config.DefaultAccount()
	}
	return k.Accounts[0]
}

// flatten is the shape the page expects: one mailbox plus what applies to all
// of them, side by side. The page shows one form, so it gets one object.
func flatten(k config.Config, acc config.Account) map[string]any {
	return map[string]any{
		"profile": acc.Profile, "region": acc.Region, "bucket": acc.Bucket,
		"prefix": acc.Prefix, "from": acc.From, "signature": acc.Signature,
		"label": acc.Label, "account": acc.ID(), "queue": acc.Queue,
		"allow_delete": k.AllowDelete, "language": k.Language,
	}
}

// Credentials writes access key and secret as a named AWS profile.
func (a *Wizard) Credentials(_ context.Context, d Data) (map[string]any, error) {
	name, err := config.WriteCredentials(d.NewProfile, d.KeyID, d.Secret, d.Region)
	if err != nil {
		return nil, InputError{err.Error()}
	}
	return map[string]any{"profile": name, "profiles": config.Profiles()}, nil
}

// Buckets fills the two picker lists.
func (a *Wizard) Buckets(ctx context.Context, d Data) (map[string]any, error) {
	cfg, err := awsx.Session(ctx, d.Profile, d.Region)
	if err == nil {
		err = awsx.CheckAccess(ctx, cfg)
	}
	if err != nil {
		return nil, InputError{awsx.PlainText(err, d.Profile, i18n.Get(d.Language))}
	}
	s3 := awsx.NewS3(cfg, "")
	buckets, err := s3.Buckets(ctx)
	if err != nil {
		return nil, InputError{awsx.PlainText(err, d.Profile, i18n.Get(d.Language))}
	}
	// Never hand nil to the interface: a nil slice marshals to JSON `null`, and
	// `null.map(...)` ends the page's script - the user then does not see "not
	// allowed to list", they see nothing at all.
	ses := awsx.NewSES(cfg, "")
	out := map[string]any{
		"buckets":    notNil(buckets),
		"identities": notNil(ses.Identities(ctx)),
		"domains":    notNil(ses.VerifiedDomains(ctx)),
	}
	// What the access reveals about itself, nobody has to retype. If that fails
	// (older mailboxes may not read their own policy), the wizard falls back to
	// typing - hence no error surfaces here.
	if f, err := awsx.Discover(ctx, cfg); err == nil && (f.Bucket != "" || f.From != "" || f.Queue != "") {
		out["found"] = f
	}
	if len(buckets) == 0 {
		// For mailbox users this is the normal case and not a shortcoming: their
		// policy deliberately withholds s3:ListAllMyBuckets, or everyone would
		// see every bucket in the account. The text therefore leads with what to
		// do, not with what is missing.
		out["note"] = i18n.Get(d.Language).T("setup.note.noListBuckets")
	}
	return out, nil
}

// Test is step 3: the checklist.
func (a *Wizard) Test(ctx context.Context, d Data) (map[string]any, error) {
	if strings.TrimSpace(d.Bucket) == "" {
		return nil, inputError("%s", i18n.Get(d.Language).T("setup.error.noBucket"))
	}
	cfg, err := awsx.Session(ctx, d.Profile, d.Region)
	if err == nil {
		err = awsx.CheckAccess(ctx, cfg)
	}
	if err != nil {
		return nil, InputError{awsx.PlainText(err, d.Profile, i18n.Get(d.Language))}
	}
	s3 := awsx.NewS3(cfg, "")
	prefix := config.NormalizePrefix(d.Prefix)
	items := check.Run(ctx, s3, awsx.NewKMS(cfg, ""), awsx.NewSES(cfg, ""),
		d.Bucket, prefix, strings.TrimSpace(d.From), i18n.Get(d.Language))
	return map[string]any{
		"checks":         items,
		"ok":             check.AllOK(items),
		"lifecycle_days": s3.LifecycleDays(ctx, d.Bucket),
	}, nil
}

// Lifecycle sets or removes the automatic emptying of the trash.
func (a *Wizard) Lifecycle(ctx context.Context, d Data) (map[string]any, error) {
	cfg, err := awsx.Session(ctx, d.Profile, d.Region)
	if err != nil {
		return nil, InputError{awsx.PlainText(err, d.Profile, i18n.Get(d.Language))}
	}
	cat := i18n.Get(d.Language)
	if err := awsx.NewS3(cfg, "").SetLifecycle(ctx, d.Bucket,
		config.NormalizePrefix(d.Prefix), d.Days); err != nil {
		return nil, InputError{awsx.PlainText(err, d.Profile, cat)}
	}
	msg := cat.T("lifecycle.off")
	if d.Days > 0 {
		msg = cat.Tf("lifecycle.on", d.Days)
	}
	return map[string]any{"message": msg}, nil
}

// Save writes the configuration and arms the mailbox.
func (a *Wizard) Save(_ context.Context, d Data) (map[string]any, error) {
	k := config.Load()
	k.AllowDelete = d.AllowDelete == nil || *d.AllowDelete

	acc := config.Account{
		Profile: d.Profile, Region: d.Region,
		Bucket:    strings.TrimSpace(d.Bucket),
		Prefix:    config.NormalizePrefix(d.Prefix),
		From:      strings.TrimSpace(d.From),
		Signature: strings.TrimRight(d.Signature, " \t\n\r"),
		Label:     strings.TrimSpace(d.Label),
		Queue:     strings.TrimSpace(d.Queue),
	}
	// Which entry this replaces is decided by the ID the form came with, not by
	// the one the new values produce: whoever corrects a prefix would otherwise
	// leave the old mailbox standing and add a second next to it.
	k.Accounts = place(k.Accounts, acc, d.Account)

	path, err := config.Save(k)
	if err != nil {
		return nil, InputError{err.Error()}
	}
	if a.Activate != nil {
		if err := a.Activate(k); err != nil {
			return nil, InputError{awsx.PlainText(err, acc.Profile, i18n.Get(d.Language))}
		}
	}
	return map[string]any{"config": flatten(k, acc), "path": path}, nil
}

// place puts the mailbox where the one being edited stood, or appends it.
func place(list []config.Account, acc config.Account, editingID string) []config.Account {
	if editingID != "" && editingID != "new" {
		for i := range list {
			if list[i].ID() == editingID {
				list[i] = acc
				return list
			}
		}
	}
	// No ID: the wizard came up on the first mailbox, which is what it edits.
	if editingID == "" && len(list) > 0 {
		list[0] = acc
		return list
	}
	// A mailbox that already exists is not added a second time - the same bucket
	// and prefix are the same mailbox, whatever the form calls it.
	for i := range list {
		if list[i].ID() == acc.ID() {
			list[i] = acc
			return list
		}
	}
	return append(list, acc)
}

// Route picks the handler for a path.
func (a *Wizard) Route(path string) (func(context.Context, Data) (map[string]any, error), bool) {
	switch strings.TrimPrefix(path, "/api/setup/") {
	case "info":
		return a.Info, true
	case "credentials":
		return a.Credentials, true
	case "buckets":
		return a.Buckets, true
	case "test":
		return a.Test, true
	case "lifecycle":
		return a.Lifecycle, true
	case "device":
		return a.Device, true
	case "device/abandon":
		return a.DeviceAbandon, true
	case "save":
		return a.Save, true
	}
	return nil, false
}

func notNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}
