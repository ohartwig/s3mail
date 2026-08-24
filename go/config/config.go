// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package config holds the configuration file and the writing of AWS
// credentials. The paths follow each platform's own habits - on Windows
// nothing ends up in a ~/.config nobody there would look in.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Account is one mailbox: where it lies, who reads it and who it writes as.
// Everything in here is a property of that one mailbox - what is shared across
// all of them sits in Config.
type Account struct {
	Profile string `json:"profile"`
	Region  string `json:"region"`
	Bucket  string `json:"bucket"`
	Prefix  string `json:"prefix"`
	From    string `json:"from"`

	// Signature goes under every message written from this mailbox. It sits in
	// the configuration and not in the catalogues: it is the writer's text, not
	// ours, and it does not change with the language of the interface.
	Signature string `json:"signature"`

	// Label is what the switcher shows. Empty means: build one from the address
	// or the bucket - a mailbox nobody named still has to be distinguishable.
	Label string `json:"label"`

	// Queue is the SQS queue new mail is announced through. Empty means: no
	// push, the timer does it. The wizard reads it out of the IAM policy, so
	// nobody has to type a URL.
	Queue string `json:"queue"`

	// Snippets are the paragraphs somebody writes over and over. Kept per
	// mailbox: what support answers is not what accounting answers.
	Snippets []Snippet `json:"snippets"`
}

// Snippet is a block of text with a name to find it by.
type Snippet struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// ID names the mailbox in a URL and in a cookie. It comes from bucket and
// prefix, so it survives a restart and stays the same on a second machine -
// a running number would point at a different mailbox after a reordering.
func (a Account) ID() string { return slug(a.Bucket + "__" + NormalizePrefix(a.Prefix)) }

// Name is what a reader sees. The label if there is one, otherwise the sender
// address, otherwise bucket and prefix - in that order, because that is the
// order in which they say something to a human.
func (a Account) Name() string {
	if l := strings.TrimSpace(a.Label); l != "" {
		return l
	}
	if f := strings.TrimSpace(a.From); f != "" {
		return f
	}
	return strings.TrimSuffix(a.Bucket+"/"+NormalizePrefix(a.Prefix), "/")
}

var unsafeInID = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

func slug(s string) string {
	out := unsafeInID.ReplaceAllString(s, "_")
	if out == "" {
		return "default"
	}
	return out
}

// Config is what config.json holds: the mailboxes and what applies to all of
// them.
type Config struct {
	Accounts []Account `json:"accounts"`

	AllowDelete bool   `json:"allow_delete"`
	Port        int    `json:"port"`
	Host        string `json:"host"`

	// Language of the interface. Empty means: take the browser's - for a
	// program running next to the browser that is a better default than a
	// guessed one.
	Language string `json:"language"`
}

// Account returns the mailbox with that ID, and whether it exists.
func (k Config) Account(id string) (Account, bool) {
	for _, a := range k.Accounts {
		if a.ID() == id {
			return a, true
		}
	}
	return Account{}, false
}

// First is the mailbox that comes up without a choice being made.
func (k Config) First() Account {
	if len(k.Accounts) == 0 {
		return Account{}
	}
	return k.Accounts[0]
}

func Defaults() Config {
	return Config{AllowDelete: true, Port: 8765, Host: "127.0.0.1"}
}

// DefaultAccount is what an empty form starts from.
func DefaultAccount() Account {
	return Account{Region: "eu-central-1", Prefix: "mail/"}
}

// Dir is where the configuration lives: ~/.config/s3mail on Linux,
// ~/Library/Application Support/s3mail on macOS, %AppData%\s3mail on Windows.
func Dir() string {
	if v := os.Getenv("S3MAIL_CONFIG_DIR"); v != "" {
		return v
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "s3mail")
}

// CacheDir is where the index lives - data whose loss costs nothing but time.
func CacheDir() string {
	if v := os.Getenv("S3MAIL_CACHE_DIR"); v != "" {
		return v
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "s3mail")
}

func File() string { return filepath.Join(Dir(), "config.json") }

func Exists() bool {
	_, err := os.Stat(File())
	return err == nil
}

// legacy is the shape the file had while there was one mailbox per instance.
// It is still read, because the file lies on real machines - a program that
// loses its configuration on an update teaches people not to update.
type legacy struct {
	Profile     string `json:"profile"`
	Region      string `json:"region"`
	Bucket      string `json:"bucket"`
	Prefix      string `json:"prefix"`
	From        string `json:"from"`
	Signature   string `json:"signature"`
	AllowDelete *bool  `json:"allow_delete"`
	Port        int    `json:"port"`
	Host        string `json:"host"`
	Language    string `json:"language"`
}

func Load() Config {
	k := Defaults()
	blob, err := os.ReadFile(File())
	if err != nil {
		return k
	}
	_ = json.Unmarshal(blob, &k) // a broken file: then the defaults it is

	// The old shape carried the mailbox at the top level. Recognised by a
	// bucket standing there, and only taken when no account list arrived -
	// otherwise a file written by a newer version would grow a duplicate on
	// every load.
	if len(k.Accounts) == 0 {
		var old legacy
		if json.Unmarshal(blob, &old) == nil && strings.TrimSpace(old.Bucket) != "" {
			k.Accounts = []Account{{
				Profile: old.Profile, Region: old.Region, Bucket: old.Bucket,
				Prefix: old.Prefix, From: old.From, Signature: old.Signature,
			}}
			if old.AllowDelete != nil {
				k.AllowDelete = *old.AllowDelete
			}
			if old.Port != 0 {
				k.Port = old.Port
			}
			if old.Host != "" {
				k.Host = old.Host
			}
			if old.Language != "" {
				k.Language = old.Language
			}
		}
	}

	if k.Port == 0 {
		k.Port = 8765
	}
	if k.Host == "" {
		k.Host = "127.0.0.1"
	}
	for i := range k.Accounts {
		if k.Accounts[i].Region == "" {
			k.Accounts[i].Region = "eu-central-1"
		}
		k.Accounts[i].Prefix = NormalizePrefix(k.Accounts[i].Prefix)
	}
	return k
}

// The three things a caller can get wrong. They are sentinels and not sentences:
// what the reader gets to see comes from the catalogue, and only the HTTP layer
// knows which language they read.
var (
	ErrNoBucket              = errors.New("no bucket given")
	ErrCredentialsIncomplete = errors.New("access key id and secret access key are both needed")
	ErrBadKeyID              = errors.New("that does not look like an access key id")
)

// Save writes the file with mode 0600 - it holds no secret, but the bucket
// name is nobody else's business either.
func Save(k Config) (string, error) {
	if len(k.Accounts) == 0 {
		return "", ErrNoBucket
	}
	for i, a := range k.Accounts {
		if strings.TrimSpace(a.Bucket) == "" {
			return "", ErrNoBucket
		}
		k.Accounts[i].Prefix = NormalizePrefix(a.Prefix)
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return "", err
	}
	blob, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return "", err
	}
	path := File()
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// NormalizePrefix turns both "/mail" and "mail" into "mail/".
func NormalizePrefix(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimLeft(p, "/")
	if p != "" && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// -- AWS credentials -------------------------------------------------------- //

// AWSDir is ~/.aws - the same place the AWS tools read.
func AWSDir() string {
	if v := os.Getenv("S3MAIL_AWS_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".aws"
	}
	return filepath.Join(home, ".aws")
}

// Profiles reads the profile names from ~/.aws/credentials and ~/.aws/config.
func Profiles() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, f := range []struct{ file, prefix string }{
		{"credentials", ""}, {"config", "profile "},
	} {
		blob, err := os.ReadFile(filepath.Join(AWSDir(), f.file))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(blob), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
				continue
			}
			name := strings.TrimSpace(line[1 : len(line)-1])
			name = strings.TrimPrefix(name, f.prefix)
			if strings.HasPrefix(name, "sso-session ") || strings.HasPrefix(name, "services ") {
				continue // these are not profiles
			}
			add(name)
		}
	}
	return out
}

// WriteCredentials stores the keys as a named profile in ~/.aws/credentials -
// additively, existing profiles are left untouched.
func WriteCredentials(profile, keyID, secret, region string) (string, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = "s3mail"
	}
	keyID, secret = strings.TrimSpace(keyID), strings.TrimSpace(secret)
	if keyID == "" || secret == "" {
		return "", ErrCredentialsIncomplete
	}
	if len(keyID) < 16 || (!strings.HasPrefix(keyID, "AKIA") && !strings.HasPrefix(keyID, "ASIA")) {
		return "", ErrBadKeyID
	}
	if err := os.MkdirAll(AWSDir(), 0o700); err != nil {
		return "", err
	}
	if err := iniSet(filepath.Join(AWSDir(), "credentials"), profile,
		map[string]string{"aws_access_key_id": keyID, "aws_secret_access_key": secret}); err != nil {
		return "", err
	}
	section := "profile " + profile
	if profile == "default" {
		section = "default"
	}
	if region == "" {
		region = "eu-central-1"
	}
	if err := iniSet(filepath.Join(AWSDir(), "config"), section,
		map[string]string{"region": region}); err != nil {
		return "", err
	}
	return profile, nil
}

// iniSet writes keys into a section and leaves everything else alone.
// Line by line on purpose rather than with an INI library: that would rewrite
// the file and throw away comments and other people's formatting.
func iniSet(path, section string, values map[string]string) error {
	blob, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := []string{}
	if len(blob) > 0 {
		lines = strings.Split(strings.TrimRight(string(blob), "\n"), "\n")
	}
	header := "[" + section + "]"

	start, end := -1, len(lines)
	for i, z := range lines {
		t := strings.TrimSpace(z)
		if t == header {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(t, "[") {
			end = i
			break
		}
	}
	if start < 0 { // Abschnitt anhaengen
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, header)
		for k, v := range values {
			lines = append(lines, fmt.Sprintf("%s = %s", k, v))
		}
	} else {
		block := append([]string{}, lines[start+1:end]...)
		for k, v := range values {
			replaced := false
			for i, z := range block {
				if strings.HasPrefix(strings.TrimSpace(z), k) &&
					strings.Contains(z, "=") &&
					strings.TrimSpace(strings.SplitN(z, "=", 2)[0]) == k {
					block[i] = fmt.Sprintf("%s = %s", k, v)
					replaced = true
					break
				}
			}
			if !replaced {
				block = append(block, fmt.Sprintf("%s = %s", k, v))
			}
		}
		fresh := append([]string{}, lines[:start+1]...)
		fresh = append(fresh, block...)
		fresh = append(fresh, lines[end:]...)
		lines = fresh
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}
