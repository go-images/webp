// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"math/rand"
	"testing"

	"github.com/go-images/webp"
	xwebp "golang.org/x/image/webp"
)

// ⛔ THE ORACLE IS SOMEBODY ELSE'S DECODER. A round trip through our own
// reader would agree with our own writer by construction — the two would share
// every misreading of the specification, and the bit order is exactly the kind
// of thing both halves get wrong together. golang.org/x/image/webp was written
// from the same specification by other people, so a disagreement is a real
// one.
//
// And because the format is LOSSLESS, the comparison is exact: not a
// tolerance, not a PSNR, every pixel or nothing.
func roundTrip(t *testing.T, src image.Image) {
	t.Helper()
	var buf bytes.Buffer
	if err := webp.Encode(&buf, src); err != nil {
		t.Fatalf("encoding: %v", err)
	}
	got, err := xwebp.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("an independent decoder refused what we wrote: %v", err)
	}
	b := src.Bounds()
	if got.Bounds().Dx() != b.Dx() || got.Bounds().Dy() != b.Dy() {
		t.Fatalf("decoded %v, encoded %v", got.Bounds(), b)
	}
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			wr, wg, wb, wa := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			gr, gg, gb, ga := got.At(got.Bounds().Min.X+x, got.Bounds().Min.Y+y).RGBA()
			if wr != gr || wg != gg || wb != gb || wa != ga {
				t.Fatalf("pixel (%d,%d): encoded %v,%v,%v,%v and decoded %v,%v,%v,%v",
					x, y, wr, wg, wb, wa, gr, gg, gb, ga)
			}
		}
	}
}

func TestOnePixel(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	m.Set(0, 0, color.NRGBA{1, 2, 3, 255})
	roundTrip(t, m)
}

// A single colour is the case where every position hashes alike and every
// match overlaps its own source — the shape that makes a naive match finder
// quadratic and a naive copy wrong.
func TestOneColour(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := range m.Pix {
		m.Pix[i] = []uint8{0x20, 0x40, 0x60, 0xff}[i%4]
	}
	roundTrip(t, m)
}

func TestGradient(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 97, 61))
	for y := 0; y < 61; y++ {
		for x := 0; x < 97; x++ {
			m.Set(x, y, color.NRGBA{uint8(x * 2), uint8(y * 4), uint8(x + y), 255})
		}
	}
	roundTrip(t, m)
}

// ⛔ TRANSLUCENT PIXELS ARE WHERE PREMULTIPLICATION SHOWS. An encoder that
// read pixels through Color.RGBA would store premultiplied values as straight
// ones: every opaque image would still be perfect and every translucent one
// would come back darker.
func TestTranslucent(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			m.Set(x, y, color.NRGBA{200, 100, 50, uint8(x * 8)})
		}
	}
	roundTrip(t, m)
}

// Noise defeats the match finder, so this is the path where almost every
// symbol is a literal and the prefix codes are nearly flat.
func TestNoise(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	m := image.NewNRGBA(image.Rect(0, 0, 73, 37))
	for i := range m.Pix {
		m.Pix[i] = uint8(r.Intn(256))
	}
	roundTrip(t, m)
}

// Two colours give a very skewed distribution, which is what pushes a plain
// Huffman tree past the fifteen bits the format can express.
func TestTwoColours(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	m := image.NewNRGBA(image.Rect(0, 0, 128, 128))
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			if r.Intn(1000) == 0 {
				m.Set(x, y, color.NRGBA{255, 255, 255, 255})
			} else {
				m.Set(x, y, color.NRGBA{0, 0, 0, 255})
			}
		}
	}
	roundTrip(t, m)
}

// Sizes around the byte and chunk boundaries, including the odd widths that
// exercise the container's padding byte.
func TestManySizes(t *testing.T) {
	for _, c := range []struct{ w, h int }{
		{1, 1}, {1, 2}, {2, 1}, {3, 3}, {7, 5}, {8, 8},
		{15, 1}, {1, 15}, {16, 16}, {17, 33}, {64, 1}, {1, 64}, {100, 100},
	} {
		r := rand.New(rand.NewSource(int64(c.w*1000 + c.h)))
		m := image.NewNRGBA(image.Rect(0, 0, c.w, c.h))
		for i := range m.Pix {
			m.Pix[i] = uint8(r.Intn(8) * 32)
		}
		m2 := m
		t.Run("", func(t *testing.T) { roundTrip(t, m2) })
	}
}

// An image that is not already NRGBA goes through a conversion, and the
// conversion is where premultiplied values would creep back in.
func TestOtherImageTypes(t *testing.T) {
	gray := image.NewGray(image.Rect(0, 0, 20, 20))
	for i := range gray.Pix {
		gray.Pix[i] = uint8(i)
	}
	roundTrip(t, gray)

	rgba := image.NewRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			rgba.Set(x, y, color.RGBA{uint8(x * 10), uint8(y * 10), 30, 255})
		}
	}
	roundTrip(t, rgba)
}

// An image whose bounds do not start at the origin is a sub-image, and the
// encoder must read it from its own minimum rather than from (0,0).
func TestAnOffsetSubImage(t *testing.T) {
	full := image.NewNRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			full.Set(x, y, color.NRGBA{uint8(x * 6), uint8(y * 6), 128, 255})
		}
	}
	roundTrip(t, full.SubImage(image.Rect(10, 10, 30, 25)))
}

func TestRefusals(t *testing.T) {
	if _, err := webp.EncodeToBytes(image.NewNRGBA(image.Rect(0, 0, 0, 10))); err == nil {
		t.Error("an image with no pixels was accepted")
	}
	if _, err := webp.EncodeToBytes(image.NewNRGBA(image.Rect(0, 0, 16385, 1))); err == nil {
		t.Error("an image wider than the format can describe was accepted")
	}
	if _, err := webp.EncodeToBytes(image.NewNRGBA(image.Rect(0, 0, 1, 16385))); err == nil {
		t.Error("an image taller than the format can describe was accepted")
	}
	// The largest side the header CAN describe is accepted.
	if _, err := webp.EncodeToBytes(image.NewNRGBA(image.Rect(0, 0, 16384, 1))); err != nil {
		t.Errorf("the largest describable width was refused: %v", err)
	}
	// Encode reports the same refusals rather than writing a broken file to
	// the writer it was handed.
	var sink bytes.Buffer
	if err := webp.Encode(&sink, image.NewNRGBA(image.Rect(0, 0, 16385, 1))); err == nil {
		t.Error("Encode accepted an image the format cannot describe")
	}
	if sink.Len() != 0 {
		t.Errorf("a refused Encode wrote %d bytes", sink.Len())
	}
}

// A writer that fails partway is reported rather than swallowed.
func TestAWriterThatFailsIsReported(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	if err := webp.Encode(failingWriter{}, m); err == nil {
		t.Error("a failing writer was not reported")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errWrite }

var errWrite = errors.New("no")

// The container, read back by hand, because a decoder that is lenient about
// the size fields would hide a wrong one.
func TestTheContainerSaysWhatItHolds(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 9, 9))
	b, err := webp.EncodeToBytes(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" || string(b[12:16]) != "VP8L" {
		t.Fatalf("the header is %q", b[:16])
	}
	fileSize := uint32(b[4]) | uint32(b[5])<<8 | uint32(b[6])<<16 | uint32(b[7])<<24
	if int(fileSize) != len(b)-8 {
		t.Errorf("the file size field says %d and the file is %d bytes after it", fileSize, len(b)-8)
	}
	chunk := uint32(b[16]) | uint32(b[17])<<8 | uint32(b[18])<<16 | uint32(b[19])<<24
	// The payload starts at 20; a chunk of odd length is followed by one
	// padding byte that its size does not count.
	padded := int(chunk)
	if padded%2 == 1 {
		padded++
	}
	if 20+padded != len(b) {
		t.Errorf("the chunk says %d bytes and the file has %d after the chunk header", chunk, len(b)-20)
	}
	if b[20] != 0x2f {
		t.Errorf("the lossless signature is 0x%02x", b[20])
	}
	if len(b)%2 != 0 {
		t.Errorf("the file is %d bytes, which is odd: every chunk is padded to an even length", len(b))
	}
}
