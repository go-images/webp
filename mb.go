// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

// The macroblock loop: for each one, predict, transform, quantise, write the
// coefficients, and reconstruct the pixels the next macroblock will be
// predicted from.

// quantiser holds the factors one frame uses, as the decoder derives them.
type quantiser struct {
	y1, y2, uv [2]uint16 // DC, AC
}

// quantiserFor builds the factors from the frame's index, following RFC 6386
// section 9.6 — including the two adjustments to the second-order block and
// the chrominance DC ceiling that the reference decoder calls out.
func quantiserFor(q uint32) quantiser {
	i := int(q)
	var out quantiser
	out.y1[0] = dequantTableDC[clipIndex(i, 127)]
	out.y1[1] = dequantTableAC[clipIndex(i, 127)]
	out.y2[0] = dequantTableDC[clipIndex(i, 127)] * 2
	out.y2[1] = dequantTableAC[clipIndex(i, 127)] * 155 / 100
	if out.y2[1] < 8 {
		out.y2[1] = 8
	}
	// ⛔ 117, NOT 127, and it is not a typo: the specification clamps the
	// chrominance DC factor at 132, which is dequantTableDC[117].
	out.uv[0] = dequantTableDC[clipIndex(i, 117)]
	out.uv[1] = dequantTableAC[clipIndex(i, 127)]
	return out
}

// clipIndex caps an index into the dequantisation tables.
//
// There is no lower bound: the index comes from qIndexFor, which maps a
// quality of 0..100 onto 0..127, so it is never negative. A guard for that
// would be a branch nothing can reach.
func clipIndex(i, hi int) int {
	if i > hi {
		return hi
	}
	return i
}

// nzState is what the next macroblock needs to know about this one: which of
// its blocks carried a coefficient, which decides the probability context.
type nzState struct {
	y  [4]uint8 // one per column (above) or row (left) of luma blocks
	u  [2]uint8
	v  [2]uint8
	y2 uint8
}

type frameEncoder struct {
	src, rec *planes
	mbw, mbh int
	first    *boolEncoder
	second   *boolEncoder
	quant    quantiser

	leftNZ []nzState // one entry, reset at the start of each row
	upNZ   []nzState // one per macroblock column
}

func (f *frameEncoder) encode() {
	for mby := 0; mby < f.mbh; mby++ {
		f.leftNZ[0] = nzState{}
		for mbx := 0; mbx < f.mbw; mbx++ {
			f.macroblock(mbx, mby)
		}
	}
}

// macroblock codes one macroblock whole: the modes into the first partition,
// the coefficients into the second.
//
// ⛔ THE MODES AND THE COEFFICIENTS GO INTO DIFFERENT PARTITIONS, and both are
// written in macroblock order. The decoder reads them from two independent
// arithmetic decoders, so the two streams must stay in step with each other
// even though neither can see the other.
func (f *frameEncoder) macroblock(mbx, mby int) {
	hasTop, hasLeft := mby > 0, mbx > 0

	// The prediction for the whole macroblock: one mode for the luma and one
	// shared by both chrominance planes, which is what the format allows.
	yMode := chooseMode(f.src.y, f.rec.y, mbx*16, mby*16, 16, hasTop, hasLeft)
	uvMode := chooseChromaMode(f.src, f.rec, mbx, mby, hasTop, hasLeft)

	yPred := predictInto(f.rec.y, mbx*16, mby*16, 16, yMode, hasTop, hasLeft)
	uPred := predictInto(f.rec.u, mbx*8, mby*8, 8, uvMode, hasTop, hasLeft)
	vPred := predictInto(f.rec.v, mbx*8, mby*8, 8, uvMode, hasTop, hasLeft)

	// Transform and quantise every luma block, keeping the DC values aside for
	// the second-order transform.
	var lumaLevels [16][16]int32
	var lumaRecon [16][16]int32
	var dcs [16]int32
	for b := 0; b < 16; b++ {
		bx, by := mbx*16+(b%4)*4, mby*16+(b/4)*4
		src := block4(f.src.y, bx, by)
		pred := sub4(yPred, 16, (b%4)*4, (b/4)*4)
		res := residual4(&src, &pred)
		coeff := forwardDCT(&res)
		dcs[b] = coeff[0]
		// The DC of each block travels in the second-order block, so it is
		// taken out of this one.
		coeff[0] = 0
		levels, recon := quantise(&coeff, f.quant.y1[0], f.quant.y1[1])
		lumaLevels[b] = levels
		lumaRecon[b] = recon
	}

	// The second-order block carries the sixteen DC values.
	y2 := forwardWHT(&dcs)
	y2Levels, y2Recon := quantise(&y2, f.quant.y2[0], f.quant.y2[1])
	dcRecon := inverseWHT(&y2Recon)

	// Chrominance.
	var chromaLevels [8][16]int32
	var chromaRecon [8][16]int32
	for b := 0; b < 8; b++ {
		p, cpred := f.src.u, uPred
		base := b
		if b >= 4 {
			p, cpred, base = f.src.v, vPred, b-4
		}
		bx, by := mbx*8+(base%2)*4, mby*8+(base/2)*4
		src := block4(p, bx, by)
		pred := sub4(cpred, 8, (base%2)*4, (base/2)*4)
		res := residual4(&src, &pred)
		coeff := forwardDCT(&res)
		levels, recon := quantise(&coeff, f.quant.uv[0], f.quant.uv[1])
		chromaLevels[b] = levels
		chromaRecon[b] = recon
	}

	// Does this macroblock carry anything at all?
	nonEmpty := anyNonZero(y2Levels[:])
	for b := range lumaLevels {
		nonEmpty = nonEmpty || anyNonZero(lumaLevels[b][1:])
	}
	for b := range chromaLevels {
		nonEmpty = nonEmpty || anyNonZero(chromaLevels[b][:])
	}

	// The first partition: the skip flag, then the modes.
	f.first.writeBool(skipProb, !nonEmpty)
	f.first.writeBool(145, true) // a whole-macroblock luma mode
	writeY16Mode(f.first, yMode)
	writeUVMode(f.first, uvMode)

	if nonEmpty {
		f.writeCoefficients(mbx, &y2Levels, &lumaLevels, &chromaLevels)
	} else {
		// ⛔ A SKIPPED MACROBLOCK RESETS EVERY CONTEXT TO ZERO, THE
		// SECOND-ORDER ONE INCLUDED. No block is coded here, so the next
		// macroblock must see "nothing non-zero" above and to the left.
		//
		// Keeping the Y2 context — on the reasoning that the macroblock still
		// has a second-order block conceptually — is wrong, and wrong in the
		// worst way: the two sides stay in step for a while and then diverge,
		// so most images and most quality settings look fine. It showed up as
		// a gradient at quality 75 decoding at 17.3 dB while the same picture
		// at 50 and at 80 came back above 31.
		f.leftNZ[0] = nzState{}
		f.upNZ[mbx] = nzState{}
		// Nothing is coded, so the reconstruction is the prediction.
		for i := range lumaRecon {
			lumaRecon[i] = [16]int32{}
		}
		for i := range chromaRecon {
			chromaRecon[i] = [16]int32{}
		}
		dcRecon = [16]int32{}
	}

	// Reconstruct, so that the next macroblock predicts from what the decoder
	// will have.
	for b := 0; b < 16; b++ {
		bx, by := mbx*16+(b%4)*4, mby*16+(b/4)*4
		coeff := lumaRecon[b]
		coeff[0] = dcRecon[b]
		pred := sub4(yPred, 16, (b%4)*4, (b/4)*4)
		out := inverseDCT(&coeff, &pred)
		writeBlock4(f.rec.y, bx, by, &out)
	}
	for b := 0; b < 8; b++ {
		p, cpred := f.rec.u, uPred
		base := b
		if b >= 4 {
			p, cpred, base = f.rec.v, vPred, b-4
		}
		bx, by := mbx*8+(base%2)*4, mby*8+(base/2)*4
		coeff := chromaRecon[b]
		pred := sub4(cpred, 8, (base%2)*4, (base/2)*4)
		out := inverseDCT(&coeff, &pred)
		writeBlock4(p, bx, by, &out)
	}
}

// chooseChromaMode picks one mode for both chrominance planes, since the
// format carries only one: the cost is their sum.
func chooseChromaMode(src, rec *planes, mbx, mby int, hasTop, hasLeft bool) int {
	best, bestCost := predDC, int(^uint(0)>>1)
	for _, mode := range []int{predDC, predVE, predHE, predTM} {
		switch mode {
		case predVE:
			if !hasTop {
				continue
			}
		case predHE:
			if !hasLeft {
				continue
			}
		case predTM:
			if !hasTop || !hasLeft {
				continue
			}
		}
		cost := planeCost(src.u, rec.u, mbx*8, mby*8, mode, hasTop, hasLeft) +
			planeCost(src.v, rec.v, mbx*8, mby*8, mode, hasTop, hasLeft)
		if cost < bestCost {
			best, bestCost = mode, cost
		}
	}
	return best
}

func planeCost(src, rec *plane, x, y, mode int, hasTop, hasLeft bool) int {
	pred := predictInto(rec, x, y, 8, mode, hasTop, hasLeft)
	cost := 0
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			d := int(src.at(x+i, y+j)) - int(pred[j*8+i])
			if d < 0 {
				d = -d
			}
			cost += d
		}
	}
	return cost
}

// writeCoefficients writes one macroblock's blocks into the second partition,
// in the order the decoder reads them and with the contexts it expects.
func (f *frameEncoder) writeCoefficients(mbx int, y2 *[16]int32, luma *[16][16]int32, chroma *[8][16]int32) {
	left, up := &f.leftNZ[0], &f.upNZ[mbx]

	nz := writeResiduals4(f.second, planeY2, left.y2+up.y2, y2, false)
	left.y2, up.y2 = nz, nz

	// The luma blocks, in raster order inside the macroblock. The context is
	// the block to the left and the block above, inside this macroblock where
	// there is one and from the neighbour where there is not.
	for y := 0; y < 4; y++ {
		n := left.y[y]
		for x := 0; x < 4; x++ {
			b := y*4 + x
			n = writeResiduals4(f.second, planeY1WithY2, n+up.y[x], &luma[b], true)
			up.y[x] = n
		}
		left.y[y] = n
	}

	// The chrominance blocks: U then V, each a 2x2 group.
	for c := 0; c < 2; c++ {
		lnz, unz := &left.u, &up.u
		if c == 1 {
			lnz, unz = &left.v, &up.v
		}
		for y := 0; y < 2; y++ {
			n := lnz[y]
			for x := 0; x < 2; x++ {
				b := c*4 + y*2 + x
				n = writeResiduals4(f.second, planeUV, n+unz[x], &chroma[b], false)
				unz[x] = n
			}
			lnz[y] = n
		}
	}
}

func anyNonZero(v []int32) bool {
	for _, x := range v {
		if x != 0 {
			return true
		}
	}
	return false
}
