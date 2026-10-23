// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp_test

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"github.com/go-images/webp"
	xwebp "golang.org/x/image/webp"
)

// ⛔ THE FIRST THING TO PROVE IS THE FRAMING, not the picture. A VP8 frame is
// a tag, a start code, two dimensions and then one long arithmetic-coded
// stream in which every header field, every probability-update flag and every
// macroblock's modes are written in a fixed order. Get any of it wrong by a
// single bit and everything after it is nonsense.
//
// So this encodes a frame carrying NO coefficients at all: every macroblock
// says it is empty and predicts flat. The picture is uniform mid-grey, which
// is what DC prediction gives with no neighbours — and the point is that an
// independent decoder reads the frame, agrees about its size, and finds that
// grey.
func TestAFrameWithNoCoefficientsDecodes(t *testing.T) {
	for _, c := range []struct{ w, h int }{
		{16, 16}, {32, 16}, {16, 32}, {64, 48}, {17, 9}, {1, 1}, {100, 73},
	} {
		m := image.NewNRGBA(image.Rect(0, 0, c.w, c.h))
		for i := range m.Pix {
			m.Pix[i] = 0x80
		}
		for i := 3; i < len(m.Pix); i += 4 {
			m.Pix[i] = 0xff
		}

		b, err := webp.EncodeLossyToBytes(m, 75)
		if err != nil {
			t.Fatalf("%dx%d: %v", c.w, c.h, err)
		}
		got, err := xwebp.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%dx%d: an independent decoder refused the frame: %v", c.w, c.h, err)
		}
		if got.Bounds().Dx() != c.w || got.Bounds().Dy() != c.h {
			t.Fatalf("%dx%d: decoded %v", c.w, c.h, got.Bounds())
		}
		// Every pixel is the same, and it is the mid-grey DC prediction
		// produces when there is nothing above or to the left.
		r0, g0, b0, _ := got.At(0, 0).RGBA()
		for y := 0; y < c.h; y++ {
			for x := 0; x < c.w; x++ {
				r, g, bb, _ := got.At(x, y).RGBA()
				if r != r0 || g != g0 || bb != b0 {
					t.Fatalf("%dx%d: pixel (%d,%d) is %v,%v,%v and (0,0) is %v,%v,%v",
						c.w, c.h, x, y, r, g, bb, r0, g0, b0)
				}
			}
		}
		if r0>>8 < 100 || r0>>8 > 160 {
			t.Errorf("%dx%d: the flat frame came out at %d, which is not the mid-grey "+
				"a DC prediction with no neighbours gives", c.w, c.h, r0>>8)
		}
	}
}

func TestLossyRefusals(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for _, c := range []struct {
		why     string
		img     image.Image
		quality int
	}{
		{"no pixels", image.NewNRGBA(image.Rect(0, 0, 0, 4)), 75},
		{"too wide", image.NewNRGBA(image.Rect(0, 0, 16385, 1)), 75},
		{"too tall", image.NewNRGBA(image.Rect(0, 0, 1, 16385)), 75},
		{"a quality below zero", m, -1},
		{"a quality above a hundred", m, 101},
	} {
		if _, err := webp.EncodeLossyToBytes(c.img, c.quality); err == nil {
			t.Errorf("%s was accepted", c.why)
		}
	}
	if _, err := webp.EncodeLossyToBytes(m, 0); err != nil {
		t.Errorf("quality 0 was refused: %v", err)
	}
	if _, err := webp.EncodeLossyToBytes(m, 100); err != nil {
		t.Errorf("quality 100 was refused: %v", err)
	}
}

// The container for a lossy frame names 'VP8 ' with a trailing SPACE, which is
// the one FourCC in this format that a careful reader gets wrong.
func TestTheLossyContainerNamesVP8WithASpace(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	b, err := webp.EncodeLossyToBytes(m, 50)
	if err != nil {
		t.Fatal(err)
	}
	if string(b[12:16]) != "VP8 " {
		t.Errorf("the chunk is named %q", b[12:16])
	}
	// The payload begins at 20: a three-byte frame tag, then the start code.
	if b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
		t.Errorf("the start code is %x %x %x", b[23], b[24], b[25])
	}
	if len(b)%2 != 0 {
		t.Errorf("the file is %d bytes, which is odd", len(b))
	}
}

func TestLossyEncodeWritesToTheWriter(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	m.Set(0, 0, color.NRGBA{1, 2, 3, 255})
	var buf bytes.Buffer
	if err := webp.EncodeLossy(&buf, m, 60); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("nothing was written")
	}
	if err := webp.EncodeLossy(failingWriter{}, m, 60); err == nil {
		t.Error("a failing writer was not reported")
	}
	var sink bytes.Buffer
	if err := webp.EncodeLossy(&sink, image.NewNRGBA(image.Rect(0, 0, 16385, 1)), 60); err == nil {
		t.Error("an image the format cannot describe was accepted")
	}
	if sink.Len() != 0 {
		t.Errorf("a refused encode wrote %d bytes", sink.Len())
	}
}
