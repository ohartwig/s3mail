// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package qr turns a string into a QR code, in byte mode, at error correction
// level M.
//
// Written here rather than pulled in, like tools/zip.go and the JSON-RPC in
// mcp/. The reservation is real and worth writing down: this is not a zip
// header. It is Reed-Solomon over GF(256), eight mask patterns scored against
// four penalty rules, and two BCH codes - and a subtle mistake in any of them
// produces a code that scans on one phone and not on the next. That failure
// mode is what makes hand-writing it questionable.
//
// What settles it is that the result can be checked against somebody else's
// reader. macOS decodes QR codes with Vision, so qr_vision_test.go feeds every
// code this package produces through Apple's decoder and compares. An encoder
// tested only against its own decoder proves nothing; this one is not.
//
// Only what s3mail needs: byte mode, level M, versions 1 to 20. See tables.go.
package qr

import (
	"errors"
	"fmt"
	"strings"
)

// ErrTooLong means the text does not fit in version 20 at level M - about a
// kilobyte. The setup payload is a few hundred bytes.
var ErrTooLong = errors.New("too long for a QR code of this size")

// Code is a finished symbol: size×size modules, true meaning dark.
type Code struct {
	Size    int
	Version int
	m       []bool
}

// At answers whether a module is dark. Outside the symbol is light, so a caller
// that walks a border does not have to check.
func (c *Code) At(x, y int) bool {
	if x < 0 || y < 0 || x >= c.Size || y >= c.Size {
		return false
	}
	return c.m[y*c.Size+x]
}

func (c *Code) set(x, y int, dark bool) { c.m[y*c.Size+x] = dark }

// Encode builds the symbol for text.
func Encode(text string) (*Code, error) {
	data := []byte(text)

	version, err := fit(len(data))
	if err != nil {
		return nil, err
	}
	b := blocks[version]

	bits := encodeData(data, version, b.data())
	codewords := interleave(bits, b)

	c := &Code{Size: 4*version + 17, Version: version}
	c.m = make([]bool, c.Size*c.Size)
	reserved := c.patterns()
	c.place(codewords, reserved)

	best, bestScore := 0, -1
	var bestModules []bool
	for mask := 0; mask < 8; mask++ {
		trial := make([]bool, len(c.m))
		copy(trial, c.m)
		applyMask(trial, reserved, c.Size, mask)
		saved := c.m
		c.m = trial
		c.formatInfo(mask)
		score := penalty(c)
		if bestScore < 0 || score < bestScore {
			bestScore, best = score, mask
			bestModules = append([]bool(nil), c.m...)
		}
		c.m = saved
	}
	c.m = bestModules
	_ = best
	return c, nil
}

// fit picks the smallest version the data fits into.
func fit(n int) (int, error) {
	for v := 1; v <= 20; v++ {
		// 4 bits mode + the character count indicator + the data itself.
		countBits := 8
		if v >= 10 {
			countBits = 16
		}
		if (4+countBits)/8+n <= blocks[v].data() {
			// The division above is a floor; check the exact bit count.
			if 4+countBits+8*n <= 8*blocks[v].data() {
				return v, nil
			}
		}
	}
	return 0, ErrTooLong
}

// encodeData writes mode, length, payload, terminator and padding.
func encodeData(data []byte, version, capacity int) []byte {
	var buf bits
	buf.add(0b0100, 4) // byte mode
	countBits := 8
	if version >= 10 {
		countBits = 16
	}
	buf.add(uint(len(data)), countBits)
	for _, b := range data {
		buf.add(uint(b), 8)
	}
	// Terminator: up to four zero bits, fewer if the capacity ends sooner.
	room := 8*capacity - buf.n
	if room > 4 {
		room = 4
	}
	buf.add(0, room)
	buf.add(0, (8-buf.n%8)%8) // to the next byte boundary

	// Pad with the two bytes the standard names, alternating. They are not
	// arbitrary: alternating 11101100/00010001 avoids long runs, which the mask
	// penalty would otherwise charge for.
	pad := []byte{0xEC, 0x11}
	for i := 0; len(buf.b) < capacity; i++ {
		buf.b = append(buf.b, pad[i%2])
	}
	return buf.b
}

// interleave splits the data into blocks, appends each block's correction
// codewords, and reads them back column by column - which is what makes a
// scratch across the symbol damage every block a little instead of one block
// fatally.
func interleave(data []byte, b block) []byte {
	type piece struct{ data, ec []byte }
	pieces := make([]piece, 0, b.n1+b.n2)

	at := 0
	for i := 0; i < b.n1; i++ {
		d := data[at : at+b.d1]
		at += b.d1
		pieces = append(pieces, piece{d, reedSolomon(d, b.ec)})
	}
	for i := 0; i < b.n2; i++ {
		d := data[at : at+b.d2]
		at += b.d2
		pieces = append(pieces, piece{d, reedSolomon(d, b.ec)})
	}

	out := make([]byte, 0, b.total())
	longest := b.d1
	if b.d2 > longest {
		longest = b.d2
	}
	for i := 0; i < longest; i++ {
		for _, p := range pieces {
			if i < len(p.data) {
				out = append(out, p.data[i])
			}
		}
	}
	for i := 0; i < b.ec; i++ {
		for _, p := range pieces {
			out = append(out, p.ec[i])
		}
	}
	return out
}

// SVG renders the code, one module per unit, with the four-module quiet zone
// the standard requires. Without it readers fail on a code that looks fine.
func (c *Code) SVG(pixelsPerModule int) string {
	if pixelsPerModule < 1 {
		pixelsPerModule = 4
	}
	const quiet = 4
	side := (c.Size + 2*quiet) * pixelsPerModule

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" `+
		`viewBox="0 0 %d %d" shape-rendering="crispEdges">`,
		side, side, c.Size+2*quiet, c.Size+2*quiet)
	// A white ground, not transparency: a dark page behind a transparent code
	// makes it unreadable, and the page's colours are not this file's business.
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/>`,
		c.Size+2*quiet, c.Size+2*quiet)
	b.WriteString(`<path fill="#000" d="`)
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.At(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}

// bits is a bit-at-a-time writer over a byte slice.
type bits struct {
	b []byte
	n int
}

func (w *bits) add(value uint, count int) {
	for i := count - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		if value&(1<<uint(i)) != 0 {
			w.b[w.n/8] |= 1 << uint(7-w.n%8)
		}
		w.n++
	}
}
