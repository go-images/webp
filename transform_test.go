// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

import (
	"math/rand"
	"testing"
)

// ⛔ THE FORWARD TRANSFORM IS CHECKED AGAINST THE INVERSE THAT WILL BE
// APPLIED, not against a table of expected coefficients. A wrong constant in a
// hand-copied forward DCT does not fail to build and does not fail to decode:
// it makes every picture softer, by an amount nobody can attribute. Running
// the decoder's own inverse over what the forward produced says, in pixels,
// whether the two are inverses at all.
func TestTheForwardDCTIsInvertedByTheDecodersInverse(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	worst := 0
	for round := 0; round < 2000; round++ {
		var src, pred [16]uint8
		for i := range src {
			pred[i] = uint8(r.Intn(256))
			// A residual of a real block is small; the extremes are included
			// so that clipping is exercised too.
			v := int(pred[i]) + r.Intn(61) - 30
			src[i] = clip8(int32(v))
		}
		var res [16]int32
		for i := range res {
			res[i] = int32(src[i]) - int32(pred[i])
		}

		coeff := forwardDCT(&res)
		got := inverseDCT(&coeff, &pred)
		for i := range got {
			d := int(got[i]) - int(src[i])
			if d < 0 {
				d = -d
			}
			if d > worst {
				worst = d
			}
		}
	}
	// ⛔ A TOLERANCE, AND A STATED ONE. The transform is integer and lossy by
	// a little even at quantiser one: the inverse rounds twice, once per pass.
	// Two levels is what that costs; more than that means the forward is not
	// the inverse of this inverse.
	if worst > 2 {
		t.Errorf("a round trip through the transform moved a pixel by %d levels", worst)
	}
	t.Logf("worst pixel error through the transform: %d", worst)
}

// The second-order transform carries the sixteen luma DC values, and the same
// relation has to hold for it.
func TestTheForwardWHTIsInvertedByTheDecodersInverse(t *testing.T) {
	r := rand.New(rand.NewSource(6))
	worst := 0
	for round := 0; round < 2000; round++ {
		var res [16]int32
		for i := range res {
			// A DC value after the first transform, in the range one occupies.
			res[i] = int32(r.Intn(4096) - 2048)
		}
		coeff := forwardWHT(&res)
		got := inverseWHT(&coeff)
		for i := range got {
			d := int(got[i]) - int(res[i])
			if d < 0 {
				d = -d
			}
			if d > worst {
				worst = d
			}
		}
	}
	if worst > 2 {
		t.Errorf("a round trip through the Walsh-Hadamard transform moved a value by %d", worst)
	}
	t.Logf("worst value error through the Walsh-Hadamard transform: %d", worst)
}

// ⛔ The Walsh-Hadamard matrix has a ZERO where a textbook elimination puts
// its pivot, so inverting it without pivoting divides by zero and fills the
// matrix with NaN — which then rounds to zero and produces an encoder that
// writes blank chrominance. Pivoting is why invert4 has that loop, and this
// asserts the matrices came out finite and are genuinely inverses.
func TestTheDerivedMatricesAreInverses(t *testing.T) {
	for name, pair := range map[string]struct {
		fwd [4][4]float64
		inv func([4]float64) [4]float64
	}{
		"DCT": {fwdDCT, idct1D},
		"WHT": {fwdWHT, wht1D},
	} {
		m := matrixOf(pair.inv)
		for i := 0; i < 4; i++ {
			for j := 0; j < 4; j++ {
				var s float64
				for k := 0; k < 4; k++ {
					s += m[i][k] * pair.fwd[k][j]
				}
				want := 0.0
				if i == j {
					want = 1
				}
				if d := s - want; d > 1e-9 || d < -1e-9 {
					t.Errorf("%s: the product is %v at (%d,%d), wanted %v", name, s, i, j, want)
				}
			}
		}
	}
}
