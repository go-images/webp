// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

import (
	"math/rand"
	"testing"
)

// kraft reports whether the code lengths form a COMPLETE prefix code: the sum
// of 2^-length over the symbols that occur must be exactly 1.
//
// ⛔ THIS IS WHAT "invalid Huffman tree" MEANS. A decoder builds the tree by
// walking the lengths, and a set that sums to less than 1 leaves a dangling
// branch while one that sums to more than 1 collides. Either is rejected —
// and an encoder can produce both without noticing, because it only ever
// writes codes and never reads them back.
func kraft(codes []code) (num, den int64) {
	den = 1 << maxCodeLength
	for _, c := range codes {
		if c.length > 0 {
			num += 1 << (maxCodeLength - c.length)
		}
	}
	return num, den
}

func checkComplete(t *testing.T, freq []int, why string) {
	t.Helper()
	used := 0
	for _, f := range freq {
		if f > 0 {
			used++
		}
	}
	codes := buildHuffman(freq)
	for i, c := range codes {
		if (freq[i] > 0) != (c.length > 0) {
			t.Fatalf("%s: symbol %d occurs %d times and has length %d", why, i, freq[i], c.length)
		}
		if c.length > maxCodeLength {
			t.Fatalf("%s: symbol %d has length %d, over the %d the format can express",
				why, i, c.length, maxCodeLength)
		}
	}
	if used < 2 {
		return // the one incomplete tree the format allows
	}
	num, den := kraft(codes)
	if num != den {
		t.Fatalf("%s: the code is not complete: sum of 2^-length is %d/%d", why, num, den)
	}
}

// ⛔ THE SKEWED CASES ARE THE ONES THAT BREAK IT. A near-uniform distribution
// gives a balanced tree that no length limit ever touches; it is the
// distributions where one symbol dominates — a page of text, a flat
// background — that push a plain Huffman tree past fifteen bits and make the
// length-limiting code run at all.
func TestEveryPrefixCodeIsComplete(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for _, size := range []int{2, 3, 17, 40, 256, 280} {
		for round := 0; round < 400; round++ {
			freq := make([]int, size)
			switch round % 4 {
			case 0: // a handful of symbols
				for i := 0; i < 1+r.Intn(5) && i < size; i++ {
					freq[r.Intn(size)] = 1 + r.Intn(10)
				}
			case 1: // one symbol dominating everything
				freq[r.Intn(size)] = 1 << 20
				for i := 0; i < size; i++ {
					if r.Intn(3) == 0 {
						freq[i] = 1
					}
				}
			case 2: // every symbol, wildly different weights
				for i := range freq {
					freq[i] = 1 << uint(r.Intn(24))
				}
			case 3: // every symbol, nearly equal
				for i := range freq {
					freq[i] = 1000 + r.Intn(3)
				}
			}
			if allZero(freq) {
				continue
			}
			checkComplete(t, freq, "random")
		}
	}
}

// A geometric distribution is the worst realistic case for the depth limit:
// the rarest symbol wants a code far longer than fifteen bits.
func TestADeepDistributionIsStillLimitedToFifteenBits(t *testing.T) {
	freq := make([]int, 280)
	w := 1 << 30
	for i := range freq {
		freq[i] = w
		if w > 1 {
			w /= 2
		}
	}
	checkComplete(t, freq, "geometric")
}

func allZero(f []int) bool {
	for _, v := range f {
		if v != 0 {
			return false
		}
	}
	return true
}
