// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

import "sort"

// maxCodeLength is the longest prefix code the format can express: a code
// length is written as a value 0..15.
const maxCodeLength = 15

// maxCodeLengthCodeLength is the longest code the CODE-LENGTH code may use.
//
// ⛔ SEVEN, NOT FIFTEEN, and the difference is a silent corruption. The
// code-length code describes the other codes, and its own lengths are written
// as THREE-BIT values — so a length of 8 is written as 0, which the decoder
// reads as "this symbol is absent". The tree it then builds is not the tree
// that was intended, and it reports "invalid Huffman tree" about an image
// whose five real codes are all perfectly well formed.
//
// Nothing warns about it: the bit writer masks to the width it is given, so
// the overlong length is truncated rather than refused. Found by fuzzing, on
// an image whose red, blue and alpha channels were almost entirely one value
// with a handful of outliers — exactly the skew that drives a 19-symbol tree
// past seven bits.
const maxCodeLengthCodeLength = 7

// A code is one symbol's prefix code.
type code struct {
	bits   uint32
	length uint8
}

// A prefixCode is one of the five codes in a group.
//
// ⛔ `single` IS NOT AN OPTIMISATION. When exactly one symbol occurs, the
// decoder builds a tree whose only leaf is reached having read NO bits at all
// — both the simple and the normal form end at `insert(symbol, 0, 0)`. An
// encoder that gives that symbol a one-bit code writes one bit per pixel that
// the decoder never consumes, and the stream desynchronises a few symbols
// later. The symptom is not "wrong colour": it is a tree error, or LZ77
// parameters out of range, reported about something entirely innocent further
// down the file. Found by encoding an image whose alpha channel was constant.
type prefixCode struct {
	codes []code
	// single is the only symbol that occurs, or -1. A code with a single
	// symbol writes nothing at all.
	single int
}

// put writes one symbol.
func (p prefixCode) put(w *bitWriter, sym int) {
	if p.single >= 0 {
		return // the decoder reaches this symbol having read nothing
	}
	c := p.codes[sym]
	w.writeCode(c.bits, c.length)
}

// buildPrefixCode returns the prefix code for the given symbol frequencies.
func buildPrefixCode(freq []int) prefixCode {
	out := prefixCode{codes: buildHuffman(freq), single: singleSymbol(freq)}
	if out.single < 0 && allZeroFreq(freq) {
		// No symbol at all is written as the one-symbol form naming symbol 0,
		// which is equally free: the stream never asks this code for anything.
		out.single = 0
	}
	return out
}

// buildHuffman returns a canonical, length-limited prefix code for the given
// symbol frequencies. A symbol whose frequency is zero gets length 0 and is
// never written.
//
// ⛔ THE LENGTHS ARE LIMITED TO 15 BITS, and that is not a nicety: a code
// length is written as a 4-bit-addressable value 0..15, so a tree one bit
// deeper cannot be expressed at all. A plain Huffman tree over 280 symbols
// reaches 16 bits as soon as the distribution is skewed enough — which a page
// of text is — so the limit has to be imposed while building rather than
// checked afterwards.
func buildHuffman(freq []int) []code { return buildHuffmanLimited(freq, maxCodeLength) }

// buildHuffmanLimited is buildHuffman with the depth limit stated, because the
// code-length code may not go past seven.
func buildHuffmanLimited(freq []int, limit int) []code {
	out := make([]code, len(freq))

	// How many symbols actually occur decides the shape of everything below.
	var used []int
	for i, f := range freq {
		if f > 0 {
			used = append(used, i)
		}
	}
	switch len(used) {
	case 0:
		return out
	case 1:
		// ⛔ A SINGLE SYMBOL IS THE ONE INCOMPLETE TREE THE FORMAT ALLOWS, and
		// its length is 1 rather than 0: a length of 0 means "absent", and a
		// tree with no present symbol cannot code the symbol that does occur.
		out[used[0]] = code{bits: 0, length: 1}
		return out
	}

	lengths := packageMerge(freq, used, limit)
	for i, l := range lengths {
		out[i].length = l
	}
	assignCanonical(out)
	return out
}

// packageMerge gives each used symbol a code length, none longer than
// maxCodeLength, minimising the total encoded size under that constraint.
//
// It is the package-merge algorithm: build maxCodeLength levels, each one the
// previous level's packages merged with the original coins, then count how
// many times each symbol appears in the cheapest 2n-2 packages of the last
// level. That count IS its code length.
func packageMerge(freq []int, used []int, limit int) []uint8 {
	type coin struct {
		weight int
		syms   []int // which symbols this package is made of
	}
	base := make([]coin, 0, len(used))
	for _, s := range used {
		base = append(base, coin{weight: freq[s], syms: []int{s}})
	}
	sort.Slice(base, func(i, j int) bool { return base[i].weight < base[j].weight })

	level := append([]coin(nil), base...)
	for depth := 1; depth < limit; depth++ {
		var packed []coin
		for i := 0; i+1 < len(level); i += 2 {
			merged := coin{weight: level[i].weight + level[i+1].weight}
			merged.syms = append(merged.syms, level[i].syms...)
			merged.syms = append(merged.syms, level[i+1].syms...)
			packed = append(packed, merged)
		}
		next := make([]coin, 0, len(packed)+len(base))
		next = append(next, base...)
		next = append(next, packed...)
		sort.SliceStable(next, func(i, j int) bool { return next[i].weight < next[j].weight })
		level = next
	}

	lengths := make([]uint8, len(freq))
	keep := 2*len(used) - 2
	for i := 0; i < keep && i < len(level); i++ {
		for _, s := range level[i].syms {
			lengths[s]++
		}
	}
	return lengths
}

// assignCanonical hands out the actual code values: shortest lengths first,
// and within a length in increasing symbol order.
func assignCanonical(codes []code) {
	var count [maxCodeLength + 1]int
	for _, c := range codes {
		if c.length > 0 {
			count[c.length]++
		}
	}
	var next [maxCodeLength + 2]uint32
	var value uint32
	for l := 1; l <= maxCodeLength; l++ {
		value = (value + uint32(count[l-1])) << 1
		next[l] = value
	}
	for i := range codes {
		if codes[i].length > 0 {
			codes[i].bits = next[codes[i].length]
			next[codes[i].length]++
		}
	}
}

// writeCodeLengths writes one prefix code's lengths, choosing between the two
// forms the specification offers.
func writeCodeLengths(w *bitWriter, p prefixCode) {
	// The simple form carries one or two symbols, which is exactly what an
	// unused tree and a single-valued tree need. It names symbols in 8 bits,
	// so it cannot express one of 256 or more; such a code takes the general
	// form, where the decoder treats a lone symbol the same way.
	var present []int
	for i, c := range p.codes {
		if c.length > 0 {
			present = append(present, i)
		}
	}
	if p.single >= 0 && p.single < 256 {
		writeSimpleCodeLengths(w, []int{p.single})
		return
	}
	if p.single < 0 && len(present) == 2 && present[1] < 256 {
		writeSimpleCodeLengths(w, present)
		return
	}
	writeNormalCodeLengths(w, p.codes)
}

// writeSimpleCodeLengths writes the one- or two-symbol form.
//
// ⛔ An EMPTY tree is written as the one-symbol form naming symbol 0. The
// format has no way to say "this code is never used", and a decoder still
// reads five prefix codes per group whether or not the stream goes on to use
// them — so the distance code of an image with no backward references still
// has to be a well-formed tree.
func writeSimpleCodeLengths(w *bitWriter, present []int) {
	// present always holds one or two symbols: a code with none is given the
	// single symbol 0 by buildPrefixCode, precisely so that there is no empty
	// case to handle here.
	w.writeBits(1, 1)                      // simple
	w.writeBits(uint32(len(present)-1), 1) // symbol count minus one
	first := present[0]
	if first < 2 {
		w.writeBits(0, 1) // the first symbol fits in one bit
		w.writeBits(uint32(first), 1)
	} else {
		w.writeBits(1, 1) // the first symbol takes eight
		w.writeBits(uint32(first), 8)
	}
	if len(present) == 2 {
		w.writeBits(uint32(present[1]), 8)
	}
}

// codeLengthOrder is the order the code-length code's own lengths are written
// in, which is fixed by the specification and is not sorted.
var codeLengthOrder = [19]int{17, 18, 0, 1, 2, 3, 4, 5, 16, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

// A clItem is one symbol of the code-length alphabet, with the raw bits that
// follow it when it stands for a run.
type clItem struct {
	sym   int
	extra uint32
	nbits uint
}

// codeLengthFrequencies is how often each code-length symbol occurs, which is
// what the code-length code is built from.
//
// ⛔ It is derived from codeLengthItems rather than counted separately: a test
// that counts the symbols its own way is a second answer to the same
// question, and the two drift.
func codeLengthFrequencies(codes []code) []int {
	_, freq := codeLengthItems(codes)
	return freq
}

// codeLengthItems turns a code's lengths into the symbols that describe them,
// with 16, 17 and 18 standing for runs.
func codeLengthItems(codes []code) ([]clItem, []int) {
	var items []clItem
	freq := make([]int, 19)

	lengths := make([]uint8, len(codes))
	for i, c := range codes {
		lengths[i] = c.length
	}
	// ⛔ EVERY SYMBOL'S LENGTH IS WRITTEN, trailing zeros included. The bit
	// below says "the maximum symbol count is the alphabet size", and the
	// decoder then keeps reading code-length symbols until it has a length for
	// every symbol in the alphabet. Stopping early — which looks free, since
	// the lengths are zero — leaves it reading the pixel data as code lengths,
	// and it reports "invalid code lengths" about a tree that was fine.
	// Long zero runs cost almost nothing: symbol 18 carries 138 of them.
	end := len(lengths)

	for i := 0; i < end; {
		l := lengths[i]
		run := 1
		for i+run < end && lengths[i+run] == l {
			run++
		}
		i += run // the whole run is consumed here, however it is emitted

		if l == 0 {
			for run > 0 {
				switch {
				case run < 3:
					items = append(items, clItem{sym: 0})
					freq[0]++
					run--
				case run <= 10:
					items = append(items, clItem{sym: 17, extra: uint32(run - 3), nbits: 3})
					freq[17]++
					run = 0
				default:
					n := min(run, 138)
					items = append(items, clItem{sym: 18, extra: uint32(n - 11), nbits: 7})
					freq[18]++
					run -= n
				}
			}
			continue
		}

		// ⛔ Symbol 16 repeats the PREVIOUS NON-ZERO length, so the first of a
		// run must be written as itself before any repeat can refer to it.
		items = append(items, clItem{sym: int(l)})
		freq[int(l)]++
		run--
		for run > 0 {
			if run < 3 {
				items = append(items, clItem{sym: int(l)})
				freq[int(l)]++
				run--
				continue
			}
			n := min(run, 6)
			items = append(items, clItem{sym: 16, extra: uint32(n - 3), nbits: 2})
			freq[16]++
			run -= n
		}
	}
	return items, freq
}

// writeNormalCodeLengths writes the general form: the code lengths are
// themselves prefix-coded, with run-length symbols for repeats and zeros.
func writeNormalCodeLengths(w *bitWriter, codes []code) {
	items, freq := codeLengthItems(codes)
	clCodes := buildHuffmanLimited(freq, maxCodeLengthCodeLength)
	clSingle := singleSymbol(freq)

	// How many of the nineteen lengths are written, in the fixed order, with
	// trailing zeros dropped but never fewer than four.
	n := 19
	for n > 4 && clCodes[codeLengthOrder[n-1]].length == 0 {
		n--
	}

	w.writeBits(0, 1)           // normal
	w.writeBits(uint32(n-4), 4) // how many code-length-code lengths follow
	for i := 0; i < n; i++ {
		w.writeBits(uint32(clCodes[codeLengthOrder[i]].length), 3)
	}
	w.writeBits(0, 1) // use the whole alphabet rather than a stated maximum

	for _, it := range items {
		// ⛔ The code-length code obeys the same one-symbol rule as the five
		// it describes: when only one symbol occurs, the decoder reaches it
		// having read NOTHING, so writing its code here spends a bit per item
		// that is never consumed. Found by fuzzing — twelve million images in,
		// on a picture whose every code length was the same.
		if clSingle < 0 {
			c := clCodes[it.sym]
			w.writeCode(c.bits, c.length)
		}
		if it.nbits > 0 {
			w.writeBits(it.extra, it.nbits)
		}
	}
}

func allZeroFreq(freq []int) bool {
	for _, f := range freq {
		if f > 0 {
			return false
		}
	}
	return true
}

// singleSymbol is the only symbol with a non-zero frequency, or -1.
func singleSymbol(freq []int) int {
	n, last := 0, -1
	for i, f := range freq {
		if f > 0 {
			n++
			last = i
		}
	}
	if n == 1 {
		return last
	}
	return -1
}
