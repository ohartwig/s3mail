//go:build ignore

// Packt Dateien in ein ZIP und behaelt dabei das Ausfuehrungs-Bit.
//
// Warum nicht /usr/bin/zip: das golang-Image bringt es nicht mit, und ein
// apt-get dafuer waere ein zweiter Paketkanal in einer Pipeline, die sonst nur
// Go braucht. Das Ausfuehrungs-Bit muss mit, weil eine roh heruntergeladene
// Datei ohne +x ankommt und sich nicht starten laesst.
//
//	go run tools/zippen.go ziel.zip datei [datei…]
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: zippen.go ziel.zip datei [datei…]")
		os.Exit(2)
	}
	ziel, err := os.Create(os.Args[1])
	if err != nil {
		abbruch(err)
	}
	defer ziel.Close()

	w := zip.NewWriter(ziel)
	for _, name := range os.Args[2:] {
		info, err := os.Stat(name)
		if err != nil {
			abbruch(err)
		}
		if info.IsDir() {
			// Ein macOS-App-Bundle ist ein Verzeichnis. Die Struktur darunter muss
			// erhalten bleiben, sonst ist es kein Bundle mehr, sondern ein Ordner.
			wurzel := filepath.Dir(strings.TrimSuffix(name, string(filepath.Separator)))
			err = filepath.WalkDir(name, func(pfad string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, err := filepath.Rel(wurzel, pfad)
				if err != nil {
					return err
				}
				return schreibe(w, pfad, filepath.ToSlash(rel), d)
			})
			if err != nil {
				abbruch(err)
			}
			continue
		}
		eintrag, err := os.Lstat(name)
		if err != nil {
			abbruch(err)
		}
		if err := schreibe(w, name, filepath.Base(name), fsEintrag{eintrag}); err != nil {
			abbruch(err)
		}
	}
	if err := w.Close(); err != nil {
		abbruch(err)
	}
}

// schreibe legt eine Datei ins Archiv und behaelt ihre Rechte.
func schreibe(w *zip.Writer, pfad, name string, d os.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	kopf, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	kopf.Name = name
	kopf.Method = zip.Deflate
	// Alles, was ausfuehrbar war, bleibt es - und das Startprogramm eines Bundles
	// muss es sein, sonst startet der Doppelklick nichts.
	modus := info.Mode()
	if modus&0o111 != 0 || filepath.Ext(name) == "" {
		modus |= 0o111
	}
	kopf.SetMode(modus)
	teil, err := w.CreateHeader(kopf)
	if err != nil {
		return err
	}
	quelle, err := os.Open(pfad)
	if err != nil {
		return err
	}
	defer quelle.Close()
	_, err = io.Copy(teil, quelle)
	return err
}

type fsEintrag struct{ os.FileInfo }

func (f fsEintrag) Name() string               { return f.FileInfo.Name() }
func (f fsEintrag) IsDir() bool                { return f.FileInfo.IsDir() }
func (f fsEintrag) Type() os.FileMode          { return f.FileInfo.Mode().Type() }
func (f fsEintrag) Info() (os.FileInfo, error) { return f.FileInfo, nil }

func abbruch(err error) {
	fmt.Fprintln(os.Stderr, "zippen:", err)
	os.Exit(1)
}
