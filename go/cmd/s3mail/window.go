// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

// profileDir is the browser profile s3mail keeps to itself, so it does not
// share the session of a running browser window.
//
// It doubles as the mark by which our own window is recognised: no other
// program passes this path, so a browser process carrying it is one we started.
func profileDir() string {
	return filepath.Join(os.TempDir(), "s3mail-fenster")
}

// windowArgs are the switches that turn a browser into a program window.
func windowArgs(url string) []string {
	return []string{
		"--app=" + url,
		"--window-size=1280,860",
		"--user-data-dir=" + profileDir(),
	}
}

// appModePID finds the browser that is already showing our window.
//
// pgrep answers with the whole family - renderer, GPU, network - and only the
// one without "--type=" is the process that owns the window. Raising a helper
// does nothing at all, silently.
func appModePID() (int, bool) {
	// Without the leading dashes: pgrep would read "--user-data-dir=..." as its
	// own option and fail, and a failing pgrep reads as "nothing running".
	out, err := exec.Command("pgrep", "-f", "user-data-dir="+profileDir()).Output()
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		cmd, err := exec.Command("ps", "-p", line, "-o", "command=").Output()
		if err != nil || strings.Contains(string(cmd), "--type=") {
			continue
		}
		return pid, true
	}
	return 0, false
}

// raiseScript is the AppleScript that brings one process to the front.
//
// By process id and not by application name: the s3mail window and whatever
// browser somebody has open for their own use are two instances of the same
// bundle, and `open -a "Google Chrome"` raises the other one - measured, not
// assumed.
func raiseScript(pid int) string {
	return fmt.Sprintf(
		"tell application \"System Events\" to set frontmost of "+
			"(first process whose unix id is %d) to true", pid)
}

// raiseWindow brings our window forward, and says whether it managed to.
//
// It can fail: macOS gates this behind Accessibility, and a program that has
// not been granted it gets an error rather than a window. That is why the
// answer is checked - see startAppModeMac for what happens then.
func raiseWindow(pid int) bool {
	return exec.Command("osascript", "-e", raiseScript(pid)).Run() == nil
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
	// A window of ours is already open. Then this start has nothing to open -
	// it has something to find. Forcing another instance is what made every
	// double click leave a browser behind; handing the address to the running
	// one is no better, because --app= is a startup switch and a running
	// browser drops it, leaving a plain window with no mailbox in it. Both
	// measured on a real machine before this was written.
	if pid, ok := appModePID(); ok {
		// Not raising is a poor outcome and still the better one: the window is
		// there, and a second browser beside it is exactly what was reported.
		raiseWindow(pid)
		return true
	}
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
