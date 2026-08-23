package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"s3mail/config"
)

// startLog oeffnet die Datei, in der der Start festgehalten wird.
//
// Startet s3mail ohne Konsole - als macOS-Bundle oder unter Windows im
// GUI-Modus -, geht jede Meldung ins Nichts. Wenn dann etwas schiefgeht, steht
// der Nutzer vor einem Programm, das scheinbar nichts tut. Die Datei ist die
// einzige Spur, die er uns schicken kann.
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

// report schreibt auf die Konsole und ins Protokoll.
func report(w io.Writer, format string, a ...any) {
	fmt.Printf(format+"\n", a...)
	fmt.Fprintf(w, format+"\n", a...)
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
