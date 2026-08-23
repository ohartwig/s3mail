package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"s3mail/config"
)

// startLog opens the file the start is recorded in.
//
// If s3mail starts without a console - as a macOS bundle or on Windows in GUI
// mode - every message goes nowhere. When something then goes wrong, the reader
// faces a program that appears to do nothing. This file is the only trace they
// can send us.
func startLog() io.WriteCloser {
	dir := config.Dir()
	if os.MkdirAll(dir, 0o700) != nil {
		return nopCloser{io.Discard}
	}
	f, err := os.OpenFile(filepath.Join(dir, "start.log"),
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nopCloser{io.Discard}
	}
	fmt.Fprintf(f, "s3mail %s gestartet %s\n", version,
		time.Now().Format("2006-01-02 15:04:05"))
	return f
}

// report writes to the console and to the log.
func report(w io.Writer, format string, a ...any) {
	fmt.Printf(format+"\n", a...)
	fmt.Fprintf(w, format+"\n", a...)
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
