package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// oeffneFenster bringt die Oberflaeche auf den Schirm.
//
// Ein eigenes Fenster mit nativem WebView haette CGO gebraucht, und damit waere
// die Cross-Kompilierung weg gewesen - Wails kann nicht einmal nach macOS
// cross-kompilieren. Stattdessen wird der Browser im App-Modus gestartet: ein
// Fenster ohne Tableiste und ohne Adresszeile. Das sieht aus wie ein Programm und
// kostet keine einzige C-Abhaengigkeit.
//
// Findet sich kein passender Browser, geht die Adresse eben in einem Tab auf.
func oeffneFenster(url string) {
	time.Sleep(400 * time.Millisecond)
	if starteAppModus(url) {
		return
	}
	oeffneBrowser(url)
}

// appModusBrowser sind die Browser, die --app= koennen, in der Reihenfolge, in
// der wir sie ausprobieren.
func appModusBrowser() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	case "windows":
		var pfade []string
		for _, basis := range []string{os.Getenv("ProgramFiles"),
			os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if basis == "" {
				continue
			}
			pfade = append(pfade,
				filepath.Join(basis, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(basis, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(basis, `BraveSoftware\Brave-Browser\Application\brave.exe`))
		}
		return pfade
	default:
		return []string{"google-chrome", "chromium", "chromium-browser",
			"microsoft-edge", "brave-browser"}
	}
}

func starteAppModus(url string) bool {
	for _, kandidat := range appModusBrowser() {
		pfad := kandidat
		if !filepath.IsAbs(pfad) {
			gefunden, err := exec.LookPath(pfad)
			if err != nil {
				continue
			}
			pfad = gefunden
		} else if _, err := os.Stat(pfad); err != nil {
			continue
		}
		cmd := exec.Command(pfad, "--app="+url,
			"--window-size=1280,860",
			// Eigenes Profil, damit s3mail nicht in einem laufenden Browserfenster
			// landet und dessen Sitzung mitbenutzt.
			"--user-data-dir="+filepath.Join(os.TempDir(), "s3mail-fenster"))
		if cmd.Start() == nil {
			return true
		}
	}
	return false
}

// oeffneBrowser ist der Rueckfall: normale Adresse im Standardbrowser.
func oeffneBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
