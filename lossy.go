// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

import (
	"encoding/binary"
	"fmt"
	"image"
	"io"
)

// EncodeLossy writes m to w as a lossy WebP image (VP8).
//
// quality runs from 0 to 100. It selects the quantiser: higher keeps more
// detail and makes a larger file.
//
// ⛔ THIS IS A KEYFRAME ENCODER AND NOTHING ELSE. VP8 is a video codec and
// most of it — golden frames, motion vectors, the alternate reference — exists
// for the frames between keyframes. A still picture is one keyframe, so none
// of that is written, and the header says so rather than leaving fields a
// decoder would read as video.
func EncodeLossy(w io.Writer, m image.Image, quality int) error {
	b, err := EncodeLossyToBytes(m, quality)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// EncodeLossyToBytes returns m as a lossy WebP file.
func EncodeLossyToBytes(m image.Image, quality int) ([]byte, error) {
	bounds := m.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, ErrEmpty
	}
	// ⛔ VP8's frame header carries the dimensions in FOURTEEN bits, the same
	// ceiling the lossless form has, so the refusal is the same one.
	if width > maxDimension || height > maxDimension {
		return nil, fmt.Errorf("%w: %dx%d", ErrTooLarge, width, height)
	}
	if quality < 0 || quality > 100 {
		return nil, fmt.Errorf("webp: quality %d is not between 0 and 100", quality)
	}

	frame := encodeVP8Frame(toNRGBA(m), width, height, quality)
	return riffChunk("VP8 ", frame), nil
}

// qIndexFor maps a quality from 0 to 100 onto VP8's quantiser index, which
// runs the other way: index 0 is the finest and 127 the coarsest.
func qIndexFor(quality int) uint32 {
	return uint32((100 - quality) * 127 / 100)
}

// encodeVP8Frame writes one key frame.
func encodeVP8Frame(pix *image.NRGBA, width, height, quality int) []byte {
	mbw := (width + 15) / 16
	mbh := (height + 15) / 16

	// The second partition carries the coefficients; the first carries the
	// headers and every macroblock's prediction modes.
	second := newBoolEncoder()
	first := newBoolEncoder()

	first.writeFlag(false) // colour space: the only one defined
	first.writeFlag(false) // no pixel clamping needed

	first.writeFlag(false) // no segmentation

	// ⛔ THE LOOP FILTER IS OFF. A level of zero tells the decoder not to
	// filter, which is legal and costs some quality at low quality settings.
	// Writing a level this encoder does not itself apply would be worse: the
	// decoder would filter a picture the encoder never saw filtered, so the
	// two would disagree about what the frame looks like.
	first.writeFlag(false)   // normal filter rather than simple
	first.writeLiteral(0, 6) // level zero: no filtering
	first.writeLiteral(0, 3) // sharpness, unused at level zero
	first.writeFlag(false)   // no per-reference filter deltas

	first.writeLiteral(0, 2) // one coefficient partition

	// The quantiser, with no per-plane deltas.
	first.writeLiteral(qIndexFor(quality), 7)
	for i := 0; i < 5; i++ {
		first.writeAbsent()
	}

	first.writeFlag(true) // refresh the entropy probabilities

	// ⛔ EVERY COEFFICIENT PROBABILITY IS LEFT AT ITS DEFAULT, and saying so
	// costs 1 056 bits: one per entry, each written at that entry's own update
	// probability. There is no shorter way to say "no change" — the format has
	// no flag for it — and omitting them shifts everything after by 1 056 bits.
	for i := range tokenProbUpdateProb {
		for j := range tokenProbUpdateProb[i] {
			for k := range tokenProbUpdateProb[i][j] {
				for l := range tokenProbUpdateProb[i][j][k] {
					first.writeBool(tokenProbUpdateProb[i][j][k][l], false)
				}
			}
		}
	}

	first.writeFlag(true)           // macroblocks may say they are empty
	first.writeLiteral(skipProb, 8) // and how likely that is

	// Every macroblock: whether it is empty, then its prediction modes in the
	// first partition, and its coefficients in the second.
	enc := &frameEncoder{
		src:    toYCbCr(pix, width, height, mbw, mbh),
		rec:    &planes{y: newPlane(mbw*16, mbh*16), u: newPlane(mbw*8, mbh*8), v: newPlane(mbw*8, mbh*8)},
		mbw:    mbw,
		mbh:    mbh,
		first:  first,
		second: second,
		quant:  quantiserFor(qIndexFor(quality)),
		leftNZ: make([]nzState, 1),
		upNZ:   make([]nzState, mbw),
	}
	enc.encode()

	firstBytes := first.bytes()
	secondBytes := second.bytes()

	out := make([]byte, 0, 10+len(firstBytes)+len(secondBytes))
	// The frame tag: a key frame, version 0, shown, and how long the first
	// partition is.
	tag := uint32(0) | 0<<1 | 1<<4 | uint32(len(firstBytes))<<5
	out = append(out, byte(tag), byte(tag>>8), byte(tag>>16))
	out = append(out, 0x9d, 0x01, 0x2a) // the start code
	out = binary.LittleEndian.AppendUint16(out, uint16(width))
	out = binary.LittleEndian.AppendUint16(out, uint16(height))
	out = append(out, firstBytes...)
	out = append(out, secondBytes...)
	return out
}

// The intra prediction modes, in the order the format numbers them.
const (
	predDC = iota
	predTM
	predVE
	predHE
)

// writeY16Mode writes a whole-macroblock luma mode, inverting the decoder's
// three fixed probabilities.
func writeY16Mode(e *boolEncoder, mode int) {
	switch mode {
	case predDC:
		e.writeBool(156, false)
		e.writeBool(163, false)
	case predVE:
		e.writeBool(156, false)
		e.writeBool(163, true)
	case predHE:
		e.writeBool(156, true)
		e.writeBool(128, false)
	default: // predTM
		e.writeBool(156, true)
		e.writeBool(128, true)
	}
}

// writeUVMode writes the chroma mode.
func writeUVMode(e *boolEncoder, mode int) {
	switch mode {
	case predDC:
		e.writeBool(142, false)
	case predVE:
		e.writeBool(142, true)
		e.writeBool(114, false)
	case predHE:
		e.writeBool(142, true)
		e.writeBool(114, true)
		e.writeBool(183, false)
	default: // predTM
		e.writeBool(142, true)
		e.writeBool(114, true)
		e.writeBool(183, true)
	}
}

// skipProb is how likely a macroblock is to carry no coefficients. Every
// macroblock this encoder writes today is empty, so the probability is set
// high and costs almost nothing per macroblock.
const skipProb = 0xf0

// riffChunk wraps a payload in the container under the given FourCC.
func riffChunk(fourCC string, payload []byte) []byte {
	padded := len(payload)
	if padded%2 == 1 {
		padded++
	}
	out := make([]byte, 0, 12+8+padded)
	out = append(out, 'R', 'I', 'F', 'F')
	out = binary.LittleEndian.AppendUint32(out, uint32(4+8+padded))
	out = append(out, 'W', 'E', 'B', 'P')
	out = append(out, fourCC[0], fourCC[1], fourCC[2], fourCC[3])
	out = binary.LittleEndian.AppendUint32(out, uint32(len(payload)))
	out = append(out, payload...)
	if padded != len(payload) {
		out = append(out, 0)
	}
	return out
}
