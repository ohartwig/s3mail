package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"os"
	"path/filepath"
)

// NewToken wuerfelt das Sitzungs-Token.
func NewToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic("kein Zufall verfuegbar: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// equal vergleicht in konstanter Zeit.
func equal(a, b string) bool {
	return len(a) > 0 && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// TokenDateiSchreiben legt die Startadresse in einer nur fuer den Benutzer
// lesbaren Datei ab.
//
// Unter Windows startet s3mail per Doppelklick, und wer das Konsolenfenster
// schliesst, kommt an die Adresse nicht mehr heran - der Server laeuft dann noch,
// ist aber unerreichbar. Deshalb steht sie zusaetzlich hier.
func TokenDateiSchreiben(verzeichnis, url string) (string, error) {
	if verzeichnis == "" {
		return "", nil
	}
	if err := os.MkdirAll(verzeichnis, 0o700); err != nil {
		return "", err
	}
	pfad := filepath.Join(verzeichnis, "adresse.txt")
	inhalt := url + "\n\nDiese Adresse gilt, solange s3mail laeuft. Nach einem\n" +
		"Neustart steht hier eine neue.\n"
	if err := os.WriteFile(pfad, []byte(inhalt), 0o600); err != nil {
		return "", err
	}
	return pfad, nil
}

// TokenDateiEntfernen raeumt beim Beenden auf - eine abgelaufene Adresse in einer
// Datei stiftet nur Verwirrung.
func TokenDateiEntfernen(verzeichnis string) {
	if verzeichnis != "" {
		_ = os.Remove(filepath.Join(verzeichnis, "adresse.txt"))
	}
}
