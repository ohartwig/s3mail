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

// appModusBrowser nennt die Browser, die --app= koennen, in der Reihenfolge, in
// der wir sie ausprobieren. Unter macOS der Bundle-Name, sonst der Programmpfad.
func appModusBrowser() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"Google Chrome", "Microsoft Edge", "Brave Browser", "Chromium"}
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

// fensterArgumente sind die Schalter, die aus einem Browser ein Programmfenster machen.
func fensterArgumente(url string) []string {
	return []string{
		"--app=" + url,
		"--window-size=1280,860",
		// Eigenes Profil, damit s3mail nicht die Sitzung eines laufenden
		// Browserfensters mitbenutzt.
		"--user-data-dir=" + filepath.Join(os.TempDir(), "s3mail-fenster"),
	}
}

func starteAppModus(url string) bool {
	if runtime.GOOS == "darwin" {
		return starteAppModusMac(url)
	}
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
		if exec.Command(pfad, fensterArgumente(url)...).Start() == nil {
			return true
		}
	}
	return false
}

// starteAppModusMac geht ueber `open -na`.
//
// Das Browser-Binary direkt aufzurufen funktioniert unter macOS nicht, wenn der
// Browser schon laeuft: der Aufruf reicht die Adresse nur an die bestehende
// Instanz weiter ("Wird in einer aktuellen Browsersitzung geoeffnet") und
// verwirft --app= und --user-data-dir, weil das Startparameter sind. Es entsteht
// dann bestenfalls ein Tab im Hintergrund - und fuer den, der doppelgeklickt
// hat, sieht es aus, als passiere nichts. `open -n` erzwingt eine neue Instanz,
// die die Schalter auch beachtet.
func starteAppModusMac(url string) bool {
	for _, name := range appModusBrowser() {
		if _, err := os.Stat("/Applications/" + name + ".app"); err != nil {
			continue
		}
		prog, args := macKommando(name, url)
		if exec.Command(prog, args...).Run() == nil {
			return true
		}
	}
	return false
}

// macKommando baut den Aufruf. Eigene Funktion, damit der Test festhalten kann,
// dass "-n" dabei ist - ohne das reicht macOS die Adresse an eine laufende
// Browserinstanz weiter, und es geht kein Fenster auf.
func macKommando(name, url string) (string, []string) {
	return "open", append([]string{"-na", name, "--args"}, fensterArgumente(url)...)
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
