// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

// Coefficient coding, RFC 6386 section 13. This is the exact inverse of the
// decoder's reader, written alongside it rather than from the prose.
//
// ⛔ THE PROBABILITY CONTEXT MOVES AS THE BLOCK IS WRITTEN. After a zero the
// next coefficient is read at context 0, after a one at context 1, and after
// anything larger at context 2 — and the BAND changes with the position too.
// An encoder that writes every coefficient at the same probabilities produces
// a stream the decoder walks differently from the first large value onward:
// the picture is right for a while and then becomes noise.

// The mapping from coefficient position to band, section 13.3.
var bands = [17]uint8{0, 1, 2, 3, 6, 4, 5, 6, 6, 6, 6, 6, 6, 6, 6, 7, 0}

// The zigzag order, section 13.3:
//
//	0  1  5  6
//	2  4  7 12
//	3  8 11 13
//	9 10 14 15
var zigzag = [16]uint8{0, 1, 4, 8, 5, 2, 3, 6, 9, 12, 13, 10, 7, 11, 14, 15}

// Category probabilities, section 13.2.
var cat3456 = [4][12]uint8{
	{173, 148, 140, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	{176, 155, 140, 135, 0, 0, 0, 0, 0, 0, 0, 0},
	{180, 157, 141, 134, 130, 0, 0, 0, 0, 0, 0, 0},
	{254, 254, 243, 230, 196, 177, 153, 140, 133, 130, 129, 0},
}

// The four coefficient planes, in the order the probability table holds them.
const (
	planeY1WithY2 = iota
	planeY2
	planeUV
	planeY1SansY2
)

// writeResiduals4 writes one 4x4 block's quantised levels, in zigzag order,
// and reports whether any of them was non-zero.
//
// levels is indexed by RASTER position; the zigzag table says which one each
// coefficient position carries.
func writeResiduals4(e *boolEncoder, plane int, context uint8, levels *[16]int32, skipFirst bool) uint8 {
	prob := &defaultTokenProb[plane]
	n := 0
	if skipFirst {
		n = 1
	}

	// Where the last non-zero coefficient is, in coefficient positions.
	last := -1
	for i := n; i < 16; i++ {
		if levels[zigzag[i]] != 0 {
			last = i
		}
	}

	p := prob[bands[n]][context]
	if last < 0 {
		e.writeBool(p[0], false) // nothing coded in this block at all
		return 0
	}
	e.writeBool(p[0], true)

	// The loop always returns: either at position 16, or at the last non-zero
	// coefficient, and `last` is known to exist by now. There is deliberately
	// no return after it — that would be a line no test can reach.
	for {
		n++
		v := levels[zigzag[n-1]]
		if v == 0 {
			e.writeBool(p[1], false)
			p = prob[bands[n]][0]
			continue
		}
		e.writeBool(p[1], true)

		mag := v
		if mag < 0 {
			mag = -mag
		}
		if mag == 1 {
			e.writeBool(p[2], false)
			p = prob[bands[n]][1]
		} else {
			e.writeBool(p[2], true)
			writeMagnitude(e, p, uint32(mag))
			p = prob[bands[n]][2]
		}
		e.writeBool(128, v < 0) // the sign, at even odds

		if n == 16 {
			return 1
		}
		if n-1 == last {
			e.writeBool(p[0], false) // no more coefficients in this block
			return 1
		}
		e.writeBool(p[0], true)
	}
}

// writeMagnitude writes a level of two or more, inverting the decoder's tree.
func writeMagnitude(e *boolEncoder, p [11]uint8, mag uint32) {
	switch {
	case mag <= 4:
		e.writeBool(p[3], false)
		if mag == 2 {
			e.writeBool(p[4], false)
			return
		}
		e.writeBool(p[4], true)
		e.writeBool(p[5], mag == 4) // 3 or 4
	case mag <= 10:
		e.writeBool(p[3], true)
		e.writeBool(p[6], false)
		if mag <= 6 {
			e.writeBool(p[7], false)
			e.writeBool(159, mag == 6) // 5 or 6
			return
		}
		e.writeBool(p[7], true)
		d := mag - 7 // 0..3
		e.writeBool(165, d>>1 == 1)
		e.writeBool(145, d&1 == 1)
	default:
		e.writeBool(p[3], true)
		e.writeBool(p[6], true)
		// Which of the four big categories the value falls in, and the bits
		// that locate it inside that category.
		cat := 0
		for cat < 3 && mag >= 3+(8<<uint(cat+1)) {
			cat++
		}
		b1 := uint32(cat >> 1)
		b0 := uint32(cat & 1)
		e.writeBool(p[8], b1 == 1)
		e.writeBool(p[9+b1], b0 == 1)

		tab := &cat3456[cat]
		nbits := 0
		for nbits < 12 && tab[nbits] != 0 {
			nbits++
		}
		rest := mag - uint32(3+(8<<uint(cat)))
		for i := 0; i < nbits; i++ {
			bit := (rest >> uint(nbits-1-i)) & 1
			e.writeBool(tab[i], bit == 1)
		}
	}
}

// quantise turns coefficients into levels, and gives back what the decoder
// will reconstruct from them.
//
// ⛔ THE RECONSTRUCTION IS RETURNED, not recomputed later. The decoder's
// coefficient is level * quantiser, and the encoder has to predict the NEXT
// macroblock from pixels built out of exactly that — not out of the
// coefficients it started with.
func quantise(coeff *[16]int32, dc, ac uint16) (levels, recon [16]int32) {
	for i := 0; i < 16; i++ {
		q := int32(ac)
		if i == 0 {
			q = int32(dc)
		}
		v := coeff[i]
		var l int32
		if v >= 0 {
			l = (v + q/2) / q
		} else {
			l = -((-v + q/2) / q)
		}
		levels[i] = l
		recon[i] = l * q
	}
	return levels, recon
}
