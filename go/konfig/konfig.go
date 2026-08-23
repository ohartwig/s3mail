// Package konfig haelt die Konfigurationsdatei und das Schreiben der
// AWS-Zugangsdaten. Die Pfade folgen den Gepflogenheiten der jeweiligen
// Plattform - unter Windows landet nichts in einem ~/.config, das dort niemand
// sucht.
package konfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Konfig ist, was in config.json steht.
type Konfig struct {
	Profil      string `json:"profile"`
	Region      string `json:"region"`
	Bucket      string `json:"bucket"`
	Prefix      string `json:"prefix"`
	Absender    string `json:"from"`
	AllowDelete bool   `json:"allow_delete"`
	Port        int    `json:"port"`
	Host        string `json:"host"`

	// Sprache der Oberflaeche. Leer heisst: die des Browsers nehmen - das ist
	// bei einem Programm, das neben dem Browser laeuft, die bessere Vorgabe
	// als eine geratene.
	Sprache string `json:"language"`
}

func Standard() Konfig {
	return Konfig{Region: "eu-central-1", Prefix: "mail/", AllowDelete: true,
		Port: 8765, Host: "127.0.0.1"}
}

// Verzeichnis ist der Ort der Konfiguration: ~/.config/s3mail unter Linux,
// ~/Library/Application Support/s3mail unter macOS, %AppData%\s3mail unter Windows.
func Verzeichnis() string {
	if v := os.Getenv("S3MAIL_CONFIG_DIR"); v != "" {
		return v
	}
	basis, err := os.UserConfigDir()
	if err != nil {
		basis = "."
	}
	return filepath.Join(basis, "s3mail")
}

// CacheVerzeichnis ist der Ort des Index - Daten, deren Verlust nur Zeit kostet.
func CacheVerzeichnis() string {
	if v := os.Getenv("S3MAIL_CACHE_DIR"); v != "" {
		return v
	}
	basis, err := os.UserCacheDir()
	if err != nil {
		basis = "."
	}
	return filepath.Join(basis, "s3mail")
}

func Datei() string { return filepath.Join(Verzeichnis(), "config.json") }

func Vorhanden() bool {
	_, err := os.Stat(Datei())
	return err == nil
}

func Laden() Konfig {
	k := Standard()
	blob, err := os.ReadFile(Datei())
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

// Speichern legt die Datei mit 0600 an - dort steht zwar kein Geheimnis, aber
// der Bucketname geht auch niemanden etwas an.
func Speichern(k Konfig) (string, error) {
	if strings.TrimSpace(k.Bucket) == "" {
		return "", errors.New("ohne Bucket geht es nicht")
	}
	k.Prefix = PrefixNormalisieren(k.Prefix)
	if err := os.MkdirAll(Verzeichnis(), 0o700); err != nil {
		return "", err
	}
	blob, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return "", err
	}
	pfad := Datei()
	if err := os.WriteFile(pfad, blob, 0o600); err != nil {
		return "", err
	}
	return pfad, nil
}

// PrefixNormalisieren macht aus "/mail" und "mail" jeweils "mail/".
func PrefixNormalisieren(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimLeft(p, "/")
	if p != "" && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// -- AWS-Zugangsdaten ------------------------------------------------------- //

// AWSVerzeichnis ist ~/.aws - derselbe Ort, den auch die AWS-Werkzeuge lesen.
func AWSVerzeichnis() string {
	if v := os.Getenv("S3MAIL_AWS_DIR"); v != "" {
		return v
	}
	heim, err := os.UserHomeDir()
	if err != nil {
		return ".aws"
	}
	return filepath.Join(heim, ".aws")
}

// Profile liest die Profilnamen aus ~/.aws/credentials und ~/.aws/config.
func Profile() []string {
	gesehen := map[string]bool{}
	var out []string
	fuegeHinzu := func(name string) {
		if name != "" && !gesehen[name] {
			gesehen[name] = true
			out = append(out, name)
		}
	}
	for _, f := range []struct{ datei, praefix string }{
		{"credentials", ""}, {"config", "profile "},
	} {
		blob, err := os.ReadFile(filepath.Join(AWSVerzeichnis(), f.datei))
		if err != nil {
			continue
		}
		for _, zeile := range strings.Split(string(blob), "\n") {
			zeile = strings.TrimSpace(zeile)
			if !strings.HasPrefix(zeile, "[") || !strings.HasSuffix(zeile, "]") {
				continue
			}
			name := strings.TrimSpace(zeile[1 : len(zeile)-1])
			name = strings.TrimPrefix(name, f.praefix)
			if strings.HasPrefix(name, "sso-session ") || strings.HasPrefix(name, "services ") {
				continue // das sind keine Profile
			}
			fuegeHinzu(name)
		}
	}
	return out
}

// ZugangsdatenSchreiben legt die Schluessel als benanntes Profil in
// ~/.aws/credentials ab - additiv, vorhandene Profile bleiben unberuehrt.
func ZugangsdatenSchreiben(profil, keyID, secret, region string) (string, error) {
	profil = strings.TrimSpace(profil)
	if profil == "" {
		profil = "s3mail"
	}
	keyID, secret = strings.TrimSpace(keyID), strings.TrimSpace(secret)
	if keyID == "" || secret == "" {
		return "", errors.New("Access Key ID und Secret Access Key werden beide gebraucht")
	}
	if len(keyID) < 16 || (!strings.HasPrefix(keyID, "AKIA") && !strings.HasPrefix(keyID, "ASIA")) {
		return "", errors.New("Das sieht nicht nach einer Access Key ID aus (beginnt mit AKIA…)")
	}
	if err := os.MkdirAll(AWSVerzeichnis(), 0o700); err != nil {
		return "", err
	}
	if err := iniSetzen(filepath.Join(AWSVerzeichnis(), "credentials"), profil,
		map[string]string{"aws_access_key_id": keyID, "aws_secret_access_key": secret}); err != nil {
		return "", err
	}
	abschnitt := "profile " + profil
	if profil == "default" {
		abschnitt = "default"
	}
	if region == "" {
		region = "eu-central-1"
	}
	if err := iniSetzen(filepath.Join(AWSVerzeichnis(), "config"), abschnitt,
		map[string]string{"region": region}); err != nil {
		return "", err
	}
	return profil, nil
}

// iniSetzen schreibt Schluessel in einen Abschnitt und laesst alles andere in Ruhe.
// Bewusst zeilenweise statt mit einer INI-Bibliothek: die schriebe die Datei neu
// und wuerfe Kommentare und fremde Formatierung weg.
func iniSetzen(pfad, abschnitt string, werte map[string]string) error {
	blob, err := os.ReadFile(pfad)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	zeilen := []string{}
	if len(blob) > 0 {
		zeilen = strings.Split(strings.TrimRight(string(blob), "\n"), "\n")
	}
	kopf := "[" + abschnitt + "]"

	start, ende := -1, len(zeilen)
	for i, z := range zeilen {
		t := strings.TrimSpace(z)
		if t == kopf {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(t, "[") {
			ende = i
			break
		}
	}
	if start < 0 { // Abschnitt anhaengen
		if len(zeilen) > 0 {
			zeilen = append(zeilen, "")
		}
		zeilen = append(zeilen, kopf)
		for k, v := range werte {
			zeilen = append(zeilen, fmt.Sprintf("%s = %s", k, v))
		}
	} else {
		block := append([]string{}, zeilen[start+1:ende]...)
		for k, v := range werte {
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
		neu := append([]string{}, zeilen[:start+1]...)
		neu = append(neu, block...)
		neu = append(neu, zeilen[ende:]...)
		zeilen = neu
	}
	return os.WriteFile(pfad, []byte(strings.Join(zeilen, "\n")+"\n"), 0o600)
}
