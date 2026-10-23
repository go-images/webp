// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"testing"

	xwebp "golang.org/x/image/webp"
)

// FuzzALossyFrameIsReadBackAsAPicture drives the lossy encoder.
//
// ⛔ THE CHECK IS NOT "IT DECODES". A VP8 stream that goes out of step still
// decodes — the arithmetic decoder always produces bits — it just produces the
// wrong ones, and the picture turns to noise partway down. The reset of the
// second-order context on a skipped macroblock was exactly that: most images
// and most quality settings looked fine. So the gate is that the decoded
// picture RESEMBLES the original, which is what catches a divergence.
func FuzzALossyFrameIsReadBackAsAPicture(f *testing.F) {
	f.Add(16, 16, 75, []byte{1, 2, 3})
	f.Add(32, 24, 20, []byte{200, 10, 90})
	f.Add(17, 9, 95, []byte{0, 0, 0})

	f.Fuzz(func(t *testing.T, w, h, quality int, seed []byte) {
		if w < 1 || h < 1 || w > 48 || h > 48 || len(seed) == 0 {
			return
		}
		if quality < 0 || quality > 100 {
			return
		}
		// ⛔ THE PICTURE IS SMOOTH BY CONSTRUCTION, and that is not making the
		// test easy — it is making it MEAN something. The first version let
		// the fuzzer paint arbitrary bytes, and it immediately produced a
		// maximum-amplitude checkerboard: alternating 48 and 245 on every
		// pixel. No transform codec can carry that at a coarse quantiser, and
		// 4:2:0 averages the chrominance of any 2x2 group whatever the
		// quality, so it is irrecoverable by construction. The 47.8 levels of
		// error it reported were CORRECT BEHAVIOUR, and a bound that such a
		// picture fails is a bound that says nothing about the codec.
		//
		// What this fuzzer is for is a bitstream that goes OUT OF STEP — a
		// wrong context, a wrong band, a missed reset — which turns the
		// picture to noise from some macroblock onward. That shows up plainly
		// on content the codec can represent, so the fuzzer varies the phase
		// and the amplitude and leaves the frequency low.
		m := image.NewNRGBA(image.Rect(0, 0, w, h))
		ax := 1 + float64(seed[0])/64
		ay := 1 + float64(seed[len(seed)-1])/64
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				v := 128 + 90*math.Sin(float64(x)/(3*ax)) + 30*math.Cos(float64(y)/(3*ay))
				m.Set(x, y, color.NRGBA{
					uint8(clampF(v)), uint8(clampF(v * 0.85)), uint8(clampF(v*0.6 + 40)), 255,
				})
			}
		}

		b, err := EncodeLossyToBytes(m, quality)
		if err != nil {
			t.Fatalf("%dx%d at %d: %v", w, h, quality, err)
		}
		got, err := xwebp.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%dx%d at %d: an independent decoder refused the frame: %v", w, h, quality, err)
		}
		if got.Bounds().Dx() != w || got.Bounds().Dy() != h {
			t.Fatalf("%dx%d: decoded %v", w, h, got.Bounds())
		}

		// The mean absolute error over the whole picture. A frame that has
		// gone out of step is wrong by tens of levels; a frame that is merely
		// quantised coarsely is not.
		var sum, n float64
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				ar, ag, ab, _ := m.At(x, y).RGBA()
				br, bg, bb, _ := got.At(x, y).RGBA()
				sum += absF(float64(ar>>8) - float64(br>>8))
				sum += absF(float64(ag>>8) - float64(bg>>8))
				sum += absF(float64(ab>>8) - float64(bb>>8))
				n += 3
			}
		}
		if mae := sum / n; mae > 40 {
			t.Fatalf("%dx%d at quality %d: the decoded picture is %.1f levels off on average",
				w, h, quality, mae)
		}
	})
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func clampF(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

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
