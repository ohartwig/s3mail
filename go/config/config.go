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
	"strings"
)

// Config is what config.json holds.
type Config struct {
	Profil      string `json:"profile"`
	Region      string `json:"region"`
	Bucket      string `json:"bucket"`
	Prefix      string `json:"prefix"`
	Absender    string `json:"from"`
	AllowDelete bool   `json:"allow_delete"`
	Port        int    `json:"port"`
	Host        string `json:"host"`

	// Language of the interface. Empty means: take the browser's - for a
	// program running next to the browser that is a better default than a
	// guessed one.
	Sprache string `json:"language"`
}

func Defaults() Config {
	return Config{Region: "eu-central-1", Prefix: "mail/", AllowDelete: true,
		Port: 8765, Host: "127.0.0.1"}
}

// Dir is where the configuration lives: ~/.config/s3mail on Linux,
// ~/Library/Application Support/s3mail on macOS, %AppData%\s3mail on Windows.
func Dir() string {
	if v := os.Getenv("S3MAIL_CONFIG_DIR"); v != "" {
		return v
	}
	basis, err := os.UserConfigDir()
	if err != nil {
		basis = "."
	}
	return filepath.Join(basis, "s3mail")
}

// CacheDir is where the index lives - data whose loss costs nothing but time.
func CacheDir() string {
	if v := os.Getenv("S3MAIL_CACHE_DIR"); v != "" {
		return v
	}
	basis, err := os.UserCacheDir()
	if err != nil {
		basis = "."
	}
	return filepath.Join(basis, "s3mail")
}

func File() string { return filepath.Join(Dir(), "config.json") }

func Exists() bool {
	_, err := os.Stat(File())
	return err == nil
}

func Load() Config {
	k := Defaults()
	blob, err := os.ReadFile(File())
	if err != nil {
		return k
	}
	_ = json.Unmarshal(blob, &k) // kaputte Datei: dann eben die Standardwerte
	if k.Port == 0 {
		k.Port = 8765
	}
	if k.Host == "" {
		k.Host = "127.0.0.1"
	}
	if k.Region == "" {
		k.Region = "eu-central-1"
	}
	return k
}

// Save writes the file with mode 0600 - it holds no secret, but the bucket
// name is nobody else's business either.
func Save(k Config) (string, error) {
	if strings.TrimSpace(k.Bucket) == "" {
		return "", errors.New("ohne Bucket geht es nicht")
	}
	k.Prefix = NormalizePrefix(k.Prefix)
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
	heim, err := os.UserHomeDir()
	if err != nil {
		return ".aws"
	}
	return filepath.Join(heim, ".aws")
}

// Profiles reads the profile names from ~/.aws/credentials and ~/.aws/config.
func Profiles() []string {
	seen := map[string]bool{}
	var out []string
	fuegeHinzu := func(name string) {
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
				continue // das sind keine Profile
			}
			fuegeHinzu(name)
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
		return "", errors.New("Access Key ID und Secret Access Key werden beide gebraucht")
	}
	if len(keyID) < 16 || (!strings.HasPrefix(keyID, "AKIA") && !strings.HasPrefix(keyID, "ASIA")) {
		return "", errors.New("Das sieht nicht nach einer Access Key ID aus (beginnt mit AKIA…)")
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

	start, ende := -1, len(lines)
	for i, z := range lines {
		t := strings.TrimSpace(z)
		if t == header {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(t, "[") {
			ende = i
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
		block := append([]string{}, lines[start+1:ende]...)
		for k, v := range values {
			ersetzt := false
			for i, z := range block {
				if strings.HasPrefix(strings.TrimSpace(z), k) &&
					strings.Contains(z, "=") &&
					strings.TrimSpace(strings.SplitN(z, "=", 2)[0]) == k {
					block[i] = fmt.Sprintf("%s = %s", k, v)
					ersetzt = true
					break
				}
			}
			if !ersetzt {
				block = append(block, fmt.Sprintf("%s = %s", k, v))
			}
		}
		neu := append([]string{}, lines[:start+1]...)
		neu = append(neu, block...)
		neu = append(neu, lines[ende:]...)
		lines = neu
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}
