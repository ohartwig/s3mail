// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package check_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/i18n"

	"git.ole-hartwig.eu/development/s3mail/s3mail/check"
	"git.ole-hartwig.eu/development/s3mail/s3mail/s3fake"
	"git.ole-hartwig.eu/development/s3mail/s3mail/store"
)

// label is the name a checklist item carries in German. The test indexes by the
// catalogue key and not by the sentence: the wording belongs to the catalogue,
// and a test hanging on it breaks the next time somebody polishes a sentence.
func label(key string) string { return i18n.Get("de").T(key) }

func byName(items []check.Item) map[string]check.Item {
	out := map[string]check.Item{}
	for _, p := range items {
		out[p.Name] = p
	}
	return out
}

func TestEverythingOK(t *testing.T) {
	f := s3fake.New()
	f.Store("mail/m1", []byte("From: a@b.de\r\nSubject: x\r\n\r\nText\r\n"))
	p := check.Run(context.Background(), f, nil, nil, "test-bucket", "mail/", "", i18n.Get("de"))
	if !check.AllOK(p) {
		t.Errorf("not everything green: %+v", p)
	}
	k := byName(p)
	if !strings.Contains(k[label("check.listBucket")].Detail, "Objekt") {
		t.Errorf("%+v", k[label("check.listBucket")])
	}
	if k[label("check.readMail")].Detail != "m1" {
		t.Errorf("%+v", k[label("check.readMail")])
	}
	if k[label("check.encryption")].Detail != label("check.encryption.none") {
		t.Errorf("%+v", k[label("check.encryption")])
	}
	if !k[label("check.write")].OK || !k[label("check.delete")].OK {
		t.Errorf("write/delete: %+v %+v", k[label("check.write")], k[label("check.delete")])
	}
	// The test object has to be gone again
	if f.Has("mail/.s3mail-probe") {
		t.Error("Testobjekt liegen gelassen")
	}
}

// TestMissingPermissionNamesTheAction - that is the whole point of the list.
func TestMissingPermissionNamesTheAction(t *testing.T) {
	f := s3fake.New()
	f.Store("mail/m1", []byte("From: a@b.de\r\n\r\nText\r\n"))
	f.PutErr = errors.New("AccessDenied")

	p := check.Run(context.Background(), f, nil, nil, "test-bucket", "mail/", "", i18n.Get("de"))
	k := byName(p)
	if k[label("check.write")].OK {
		t.Fatal("write error not noticed")
	}
	if !strings.Contains(k[label("check.write")].Hint, "s3:PutObject") {
		t.Errorf("the hint does not name the IAM action: %q", k[label("check.write")].Hint)
	}
	if check.AllOK(p) {
		t.Error("overall verdict green despite the error")
	}
}

func TestEmptyMailboxIsNoError(t *testing.T) {
	f := s3fake.New()
	p := check.Run(context.Background(), f, nil, nil, "test-bucket", "mail/", "", i18n.Get("de"))
	k := byName(p)
	if !k[label("check.listBucket")].OK || !strings.Contains(k[label("check.listBucket")].Detail, "noch nichts") {
		t.Errorf("%+v", k[label("check.listBucket")])
	}
	if !k[label("check.readMail")].Skipped || !k[label("check.encryption")].Skipped {
		t.Error("an empty mailbox has to be skipped, not turn red")
	}
	if !check.AllOK(p) {
		t.Errorf("empty mailbox counted as an error: %+v", p)
	}
}

// TestInternalObjectsAreNotMail - otherwise the test checks the state instead of a message.
func TestInternalObjectsAreNotMail(t *testing.T) {
	f := s3fake.New()
	f.Store("mail/"+store.StateObject, []byte(`{"messages":{}}`))
	f.Store("mail/"+store.StateOps+"x.json", []byte(`{"ops":[]}`))
	p := byName(check.Run(context.Background(), f, nil, nil, "test-bucket", "mail/", "", i18n.Get("de")))
	if !p[label("check.readMail")].Skipped {
		t.Errorf("state file checked as mail: %+v", p[label("check.readMail")])
	}
}

type kmsFake struct {
	key []byte
	err error
}

func (k *kmsFake) Decrypt(_ []byte, _ map[string]string) ([]byte, error) {
	if k.err != nil {
		return nil, k.err
	}
	return k.key, nil
}

func envelope(t *testing.T) ([]byte, map[string]string, []byte) {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join("..", "store", "testdata", "envelopes.json"))
	if err != nil {
		t.Skip("keine Umschlaege vorhanden")
	}
	var d struct {
		Cases map[string]struct {
			Body string            `json:"body"`
			Meta map[string]string `json:"meta"`
			Key  string            `json:"key"`
		} `json:"faelle"`
	}
	if err := json.Unmarshal(blob, &d); err != nil {
		t.Fatal(err)
	}
	f := d.Cases["gcm"]
	body, _ := base64.StdEncoding.DecodeString(f.Body)
	key, _ := base64.StdEncoding.DecodeString(f.Key)
	return body, f.Meta, key
}

// TestEncryptionIsDetected - the wizard should say what it is dealing with
// instead of leaving the reader to guess.
func TestEncryptionIsDetected(t *testing.T) {
	body, meta, key := envelope(t)

	f := s3fake.New()
	f.Store("mail/enc", body)
	f.SetMeta("mail/enc", meta)
	p := byName(check.Run(context.Background(), f, &kmsFake{key: key}, nil,
		"test-bucket", "mail/", "", i18n.Get("de")))
	if !p[label("check.encryption")].OK || p[label("check.encryption")].Detail != label("check.encryption.clientOk") {
		t.Errorf("%+v", p[label("check.encryption")])
	}

	// without kms:Decrypt the item has to be red and name the action
	p = byName(check.Run(context.Background(), f,
		&kmsFake{err: errors.New("AccessDenied")}, nil, "test-bucket", "mail/", "", i18n.Get("de")))
	if p[label("check.encryption")].OK {
		t.Error("missing kms:Decrypt not noticed")
	}
	if !strings.Contains(p[label("check.encryption")].Hint, "kms:Decrypt") {
		t.Errorf("hint: %q", p[label("check.encryption")].Hint)
	}

	// encrypted server-side: only a hint, no error
	g := s3fake.New()
	g.Store("mail/m1", []byte("From: a@b.de\r\n\r\nText\r\n"))
	g.SetSSE("mail/m1", store.CopyOpts{ServerSideEncryption: "aws:kms"})
	p = byName(check.Run(context.Background(), g, nil, nil, "test-bucket", "mail/", "", i18n.Get("de")))
	if !p[label("check.encryption")].OK || p[label("check.encryption")].Detail != label("check.encryption.serverKms") {
		t.Errorf("%+v", p[label("check.encryption")])
	}
	if !strings.Contains(p[label("check.encryption")].Hint, "kms:GenerateDataKey") {
		t.Errorf("hint about SSE-KMS missing: %q", p[label("check.encryption")].Hint)
	}
}

type sesFake struct {
	good []string
	err  error
}

func (s *sesFake) Verified(_ context.Context, _, _ string) ([]string, error) {
	return s.good, s.err
}

func TestSESSender(t *testing.T) {
	f := s3fake.New()
	f.Store("mail/m1", []byte("From: a@b.de\r\n\r\nText\r\n"))
	ctx := context.Background()

	p := byName(check.Run(ctx, f, nil, &sesFake{good: []string{"support@firma.de"}},
		"test-bucket", "mail/", "support@firma.de", i18n.Get("de")))
	if !p[label("check.sender")].OK {
		t.Errorf("%+v", p[label("check.sender")])
	}
	p = byName(check.Run(ctx, f, nil, &sesFake{}, "test-bucket", "mail/", "x@y.de", i18n.Get("de")))
	if p[label("check.sender")].OK {
		t.Error("unverifizierte Adresse als in Ordnung gemeldet")
	}
	if p[label("check.sender")].Hint != label("check.sender.hint") {
		t.Errorf("hint: %q", p[label("check.sender")].Hint)
	}
	// without a sender: skipped, not red
	p = byName(check.Run(ctx, f, nil, &sesFake{}, "test-bucket", "mail/", "", i18n.Get("de")))
	if !p[label("check.sender")].Skipped {
		t.Errorf("%+v", p[label("check.sender")])
	}
}

// TestHintNamesThePrefixFirst - for prefix-restricted accesses the most
// common reason for a 403 while listing is not the missing right but a prefix
// one level too high. The old version named exactly that not at all and sent
// the reader to the IAM console instead of to the field above.
func TestHintNamesThePrefixFirst(t *testing.T) {
	f := s3fake.New()
	f.ListErr = errors.New("AccessDenied")
	p := byName(check.Run(context.Background(), f, nil, nil,
		"test-bucket", "mail/ole/", "", i18n.Get("de")))

	h := p[label("check.listBucket")].Hint
	if !strings.Contains(h, "mail/ole/") {
		t.Errorf("the expected prefix is not named: %q", h)
	}
	if !strings.Contains(h, "Prefix") {
		t.Errorf("the prefix does not appear in the hint: %q", h)
	}
	if strings.Index(h, "Prefix") > strings.Index(h, "s3:ListBucket") {
		t.Errorf("the less likely cause comes first: %q", h)
	}
}
