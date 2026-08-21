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
		kopf, err := zip.FileInfoHeader(info)
		if err != nil {
			abbruch(err)
		}
		kopf.Name = filepath.Base(name)
		kopf.Method = zip.Deflate
		kopf.SetMode(info.Mode() | 0o111) // ausfuehrbar, auch wenn die Quelle es nicht war
		teil, err := w.CreateHeader(kopf)
		if err != nil {
			abbruch(err)
		}
		quelle, err := os.Open(name)
		if err != nil {
			abbruch(err)
		}
		if _, err := io.Copy(teil, quelle); err != nil {
			abbruch(err)
		}
		quelle.Close()
	}
	if err := w.Close(); err != nil {
		abbruch(err)
	}
}

func abbruch(err error) {
	fmt.Fprintln(os.Stderr, "zippen:", err)
	os.Exit(1)
}
