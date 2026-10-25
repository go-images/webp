// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ⛔ THIS FILE EXISTS BECAUSE AN INDEPENDENT IMPLEMENTATION IS NOT THE SAME AS
// THE REFERENCE ONE. Every other test here judges with
// golang.org/x/image/webp, which is a separate reading of the specification by
// other people — a real oracle, and it caught real defects. But it is written
// in Go, and it shares Go's convention for the luma RANGE: full scale, 0..255.
//
// libwebp, which is what every browser ships, reads luma as studio range,
// 16..235. This encoder wrote Go's convention for a whole release. The suite
// agreed with it, because judge and subject shared the mistake, and the only
// symptom was a figure that stopped improving however fine the quantiser got.
// Measured against libwebp's own decoder, a source grey of 20 came back as 5.
//
// So: when libwebp's tools are present, they judge. CI installs them.
func TestTheReferenceDecoderAgreesAboutColour(t *testing.T) {
	dwebp, err := exec.LookPath("dwebp")
	if err != nil {
		t.Skip("dwebp is not installed; this is the one check x/image cannot make")
	}
	dir := t.TempDir()

	// ⛔ GREYS, because a grey is where a range mismatch is unmistakable and
	// where chrominance cannot be blamed: Cb and Cr are both 128 for every
	// one of these, so only the luma scale is under test.
	for _, v := range []uint8{0, 20, 64, 128, 200, 255} {
		m := image.NewNRGBA(image.Rect(0, 0, 32, 32))
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				m.Set(x, y, color.NRGBA{v, v, v, 255})
			}
		}
		b, err := EncodeLossyToBytes(m, 100)
		if err != nil {
			t.Fatal(err)
		}
		in := filepath.Join(dir, "grey.webp")
		out := filepath.Join(dir, "grey.png")
		if err := os.WriteFile(in, b, 0o644); err != nil {
			t.Fatal(err)
		}
		if o, err := exec.Command(dwebp, in, "-o", out).CombinedOutput(); err != nil {
			t.Fatalf("the reference decoder refused a frame: %v: %s", err, o)
		}
		f, err := os.Open(out)
		if err != nil {
			t.Fatal(err)
		}
		got, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		r, g, bb, _ := got.At(16, 16).RGBA()
		for name, c := range map[string]uint32{"red": r >> 8, "green": g >> 8, "blue": bb >> 8} {
			d := int(c) - int(v)
			if d < 0 {
				d = -d
			}
			// At the finest quantiser the only loss left is the transform's
			// own rounding and the colour round trip, which is a level or two.
			if d > 3 {
				t.Errorf("a grey of %d came back from the reference decoder with %s = %d, "+
					"%d levels out — the luma range convention does not match", v, name, c, d)
			}
		}
	}
}

// ⛔ AND THE CONVENTION IS PINNED WITHOUT ANY DECODER AT ALL, so that it is
// held even where libwebp is not installed. Studio range means black is 16 and
// white is 235, and the three coefficient rows sum to the ranges they span.
func TestTheConversionIsStudioRange(t *testing.T) {
	y, cb, cr := rgbToYCbCr(0, 0, 0)
	if y != 16 || cb != 128 || cr != 128 {
		t.Errorf("black converts to Y=%d Cb=%d Cr=%d, wanted 16, 128, 128", y, cb, cr)
	}
	y, cb, cr = rgbToYCbCr(255, 255, 255)
	if y != 235 || cb != 128 || cr != 128 {
		t.Errorf("white converts to Y=%d Cb=%d Cr=%d, wanted 235, 128, 128", y, cb, cr)
	}
	// Full range would put black at 0 and white at 255, which is the mistake
	// this is here to prevent coming back.
	if y2, _, _ := rgbToYCbCr(0, 0, 0); y2 == 0 {
		t.Error("black converts to 0, which is the FULL-range convention Go uses and " +
			"libwebp does not")
	}

	// A grey stays grey: no chrominance is invented by the rounding.
	for v := 0; v < 256; v++ {
		_, cb, cr := rgbToYCbCr(uint8(v), uint8(v), uint8(v))
		if cb != 128 || cr != 128 {
			t.Fatalf("the grey %d produced chrominance %d, %d", v, cb, cr)
		}
	}

	// The primaries land where BT.601 says, within the rounding.
	for _, c := range []struct {
		name      string
		r, g, b   uint8
		y, cb, cr int
	}{
		{"red", 255, 0, 0, 81, 90, 240},
		{"green", 0, 255, 0, 145, 54, 34},
		{"blue", 0, 0, 255, 41, 240, 110},
	} {
		y, cb, cr := rgbToYCbCr(c.r, c.g, c.b)
		if abs3(int(y)-c.y) > 2 || abs3(int(cb)-c.cb) > 2 || abs3(int(cr)-c.cr) > 2 {
			t.Errorf("%s converts to %d, %d, %d; BT.601 studio range says about %d, %d, %d",
				c.name, y, cb, cr, c.y, c.cb, c.cr)
		}
	}
}

func abs3(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
