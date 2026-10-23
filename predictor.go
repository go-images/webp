// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

// The predictor transform replaces each pixel by its difference from a
// prediction made out of its neighbours, which is what PNG's filters do and
// what this encoder was missing.
//
// ⛔ WITHOUT IT A SMOOTH IMAGE IS CATASTROPHIC: a 256x256 gradient came out at
// 194 666 bytes against PNG's 1 185 — 164 times larger — because every pixel
// differs from the last and nothing matches anything. With it the same image
// is a field of near-zero residuals that the prefix codes flatten. Measured,
// not assumed; the figures are in the README.
//
// The pixels are predicted in a fixed order, so the ENCODER must predict from
// the ORIGINAL neighbours — the decoder reconstructs them exactly before it
// needs them — and store value minus prediction, modulo 256.

// predictorBits is the log2 of the block side. Each block of 2^bits pixels
// square chooses one predictor, and the choices are themselves coded as a
// small image. Four (16x16 blocks) is a middle setting: finer blocks adapt
// better and cost more sub-image pixels.
const predictorBits = 4

// nTiles is how many blocks cover a span, rounded up.
func nTiles(size, bits int) int { return (size + (1 << bits) - 1) >> bits }

// predictorModes is how many predictors the format defines.
const predictorModes = 14

// applyPredictor rewrites argb in place as residuals and returns the per-block
// predictor choices, as the green channel of a sub-image.
func applyPredictor(argb []uint32, width, height int) []uint32 {
	tilesX := nTiles(width, predictorBits)
	tilesY := nTiles(height, predictorBits)
	modes := make([]uint32, tilesX*tilesY)

	// Each block's predictor is chosen by trying all fourteen and keeping the
	// one whose residuals are cheapest to code. The cost used is the sum of
	// the absolute residuals read as signed bytes, which is a stand-in for
	// entropy that costs one pass instead of fourteen histograms.
	for ty := 0; ty < tilesY; ty++ {
		for tx := 0; tx < tilesX; tx++ {
			best, bestCost := 0, int(^uint(0)>>1)
			for mode := 0; mode < predictorModes; mode++ {
				c := blockCost(argb, width, height, tx, ty, mode)
				if c < bestCost {
					best, bestCost = mode, c
				}
			}
			modes[ty*tilesX+tx] = uint32(best)
		}
	}

	// ⛔ The residuals are written into a COPY-FREE second pass over a
	// snapshot: a pixel's prediction uses its neighbours' ORIGINAL values, and
	// overwriting as we go would predict from residuals instead.
	src := make([]uint32, len(argb))
	copy(src, argb)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			i := y*width + x
			mode := 0
			if y > 0 && x > 0 {
				mode = int(modes[(y>>predictorBits)*tilesX+(x>>predictorBits)])
			}
			argb[i] = subPixels(src[i], predict(src, width, x, y, mode))
		}
	}

	// The sub-image carries the mode in its green channel, opaque elsewhere.
	out := make([]uint32, len(modes))
	for i, m := range modes {
		out[i] = 0xff000000 | m<<8
	}
	return out
}

// predict is the prediction for one pixel.
//
// ⛔ The three borders are NOT the block's choice. The top-left pixel is
// opaque black, the rest of the first row is L and the rest of the first
// column is T, whatever predictor the block names — the decoder applies those
// unconditionally, so an encoder that honoured the block's mode there would
// disagree with it on the first row of every image.
func predict(src []uint32, width, x, y, mode int) uint32 {
	i := y*width + x
	switch {
	case x == 0 && y == 0:
		return 0xff000000
	case y == 0:
		return src[i-1] // L
	case x == 0:
		return src[i-width] // T
	}
	l := src[i-1]
	t := src[i-width]
	tl := src[i-width-1]
	// For the rightmost column the pixel after T is the first of this row,
	// which is what the decoder reads from the same linear buffer.
	tr := src[i-width+1]

	// Mode 0 is the default arm rather than a case of its own: the modes are
	// 0..13 and nothing else can arrive, so a fallback after the switch would
	// be a line no test can reach, reading like a safeguard.
	switch mode {
	case 1:
		return l
	case 2:
		return t
	case 3:
		return tr
	case 4:
		return tl
	case 5:
		return avgPixels(avgPixels(l, tr), t)
	case 6:
		return avgPixels(l, tl)
	case 7:
		return avgPixels(l, t)
	case 8:
		return avgPixels(tl, t)
	case 9:
		return avgPixels(t, tr)
	case 10:
		return avgPixels(avgPixels(l, tl), avgPixels(t, tr))
	case 11:
		return selectPixel(l, t, tl)
	case 12:
		return clampAddSubtractFullPixels(l, t, tl)
	case 13:
		return clampAddSubtractHalfPixels(avgPixels(l, t), tl)
	}
	return 0xff000000 // mode 0: opaque black
}

// blockCost is how expensive one block's residuals are under one predictor.
func blockCost(argb []uint32, width, height, tx, ty, mode int) int {
	x0, y0 := tx<<predictorBits, ty<<predictorBits
	x1 := min(x0+1<<predictorBits, width)
	y1 := min(y0+1<<predictorBits, height)
	cost := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if y == 0 || x == 0 {
				continue // the borders do not use the block's predictor
			}
			r := subPixels(argb[y*width+x], predict(argb, width, x, y, mode))
			cost += residualCost(r)
		}
	}
	return cost
}

// residualCost reads each byte as a signed difference and sums the magnitudes:
// a residual near zero or near 255 is a small difference either way.
func residualCost(v uint32) int {
	c := 0
	for s := 0; s < 32; s += 8 {
		b := int((v >> s) & 0xff)
		if b > 128 {
			b = 256 - b
		}
		c += b
	}
	return c
}

// subPixels is a minus b, CHANNEL BY CHANNEL, each modulo 256.
//
// ⛔ NOT a single 32-bit subtraction. That lets a borrow out of one channel
// corrupt the next — blue borrowing from green — and the damage is invisible
// on any image whose channels never underflow, which includes every test that
// uses an opaque white background.
func subPixels(a, b uint32) uint32 {
	var out uint32
	for s := 0; s < 32; s += 8 {
		av := (a >> s) & 0xff
		bv := (b >> s) & 0xff
		out |= ((av - bv) & 0xff) << s
	}
	return out
}

func avgPixels(a, b uint32) uint32 {
	var out uint32
	for s := 0; s < 32; s += 8 {
		av := (a >> s) & 0xff
		bv := (b >> s) & 0xff
		out |= ((av + bv) / 2) << s
	}
	return out
}

func selectPixel(l, t, tl uint32) uint32 {
	var dl, dt int
	for s := 0; s < 32; s += 8 {
		lv := int((l >> s) & 0xff)
		tv := int((t >> s) & 0xff)
		cv := int((tl >> s) & 0xff)
		dl += absInt(cv - tv)
		dt += absInt(cv - lv)
	}
	if dl < dt {
		return l
	}
	return t
}

func clampAddSubtractFullPixels(a, b, c uint32) uint32 {
	var out uint32
	for s := 0; s < 32; s += 8 {
		x := int((a>>s)&0xff) + int((b>>s)&0xff) - int((c>>s)&0xff)
		out |= uint32(clamp255(x)) << s
	}
	return out
}

func clampAddSubtractHalfPixels(a, b uint32) uint32 {
	var out uint32
	for s := 0; s < 32; s += 8 {
		av := int((a >> s) & 0xff)
		bv := int((b >> s) & 0xff)
		out |= uint32(clamp255(av+(av-bv)/2)) << s
	}
	return out
}

func clamp255(x int) int {
	if x < 0 {
		return 0
	}
	if x > 255 {
		return 255
	}
	return x
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
