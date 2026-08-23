package config

import (
	"os"
	"path/filepath"
	"runtime"
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
	k.Bucket, k.Prefix, k.From = "mein-bucket", "mail", "support@firma.de"
	path, err := Save(k)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("Rechte: %v", info.Mode().Perm())
		}
	}
	back := Load()
	if back.Bucket != "mein-bucket" || back.From != "support@firma.de" {
		t.Errorf("%+v", back)
	}
	if back.Prefix != "mail/" {
		t.Errorf("prefix not normalised: %q", back.Prefix)
	}
	if _, err := Save(Config{}); err == nil {
		t.Error("ohne Bucket gespeichert")
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
		if !contains(p, must) {
			t.Errorf("%q missing from %v", must, p)
		}
	}
	if contains(p, "sso-session firma") || contains(p, "firma") {
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

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
