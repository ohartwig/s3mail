//go:build ignore

// Packs files into a ZIP and keeps the executable bit.
//
// Why not /usr/bin/zip: the golang image does not ship it, and an apt-get for
// it would be a second package channel in a pipeline that otherwise needs
// nothing but Go. The executable bit has to travel, because a file downloaded
// raw arrives without +x and cannot be started.
//
//	go run tools/zip.go ziel.zip datei [datei…]
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
	target, err := os.Create(os.Args[1])
	if err != nil {
		fail(err)
	}
	defer target.Close()

	w := zip.NewWriter(target)
	for _, name := range os.Args[2:] {
		info, err := os.Stat(name)
		if err != nil {
			fail(err)
		}
		if info.IsDir() {
			// A macOS app bundle is a directory. The structure below it has to
			// survive, or it stops being a bundle and becomes a folder.
			base := filepath.Dir(strings.TrimSuffix(name, string(filepath.Separator)))
			err = filepath.WalkDir(name, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, err := filepath.Rel(base, path)
				if err != nil {
					return err
				}
				return write(w, path, filepath.ToSlash(rel), d)
			})
			if err != nil {
				fail(err)
			}
			continue
		}
		entry, err := os.Lstat(name)
		if err != nil {
			fail(err)
		}
		if err := write(w, name, filepath.Base(name), fsEntry{entry}); err != nil {
			fail(err)
		}
	}
	if err := w.Close(); err != nil {
		fail(err)
	}
}

// write puts a file into the archive and keeps its permissions.
func write(w *zip.Writer, path, name string, d os.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = name
	header.Method = zip.Deflate
	// Whatever was executable stays executable - and a bundle's launcher has to
	// be, or the double click starts nothing.
	mode := info.Mode()
	if mode&0o111 != 0 || filepath.Ext(name) == "" {
		mode |= 0o111
	}
	header.SetMode(mode)
	part, err := w.CreateHeader(header)
	if err != nil {
		return err
	}
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	_, err = io.Copy(part, source)
	return err
}

type fsEntry struct{ os.FileInfo }

func (f fsEntry) Name() string               { return f.FileInfo.Name() }
func (f fsEntry) IsDir() bool                { return f.FileInfo.IsDir() }
func (f fsEntry) Type() os.FileMode          { return f.FileInfo.Mode().Type() }
func (f fsEntry) Info() (os.FileInfo, error) { return f.FileInfo, nil }

func fail(err error) {
	fmt.Fprintln(os.Stderr, "zippen:", err)
	os.Exit(1)
}
