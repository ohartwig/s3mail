// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// openWindow puts the interface on the screen.
//
// A window of our own with a native WebView would have needed CGO, and cross
// compilation would have been gone with it - Wails cannot even cross compile to
// macOS. Instead the browser is started in app mode: a window without a tab bar
// and without an address bar. That looks like a program and costs not a single
// C dependency.
//
// If no suitable browser is found, the address simply opens in a tab.
func openWindow(url string) {
	time.Sleep(400 * time.Millisecond)
	if startAppMode(url) {
		return
	}
	openBrowser(url)
}

// appModeBrowser names the browsers that can do --app=, in the order we try
// them. On macOS the bundle name, elsewhere the program path.
func appModeBrowser() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"Google Chrome", "Microsoft Edge", "Brave Browser", "Chromium"}
	case "windows":
		var paths []string
		for _, base := range []string{os.Getenv("ProgramFiles"),
			os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if base == "" {
				continue
			}
			paths = append(paths,
				filepath.Join(base, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(base, `BraveSoftware\Brave-Browser\Application\brave.exe`))
		}
		return paths
	default:
		return []string{"google-chrome", "chromium", "chromium-browser",
			"microsoft-edge", "brave-browser"}
	}
}

// windowArgs are the switches that turn a browser into a program window.
func windowArgs(url string) []string {
	return []string{
		"--app=" + url,
		"--window-size=1280,860",
		// A profile of our own, so s3mail does not share the session of a running
		// browser window.
		"--user-data-dir=" + filepath.Join(os.TempDir(), "s3mail-fenster"),
	}
}

func startAppMode(url string) bool {
	if runtime.GOOS == "darwin" {
		return startAppModeMac(url)
	}
	for _, candidate := range appModeBrowser() {
		path := candidate
		if !filepath.IsAbs(path) {
			found, err := exec.LookPath(path)
			if err != nil {
				continue
			}
			path = found
		} else if _, err := os.Stat(path); err != nil {
			continue
		}
		if exec.Command(path, windowArgs(url)...).Start() == nil {
			return true
		}
	}
	return false
}

// startAppModeMac goes through `open -na`.
//
// Calling the browser binary directly does not work on macOS while the browser
// is already running: the call only hands the address to the existing instance
// ("opened in a current browser session") and discards --app= and
// --user-data-dir, because those are startup parameters. At best a tab appears
// in the background - and to whoever double clicked, it looks like nothing
// happens. `open -n` forces a new instance, and that one honours the switches.
func startAppModeMac(url string) bool {
	for _, name := range appModeBrowser() {
		if _, err := os.Stat("/Applications/" + name + ".app"); err != nil {
			continue
		}
		prog, args := macCommand(name, url)
		if exec.Command(prog, args...).Run() == nil {
			return true
		}
	}
	return false
}

// macCommand builds the call. A function of its own so the test can pin down
// that "-n" is part of it - without it macOS hands the address to a running
// browser instance and no window opens.
func macCommand(name, url string) (string, []string) {
	return "open", append([]string{"-na", name, "--args"}, windowArgs(url)...)
}

// openBrowser is the fallback: a normal address in the default browser.
func openBrowser(url string) {
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
