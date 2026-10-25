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
	"math/rand"
	"testing"

	xvp8 "golang.org/x/image/vp8"
)

// ⛔ A PSNR THAT STOPS IMPROVING AS THE QUANTISER GETS FINER IS A BUG, not a
// limit. Measured against libwebp, our lossy encoder plateaus at about 31 dB
// on a page of text and 29.5 dB on a photograph, while the file grows past a
// quarter of a megabyte; libwebp climbs to 55 dB. At quality 99 the quantiser
// index is 1 and the reconstruction should be very nearly exact.
//
// This separates the three things that could hold the floor up:
//
//   - the CODEC: what the encoder reconstructs internally against the planes
//     it was given. That is quantisation and nothing else, so at index 0 it
//     must be tiny.
//   - the DECODER AGREEING: what a foreign decoder produces against what the
//     encoder thinks it produced. A disagreement here is a bitstream fault.
//   - the COLOUR: the trip through YCbCr and back, which is lossy on its own
//     and has nothing to do with VP8.
//
// Comparing in YCbCr is what makes that possible: x/image/vp8 hands back the
// planes, so the colour conversion never enters the measurement.
func TestWhereTheErrorFloorIs(t *testing.T) {
	src := greyTextLike(64, 64)

	for _, q := range []int{50, 90, 100} {
		b, err := EncodeLossyToBytes(src, q)
		if err != nil {
			t.Fatal(err)
		}

		// What the encoder believes it produced.
		mbw, mbh := 4, 4
		pix := toNRGBA(src)
		planesIn := toYCbCr(pix, 64, 64, mbw, mbh)
		enc := &frameEncoder{
			src:    planesIn,
			rec:    &planes{y: newPlane(mbw*16, mbh*16), u: newPlane(mbw*8, mbh*8), v: newPlane(mbw*8, mbh*8)},
			mbw:    mbw,
			mbh:    mbh,
			first:  newBoolEncoder(),
			second: newBoolEncoder(),
			quant:  quantiserFor(qIndexFor(q)),
			leftNZ: make([]nzState, 1),
			upNZ:   make([]nzState, mbw),
		}
		enc.encode()
		selfY := planePSNR(planesIn.y, enc.rec.y, 64, 64)

		// What a foreign decoder produces, in the same colour space.
		d := xvp8.NewDecoder()
		// The VP8 chunk starts 20 bytes in: 12 of RIFF plus an 8-byte header.
		d.Init(bytes.NewReader(b[20:]), len(b)-20)
		if _, err := d.DecodeFrameHeader(); err != nil {
			t.Fatalf("quality %d: %v", q, err)
		}
		img, err := d.DecodeFrame()
		if err != nil {
			t.Fatalf("quality %d: %v", q, err)
		}
		decY := ycbcrPSNR(enc.rec.y, img, 64, 64)
		srcY := ycbcrPSNR(planesIn.y, img, 64, 64)

		t.Logf("quality %3d: encoder against its input %5.1f dB | decoder against the encoder %5.1f dB | decoder against the input %5.1f dB",
			q, selfY, decY, srcY)

		// ⛔ The decoder and the encoder must agree almost exactly: they run
		// the same inverse transform on the same coefficients. A gap here is a
		// bitstream fault and nothing else.
		if q == 100 && decY < 45 {
			t.Errorf("quality 100: the decoder and the encoder disagree at %.1f dB, "+
				"so the bitstream does not say what the encoder reconstructed", decY)
		}
		if q == 100 && selfY < 45 {
			t.Errorf("quality 100: the encoder's own reconstruction is %.1f dB from its "+
				"input, so the loss is in the codec rather than in the colour", selfY)
		}
	}
}

// greyTextLike is near-grey, so that chrominance subsampling cannot be what
// holds the floor up.
func greyTextLike(w, h int) image.Image {
	r := rand.New(rand.NewSource(31))
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range m.Pix {
		m.Pix[i] = 0xff
	}
	for y := 4; y < h-4; y += 6 {
		for x := 3; x < w-3; x += 5 {
			if r.Intn(4) == 0 {
				continue
			}
			for dy := 0; dy < 4; dy++ {
				for dx := 0; dx < 3; dx++ {
					m.Set(x+dx, y+dy, color.NRGBA{20, 20, 20, 255})
				}
			}
		}
	}
	return m
}

func planePSNR(a, b *plane, w, h int) float64 {
	var sq, n float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d := float64(a.at(x, y)) - float64(b.at(x, y))
			sq += d * d
			n++
		}
	}
	if sq == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/(sq/n))
}

func ycbcrPSNR(a *plane, img *image.YCbCr, w, h int) float64 {
	var sq, n float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			got := img.Y[y*img.YStride+x]
			d := float64(a.at(x, y)) - float64(got)
			sq += d * d
			n++
		}
	}
	if sq == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/(sq/n))
}
