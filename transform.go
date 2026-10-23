// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

// VP8's transforms. The decoder's INVERSE is the normative one (RFC 6386
// sections 14.3 and 14.4); the forward is the encoder's business, and this one
// is derived from the inverse rather than copied from anywhere.
//
// ⛔ THE FORWARD MATRIX IS COMPUTED BY INVERTING THE INVERSE, at startup, in
// floating point. Writing the usual integer constants from memory is how an
// encoder ends up almost right: the frames still decode, the pictures are
// merely soft, and nothing says which of a dozen magic numbers is wrong.
// Inverting the transform that is actually applied cannot be off by a
// constant, and a test checks the round trip on random blocks.

const (
	// The decoder's rotation constants, as fractions of 65536.
	idctC1 = 85627.0 / 65536.0 // cos(pi/8) * sqrt(2)
	idctC2 = 35468.0 / 65536.0 // sin(pi/8) * sqrt(2)
)

// idct1D is one pass of the decoder's inverse, in exact arithmetic.
func idct1D(c [4]float64) [4]float64 {
	a := c[0] + c[2]
	b := c[0] - c[2]
	cc := c[1]*idctC2 - c[3]*idctC1
	d := c[1]*idctC1 + c[3]*idctC2
	return [4]float64{a + d, b + cc, b - cc, a - d}
}

// wht1D is one pass of the decoder's inverse Walsh-Hadamard transform.
func wht1D(c [4]float64) [4]float64 {
	a0 := c[0] + c[3]
	a1 := c[1] + c[2]
	a2 := c[1] - c[2]
	a3 := c[0] - c[3]
	return [4]float64{a0 + a1, a3 + a2, a0 - a1, a3 - a2}
}

// fwdDCT and fwdWHT are the inverses of those passes: the matrices that take a
// residual back to coefficients.
var (
	fwdDCT = invert4(matrixOf(idct1D))
	fwdWHT = invert4(matrixOf(wht1D))
)

// matrixOf is the matrix a linear pass applies, read off its action on the
// unit vectors.
func matrixOf(pass func([4]float64) [4]float64) [4][4]float64 {
	var m [4][4]float64
	for j := 0; j < 4; j++ {
		var e [4]float64
		e[j] = 1
		col := pass(e)
		for i := 0; i < 4; i++ {
			m[i][j] = col[i]
		}
	}
	return m
}

// invert4 inverts a 4x4 matrix by Gauss-Jordan elimination.
func invert4(m [4][4]float64) [4][4]float64 {
	var a [4][8]float64
	for i := 0; i < 4; i++ {
		copy(a[i][:4], m[i][:])
		a[i][4+i] = 1
	}
	for col := 0; col < 4; col++ {
		// Partial pivoting, because the Walsh-Hadamard matrix has a zero in
		// the natural pivot position and a textbook elimination divides by it.
		best := col
		for r := col + 1; r < 4; r++ {
			if abs64(a[r][col]) > abs64(a[best][col]) {
				best = r
			}
		}
		a[col], a[best] = a[best], a[col]
		p := a[col][col]
		for k := 0; k < 8; k++ {
			a[col][k] /= p
		}
		for r := 0; r < 4; r++ {
			if r == col {
				continue
			}
			f := a[r][col]
			for k := 0; k < 8; k++ {
				a[r][k] -= f * a[col][k]
			}
		}
	}
	var out [4][4]float64
	for i := 0; i < 4; i++ {
		copy(out[i][:], a[i][4:])
	}
	return out
}

func abs64(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// forwardDCT turns a 4x4 block of residuals into coefficients.
//
// The decoder reconstructs out = (M C M^T) / 8, so C = 8 * Minv R Minv^T.
func forwardDCT(res *[16]int32) [16]int32 {
	return forward(res, &fwdDCT, 8)
}

// forwardWHT turns the sixteen luma DC values into the second-order block.
//
// The decoder reconstructs out = (H C H^T) / 8 as well, with H the
// Walsh-Hadamard pass, so the same relation holds.
func forwardWHT(res *[16]int32) [16]int32 {
	return forward(res, &fwdWHT, 8)
}

func forward(res *[16]int32, minv *[4][4]float64, scale float64) [16]int32 {
	// t = Minv * R
	var t [4][4]float64
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			var s float64
			for k := 0; k < 4; k++ {
				s += minv[i][k] * float64(res[k*4+j])
			}
			t[i][j] = s
		}
	}
	// out = scale * t * Minv^T
	var out [16]int32
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			var s float64
			for k := 0; k < 4; k++ {
				s += t[i][k] * minv[j][k]
			}
			out[i*4+j] = roundToInt(s * scale)
		}
	}
	return out
}

func roundToInt(x float64) int32 {
	if x < 0 {
		return -int32(-x + 0.5)
	}
	return int32(x + 0.5)
}

// inverseDCT is the decoder's own inverse, in the decoder's integer
// arithmetic, added onto a prediction.
//
// ⛔ THE ENCODER MUST RECONSTRUCT EXACTLY AS THE DECODER DOES, because the
// next macroblock is predicted from these pixels. Predicting from the ORIGINAL
// image instead would drift: every macroblock would be predicted from
// something the decoder never had, and the error would accumulate down the
// picture rather than stay inside one block.
func inverseDCT(coeff *[16]int32, pred *[16]uint8) [16]uint8 {
	const c1, c2 = 85627, 35468
	var m [4][4]int32
	for i := 0; i < 4; i++ {
		a := coeff[0+i] + coeff[8+i]
		b := coeff[0+i] - coeff[8+i]
		c := (coeff[4+i]*c2)>>16 - (coeff[12+i]*c1)>>16
		d := (coeff[4+i]*c1)>>16 + (coeff[12+i]*c2)>>16
		m[i][0] = a + d
		m[i][1] = b + c
		m[i][2] = b - c
		m[i][3] = a - d
	}
	var out [16]uint8
	for j := 0; j < 4; j++ {
		dc := m[0][j] + 4
		a := dc + m[2][j]
		b := dc - m[2][j]
		c := (m[1][j]*c2)>>16 - (m[3][j]*c1)>>16
		d := (m[1][j]*c1)>>16 + (m[3][j]*c2)>>16
		out[j*4+0] = clip8(int32(pred[j*4+0]) + (a+d)>>3)
		out[j*4+1] = clip8(int32(pred[j*4+1]) + (b+c)>>3)
		out[j*4+2] = clip8(int32(pred[j*4+2]) + (b-c)>>3)
		out[j*4+3] = clip8(int32(pred[j*4+3]) + (a-d)>>3)
	}
	return out
}

// inverseWHT is the decoder's inverse second-order transform: it spreads the
// sixteen DC values back over the sixteen luma blocks.
func inverseWHT(coeff *[16]int32) [16]int32 {
	var m [16]int32
	for i := 0; i < 4; i++ {
		a0 := coeff[0+i] + coeff[12+i]
		a1 := coeff[4+i] + coeff[8+i]
		a2 := coeff[4+i] - coeff[8+i]
		a3 := coeff[0+i] - coeff[12+i]
		m[0+i] = a0 + a1
		m[8+i] = a0 - a1
		m[4+i] = a3 + a2
		m[12+i] = a3 - a2
	}
	var out [16]int32
	for i := 0; i < 4; i++ {
		dc := m[0+i*4] + 3
		a0 := dc + m[3+i*4]
		a1 := m[1+i*4] + m[2+i*4]
		a2 := m[1+i*4] - m[2+i*4]
		a3 := dc - m[3+i*4]
		out[i*4+0] = (a0 + a1) >> 3
		out[i*4+1] = (a3 + a2) >> 3
		out[i*4+2] = (a0 - a1) >> 3
		out[i*4+3] = (a3 - a2) >> 3
	}
	return out
}

func clip8(x int32) uint8 {
	if x < 0 {
		return 0
	}
	if x > 255 {
		return 255
	}
	return uint8(x)
}
