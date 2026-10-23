// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"testing"

	"github.com/go-images/webp"
)

// ⛔ A LOSSLESS ENCODER THAT IS CORRECT AND DOES NOT COMPRESS IS NOT DONE, and
// correctness tests cannot tell the two apart: writing every pixel as a
// literal with a flat code round-trips perfectly and is larger than the raw
// bytes. PNG is the honest comparison — the other lossless encoder in the
// standard library, on the same pixels.
func TestItCompresses(t *testing.T) {
	for _, c := range []struct {
		name  string
		image func() image.Image
		// ratio is the most of PNG's size this may take. A document page is
		// where this encoder is meant to be used, so that is the strict one.
		ratio float64
	}{
		// ⛔ These are set just above what is MEASURED, not at a round number
		// that anything would pass. The gradient one is the reason the
		// predictor transform exists: without it that row is 164.28.
		{"a page of text", textPage, 0.55},
		{"flat colour", flatColour, 0.10},
		{"a gradient", gradient, 0.15},
	} {
		m := c.image()
		w, err := webp.EncodeToBytes(m)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var p bytes.Buffer
		if err := png.Encode(&p, m); err != nil {
			t.Fatal(err)
		}
		raw := m.Bounds().Dx() * m.Bounds().Dy() * 4
		t.Logf("%-16s raw %7d   png %6d   webp %6d   (%.2f of png, %.3f of raw)",
			c.name, raw, p.Len(), len(w), float64(len(w))/float64(p.Len()),
			float64(len(w))/float64(raw))
		if float64(len(w)) > float64(p.Len())*c.ratio {
			t.Errorf("%s: webp is %d bytes against png's %d, over the %.2f allowed",
				c.name, len(w), p.Len(), c.ratio)
		}
		if len(w) >= raw {
			t.Errorf("%s: webp is %d bytes for %d bytes of pixels, so it did not compress at all",
				c.name, len(w), raw)
		}
	}
}

// textPage is what a rasterised document page looks like: a white field with
// dark marks on it, in runs.
func textPage() image.Image {
	r := rand.New(rand.NewSource(3))
	m := image.NewNRGBA(image.Rect(0, 0, 400, 520))
	for i := range m.Pix {
		m.Pix[i] = 0xff
	}
	for line := 0; line < 40; line++ {
		y := 20 + line*12
		x := 30
		for x < 370 {
			wordLen := 3 + r.Intn(9)
			for k := 0; k < wordLen && x < 370; k++ {
				for dy := 0; dy < 7; dy++ {
					for dx := 0; dx < 4; dx++ {
						if r.Intn(3) == 0 {
							continue
						}
						m.Set(x+dx, y+dy, color.NRGBA{20, 20, 20, 255})
					}
				}
				x += 5
			}
			x += 4
		}
	}
	return m
}

func flatColour() image.Image {
	m := image.NewNRGBA(image.Rect(0, 0, 300, 300))
	for i := range m.Pix {
		m.Pix[i] = []uint8{0x1e, 0x6b, 0xa8, 0xff}[i%4]
	}
	return m
}

func gradient() image.Image {
	m := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			m.Set(x, y, color.NRGBA{uint8(x), uint8(y), uint8((x + y) / 2), 255})
		}
	}
	return m
}
