// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

import (
	"bytes"
	"image"
	"testing"

	xwebp "golang.org/x/image/webp"
)

// FuzzEveryImageSurvivesAnIndependentDecoder is the whole promise of a
// lossless encoder, held against somebody else's reader.
//
// ⛔ THE FUZZER DRIVES THE PIXELS, NOT A FILE. Fuzzing the DECODER would be
// the usual shape and would say nothing here: what has to hold is that for
// every image, what this writes is read back as that image. The interesting
// inputs are the ones nobody draws — a single colour (every position hashes
// alike, every match overlaps its own source), two colours (a distribution
// skewed enough to push a plain Huffman tree past the fifteen bits the format
// can express), and a constant channel (a one-symbol tree, which costs ZERO
// bits and desynchronises an encoder that gives it one).
func FuzzEveryImageSurvivesAnIndependentDecoder(f *testing.F) {
	f.Add(1, 1, []byte{1, 2, 3, 255})
	f.Add(4, 4, bytes.Repeat([]byte{9, 9, 9, 255}, 16))
	f.Add(3, 5, bytes.Repeat([]byte{0, 0, 0, 255}, 15))
	f.Add(8, 2, bytes.Repeat([]byte{255, 0, 128, 7}, 16))

	f.Fuzz(func(t *testing.T, w, h int, pix []byte) {
		// A fuzzer picks the dimensions too, so they are bounded here rather
		// than left to produce a gigabyte allocation per execution.
		if w < 1 || h < 1 || w > 64 || h > 64 {
			return
		}
		if len(pix) < w*h*4 {
			return
		}
		m := image.NewNRGBA(image.Rect(0, 0, w, h))
		copy(m.Pix, pix[:w*h*4])

		// ⛔ The codes are checked BEFORE the round trip, because a decoder
		// says "invalid Huffman tree" without saying WHICH of the six trees it
		// was. Checking the structure first turns one opaque message into a
		// named code and a reason.
		argb := toARGB(m, w, h)
		green, red, blue, alpha, dist := buildCodes(argb, compress(argb, w))
		for name, p := range map[string]prefixCode{
			"green": green, "red": red, "blue": blue, "alpha": alpha, "distance": dist,
		} {
			checkBuildable(t, name, p)
		}

		var buf bytes.Buffer
		if err := Encode(&buf, m); err != nil {
			t.Fatalf("encoding a %dx%d image: %v", w, h, err)
		}
		got, err := xwebp.Decode(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("an independent decoder refused a %dx%d image: %v", w, h, err)
		}
		if got.Bounds().Dx() != w || got.Bounds().Dy() != h {
			t.Fatalf("encoded %dx%d and decoded %v", w, h, got.Bounds())
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				wr, wg, wb, wa := m.At(x, y).RGBA()
				gr, gg, gb, ga := got.At(x, y).RGBA()
				if wr != gr || wg != gg || wb != gb || wa != ga {
					t.Fatalf("%dx%d pixel (%d,%d): wrote %v,%v,%v,%v and read %v,%v,%v,%v",
						w, h, x, y, wr, wg, wb, wa, gr, gg, gb, ga)
				}
			}
		}
	})
}
