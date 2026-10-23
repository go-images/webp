// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

import "testing"

// ⛔ THE QUANTISER RULES ARE TESTED DIRECTLY, because end to end they are
// nearly invisible. The encoder and the decoder each derive these factors from
// the same index, so a disagreement scales the reconstruction slightly — and
// at the coarse qualities where the difference exists, the levels are mostly
// 0 and ±1, so a picture measure barely moves. A mutation changing the
// chrominance ceiling from 117 to 127 survived the whole suite, including a
// test written specifically to catch it with PSNR.
//
// Section 9.6 states these, and they are worth stating back:
//
//   - the second-order DC factor is TWICE the luma one;
//   - the second-order AC factor is 155/100 of the luma one, with a floor of 8;
//   - the chrominance DC factor is clamped at index 117, not 127, because the
//     specification clamps the value at 132 and that is dequantTableDC[117].
func TestTheQuantiserRules(t *testing.T) {
	for _, q := range []uint32{0, 1, 40, 100, 116, 117, 118, 126, 127} {
		got := quantiserFor(q)
		i := int(q)

		if want := dequantTableDC[i]; got.y1[0] != want {
			t.Errorf("q=%d: the luma DC factor is %d, wanted %d", q, got.y1[0], want)
		}
		if want := dequantTableAC[i]; got.y1[1] != want {
			t.Errorf("q=%d: the luma AC factor is %d, wanted %d", q, got.y1[1], want)
		}
		if want := dequantTableDC[i] * 2; got.y2[0] != want {
			t.Errorf("q=%d: the second-order DC factor is %d, wanted twice the luma one, %d",
				q, got.y2[0], want)
		}
		want2 := dequantTableAC[i] * 155 / 100
		if want2 < 8 {
			want2 = 8
		}
		if got.y2[1] != want2 {
			t.Errorf("q=%d: the second-order AC factor is %d, wanted %d", q, got.y2[1], want2)
		}

		// The one with the different ceiling.
		ci := i
		if ci > 117 {
			ci = 117
		}
		if want := dequantTableDC[ci]; got.uv[0] != want {
			t.Errorf("q=%d: the chrominance DC factor is %d, wanted %d (index %d)",
				q, got.uv[0], want, ci)
		}
		if want := dequantTableAC[i]; got.uv[1] != want {
			t.Errorf("q=%d: the chrominance AC factor is %d, wanted %d", q, got.uv[1], want)
		}
	}

	// And the ceiling must actually CLAMP something: if the table were flat up
	// there, the rule would be untestable and this test would be theatre.
	if dequantTableDC[127] == dequantTableDC[117] {
		t.Fatal("the dequantisation table does not change between 117 and 127, " +
			"so the chrominance ceiling cannot be observed")
	}
	if quantiserFor(127).uv[0] != dequantTableDC[117] {
		t.Error("the chrominance DC factor at the coarsest quality is not the clamped one")
	}

	// The second-order AC floor, which only shows at the finest qualities.
	if quantiserFor(0).y2[1] < 8 {
		t.Error("the second-order AC factor fell below its floor of 8")
	}
}

// The quality a caller gives is mapped onto the index the format uses, and the
// two run in OPPOSITE directions: quality 100 is index 0.
func TestQualityMapsOntoTheIndexBackwards(t *testing.T) {
	if got := qIndexFor(100); got != 0 {
		t.Errorf("quality 100 gave index %d, wanted the finest, 0", got)
	}
	if got := qIndexFor(0); got != 127 {
		t.Errorf("quality 0 gave index %d, wanted the coarsest, 127", got)
	}
	last := uint32(0)
	for q := 100; q >= 0; q-- {
		got := qIndexFor(q)
		if got < last {
			t.Fatalf("quality %d gave index %d, after %d: the mapping is not monotonic", q, got, last)
		}
		last = got
	}
}
