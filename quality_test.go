// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp_test

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"math/rand"
	"testing"

	"github.com/go-images/webp"
	xwebp "golang.org/x/image/webp"
)

// psnr is the usual measure of how close a lossy reconstruction is, in
// decibels. Above about 30 is "you have to look for the difference"; above 40
// is "you cannot see it".
//
// ⛔ A LOSSY ENCODER NEEDS A MEASURE, NOT AN ASSERTION. "It decodes" is true
// of an encoder that writes grey, and "the pixels match" is false of every
// correct lossy encoder. The only honest gate is a number with a threshold,
// and the threshold has to be stated.
func psnr(a, b image.Image, w, h int) float64 {
	var sum float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x, y).RGBA()
			for _, d := range []float64{
				float64(int(ar>>8) - int(br>>8)),
				float64(int(ag>>8) - int(bg>>8)),
				float64(int(ab>>8) - int(bb>>8)),
			} {
				sum += d * d
			}
		}
	}
	mse := sum / float64(w*h*3)
	if mse == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/mse)
}

// gradientImage is a smooth picture: easy for a transform codec, and the case
// where a broken prediction or transform is most obvious.
func gradientImage(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Set(x, y, color.NRGBA{
				uint8(x * 255 / max(w-1, 1)),
				uint8(y * 255 / max(h-1, 1)),
				uint8((x + y) * 255 / max(w+h-2, 1)),
				255,
			})
		}
	}
	return m
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ⛔ THE PICTURE MUST SURVIVE, and this is the test that says whether the
// transform, the quantiser, the token coding, the prediction and the
// reconstruction all agree with the decoder. Any one of them wrong and the
// PSNR falls off a cliff — a wrong context or a wrong band does not produce a
// slightly worse picture, it produces noise from that point on.
func TestALossyFrameLooksLikeThePicture(t *testing.T) {
	for _, c := range []struct {
		name    string
		img     *image.NRGBA
		quality int
		floor   float64
	}{
		{"a gradient at 90", gradientImage(64, 64), 90, 30},
		{"a gradient at 75", gradientImage(64, 64), 75, 26},
		{"a gradient at 50", gradientImage(96, 48), 50, 22},
		{"an odd size", gradientImage(37, 23), 80, 25},
		{"one macroblock", gradientImage(16, 16), 90, 28},
	} {
		b, err := webp.EncodeLossyToBytes(c.img, c.quality)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got, err := xwebp.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: an independent decoder refused the frame: %v", c.name, err)
		}
		w, h := c.img.Bounds().Dx(), c.img.Bounds().Dy()
		if got.Bounds().Dx() != w || got.Bounds().Dy() != h {
			t.Fatalf("%s: decoded %v, encoded %dx%d", c.name, got.Bounds(), w, h)
		}
		p := psnr(c.img, got, w, h)
		t.Logf("%-20s quality %3d  %6d bytes  PSNR %.1f dB", c.name, c.quality, len(b), p)
		if p < c.floor {
			t.Errorf("%s: PSNR %.1f dB, under the %.0f dB floor", c.name, p, c.floor)
		}
	}
}

// Higher quality must give a better picture and a bigger file. If it does not,
// the quantiser is not reaching the coefficients.
func TestQualityDoesSomething(t *testing.T) {
	img := gradientImage(64, 64)
	var lastSize int
	var lastPSNR float64
	for _, q := range []int{20, 50, 80, 95} {
		b, err := webp.EncodeLossyToBytes(img, q)
		if err != nil {
			t.Fatal(err)
		}
		got, err := xwebp.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("quality %d: %v", q, err)
		}
		p := psnr(img, got, 64, 64)
		t.Logf("quality %3d: %6d bytes, PSNR %.1f dB", q, len(b), p)
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
}

// ⛔ THE COARSEST QUALITIES ARE WHERE THE CHROMINANCE DC CEILING BITES. The
// specification clamps that one factor at index 117 rather than 127, and the
// index only passes 117 below quality 8 — so every test above it agrees
// whichever ceiling the encoder uses. A mutation changing 117 to 127 survived
// the whole suite until this existed.
//
// The colours have to be strong, too: on a grey picture the chrominance is
// zero and its quantiser cannot matter.
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
		b, err := webp.EncodeLossyToBytes(m, q)
		if err != nil {
			t.Fatalf("quality %d: %v", q, err)
		}
		got, err := xwebp.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("quality %d: %v", q, err)
		}
		p := psnr(m, got, 64, 64)
		t.Logf("three strong colours at quality %d: %d bytes, PSNR %.1f dB", q, len(b), p)
		// Coarse, but the colours must still be the right ones: a wrong
		// chrominance quantiser shifts every hue at once.
		if p < 18 {
			t.Errorf("quality %d: PSNR %.1f dB, so the colours did not survive", q, p)
		}
	}
}

// A photograph-like picture, where the lossy form is supposed to beat the
// lossless one — which is the whole reason it exists.
func TestLossyBeatsLosslessOnAPhotograph(t *testing.T) {
	r := rand.New(rand.NewSource(9))
	m := image.NewNRGBA(image.Rect(0, 0, 128, 128))
	// Smooth noise: a plausible stand-in for a photograph, with no exact
	// repeats for the lossless match finder to exploit.
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			v := 128 + 60*math.Sin(float64(x)/9) + 40*math.Cos(float64(y)/7)
			v += float64(r.Intn(11) - 5)
			m.Set(x, y, color.NRGBA{uint8(clamp(v)), uint8(clamp(v * 0.8)), uint8(clamp(v * 1.1)), 255})
		}
	}
	lossless, err := webp.EncodeToBytes(m)
	if err != nil {
		t.Fatal(err)
	}
	lossy, err := webp.EncodeLossyToBytes(m, 75)
	if err != nil {
		t.Fatal(err)
	}
	got, err := xwebp.Decode(bytes.NewReader(lossy))
	if err != nil {
		t.Fatal(err)
	}
	p := psnr(m, got, 128, 128)
	t.Logf("a photograph: lossless %d bytes, lossy %d bytes at %.1f dB", len(lossless), len(lossy), p)
	if len(lossy) >= len(lossless) {
		t.Errorf("lossy is %d bytes and lossless is %d, so the lossy form earns nothing here",
			len(lossy), len(lossless))
	}
	if p < 25 {
		t.Errorf("the lossy picture is only %.1f dB", p)
	}
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}
