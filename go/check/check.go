// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package check is the connection test from step 3 of the wizard: it walks
// through what s3mail needs, in order, and names the IAM action behind every
// item that is missing. This is where somebody without prior AWS knowledge
// finds out what the problem actually is.
package check

import (
	"context"
	"strings"

	"git.ole-hartwig.eu/development/s3mail/s3mail/go/i18n"

	"git.ole-hartwig.eu/development/s3mail/s3mail/go/store"
)

// Item is one entry of the checklist.
type Item struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"`
	Hint    string `json:"hint"`
	Skipped bool   `json:"skipped"`
}

// Environment is what the test needs. An interface, so it runs without AWS.
type Environment interface {
	store.S3
	Buckets(ctx context.Context) ([]string, error)
	LifecycleDays(ctx context.Context, bucket string) int
}

// SESChecker reports whether a sender address is verified in SES.
type SESChecker interface {
	Verified(ctx context.Context, address, domain string) ([]string, error)
}

// Run walks the list. It creates a test object and deletes it again - the only
// honest way to check the write permission.
func Run(ctx context.Context, s3 store.S3, kms store.KMS, ses SESChecker,
	bucket, prefix, sender string, cat i18n.Catalog) []Item {
	var items []Item
	add := func(p Item) { items = append(items, p) }

	// 1. Auflisten
	objs, err := s3.List(ctx, bucket, prefix)
	if err != nil {
		add(Item{Name: cat.T("check.listBucket"), Detail: short(err),
			Hint: listHint(prefix, cat)})
		return items
	}
	var example string
	for _, o := range objs {
		if o.Size > 0 && !isInternal(o.Key, prefix) {
			example = o.Key
			break
		}
	}
	if example != "" {
		add(Item{Name: cat.T("check.listBucket"), OK: true,
			Detail: cat.Tf("check.listBucket.found", len(objs), display(prefix))})
	} else {
		add(Item{Name: cat.T("check.listBucket"), OK: true,
			Detail: cat.Tf("check.listBucket.empty", display(prefix)),
			Hint:   cat.T("check.listBucket.emptyHint")})
	}

	// 2. Read a real message - and see how it is encrypted while doing so
	if example == "" {
		add(Item{Name: cat.T("check.readMail"), OK: true, Detail: cat.T("check.skipped.noMail"),
			Skipped: true})
		add(Item{Name: cat.T("check.encryption"), OK: true, Detail: cat.T("check.skipped.noMail"),
			Skipped: true})
	} else {
		obj, err := s3.Get(ctx, bucket, example, "bytes=0-2047")
		if err != nil {
			add(Item{Name: cat.T("check.readMail"), Detail: short(err), Hint: cat.T("check.readMail.hint")})
		} else {
			add(Item{Name: cat.T("check.readMail"), OK: true, Detail: baseName(example)})
			add(encryption(ctx, s3, kms, bucket, example, obj, cat))
		}
	}

	// 3./4. Writing and deleting, on a test object
	probe := prefix + ".s3mail-probe"
	if err := s3.Put(ctx, bucket, probe, []byte("s3mail"), "text/plain"); err != nil {
		add(Item{Name: cat.T("check.write"), Detail: short(err),
			Hint: cat.T("check.write.hint")})
		add(Item{Name: cat.T("check.delete"), Detail: cat.T("check.skipped"), Skipped: true, OK: true})
	} else {
		add(Item{Name: cat.T("check.write"), OK: true, Detail: cat.T("check.write.ok")})
		if err := s3.Delete(ctx, bucket, probe); err != nil {
			add(Item{Name: cat.T("check.delete"), Detail: short(err),
				Hint: cat.T("check.delete.hint")})
		} else {
			add(Item{Name: cat.T("check.delete"), OK: true, Detail: cat.T("check.delete.ok")})
		}
	}

	// 5. The folder holding the state changes
	if _, err := s3.List(ctx, bucket, prefix+store.StateOps); err != nil {
		add(Item{Name: cat.T("check.sharedState"), Detail: short(err),
			Hint: cat.Tf("check.sharedState.hint", prefix, store.StateOps)})
	} else {
		add(Item{Name: cat.T("check.sharedState"), OK: true,
			Detail: cat.T("check.sharedState.ok")})
	}

	// 6. SES-Absender
	if sender == "" {
		add(Item{Name: cat.T("check.sender"), OK: true, Skipped: true,
			Detail: cat.T("check.sender.none")})
	} else if ses == nil {
		add(Item{Name: cat.T("check.sender"), OK: true, Skipped: true, Detail: cat.T("check.notChecked")})
	} else {
		domain := sender
		if _, after, ok := strings.CutLast(sender, "@"); ok {
			domain = after
		}
		good, err := ses.Verified(ctx, sender, domain)
		switch {
		case err != nil:
			add(Item{Name: cat.T("check.sender"), Detail: short(err), Skipped: true,
				Hint: cat.T("check.sender.noPermission")})
		case len(good) > 0:
			add(Item{Name: cat.T("check.sender"), OK: true, Detail: cat.Tf("check.sender.verified", strings.Join(good, ", "))})
		default:
			add(Item{Name: cat.T("check.sender"), Detail: cat.Tf("check.sender.unverified", sender),
				Hint: cat.T("check.sender.hint")})
		}
	}
	return items
}

// encryption looks at a real message and says what one is dealing with.
func encryption(ctx context.Context, s3 store.S3, kms store.KMS,
	bucket, key string, obj store.Object, cat i18n.Catalog) Item {
	if store.IsEnvelope(obj.Meta) {
		// A partial fetch cannot be decrypted - so fetch the whole thing.
		obj, err := s3.Get(ctx, bucket, key, "")
		if err == nil {
			_, err = store.Decrypt(obj.Body, obj.Meta, kms)
		}
		if err != nil {
			return Item{Name: cat.T("check.encryption"),
				Detail: cat.Tf("check.encryption.clientFailed", short(err)),
				Hint:   cat.T("check.encryption.clientHint")}
		}
		return Item{Name: cat.T("check.encryption"), OK: true,
			Detail: cat.T("check.encryption.clientOk")}
	}
	head, err := s3.Head(ctx, bucket, key)
	if err != nil {
		return Item{Name: cat.T("check.encryption"), OK: true, Skipped: true, Detail: short(err)}
	}
	switch {
	case strings.Contains(head.ServerSideEncryption, "kms"):
		return Item{Name: cat.T("check.encryption"), OK: true, Detail: cat.T("check.encryption.serverKms"),
			Hint: cat.T("check.encryption.serverKmsHint")}
	case head.ServerSideEncryption != "":
		return Item{Name: cat.T("check.encryption"), OK: true,
			Detail: cat.Tf("check.encryption.server", head.ServerSideEncryption)}
	default:
		return Item{Name: cat.T("check.encryption"), OK: true, Detail: cat.T("check.encryption.none")}
	}
}

// AllOK says whether the list as a whole is in order.
func AllOK(items []Item) bool {
	for _, p := range items {
		if !p.OK && !p.Skipped {
			return false
		}
	}
	return true
}

func isInternal(key, prefix string) bool {
	for part := range strings.SplitSeq(strings.TrimPrefix(key, prefix), "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

func display(prefix string) string {
	if prefix == "" {
		return "/"
	}
	return prefix
}

func baseName(key string) string {
	if _, after, ok := strings.CutLast(key, "/"); ok {
		return after
	}
	return key
}

func short(err error) string {
	s := err.Error()
	if i := strings.Index(s, "\n"); i > 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// listHint names the three causes in order of likelihood - the commonest first.
//
// When an access is restricted to its own prefix (one mailbox per person),
// listing fails on a prefix that sits even one level too high: the s3:prefix
// condition compares the string, not the path. "mail/" does not match
// "mail/person/*", and neither does "mail/person" without the trailing slash.
// It looks like a missing permission and is a missing character.
func listHint(prefix string, cat i18n.Catalog) string {
	if prefix == "" {
		return cat.T("check.listBucket.hintNoPrefix")
	}
	return cat.Tf("check.listBucket.hintPrefix", prefix)
}
