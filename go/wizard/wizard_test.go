// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package wizard

import (
	"encoding/json"
	"testing"

	"git.ole-hartwig.eu/development/s3mail/s3mail/go/config"
)

// TestNeverNullToTheInterface pins down the bug that made v0.2.x unusable for
// every mailbox user.
//
// The mailbox policy deliberately grants no s3:ListAllMyBuckets - otherwise
// everyone would see every bucket of the account, backups included. So the
// call ran into an AccessDenied and returned a nil slice. In Go that becomes
// JSON `null`, not `[]`, and `null.map(...)` ends the page's script. The
// reader then saw not "no right to list" but nothing at all any more:
//
//	✕ Cannot read properties of null (reading 'map')
func TestNeverNullToTheInterface(t *testing.T) {
	for name, value := range map[string][]string{
		"nil":  nil,
		"leer": {},
		"voll": {"a", "b"},
	} {
		blob, err := json.Marshal(map[string]any{"buckets": notNil(value)})
		if err != nil {
			t.Fatal(err)
		}
		if string(blob) == `{"buckets":null}` {
			t.Errorf("%s: becomes null - the page dies on it", name)
		}
	}
}

// TestNilReallyBecomesNull documents why notNil is needed at all - so nobody
// clears the function away as superfluous.
func TestNilReallyBecomesNull(t *testing.T) {
	var empty []string
	blob, _ := json.Marshal(map[string]any{"buckets": empty})
	if string(blob) != `{"buckets":null}` {
		t.Skip("Go marshalt nil-Slices nicht mehr als null - nichtNil kann weg")
	}
}

// TestEditingTheSecondLeavesTheFirstAlone is the trap in a wizard that only
// ever knew one mailbox: it loads the configuration, overwrites the fields and
// writes back. Without the ID from the form it would replace the wrong entry
// every time somebody corrects the second one.
func TestEditingTheSecondLeavesTheFirstAlone(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("S3MAIL_CONFIG_DIR", dir)
	start := config.Defaults()
	start.Accounts = []config.Account{
		{Bucket: "post", Prefix: "mail/info/", From: "info@firma.de"},
		{Bucket: "post", Prefix: "mail/support/", From: "support@firma.de"},
	}
	if _, err := config.Save(start); err != nil {
		t.Fatal(err)
	}

	wiz := &Wizard{}
	second := start.Accounts[1].ID()
	if _, err := wiz.Save(t.Context(), Data{
		Account: second, Bucket: "post", Prefix: "mail/support/",
		From: "hilfe@firma.de", Signature: "Support",
	}); err != nil {
		t.Fatal(err)
	}

	back := config.Load()
	if len(back.Accounts) != 2 {
		t.Fatalf("%d mailboxes after the edit, expected 2: %+v", len(back.Accounts), back.Accounts)
	}
	if back.Accounts[0].From != "info@firma.de" {
		t.Errorf("the first was overwritten: %+v", back.Accounts[0])
	}
	if back.Accounts[1].From != "hilfe@firma.de" {
		t.Errorf("the second was not taken over: %+v", back.Accounts[1])
	}
}

// TestANewMailboxIsAdded, not written over the first - which is what a wizard
// without the "new" marker would do.
func TestANewMailboxIsAdded(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("S3MAIL_CONFIG_DIR", dir)
	start := config.Defaults()
	start.Accounts = []config.Account{{Bucket: "post", Prefix: "mail/info/", From: "info@firma.de"}}
	if _, err := config.Save(start); err != nil {
		t.Fatal(err)
	}

	wiz := &Wizard{}
	if _, err := wiz.Save(t.Context(), Data{
		Account: "new", Bucket: "post", Prefix: "mail/rechnungen/", From: "rechnung@firma.de",
	}); err != nil {
		t.Fatal(err)
	}
	back := config.Load()
	if len(back.Accounts) != 2 {
		t.Fatalf("%d mailboxes, expected 2", len(back.Accounts))
	}
	if back.Accounts[0].From != "info@firma.de" {
		t.Errorf("the existing one was overwritten: %+v", back.Accounts[0])
	}
}

// TestTheSameMailboxTwiceStaysOne - somebody who types bucket and prefix of an
// existing mailbox into the "new" form means that mailbox, whatever the form
// says. Two entries pointing at the same prefix would share their state and
// fight over it.
func TestTheSameMailboxTwiceStaysOne(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("S3MAIL_CONFIG_DIR", dir)
	start := config.Defaults()
	start.Accounts = []config.Account{{Bucket: "post", Prefix: "mail/info/", From: "info@firma.de"}}
	if _, err := config.Save(start); err != nil {
		t.Fatal(err)
	}

	wiz := &Wizard{}
	if _, err := wiz.Save(t.Context(), Data{
		Account: "new", Bucket: "post", Prefix: "mail/info/", From: "neu@firma.de",
	}); err != nil {
		t.Fatal(err)
	}
	back := config.Load()
	if len(back.Accounts) != 1 {
		t.Fatalf("%d mailboxes for one prefix: %+v", len(back.Accounts), back.Accounts)
	}
	if back.Accounts[0].From != "neu@firma.de" {
		t.Errorf("the entry was not updated: %+v", back.Accounts[0])
	}
}
