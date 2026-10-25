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

	xwebp "golang.org/x/image/webp"
)

// ⛔ THE LOSSY MEASUREMENT IS MADE IN YCbCr, NOT IN RGB, and that is the whole
// lesson of this file. It used to compare RGB: source against what
// x/image/webp handed back. That looked rigorous — an independent decoder,
// every pixel — and it was measuring the wrong thing, because the two
// conventions for the luma RANGE differ.
//
// x/image treats luma as full range, 0..255. libwebp — and so every browser —
// treats it as studio range, 16..235. This encoder wrote Go's convention, and
// because the oracle shared it, the suite agreed. A PSNR that stopped
// improving at about 31 dB however fine the quantiser got was the only hint,
// and it reads as "the codec is weak" rather than "the judge and the subject
// share a mistake".
//
// VP8 carries PLANES. Comparing them leaves no room for a convention to enter
// the measurement at all: the source is converted once, by the same function
// the encoder uses, and what comes back is compared to that.
func planesPSNR(t *testing.T, src image.Image, encoded []byte) float64 {
	t.Helper()
	got, err := xwebp.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("an independent decoder refused the frame: %v", err)
	}
	ycc, ok := got.(*image.YCbCr)
	if !ok {
		t.Fatalf("a lossy frame decoded to %T rather than to planes", got)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	pix := toNRGBA(src)

	var sq, n float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := y*pix.Stride + x*4
			wantY, wantCb, wantCr := rgbToYCbCr(pix.Pix[o], pix.Pix[o+1], pix.Pix[o+2])
			gotY := ycc.Y[y*ycc.YStride+x]
			ci := (y/2)*ycc.CStride + x/2
			gotCb, gotCr := ycc.Cb[ci], ycc.Cr[ci]
			for _, d := range []float64{
				float64(wantY) - float64(gotY),
				float64(wantCb) - float64(gotCb),
				float64(wantCr) - float64(gotCr),
			} {
				sq += d * d
				n++
			}
		}
	}
	if sq == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/(sq/n))
}

// gradientImage is a smooth picture: easy for a transform codec, and the case
// where a broken prediction or transform is most obvious.
func gradientImage(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Set(x, y, color.NRGBA{
				uint8(x * 255 / maxOf(w-1, 1)),
				uint8(y * 255 / maxOf(h-1, 1)),
				uint8((x + y) * 255 / maxOf(w+h-2, 1)),
				255,
			})
		}
	}
	return m
}

func maxOf(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ⛔ THE PICTURE MUST SURVIVE, and this is the test that says whether the
// transform, the quantiser, the token coding, the prediction and the
// reconstruction all agree with the decoder. Any one of them wrong and the
// figure falls off a cliff — a wrong context or a wrong band does not produce
// a slightly worse picture, it produces noise from that point on.
func TestALossyFrameLooksLikeThePicture(t *testing.T) {
	for _, c := range []struct {
		name    string
		img     *image.NRGBA
		quality int
		floor   float64
	}{
		{"a gradient at 90", gradientImage(64, 64), 90, 38},
		{"a gradient at 75", gradientImage(64, 64), 75, 33},
		{"a gradient at 50", gradientImage(96, 48), 50, 28},
		{"an odd size", gradientImage(37, 23), 80, 32},
		{"one macroblock", gradientImage(16, 16), 90, 34},
	} {
		b, err := EncodeLossyToBytes(c.img, c.quality)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		p := planesPSNR(t, c.img, b)
		t.Logf("%-20s quality %3d  %6d bytes  %.1f dB", c.name, c.quality, len(b), p)
		if p < c.floor {
			t.Errorf("%s: %.1f dB, under the %.0f dB floor", c.name, p, c.floor)
		}
	}
}

// ⛔ A FINER QUANTISER MUST GIVE A BETTER PICTURE, and that is not a nicety:
// a figure that stops climbing while the file keeps growing is an error floor,
// and an error floor is a defect. The colour-range mismatch held this at about
// 31 dB from quality 50 upwards while the file tripled.
func TestQualityDoesSomething(t *testing.T) {
	img := gradientImage(64, 64)
	var lastSize int
	var lastPSNR float64
	for _, q := range []int{20, 50, 80, 95} {
		b, err := EncodeLossyToBytes(img, q)
		if err != nil {
			t.Fatal(err)
		}
		p := planesPSNR(t, img, b)
		t.Logf("quality %3d: %6d bytes, %.1f dB", q, len(b), p)
		if lastSize != 0 {
			if len(b) <= lastSize {
				t.Errorf("quality %d gave %d bytes, no more than the %d of the step before",
					q, len(b), lastSize)
			}
			if p <= lastPSNR {
				t.Errorf("quality %d gave %.1f dB, no better than the %.1f of the step before",
					q, p, lastPSNR)
			}
		}
		lastSize, lastPSNR = len(b), p
	}
	// And the finest setting has to be genuinely close, which is what says
	// there is no floor left.
	b, err := EncodeLossyToBytes(img, 100)
	if err != nil {
		t.Fatal(err)
	}
	if p := planesPSNR(t, img, b); p < 45 {
		t.Errorf("the finest quality is only %.1f dB from the source, which is a floor", p)
	} else {
		t.Logf("quality 100: %6d bytes, %.1f dB", len(b), p)
	}
}

// ⛔ THE COARSEST QUALITIES ARE WHERE THE CHROMINANCE DC CEILING BITES. The
// specification clamps that one factor at index 117 rather than 127, and the
// index only passes 117 below quality 8.
func TestTheCoarsestQualitiesKeepTheirColours(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			switch {
			case x < 21:
				m.Set(x, y, color.NRGBA{220, 30, 30, 255})
			case x < 42:
				m.Set(x, y, color.NRGBA{30, 200, 60, 255})
			default:
				m.Set(x, y, color.NRGBA{40, 60, 230, 255})
			}
		}
	}
	for _, q := range []int{0, 3, 7} {
		b, err := EncodeLossyToBytes(m, q)
		if err != nil {
			t.Fatalf("quality %d: %v", q, err)
		}
		p := planesPSNR(t, m, b)
		t.Logf("three strong colours at quality %d: %d bytes, %.1f dB", q, len(b), p)
		if p < 18 {
			t.Errorf("quality %d: %.1f dB, so the colours did not survive", q, p)
		}
	}
}

// A photograph-like picture, where the lossy form is supposed to beat the
// lossless one — which is the whole reason it exists.
func TestLossyBeatsLosslessOnAPhotograph(t *testing.T) {
	r := rand.New(rand.NewSource(9))
	m := image.NewNRGBA(image.Rect(0, 0, 128, 128))
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			v := 128 + 60*math.Sin(float64(x)/9) + 40*math.Cos(float64(y)/7)
			v += float64(r.Intn(11) - 5)
			m.Set(x, y, color.NRGBA{uint8(clampQ(v)), uint8(clampQ(v * 0.8)), uint8(clampQ(v * 1.1)), 255})
		}
	}
	lossless, err := EncodeToBytes(m)
	if err != nil {
		t.Fatal(err)
	}
	lossy, err := EncodeLossyToBytes(m, 75)
	if err != nil {
		t.Fatal(err)
	}
	p := planesPSNR(t, m, lossy)
	t.Logf("a photograph: lossless %d bytes, lossy %d bytes at %.1f dB", len(lossless), len(lossy), p)
	if len(lossy) >= len(lossless) {
		t.Errorf("lossy is %d bytes and lossless is %d, so the lossy form earns nothing here",
			len(lossy), len(lossless))
	}
	if p < 30 {
		t.Errorf("the lossy picture is only %.1f dB", p)
	}
}

func clampQ(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}
