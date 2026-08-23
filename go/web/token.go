package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"os"
	"path/filepath"
)

// NewToken rolls the session token.
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

// WriteTokenFile puts the start address into a file readable only by the user.
//
// On Windows s3mail starts by double click, and whoever closes the console window
// has no way back to the address - the server then still runs but is
// unreachable. So it is written here as well.
func WriteTokenFile(dir, url string) (string, error) {
	if dir == "" {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "adresse.txt")
	content := url + "\n\nDiese Adresse gilt, solange s3mail laeuft. Nach einem\n" +
		"Neustart steht hier eine neue.\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// RemoveTokenFile cleans up on exit - an address that has expired only causes
// confusion when it lies around in a file.
func RemoveTokenFile(dir string) {
	if dir != "" {
		_ = os.Remove(filepath.Join(dir, "adresse.txt"))
	}
}
