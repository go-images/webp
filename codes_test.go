// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

import (
	"image"
	"math/rand"
	"testing"
)

// ⛔ THE CODE-LENGTH CODE IS A PREFIX CODE TOO, read by the very same routine
// in the decoder — so every rule that governs the five codes governs it as
// well, including the one that says a code with a SINGLE symbol is reached
// having read no bits. Checking only the five leaves that one unexamined, and
// it is built from frequencies nobody chose directly.
//
// This walks images, takes the codes the encoder would actually write, and
// checks each one is something a decoder can build.
func TestEveryCodeAnImageProducesIsBuildable(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	for round := 0; round < 300; round++ {
		w := 1 + r.Intn(60)
		h := 1 + r.Intn(60)
		m := image.NewNRGBA(image.Rect(0, 0, w, h))
		switch round % 5 {
		case 0: // one colour
			for i := range m.Pix {
				m.Pix[i] = []uint8{0x30, 0x30, 0x30, 0xff}[i%4]
			}
		case 1: // mostly one colour, a few others
			for i := range m.Pix {
				m.Pix[i] = 0x30
			}
			for k := 0; k < 1+r.Intn(8); k++ {
				m.Pix[r.Intn(len(m.Pix))] = uint8(r.Intn(256))
			}
		case 2: // noise
			for i := range m.Pix {
				m.Pix[i] = uint8(r.Intn(256))
			}
		case 3: // a few values, constant alpha
			for i := range m.Pix {
				if i%4 == 3 {
					m.Pix[i] = 0xff
					continue
				}
				m.Pix[i] = uint8(r.Intn(3) * 0x40)
			}
		case 4: // long runs
			v := uint8(0)
			for i := 0; i < len(m.Pix); i += 4 {
				if r.Intn(20) == 0 {
					v = uint8(r.Intn(256))
				}
				m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = v, v, v, 0xff
			}
		}

		argb := toARGB(m, w, h)
		ops := compress(argb, w)
		green, red, blue, alpha, dist := buildCodes(argb, ops)
		for name, p := range map[string]prefixCode{
			"green": green, "red": red, "blue": blue, "alpha": alpha, "distance": dist,
		} {
			checkBuildable(t, name, p)
		}
	}
}

// checkBuildable asserts the code is one a decoder can build: either a single
// symbol, which costs no bits, or a complete prefix code.
func checkBuildable(t *testing.T, name string, p prefixCode) {
	t.Helper()
	if p.single >= 0 {
		return
	}
	used := 0
	for _, c := range p.codes {
		if c.length > 0 {
			used++
		}
	}
	if used == 0 {
		t.Fatalf("%s: no symbol has a length and the code is not marked single", name)
	}
	if used == 1 {
		t.Fatalf("%s: exactly one symbol has a length but the code is not marked single, "+
			"so the encoder will write a bit the decoder does not read", name)
	}
	num, den := kraft(p.codes)
	if num != den {
		t.Fatalf("%s: the code is not complete: sum of 2^-length is %d/%d", name, num, den)
	}

	// And the code-length code the writer will derive from it, which the
	// decoder builds with the same routine and the same rules.
	checkCodeLengthCode(t, name, p.codes)
}

func checkCodeLengthCode(t *testing.T, name string, codes []code) {
	t.Helper()
	freq := codeLengthFrequencies(codes)
	n := 0
	for _, f := range freq {
		if f > 0 {
			n++
		}
	}
	if n == 0 {
		t.Fatalf("%s: its code-length code has no symbols", name)
	}
	if n == 1 {
		// Allowed, and the writer spends no bits on it — the case fuzzing
		// found. singleSymbol is what the writer asks, so the test asks it too.
		if singleSymbol(freq) < 0 {
			t.Fatalf("%s: one symbol occurs and singleSymbol did not say so", name)
		}
		return
	}
	cl := buildHuffmanLimited(freq, maxCodeLengthCodeLength)
	// ⛔ SEVEN BITS, because each of these lengths is written in a THREE-BIT
	// field. A length of 8 is written as 0, which says "absent" — so the
	// decoder builds a different tree and blames the image. The bit writer
	// masks silently, so nothing else catches it.
	for sym, c := range cl {
		if c.length > maxCodeLengthCodeLength {
			t.Fatalf("%s: its code-length code gives symbol %d a length of %d, "+
				"and the field it is written in holds %d",
				name, sym, c.length, maxCodeLengthCodeLength)
		}
	}
	num, den := kraft(cl)
	if num != den {
		t.Fatalf("%s: its code-length code is not complete: %d/%d", name, num, den)
	}
}
