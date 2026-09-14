// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package qr

// Where the modules go: the fixed patterns, the data path, the mask and the
// two BCH-coded fields that tell a reader how to read the rest.

// patterns draws everything that is not data and returns which modules are
// therefore off limits.
func (c *Code) patterns() []bool {
	reserved := make([]bool, c.Size*c.Size)
	mark := func(x, y int, dark bool) {
		if x < 0 || y < 0 || x >= c.Size || y >= c.Size {
			return
		}
		c.set(x, y, dark)
		reserved[y*c.Size+x] = true
	}

	// The three finder patterns and their separators. The separator is the
	// light ring that keeps the finder distinguishable from whatever data sits
	// beside it.
	finder := func(ox, oy int) {
		for dy := -1; dy <= 7; dy++ {
			for dx := -1; dx <= 7; dx++ {
				x, y := ox+dx, oy+dy
				inRing := dx >= 0 && dx <= 6 && dy >= 0 && dy <= 6
				dark := inRing &&
					(dx == 0 || dx == 6 || dy == 0 || dy == 6 ||
						(dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4))
				mark(x, y, dark)
			}
		}
	}
	finder(0, 0)
	finder(c.Size-7, 0)
	finder(0, c.Size-7)

	// Timing patterns: the alternating line that tells a reader how wide a
	// module is.
	for i := 8; i < c.Size-8; i++ {
		mark(i, 6, i%2 == 0)
		mark(6, i, i%2 == 0)
	}

	// Alignment patterns, except where they would sit on a finder.
	centres := alignment[c.Version]
	for _, cy := range centres {
		for _, cx := range centres {
			onFinder := (cx <= 8 && cy <= 8) ||
				(cx >= c.Size-9 && cy <= 8) ||
				(cx <= 8 && cy >= c.Size-9)
			if onFinder {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					dark := dx == -2 || dx == 2 || dy == -2 || dy == 2 ||
						(dx == 0 && dy == 0)
					mark(cx+dx, cy+dy, dark)
				}
			}
		}
	}

	// The dark module. Always dark, always here, and the standard gives no
	// reason beyond "so".
	mark(8, c.Size-8, true)

	// The format information areas, filled in later once the mask is chosen.
	for i := 0; i <= 8; i++ {
		if i != 6 {
			reserved[6*c.Size+8] = true // placeholder, overwritten below
			mark(8, i, false)
			mark(i, 8, false)
		}
	}
	for i := range 8 {
		mark(c.Size-1-i, 8, false)
		mark(8, c.Size-1-i, false)
	}

	// Version information, from version 7 on: two 3×6 blocks that say which
	// version this is, so a reader need not measure.
	if c.Version >= 7 {
		v := versionBits(c.Version)
		for i := range 18 {
			dark := v&(1<<uint(i)) != 0
			x, y := i/3, c.Size-11+i%3
			mark(x, y, dark)
			mark(y, x, dark)
		}
	}
	return reserved
}

// place walks the data path: two columns at a time, from the bottom right,
// upwards then downwards, skipping the column that holds the vertical timing
// pattern.
func (c *Code) place(codewords []byte, reserved []bool) {
	bit := 0
	up := true
	for right := c.Size - 1; right > 0; right -= 2 {
		if right == 6 {
			// Column 6 is the timing pattern; the path steps over it entirely.
			right--
		}
		for i := range c.Size {
			y := i
			if up {
				y = c.Size - 1 - i
			}
			for dx := range 2 {
				x := right - dx
				if reserved[y*c.Size+x] {
					continue
				}
				dark := false
				if bit < 8*len(codewords) {
					dark = codewords[bit/8]&(1<<uint(7-bit%8)) != 0
				}
				c.set(x, y, dark)
				bit++
			}
		}
		up = !up
	}
}

// applyMask flips data modules according to one of the eight patterns. The
// point is to break up large blank areas, which readers cope with badly.
func applyMask(m []bool, reserved []bool, size, mask int) {
	for y := range size {
		for x := range size {
			if reserved[y*size+x] {
				continue
			}
			var flip bool
			switch mask {
			case 0:
				flip = (y+x)%2 == 0
			case 1:
				flip = y%2 == 0
			case 2:
				flip = x%3 == 0
			case 3:
				flip = (y+x)%3 == 0
			case 4:
				flip = (y/2+x/3)%2 == 0
			case 5:
				flip = (y*x)%2+(y*x)%3 == 0
			case 6:
				flip = ((y*x)%2+(y*x)%3)%2 == 0
			case 7:
				flip = ((y+x)%2+(y*x)%3)%2 == 0
			}
			if flip {
				m[y*size+x] = !m[y*size+x]
			}
		}
	}
}

// formatInfo writes the fifteen bits that name the error correction level and
// the mask - twice, in two places, because losing them loses everything.
func (c *Code) formatInfo(mask int) {
	const levelM = 0b00
	data := levelM<<3 | mask
	bits := data << 10
	for i := 14; i >= 10; i-- {
		if bits&(1<<uint(i)) != 0 {
			bits ^= 0b10100110111 << uint(i-10)
		}
	}
	bits = (data<<10 | bits) ^ 0b101010000010010

	for i := range 15 {
		dark := bits&(1<<uint(i)) != 0
		// First copy, around the top left finder.
		switch {
		case i < 6:
			c.set(8, i, dark)
		case i == 6:
			c.set(8, 7, dark)
		case i == 7:
			c.set(8, 8, dark)
		case i == 8:
			c.set(7, 8, dark)
		default:
			c.set(14-i, 8, dark)
		}
		// Second copy, split between the other two finders.
		if i < 8 {
			c.set(c.Size-1-i, 8, dark)
		} else {
			c.set(8, c.Size-15+i, dark)
		}
	}
	c.set(8, c.Size-8, true) // the dark module, again
}

// versionBits is the eighteen-bit BCH code that names the version.
func versionBits(version int) int {
	bits := version << 12
	for i := 17; i >= 12; i-- {
		if bits&(1<<uint(i)) != 0 {
			bits ^= 0b1111100100101 << uint(i-12)
		}
	}
	return version<<12 | bits
}

// penalty scores a masked symbol by the four rules in the standard. Lower is
// better; the encoder tries all eight masks and keeps the best.
func penalty(c *Code) int {
	size, score := c.Size, 0

	// Rule 1: runs of five or more of the same colour, in both directions.
	for _, byRow := range []bool{true, false} {
		for a := range size {
			run, last := 0, false
			for b := range size {
				var v bool
				if byRow {
					v = c.At(b, a)
				} else {
					v = c.At(a, b)
				}
				if b > 0 && v == last {
					run++
				} else {
					if run >= 5 {
						score += run - 2
					}
					run = 1
				}
				last = v
			}
			if run >= 5 {
				score += run - 2
			}
		}
	}

	// Rule 2: every 2×2 block of one colour.
	for y := range size - 1 {
		for x := range size - 1 {
			v := c.At(x, y)
			if c.At(x+1, y) == v && c.At(x, y+1) == v && c.At(x+1, y+1) == v {
				score += 3
			}
		}
	}

	// Rule 3: the finder-like sequence 1011101 with four light modules on
	// either side - the pattern a reader could mistake for a finder.
	want := []bool{true, false, true, true, true, false, true}
	light4 := []bool{false, false, false, false}
	matches := func(get func(int) bool, at, n int) bool {
		for i, w := range want {
			if at+i >= n || get(at+i) != w {
				return false
			}
		}
		return true
	}
	for _, byRow := range []bool{true, false} {
		for a := range size {
			get := func(b int) bool {
				if byRow {
					return c.At(b, a)
				}
				return c.At(a, b)
			}
			for b := range size {
				if !matches(get, b, size) {
					continue
				}
				before, after := true, true
				for i, w := range light4 {
					if b-4+i >= 0 && get(b-4+i) != w {
						before = false
					}
					if b+7+i < size && get(b+7+i) != w {
						after = false
					}
				}
				if before || after {
					score += 40
				}
			}
		}
	}

	// Rule 4: how far the proportion of dark modules is from a half.
	dark := 0
	for _, v := range c.m {
		if v {
			dark++
		}
	}
	percent := dark * 100 / (size * size)
	deviation := percent - 50
	if deviation < 0 {
		deviation = -deviation
	}
	score += deviation / 5 * 10
	return score
}
