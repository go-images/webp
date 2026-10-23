// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

// An op is one step of the coded stream: either the pixel at `at`, or a copy
// of `length` pixels from `distance` pixels back.
type op struct {
	isLiteral bool
	at        int
	length    int
	distance  int
}

const (
	// minMatch is the shortest copy worth making. Two pixels cost a length
	// symbol plus a distance symbol plus their extra bits, which is rarely
	// cheaper than two literals.
	minMatch = 3
	// maxMatch is the format's own ceiling on a backward reference.
	maxMatch = 4096
	// hashBits sizes the match finder's table. 16 bits keeps it small enough
	// to stay in cache on a page-sized image.
	hashBits = 16
	hashSize = 1 << hashBits
	// maxChain bounds how far back the finder looks for a better match. A
	// bound is what keeps a pathological image — one colour, so every position
	// hashes alike — from costing quadratic time.
	maxChain = 32
)

// compress turns the pixels into literals and backward references.
//
// ⛔ THE DISTANCE IS WRITTEN AS A PLAIN SCAN-LINE DISTANCE, offset by 120.
// The format also offers 120 short codes standing for a small two-dimensional
// neighbourhood, which would pay for itself on photographic rows; writing the
// plain form is correct for every distance and is what this encodes today.
// Said here rather than discovered later from a ratio that is a few per cent
// off libwebp's.
func compress(argb []uint32, width int) []op {
	n := len(argb)
	ops := make([]op, 0, n/2+1)

	head := make([]int32, hashSize)
	for i := range head {
		head[i] = -1
	}
	prev := make([]int32, n)

	i := 0
	for i < n {
		bestLen, bestDist := 0, 0
		if i+minMatch <= n {
			h := hash4(argb, i)
			for cand, steps := head[h], 0; cand >= 0 && steps < maxChain; cand, steps = prev[cand], steps+1 {
				dist := i - int(cand)
				if dist <= 0 || dist > maxMatch {
					break
				}
				l := matchLen(argb, int(cand), i, n)
				if l > bestLen {
					bestLen, bestDist = l, dist
					if l >= maxMatch {
						break
					}
				}
			}
		}

		if bestLen >= minMatch {
			ops = append(ops, op{length: bestLen, distance: bestDist})
			// Every position inside the match still has to be registered, or
			// the finder goes blind to everything that follows it.
			for k := 0; k < bestLen; k++ {
				insert(argb, head, prev, i+k, n)
			}
			i += bestLen
			continue
		}
		ops = append(ops, op{isLiteral: true, at: i})
		insert(argb, head, prev, i, n)
		i++
	}
	return ops
}

func insert(argb []uint32, head, prev []int32, i, n int) {
	if i+minMatch > n {
		return
	}
	h := hash4(argb, i)
	prev[i] = head[h]
	head[h] = int32(i)
}

// matchLen is how many pixels agree from a and b onwards.
//
// ⛔ A MATCH MAY OVERLAP ITS OWN SOURCE, and that is not a bug to guard
// against but the mechanism that codes a run of one colour in a few bits: the
// decoder copies pixel by pixel, so a distance of 1 and a length of 400 paints
// four hundred copies of one pixel. The loop therefore compares forwards
// without stopping at b.
func matchLen(argb []uint32, a, b, n int) int {
	l := 0
	for b+l < n && l < maxMatch && argb[a+l] == argb[b+l] {
		l++
	}
	return l
}

// hash4 mixes four consecutive pixels, or fewer at the end of the image.
func hash4(argb []uint32, i int) uint32 {
	h := argb[i]*0x9e3779b1 ^ argb[i+1]*0x85ebca6b
	if i+2 < len(argb) {
		h ^= argb[i+2] * 0xc2b2ae35
	}
	return (h >> (32 - hashBits)) & (hashSize - 1)
}
