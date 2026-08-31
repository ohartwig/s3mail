// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package qr

// Reed-Solomon over GF(256), the field QR codes use: bytes, with the primitive
// polynomial x^8 + x^4 + x^3 + x^2 + 1 (0x11D).
//
// Multiplication is done through logarithms, as everywhere in this corner of
// mathematics: in a finite field every non-zero element is a power of the
// generator, so a product is a sum of exponents.

var (
	expTable [512]byte // doubled, so an exponent sum needs no modulo
	logTable [256]byte
)

func init() {
	x := 1
	for i := range 255 {
		expTable[i] = byte(x)
		logTable[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D
		}
	}
	for i := 255; i < 512; i++ {
		expTable[i] = expTable[i-255]
	}
}

func mul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return expTable[int(logTable[a])+int(logTable[b])]
}

// generator builds the polynomial whose roots are the first n powers of 2 -
// the divisor the message is divided by.
func generator(n int) []byte {
	g := []byte{1}
	for i := range n {
		// Multiply by (x - alpha^i), which in this field is (x + alpha^i).
		next := make([]byte, len(g)+1)
		for j, c := range g {
			next[j] ^= c
			next[j+1] ^= mul(c, expTable[i])
		}
		g = next
	}
	return g
}

// reedSolomon returns the n correction codewords for data: the remainder of
// dividing the message polynomial by the generator.
func reedSolomon(data []byte, n int) []byte {
	g := generator(n)
	rest := make([]byte, len(data)+n)
	copy(rest, data)

	for i := range len(data) {
		lead := rest[i]
		if lead == 0 {
			continue
		}
		for j, c := range g {
			rest[i+j] ^= mul(c, lead)
		}
	}
	return rest[len(data):]
}
