// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package qr

// The tables from ISO/IEC 18004. They are data, not logic - kept in their own
// file so qr.go stays readable.
//
// Only error correction level M, and only versions 1 to 20. Both are choices:
//
//   - M recovers about 15% of a damaged code. L would make the image smaller
//     and a fingerprint on a phone screen more likely to break it; Q and H buy
//     robustness this code does not need, because it is read off a monitor
//     thirty centimetres away and not off a parcel.
//   - Version 20 holds 1085 codewords, far more than the setup payload will
//     ever be. Carrying all forty versions would mean twenty more rows nobody
//     can check by reading them.

// block describes how a version's codewords are split. Two groups, because
// from version 8 on the blocks are not all the same size.
type block struct {
	ec int // error correction codewords per block
	n1 int // blocks in group 1
	d1 int // data codewords in each
	n2 int // blocks in group 2, often zero
	d2 int
}

// blocks is indexed by version, so index 0 is unused.
var blocks = [21]block{
	{},
	{10, 1, 16, 0, 0},
	{16, 1, 28, 0, 0},
	{26, 1, 44, 0, 0},
	{18, 2, 32, 0, 0},
	{24, 2, 43, 0, 0},
	{16, 4, 27, 0, 0},
	{18, 4, 31, 0, 0},
	{22, 2, 38, 2, 39},
	{22, 3, 36, 2, 37},
	{26, 4, 43, 1, 44},
	{30, 1, 50, 4, 51},
	{22, 6, 36, 2, 37},
	{22, 8, 37, 1, 38},
	{24, 4, 40, 5, 41},
	{24, 5, 41, 5, 42},
	{28, 7, 45, 3, 46},
	{28, 10, 46, 1, 47},
	{26, 9, 43, 4, 44},
	{26, 3, 44, 11, 45},
	{26, 3, 41, 13, 42},
}

// data is how many data codewords a version holds at level M.
func (b block) data() int { return b.n1*b.d1 + b.n2*b.d2 }

// total is every codeword, data and correction together. Derived rather than
// tabulated: a second table would be a second thing to get wrong, and this one
// cannot disagree with the first.
func (b block) total() int { return b.n1*(b.d1+b.ec) + b.n2*(b.d2+b.ec) }

// alignment holds the centre coordinates of the alignment patterns per version.
// Version 1 has none.
var alignment = [21][]int{
	{},
	{},
	{6, 18},
	{6, 22},
	{6, 26},
	{6, 30},
	{6, 34},
	{6, 22, 38},
	{6, 24, 42},
	{6, 26, 46},
	{6, 28, 50},
	{6, 30, 54},
	{6, 32, 58},
	{6, 34, 62},
	{6, 26, 46, 66},
	{6, 26, 48, 70},
	{6, 26, 50, 74},
	{6, 30, 54, 78},
	{6, 30, 56, 82},
	{6, 30, 58, 86},
	{6, 34, 62, 90},
}
