// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package qr

import (
	"strings"
	"testing"
)

// The structural checks: things that are true of every QR code, whatever it
// says. They catch a broken matrix without needing a reader.

func TestTheFinderPatternsAreWhereTheyBelong(t *testing.T) {
	c, err := Encode("hallo")
	if err != nil {
		t.Fatal(err)
	}
	// Three corners carry a finder, the fourth does not - that is how a reader
	// works out which way up the code is.
	corners := [][2]int{{0, 0}, {c.Size - 7, 0}, {0, c.Size - 7}}
	for _, p := range corners {
		for _, d := range [][2]int{{0, 0}, {6, 0}, {0, 6}, {6, 6}, {3, 3}} {
			if !c.At(p[0]+d[0], p[1]+d[1]) {
				t.Errorf("finder at %v incomplete at %v", p, d)
			}
		}
		if c.At(p[0]+1, p[1]+1) {
			t.Errorf("finder at %v has no light ring", p)
		}
	}
	if c.At(c.Size-4, c.Size-4) {
		t.Error("something finder-shaped in the fourth corner")
	}
}

func TestTheTimingPatternAlternates(t *testing.T) {
	c, _ := Encode("hallo")
	for i := 8; i < c.Size-8; i++ {
		if c.At(i, 6) != (i%2 == 0) {
			t.Fatalf("horizontal timing wrong at %d", i)
		}
		if c.At(6, i) != (i%2 == 0) {
			t.Fatalf("vertical timing wrong at %d", i)
		}
	}
}

// The module at (8, size-8) is dark in every QR code ever made. If it is light,
// the format information was written to the wrong place.
func TestTheDarkModuleIsDark(t *testing.T) {
	for _, text := range []string{"a", strings.Repeat("x", 300)} {
		c, err := Encode(text)
		if err != nil {
			t.Fatal(err)
		}
		if !c.At(8, c.Size-8) {
			t.Errorf("dark module missing for %d bytes", len(text))
		}
	}
}

func TestTheVersionGrowsWithTheText(t *testing.T) {
	small, _ := Encode("hallo")
	large, err := Encode(strings.Repeat("x", 500))
	if err != nil {
		t.Fatal(err)
	}
	if large.Version <= small.Version {
		t.Errorf("500 bytes fit in version %d, 5 bytes in %d", large.Version, small.Version)
	}
	if large.Size != 4*large.Version+17 {
		t.Errorf("size %d does not match version %d", large.Size, large.Version)
	}
}

// Beyond version 20 this package refuses rather than producing something
// unreadable.
func TestTooLongIsRefused(t *testing.T) {
	if _, err := Encode(strings.Repeat("x", 5000)); err != ErrTooLong {
		t.Errorf("expected ErrTooLong, got %v", err)
	}
}

// The quiet zone is not decoration: without four light modules around it, a
// reader fails on a code that looks perfectly fine to a person.
func TestTheSVGCarriesAQuietZone(t *testing.T) {
	c, _ := Encode("hallo")
	svg := c.SVG(4)
	if !strings.Contains(svg, `viewBox="0 0 `+itoa(c.Size+8)+` `+itoa(c.Size+8)+`"`) {
		t.Errorf("no four-module quiet zone in the viewBox: %.90s", svg)
	}
	if !strings.Contains(svg, `fill="#fff"`) {
		t.Error("no white ground - on a dark page the code would be unreadable")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Reed-Solomon against the worked example in the standard: the message
// "HELLO WORLD" at version 1-M has known correction codewords.
func TestReedSolomonAgainstTheStandardsExample(t *testing.T) {
	data := []byte{32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17}
	want := []byte{196, 35, 39, 119, 235, 215, 231, 226, 93, 23}
	got := reedSolomon(data, 10)
	if len(got) != len(want) {
		t.Fatalf("got %d codewords, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("codeword %d: got %d, want %d\nall: %v", i, got[i], want[i], got)
		}
	}
}
