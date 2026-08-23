package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"s3mail/config"
)

// laufendeInstanz prueft, ob unter der gemerkten Adresse schon ein s3mail
// antwortet, und gibt sie zurueck.
//
// Die Adresse steht in adresse.txt, samt Token - dieselbe Datei, die es fuer den
// Fall gibt, dass jemand das Fenster geschlossen hat. Geprueft wird trotzdem: die
// Datei ueberlebt einen Absturz, der Port ist dann aber von etwas anderem belegt,
// und dann waere ein "laeuft schon" schlicht gelogen.
func laufendeInstanz() (string, bool) {
	blob, err := os.ReadFile(filepath.Join(config.Dir(), "adresse.txt"))
	if err != nil {
		return "", false
	}
	url := strings.TrimSpace(strings.SplitN(string(blob), "\n", 2)[0])
	if !strings.HasPrefix(url, "http://") {
		return "", false
	}

	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	// 200 heisst: das ist unsere Instanz und das Token stimmt noch. Alles andere
	// - auch ein 403 von einem fremden Dienst auf demselben Port - zaehlt nicht.
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	return url, true
}
