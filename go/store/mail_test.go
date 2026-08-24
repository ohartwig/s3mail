// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"s3mail/core"
	"s3mail/mimeparse"
	"s3mail/s3fake"
	"s3mail/store"
)

func buildMail(from, to, subject, body, date, mid string) []byte {
	return []byte("From: " + from + "\r\nTo: " + to + "\r\nSubject: " + subject +
		"\r\nDate: " + date + "\r\nMessage-ID: " + mid +
		"\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
}

func buildMailbox(t *testing.T) (*s3fake.Fake, *store.Mailbox) {
	t.Helper()
	f := s3fake.New()
	f.Objs["mail/m1"] = buildMail("Anna <anna@kunde.de>", "post@firma.de",
		"Rechnung 1", "Anbei die Rechnung.", "Mon, 03 Aug 2026 09:00:00 +0000", "<m1@x>")
	f.Objs["mail/m2"] = buildMail("Shop <news@shop.io>", "post@firma.de",
		"Angebot", "Neu im Sortiment.", "Tue, 04 Aug 2026 09:00:00 +0000", "<m2@x>")
	f.Objs["mail/archiv/alt1"] = buildMail("Alt <alt@firma.de>", "post@firma.de",
		"Altes", "Alter Text.", "Wed, 01 Jul 2026 08:00:00 +0000", "<alt1@x>")
	f.Objs["andere/nicht-meins"] = []byte("ausserhalb")
	m := store.NewMailbox(context.Background(), f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	return f, m
}

func TestIndexAndFolders(t *testing.T) {
	ctx := context.Background()
	_, m := buildMailbox(t)
	res, err := m.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 3 {
		t.Errorf("%d new messages, expected 3", res.New)
	}
	for _, msg := range m.Index() {
		if strings.HasPrefix(msg.Key, "andere/") {
			t.Errorf("object outside the prefix in the index: %s", msg.Key)
		}
	}
	if got := m.Index()[0].Subject; got != "Altes" {
		t.Errorf("subject not parsed: %q", got)
	}
	to := map[string]core.FolderInfo{}
	for _, o := range m.Folders() {
		to[o.Name] = o
	}
	if to[core.Inbox].Count != 2 || to[core.Archive].Count != 1 {
		t.Errorf("Ordnerzaehler: %+v", to)
	}
}

// TestStateAndOpsAreNotMail - otherwise they turn up as messages.
func TestStateAndOpsAreNotMail(t *testing.T) {
	ctx := context.Background()
	_, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.State.Mutate(ctx, core.Op{T: "flags", Mids: []string{"m1"}, Read: core.Ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for _, msg := range m.Index() {
		if strings.Contains(msg.Key, "s3mail-state") {
			t.Errorf("interner Schluessel im Index: %s", msg.Key)
		}
	}
	for _, o := range m.Folders() {
		if strings.HasPrefix(o.Name, ".") {
			t.Errorf("internal folder in the sidebar: %s", o.Name)
		}
	}
}

func TestMoveCarriesTheState(t *testing.T) {
	ctx := context.Background()
	f, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.State.Mutate(ctx,
		core.Op{T: "tags", Mids: []string{"m1"}, Add: []string{"wichtig"}},
		core.Op{T: "flags", Mids: []string{"m1"}, Read: core.Ptr(true), Star: core.Ptr(true)}); err != nil {
		t.Fatal(err)
	}
	res, err := m.Move(ctx, []string{"mail/m1"}, core.Archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].NewKey != "mail/archiv/m1" {
		t.Fatalf("%+v", res)
	}
	if _, present := f.Objs["mail/m1"]; present {
		t.Error("original not deleted")
	}
	e := m.State.Get("m1")
	if !e.Read || !e.Star || !has(e.Tags, "wichtig") {
		t.Errorf("state lost after the move: %+v", e)
	}
}

func TestMoveInheritsEncryption(t *testing.T) {
	ctx := context.Background()
	f, m := buildMailbox(t)
	f.SSE["mail/m1"] = store.CopyOpts{ServerSideEncryption: "aws:kms",
		SSEKMSKeyID: "arn:aws:kms:eu-central-1:1:key/abc", StorageClass: "STANDARD_IA"}
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Move(ctx, []string{"mail/m1"}, core.Archive); err != nil {
		t.Fatal(err)
	}
	fresh := f.SSE["mail/archiv/m1"]
	if fresh.ServerSideEncryption != "aws:kms" || fresh.SSEKMSKeyID == "" {
		t.Errorf("encryption not carried over: %+v", fresh)
	}
	if fresh.StorageClass != "STANDARD_IA" {
		t.Errorf("storage class not carried over: %+v", fresh)
	}
}

func TestMoveChecks(t *testing.T) {
	ctx := context.Background()
	_, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Move(ctx, []string{"mail/m1"}, "../boese"); err == nil {
		t.Error("folder name with .. let through")
	}
	if _, err := m.Move(ctx, []string{"andere/nicht-meins"}, core.Archive); err == nil {
		t.Error("Key ausserhalb des Prefix durchgelassen")
	}
	if _, err := m.Move(ctx, []string{"mail/" + store.StateObject}, core.Archive); err == nil {
		t.Error("Snapshot verschiebbar")
	}
	// into the same folder: skipped, not copied
	res, err := m.Move(ctx, []string{"mail/m1"}, core.Inbox)
	if err != nil || len(res) != 1 || !res[0].Skipped {
		t.Errorf("move into the same folder: %+v %v", res, err)
	}
}

func TestNameCollision(t *testing.T) {
	ctx := context.Background()
	f, m := buildMailbox(t)
	f.Objs["mail/archiv/m1"] = buildMail("X <x@y.de>", "post@firma.de", "Kollision",
		"Text", "Thu, 05 Aug 2026 09:00:00 +0000", "<k@x>")
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := m.Move(ctx, []string{"mail/m1"}, core.Archive)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].NewKey != "mail/archiv/m1-1" {
		t.Errorf("collision not defused: %q", res[0].NewKey)
	}
	if _, present := f.Objs["mail/archiv/m1"]; !present {
		t.Error("existing message overwritten")
	}
}

// TestDeleteOnlyFromTrash - the check sits in the store, not in the UI.
func TestDeleteOnlyFromTrash(t *testing.T) {
	ctx := context.Background()
	_, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Delete(ctx, []string{"mail/m1"}, false); !errors.Is(err, store.ErrTrashOnly) {
		t.Errorf("Loeschen ausserhalb des Papierkorbs: %v", err)
	}
	if _, err := m.Move(ctx, []string{"mail/m1"}, core.Trash); err != nil {
		t.Fatal(err)
	}
	n, err := m.Delete(ctx, []string{"mail/trash/m1"}, false)
	if err != nil || n != 1 {
		t.Errorf("delete from the trash: %d %v", n, err)
	}
	if _, present := m.State.Data().Messages["m1"]; present {
		t.Error("the state entry of the deleted message stayed behind")
	}
}

func TestDeleteBlocked(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	f.Objs["mail/trash/m1"] = buildMail("a@b.de", "c@d.de", "x", "y",
		"Mon, 03 Aug 2026 09:00:00 +0000", "<m1@x>")
	m := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, false)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Delete(ctx, []string{"mail/trash/m1"}, false); !errors.Is(err, store.ErrDeleteBlocked) {
		t.Errorf("--no-delete not enforced: %v", err)
	}
}

// TestEncryptedMeansNoRangeGet - half a ciphertext cannot be decrypted, so the
// range GET has to fall away as soon as such an object turns up.
func TestEncryptedMeansNoRangeGet(t *testing.T) {
	ctx := context.Background()
	plain, cases := loadEnvelopes(t)
	f := s3fake.New()
	body, key := unpack(t, cases["gcm"])
	f.Objs["mail/enc1"] = body
	f.Meta["mail/enc1"] = cases["gcm"].Meta

	m := store.NewMailbox(ctx, f, &fakeKMS{key: key}, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	raw, err := m.Fetch(ctx, "mail/enc1", store.HeaderChunk)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(plain) {
		t.Error("Klartext weicht ab")
	}
	if !m.Encrypted() {
		t.Error("mailbox not remembered as encrypted")
	}
	// the second access must not send a range at all any more
	f.ClearCalls()
	if _, err := m.Fetch(ctx, "mail/enc1", store.HeaderChunk); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.CallLog {
		if strings.Contains(a, "bytes=") {
			t.Errorf("Teilstueck trotz Verschluesselung angefordert: %s", a)
		}
	}
}

func TestEncryptedWithoutPermissionFailsOnlyThatMail(t *testing.T) {
	ctx := context.Background()
	_, cases := loadEnvelopes(t)
	f := s3fake.New()
	body, _ := unpack(t, cases["gcm"])
	f.Objs["mail/enc1"] = body
	f.Meta["mail/enc1"] = cases["gcm"].Meta
	f.Objs["mail/klar"] = buildMail("a@b.de", "c@d.de", "Lesbar", "Text",
		"Mon, 03 Aug 2026 09:00:00 +0000", "<k@x>")

	m := store.NewMailbox(ctx, f, &fakeKMS{err: errors.New("AccessDenied")},
		"test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	to := map[string]core.Message{}
	for _, msg := range m.Index() {
		to[msg.Mid] = msg
	}
	if to["klar"].Subject != "Lesbar" {
		t.Errorf("readable message dragged down: %+v", to["klar"])
	}
	if to["enc1"].Subject != mimeparse.SubjectUnreadable {
		t.Errorf("unreadable message not marked: %+v", to["enc1"])
	}
}

func TestRulesWhileIndexing(t *testing.T) {
	ctx := context.Background()
	_, m := buildMailbox(t)
	archiv := core.Archive
	rules, err := core.CleanRules([]core.Rule{{Contains: "shop.io", Field: "from",
		Folder: &archiv, Tags: []string{"Werbung"}, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.State.Mutate(ctx, core.Op{T: "rules", Rules: rules}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyRules(ctx, nil, false); err != nil {
		t.Fatal(err)
	}
	to := map[string]core.Message{}
	for _, msg := range m.Index() {
		to[msg.Mid] = msg
	}
	if to["m2"].Folder != core.Archive {
		t.Errorf("the rule did not move it: %+v", to["m2"])
	}
	if !has(m.State.Get("m2").Tags, "Werbung") {
		t.Error("rule tag missing")
	}
	if to["m1"].Folder != core.Inbox {
		t.Error("the rule touched an uninvolved message")
	}
}

func TestCacheSavesRequests(t *testing.T) {
	ctx := context.Background()
	f, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	f.ClearCalls()
	res, err := m.Refresh(ctx) // nothing has changed
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 0 {
		t.Errorf("%d messages fetched again, expected 0", res.New)
	}
	for _, a := range f.CallLog {
		if strings.HasPrefix(a, "get mail/m") {
			t.Errorf("message fetched again despite the same ETag: %s", a)
		}
	}
}

// TestBodyFromTheCache - opening a message a second time must not cost an S3
// access any more. The index was always local, the content was not: until now
// every opened message was fetched again, attachments and all.
func TestBodyFromTheCache(t *testing.T) {
	ctx := context.Background()
	f, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	roh1, err := m.Fetch(ctx, "mail/m1", 0)
	if err != nil {
		t.Fatal(err)
	}
	f.ClearCalls()

	roh2, err := m.Fetch(ctx, "mail/m1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(roh1) != string(roh2) {
		t.Error("zwischengespeicherter Inhalt weicht ab")
	}
	for _, a := range f.Calls() {
		if strings.HasPrefix(a, "get mail/m1") {
			t.Errorf("trotz Zwischenspeicher erneut geholt: %s", a)
		}
	}
}

// TestCacheHangsOnTheETag - if the object changes, the entry has to expire.
// Otherwise s3mail shows the old version after the content changed.
func TestCacheHangsOnTheETag(t *testing.T) {
	ctx := context.Background()
	f, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Fetch(ctx, "mail/m1", 0); err != nil {
		t.Fatal(err)
	}
	f.Store("mail/m1", buildMail("Neu <neu@x.de>", "post@firma.de", "Anderer Inhalt",
		"Voellig andere Mail.", "Fri, 07 Aug 2026 09:00:00 +0000", "<neu@x>"))
	if _, err := m.Refresh(ctx); err != nil { // neues ETag landet im Index
		t.Fatal(err)
	}
	raw, err := m.Fetch(ctx, "mail/m1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Anderer Inhalt") {
		t.Error("old version served from the cache")
	}
}

// TestPartialFetchesAreNotCached - ein gespeichertes Teilstueck
// would be a truncated message on the next open, without anyone noticing.
func TestPartialFetchesAreNotCached(t *testing.T) {
	ctx := context.Background()
	f, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil { // fetches only HeaderChunk
		t.Fatal(err)
	}
	f.ClearCalls()
	if _, err := m.Fetch(ctx, "mail/m1", 0); err != nil {
		t.Fatal(err)
	}
	fetched := false
	for _, a := range f.Calls() {
		if strings.HasPrefix(a, "get mail/m1") {
			fetched = true
		}
	}
	if !fetched {
		t.Error("a whole message came out of a fragment in the cache")
	}
}
