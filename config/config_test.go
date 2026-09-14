// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func sandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("S3MAIL_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("S3MAIL_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("S3MAIL_AWS_DIR", filepath.Join(dir, "aws"))
	return dir
}

func TestSaveAndLoad(t *testing.T) {
	sandbox(t)
	k := Defaults()
	k.Accounts = []Account{{Bucket: "mein-bucket", Prefix: "mail", From: "support@firma.de"}}
	path, err := Save(k)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("permissions: %v", info.Mode().Perm())
		}
	}
	back := Load().First()
	if back.Bucket != "mein-bucket" || back.From != "support@firma.de" {
		t.Errorf("%+v", back)
	}
	if back.Prefix != "mail/" {
		t.Errorf("prefix not normalised: %q", back.Prefix)
	}
	if _, err := Save(Config{}); err == nil {
		t.Error("saved without a bucket")
	}
}

func TestPrefixNormalisieren(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "mail": "mail/", "mail/": "mail/", "/mail": "mail/",
		" mail ": "mail/", "/mail/unter": "mail/unter/",
	} {
		if got := NormalizePrefix(in); got != want {
			t.Errorf("%q -> %q, expected %q", in, got, want)
		}
	}
}

// TestCredentialsAreAdditive - the wizard must not touch an existing profile
// that works completely differently.
func TestCredentialsAreAdditive(t *testing.T) {
	dir := sandbox(t)
	awsDir := filepath.Join(dir, "aws")
	if err := os.MkdirAll(awsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	before := "[default]\nregion = eu-central-1\nlogin_session = mein-login\n\n" +
		"[profile arbeit]\nregion = eu-west-1\n"
	if err := os.WriteFile(filepath.Join(awsDir, "config"), []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	name, err := WriteCredentials("s3mail", "AKIAEXAMPLE1234567", "geheim", "eu-central-1")
	if err != nil {
		t.Fatal(err)
	}
	if name != "s3mail" {
		t.Errorf("Profilname: %q", name)
	}
	conf, _ := os.ReadFile(filepath.Join(awsDir, "config"))
	if !strings.Contains(string(conf), "login_session = mein-login") {
		t.Errorf("existing default profile damaged:\n%s", conf)
	}
	if !strings.Contains(string(conf), "[profile arbeit]") {
		t.Errorf("foreign profile lost:\n%s", conf)
	}
	if !strings.Contains(string(conf), "[profile s3mail]") {
		t.Errorf("new profile missing:\n%s", conf)
	}
	creds, _ := os.ReadFile(filepath.Join(awsDir, "credentials"))
	if !strings.Contains(string(creds), "AKIAEXAMPLE1234567") {
		t.Errorf("key missing:\n%s", creds)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(awsDir, "credentials"))
		if info.Mode().Perm() != 0o600 {
			t.Errorf("credentials-Rechte: %v", info.Mode().Perm())
		}
	}
}

func TestCredentialsTwiceOverwriteOnlyThatProfile(t *testing.T) {
	dir := sandbox(t)
	if _, err := WriteCredentials("s3mail", "AKIAALTALTALTALT12", "alt", "eu-west-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteCredentials("s3mail", "AKIANEUNEUNEUNEU12", "neu", "eu-central-1"); err != nil {
		t.Fatal(err)
	}
	creds, _ := os.ReadFile(filepath.Join(dir, "aws", "credentials"))
	if strings.Contains(string(creds), "AKIAALT") {
		t.Errorf("the old key is still there:\n%s", creds)
	}
	if strings.Count(string(creds), "[s3mail]") != 1 {
		t.Errorf("Abschnitt doppelt angelegt:\n%s", creds)
	}
}

func TestCredentialsValidation(t *testing.T) {
	sandbox(t)
	for name, f := range map[string]func() error{
		"ohne Secret":   func() error { _, e := WriteCredentials("s3mail", "AKIA1234567890123", "", ""); return e },
		"ohne Key":      func() error { _, e := WriteCredentials("s3mail", "", "geheim", ""); return e },
		"kein AKIA":     func() error { _, e := WriteCredentials("s3mail", "XYZ1234567890123", "geheim", ""); return e },
		"zu kurzer Key": func() error { _, e := WriteCredentials("s3mail", "AKIA123", "geheim", ""); return e },
	} {
		if err := f(); err == nil {
			t.Errorf("%s: durchgelassen", name)
		}
	}
}

func TestProfileLesen(t *testing.T) {
	dir := sandbox(t)
	awsDir := filepath.Join(dir, "aws")
	_ = os.MkdirAll(awsDir, 0o700)
	_ = os.WriteFile(filepath.Join(awsDir, "credentials"), []byte("[default]\nx=1\n[s3mail]\ny=2\n"), 0o600)
	_ = os.WriteFile(filepath.Join(awsDir, "config"),
		[]byte("[default]\n[profile arbeit]\n[sso-session firma]\n"), 0o600)

	p := Profiles()
	for _, must := range []string{"default", "s3mail", "arbeit"} {
		if !slices.Contains(p, must) {
			t.Errorf("%q missing from %v", must, p)
		}
	}
	if slices.Contains(p, "sso-session firma") || slices.Contains(p, "firma") {
		t.Errorf("sso-session read as a profile: %v", p)
	}
	if len(p) != 3 {
		t.Errorf("duplicates: %v", p)
	}
}

// TestPathsSuitThePlatform - on Windows nothing may land in a ~/.config that
// nobody looks in there.
func TestPathsSuitThePlatform(t *testing.T) {
	os.Unsetenv("S3MAIL_CONFIG_DIR")
	os.Unsetenv("S3MAIL_CACHE_DIR")
	k, c := Dir(), CacheDir()
	if !strings.HasSuffix(k, "s3mail") || !strings.HasSuffix(c, "s3mail") {
		t.Errorf("Verzeichnisse: %q %q", k, c)
	}
	if k == c {
		t.Error("configuration and cache in the same directory")
	}
	if runtime.GOOS == "windows" && !strings.Contains(k, "AppData") {
		t.Errorf("Windows-Pfad: %q", k)
	}
}

// TestTheOldShapeStillLoads is the one that matters on an update: the file with
// a single mailbox at the top level lies on real machines. A program that loses
// its configuration on an update teaches people not to update.
func TestTheOldShapeStillLoads(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("S3MAIL_CONFIG_DIR", dir)
	old := `{"profile":"s3mail-ole","region":"eu-north-1","bucket":"post","prefix":"mail/ole/",
	         "from":"ole@firma.de","signature":"Ole","allow_delete":false,"port":9000,
	         "host":"127.0.0.1","language":"es"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	k := Load()
	if len(k.Accounts) != 1 {
		t.Fatalf("%d mailboxes out of the old shape, expected 1: %+v", len(k.Accounts), k)
	}
	a := k.Accounts[0]
	if a.Bucket != "post" || a.Prefix != "mail/ole/" || a.From != "ole@firma.de" ||
		a.Profile != "s3mail-ole" || a.Region != "eu-north-1" || a.Signature != "Ole" {
		t.Errorf("mailbox not taken over: %+v", a)
	}
	// The shared settings have to come along, and allow_delete=false must not
	// be swallowed by the default true.
	if k.AllowDelete || k.Port != 9000 || k.Language != "es" {
		t.Errorf("shared settings lost: %+v", k)
	}
}

// TestTheNewShapeWinsOverTheOld - a file written by a newer version must not
// grow a duplicate mailbox on every load just because the old keys are still
// standing next to the list.
func TestTheNewShapeWinsOverTheOld(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("S3MAIL_CONFIG_DIR", dir)
	both := `{"bucket":"alt","prefix":"mail/","accounts":[
	           {"bucket":"neu","prefix":"mail/a/"},{"bucket":"neu","prefix":"mail/b/"}]}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(both), 0o600); err != nil {
		t.Fatal(err)
	}
	k := Load()
	if len(k.Accounts) != 2 {
		t.Fatalf("%d mailboxes, expected 2 - the old shape was taken along", len(k.Accounts))
	}
}

// TestSaveAndLoadRoundTripsTwoMailboxes - what goes in has to come back out,
// and the second one must not overwrite the first.
func TestSaveAndLoadRoundTripsTwoMailboxes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("S3MAIL_CONFIG_DIR", dir)
	in := Defaults()
	in.Accounts = []Account{
		{Bucket: "post", Prefix: "mail/info", From: "info@firma.de"},
		{Bucket: "post", Prefix: "mail/support", From: "support@firma.de", Label: "Support"},
	}
	if _, err := Save(in); err != nil {
		t.Fatal(err)
	}
	back := Load()
	if len(back.Accounts) != 2 {
		t.Fatalf("%d mailboxes came back", len(back.Accounts))
	}
	// Save normalises the prefix - without it two IDs collide that are meant to
	// be different.
	if back.Accounts[0].Prefix != "mail/info/" || back.Accounts[1].Prefix != "mail/support/" {
		t.Errorf("prefix not normalised: %+v", back.Accounts)
	}
	if back.Accounts[0].ID() == back.Accounts[1].ID() {
		t.Fatalf("both mailboxes carry the same ID: %q", back.Accounts[0].ID())
	}
	if a, ok := back.Account(back.Accounts[1].ID()); !ok || a.From != "support@firma.de" {
		t.Errorf("lookup by ID does not find the second: %+v %v", a, ok)
	}
}

// TestTheIDSurvivesAReordering - it lands in a cookie and in a URL. A running
// number would point at a different mailbox after somebody sorts the list.
func TestTheIDSurvivesAReordering(t *testing.T) {
	a := Account{Bucket: "post", Prefix: "mail/info/"}
	b := Account{Bucket: "post", Prefix: "mail/info"} // same mailbox, sloppier prefix
	if a.ID() != b.ID() {
		t.Errorf("the same mailbox yields two IDs: %q vs %q", a.ID(), b.ID())
	}
	if strings.ContainsAny(a.ID(), "/ ?&#") {
		t.Errorf("ID has to survive a URL and a cookie: %q", a.ID())
	}
}

// TestTheNameSaysSomething - the switcher shows this. A mailbox nobody named
// still has to be distinguishable from the one next to it.
func TestTheNameSaysSomething(t *testing.T) {
	for _, f := range []struct {
		account Account
		want    string
	}{
		{Account{Label: "Support", From: "s@x.de", Bucket: "b"}, "Support"},
		{Account{From: "s@x.de", Bucket: "b", Prefix: "mail/"}, "s@x.de"},
		{Account{Bucket: "b", Prefix: "mail/support/"}, "b/mail/support"},
	} {
		if got := f.account.Name(); got != f.want {
			t.Errorf("%+v -> %q, expected %q", f.account, got, f.want)
		}
	}
}
