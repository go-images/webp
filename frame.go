// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

import "image"

// A plane is one of the three components, padded out to whole macroblocks.
type plane struct {
	pix    []uint8
	stride int
	w, h   int
}

func newPlane(w, h int) *plane {
	return &plane{pix: make([]uint8, w*h), stride: w, w: w, h: h}
}

func (p *plane) at(x, y int) uint8     { return p.pix[y*p.stride+x] }
func (p *plane) set(x, y int, v uint8) { p.pix[y*p.stride+x] = v }

// planes holds the picture as VP8 codes it: full-resolution luma and
// half-resolution chrominance.
type planes struct {
	y, u, v *plane
}

// toYCbCr converts to the colour space VP8 codes in, padding the edges by
// repeating the last pixel so that a partial macroblock has something
// plausible in it rather than black.
//
// ⛔ THE PADDING IS NOT COSMETIC. A 17-pixel-wide image is two macroblocks
// wide, and the fifteen columns past the edge are still transformed and
// coded. Filling them with black puts a hard edge inside the last macroblock,
// which the transform turns into large coefficients — so the invisible part
// of the picture costs more bits than the visible part.
func toYCbCr(src *image.NRGBA, width, height, mbw, mbh int) *planes {
	yw, yh := mbw*16, mbh*16
	cw, ch := mbw*8, mbh*8
	out := &planes{y: newPlane(yw, yh), u: newPlane(cw, ch), v: newPlane(cw, ch)}

	for j := 0; j < yh; j++ {
		sy := min(j, height-1)
		for i := 0; i < yw; i++ {
			sx := min(i, width-1)
			o := sy*src.Stride + sx*4
			yy, _, _ := rgbToYCbCr(src.Pix[o], src.Pix[o+1], src.Pix[o+2])
			out.y.set(i, j, yy)
		}
	}
	// Chrominance is the average of each 2x2 group, which is what 4:2:0 means.
	for j := 0; j < ch; j++ {
		for i := 0; i < cw; i++ {
			var su, sv int
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					sy := min(j*2+dy, height-1)
					sx := min(i*2+dx, width-1)
					o := sy*src.Stride + sx*4
					_, cb, cr := rgbToYCbCr(src.Pix[o], src.Pix[o+1], src.Pix[o+2])
					su += int(cb)
					sv += int(cr)
				}
			}
			out.u.set(i, j, uint8((su+2)/4))
			out.v.set(i, j, uint8((sv+2)/4))
		}
	}
	return out
}

// rgbToYCbCr is the BT.601 conversion in the fixed-point form the decoder's
// image.YCbCr inverse expects.
func rgbToYCbCr(r, g, b uint8) (uint8, uint8, uint8) {
	r1, g1, b1 := int32(r), int32(g), int32(b)
	yy := (19595*r1 + 38470*g1 + 7471*b1 + 1<<15) >> 16
	cb := (-11056*r1 - 21712*g1 + 32768*b1 + 257<<15) >> 16
	cr := (32768*r1 - 27440*g1 - 5328*b1 + 257<<15) >> 16
	return clip8(yy), clip8(cb), clip8(cr)
}

// predictDC fills a square block with the DC prediction, exactly as the
// decoder computes it — including the four cases at the edges of the picture,
// where one or both of the neighbouring edges does not exist.
//
// ⛔ 0x80 WHEN NEITHER EDGE EXISTS, and the rounding differs between the three
// other cases: with both edges the sum starts at size and is divided by 2*size,
// with one it starts at size/2 and is divided by size. Getting the rounding
// wrong shifts the whole first row or column of macroblocks by a level, which
// then propagates into everything predicted from them.
func predictDC(p *plane, x, y, size int, hasTop, hasLeft bool) uint8 {
	switch {
	case hasTop && hasLeft:
		sum := uint32(size)
		for i := 0; i < size; i++ {
			sum += uint32(p.at(x+i, y-1))
			sum += uint32(p.at(x-1, y+i))
		}
		return uint8(sum / uint32(2*size))
	case hasTop:
		sum := uint32(size / 2)
		for i := 0; i < size; i++ {
			sum += uint32(p.at(x+i, y-1))
		}
		return uint8(sum / uint32(size))
	case hasLeft:
		sum := uint32(size / 2)
		for i := 0; i < size; i++ {
			sum += uint32(p.at(x-1, y+i))
		}
		return uint8(sum / uint32(size))
	default:
		return 0x80
	}
}

// predictInto fills a size x size prediction for one of the four whole-block
// modes, exactly as the decoder builds it.
//
// ⛔ A MODE WHOSE EDGE DOES NOT EXIST IS NOT AVAILABLE. Vertical prediction on
// the top row of the picture would read the row above it, which is outside the
// frame: the decoder substitutes a different predictor there, so an encoder
// that offered the mode would be predicting from pixels the decoder does not
// use. chooseMode only considers the modes whose edges are present.
func predictInto(p *plane, x, y, size, mode int, hasTop, hasLeft bool) []uint8 {
	out := make([]uint8, size*size)
	switch mode {
	case predVE:
		for j := 0; j < size; j++ {
			for i := 0; i < size; i++ {
				out[j*size+i] = p.at(x+i, y-1)
			}
		}
	case predHE:
		for j := 0; j < size; j++ {
			for i := 0; i < size; i++ {
				out[j*size+i] = p.at(x-1, y+j)
			}
		}
	case predTM:
		corner := int32(p.at(x-1, y-1))
		for j := 0; j < size; j++ {
			l := int32(p.at(x-1, y+j)) - corner
			for i := 0; i < size; i++ {
				out[j*size+i] = clip8(l + int32(p.at(x+i, y-1)))
			}
		}
	default: // predDC
		dc := predictDC(p, x, y, size, hasTop, hasLeft)
		for i := range out {
			out[i] = dc
		}
	}
	return out
}

// chooseMode picks the whole-block prediction whose residual is smallest.
//
// The measure is the sum of absolute differences, which is what every encoder
// uses for this decision: it costs one pass and ranks the modes the same way a
// full trial encode would, nearly always.
func chooseMode(src, rec *plane, x, y, size int, hasTop, hasLeft bool) int {
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
		pred := predictInto(rec, x, y, size, mode, hasTop, hasLeft)
		cost := 0
		for j := 0; j < size; j++ {
			for i := 0; i < size; i++ {
				d := int(src.at(x+i, y+j)) - int(pred[j*size+i])
				if d < 0 {
					d = -d
				}
				cost += d
			}
		}
		if cost < bestCost {
			best, bestCost = mode, cost
		}
	}
	return best
}

// sub4 is the 4x4 piece of a size x size prediction at (ox, oy) within it.
func sub4(pred []uint8, size, ox, oy int) [16]uint8 {
	var out [16]uint8
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			out[j*4+i] = pred[(oy+j)*size+ox+i]
		}
	}
	return out
}

// block4 reads a 4x4 block out of a plane.
func block4(p *plane, x, y int) [16]uint8 {
	var out [16]uint8
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			out[j*4+i] = p.at(x+i, y+j)
		}
	}
	return out
}

// writeBlock4 puts a reconstructed 4x4 block back, which is what the next
// macroblock will be predicted from.
func writeBlock4(p *plane, x, y int, b *[16]uint8) {
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			p.set(x+i, y+j, b[j*4+i])
		}
	}
}

// residual4 is the difference between a block and its prediction.
func residual4(src *[16]uint8, pred *[16]uint8) [16]int32 {
	var out [16]int32
	for i := range out {
		out[i] = int32(src[i]) - int32(pred[i])
	}
	return out
}
