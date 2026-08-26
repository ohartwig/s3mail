// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package qr

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The test that makes writing this package defensible.
//
// Everything else here checks the encoder against itself or against what the
// standard says about shapes. This one hands each code to Apple's Vision
// framework - a decoder nobody in this repository wrote - and compares what
// comes back with what went in. Without it, "the tests pass" would mean the
// encoder agrees with my reading of the standard, which is exactly the thing in
// doubt.
//
// darwin only, and that is the honest limit: it runs on the Mac runner and on a
// developer's machine, not in the Linux pipeline. Acceptable because the
// encoder will not change once it is right - and if somebody does change it,
// the Mac job is where they find out.
func TestAppleCanReadWhatWeWrote(t *testing.T) {
	if _, err := exec.LookPath("swift"); err != nil {
		t.Skip("no swift - Vision cannot be asked")
	}

	cases := []string{
		"hallo",
		// The real thing: a setup payload with both platform application ARNs.
		`{"bucket":"koh-findready-mail","prefix":"mail/ole/","region":"eu-north-1",` +
			`"from":"ole@findready.ai","label":"Ole","accessKey":"","secret":"",` +
			`"pushApps":{"development":"arn:aws:sns:eu-north-1:714476370938:app/APNS_SANDBOX/s3mail-ios-sandbox",` +
			`"production":"arn:aws:sns:eu-north-1:714476370938:app/APNS/s3mail-ios-production"},` +
			`"pushTopic":"arn:aws:sns:eu-north-1:714476370938:koh-mail-ole-push"}`,
		// Umlauts, because the payload carries names and labels.
		`{"label":"Ole Hartwig – Geschäftlich","from":"post@ole-hartwig.eu"}`,
		// One byte, and a length that lands exactly on a version boundary.
		"a",
		strings.Repeat("x", 271),
		strings.Repeat("y", 272),
	}

	dir := t.TempDir()
	var paths []string
	for i, text := range cases {
		c, err := Encode(text)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		p := filepath.Join(dir, "code"+string(rune('a'+i))+".png")
		if err := writePNG(p, c); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}

	args := append([]string{"qr/decode.swift"}, paths...)
	if wd, _ := os.Getwd(); strings.HasSuffix(wd, "/qr") {
		args[0] = "decode.swift"
	}
	out, err := exec.Command("swift", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("swift: %v\n%s", err, out)
	}
	got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(got) != len(cases) {
		t.Fatalf("expected %d lines, got %d:\n%s", len(cases), len(got), out)
	}
	for i, want := range cases {
		if got[i] != want {
			t.Errorf("case %d did not survive the round trip\n got: %.80s\nwant: %.80s",
				i, got[i], want)
		}
	}
}

// writePNG renders the code big enough for a decoder, quiet zone included.
func writePNG(path string, c *Code) error {
	const scale, quiet = 8, 4
	side := (c.Size + 2*quiet) * scale
	img := image.NewGray(image.Rect(0, 0, side, side))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if !c.At(x, y) {
				continue
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.Set((x+quiet)*scale+dx, (y+quiet)*scale+dy, color.Gray{})
				}
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
