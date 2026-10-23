// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

// Package webp writes WebP images in pure Go, with CGO_ENABLED=0.
//
// It encodes the lossless form (VP8L). What comes out is bit-for-bit the same
// pixels that went in, which is what makes it checkable: the test suite
// encodes an image, decodes it with a DIFFERENT implementation, and compares
// every pixel.
//
// ⛔ WHY IT EXISTS: the fleet could read WebP and could not write one, so
// every tool that wanted to produce WebP had nothing to call. go-avkit/vp8
// parses frame headers and does not encode.
package webp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"io"
)

// maxDimension is the largest side VP8L can describe: the header carries
// width-1 and height-1 in fourteen bits each.
const maxDimension = 1 << 14

// Errors returned by Encode.
var (
	ErrTooLarge = errors.New("webp: the image is larger than 16384 pixels on a side, which the format cannot describe")
	ErrEmpty    = errors.New("webp: the image has no pixels")
)

// Encode writes m to w as a lossless WebP image.
func Encode(w io.Writer, m image.Image) error {
	b, err := EncodeToBytes(m)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// EncodeToBytes returns m as a lossless WebP file.
func EncodeToBytes(m image.Image) ([]byte, error) {
	bounds := m.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, ErrEmpty
	}
	if width > maxDimension || height > maxDimension {
		return nil, fmt.Errorf("%w: %dx%d", ErrTooLarge, width, height)
	}

	pix := toNRGBA(m)

	// ⛔ BOTH WAYS ARE TRIED, because neither wins everywhere and the
	// difference is large in both directions. On a 256x256 gradient the
	// predictor transform takes 194 666 bytes down to 114. On a rasterised
	// page of text it makes things WORSE — 17 300 up to 27 124 — because
	// prediction breaks the long exact matches that repeated glyphs give the
	// match finder, and a residual that depends on its neighbourhood no longer
	// equals the residual of the same glyph drawn elsewhere.
	//
	// Guessing from the image would be a heuristic to maintain and to be wrong
	// about. Encoding twice and keeping the smaller costs one more pass and is
	// never worse than either.
	plain := encodeVP8L(pix, width, height, false)
	if width > 1 || height > 1 {
		if predicted := encodeVP8L(pix, width, height, true); len(predicted) < len(plain) {
			plain = predicted
		}
	}
	return riff(plain), nil
}

// toNRGBA reads the image into straight (non-premultiplied) RGBA pixels.
//
// ⛔ NON-PREMULTIPLIED, and the distinction is not academic. Go's Color.RGBA
// returns ALPHA-PREMULTIPLIED values, so reading pixels through it and storing
// them as WebP's straight ARGB darkens everything translucent — and leaves
// fully opaque images looking perfect, which is how such a defect ships.
// image.NRGBA is the type that already holds straight values, and draw.Draw
// does the conversion for everything else.
func toNRGBA(m image.Image) *image.NRGBA {
	b := m.Bounds()
	if src, ok := m.(*image.NRGBA); ok && src.Rect == b {
		return src
	}
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), m, b.Min, draw.Src)
	return dst
}

// riff wraps a VP8L bitstream in the container.
func riff(stream []byte) []byte {
	// A chunk of odd length carries one padding byte, which must be zero, and
	// the padding is NOT counted in the chunk's own size.
	padded := len(stream)
	if padded%2 == 1 {
		padded++
	}
	out := make([]byte, 0, 12+8+padded)
	out = append(out, 'R', 'I', 'F', 'F')
	// The file size field counts everything after it: 'WEBP' plus the chunks.
	out = binary.LittleEndian.AppendUint32(out, uint32(4+8+padded))
	out = append(out, 'W', 'E', 'B', 'P')
	out = append(out, 'V', 'P', '8', 'L')
	out = binary.LittleEndian.AppendUint32(out, uint32(len(stream)))
	out = append(out, stream...)
	if padded != len(stream) {
		out = append(out, 0)
	}
	return out
}

// The alphabet sizes of the five prefix codes in a group, with no colour
// cache: green carries the 256 literals and the 24 length codes.
const (
	greenSymbols    = 256 + 24
	literalSymbols  = 256
	distanceSymbols = 40
)

// encodeVP8L writes the whole lossless bitstream for one image, with or
// without the predictor transform.
func encodeVP8L(pix *image.NRGBA, width, height int, predictor bool) []byte {
	w := &bitWriter{}
	w.writeBits(0x2f, 8)                   // the signature
	w.writeBits(uint32(width-1), 14)       //
	w.writeBits(uint32(height-1), 14)      //
	w.writeBits(boolBit(hasAlpha(pix)), 1) // a hint only
	w.writeBits(0, 3)                      // version, which must be zero

	argb := toARGB(pix, width, height)

	// ⛔ THE TRANSFORM COMES FIRST IN THE STREAM AND LAST IN THE DECODER. The
	// sub-image naming each block's predictor is written here, then the
	// transform list is ended, then the main image — which by now holds
	// RESIDUALS rather than colours.
	if predictor {
		modes := applyPredictor(argb, width, height)
		w.writeBits(1, 1)                       // a transform follows
		w.writeBits(0, 2)                       // the predictor transform
		w.writeBits(uint32(predictorBits-2), 3) // its block size
		// ⛔ The sub-image is NOT top level, and that is a bit of difference:
		// only a top-level image carries the meta-prefix bit. Writing one here
		// shifts everything after it by one bit.
		writeImage(w, modes, false)
	}
	w.writeBits(0, 1) // no more transforms

	writeImage(w, argb, true)
	return w.bytes()
}

// writeImage writes one entropy-coded image: the colour cache flag, the
// meta-prefix flag when this is the top-level image, the five prefix codes,
// and the pixels.
func writeImage(w *bitWriter, argb []uint32, topLevel bool) {
	w.writeBits(0, 1) // no colour cache
	if topLevel {
		w.writeBits(0, 1) // one prefix code group for the whole image
	}

	ops := compress(argb, 0)
	green, red, blue, alpha, dist := buildCodes(argb, ops)

	writeCodeLengths(w, green)
	writeCodeLengths(w, red)
	writeCodeLengths(w, blue)
	writeCodeLengths(w, alpha)
	writeCodeLengths(w, dist)

	for _, op := range ops {
		if op.isLiteral {
			c := argb[op.at]
			green.put(w, int((c>>8)&0xff))
			red.put(w, int((c>>16)&0xff))
			blue.put(w, int(c&0xff))
			alpha.put(w, int((c>>24)&0xff))
			continue
		}
		lenCode, lenExtra, lenBits := prefixOf(op.length)
		green.put(w, 256+lenCode)
		w.writeBits(lenExtra, lenBits)
		distCode, distExtra, distBits := prefixOf(op.distance + 120)
		dist.put(w, distCode)
		w.writeBits(distExtra, distBits)
	}
}

// buildCodes counts what the stream will contain and builds the five prefix
// codes from it. It is separate so that a test can look at the codes
// themselves rather than at the bits they turn into.
func buildCodes(argb []uint32, ops []op) (green, red, blue, alpha, dist prefixCode) {
	greenFreq := make([]int, greenSymbols)
	redFreq := make([]int, literalSymbols)
	blueFreq := make([]int, literalSymbols)
	alphaFreq := make([]int, literalSymbols)
	distFreq := make([]int, distanceSymbols)
	for _, o := range ops {
		if o.isLiteral {
			c := argb[o.at]
			greenFreq[(c>>8)&0xff]++
			redFreq[(c>>16)&0xff]++
			blueFreq[c&0xff]++
			alphaFreq[(c>>24)&0xff]++
			continue
		}
		lenCode, _, _ := prefixOf(o.length)
		greenFreq[256+lenCode]++
		distCode, _, _ := prefixOf(o.distance + 120)
		distFreq[distCode]++
	}
	return buildPrefixCode(greenFreq), buildPrefixCode(redFreq),
		buildPrefixCode(blueFreq), buildPrefixCode(alphaFreq), buildPrefixCode(distFreq)
}

func boolBit(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

func hasAlpha(pix *image.NRGBA) bool {
	for i := 3; i < len(pix.Pix); i += 4 {
		if pix.Pix[i] != 0xff {
			return true
		}
	}
	return false
}

// toARGB packs the image into one word per pixel, which is the order the
// format counts in.
func toARGB(pix *image.NRGBA, width, height int) []uint32 {
	out := make([]uint32, width*height)
	for y := 0; y < height; y++ {
		row := pix.Pix[y*pix.Stride : y*pix.Stride+width*4]
		for x := 0; x < width; x++ {
			r := uint32(row[x*4+0])
			g := uint32(row[x*4+1])
			b := uint32(row[x*4+2])
			a := uint32(row[x*4+3])
			out[y*width+x] = a<<24 | r<<16 | g<<8 | b
		}
	}
	return out
}

// prefixOf splits a length or a distance into the prefix symbol and the extra
// bits that follow it, inverting the specification's reader:
//
//	if prefix < 4 { return prefix + 1 }
//	extra  := (prefix - 2) >> 1
//	offset := (2 + (prefix & 1)) << extra
//	return offset + ReadBits(extra) + 1
func prefixOf(value int) (prefix int, extra uint32, nbits uint) {
	if value <= 4 {
		return value - 1, 0, 0
	}
	v := value - 1
	// The highest set bit decides the pair of codes the value falls in.
	b := 0
	for v>>(b+1) != 0 {
		b++
	}
	// Two codes share each magnitude: one for the lower half, one for the upper.
	half := (v >> (b - 1)) & 1
	prefix = 2*b + int(half)
	nbits = uint(b - 1)
	offset := (2 + half) << nbits
	extra = uint32(v - offset)
	return prefix, extra, nbits
}
