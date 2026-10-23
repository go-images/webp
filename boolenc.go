// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

// A boolEncoder writes VP8's binary arithmetic code: every decision in a frame
// is one call, carrying a probability that the bit is zero.
//
// RFC 6386 section 7. The decoder's half is the normative definition; this is
// its inverse, and the only thing that proves it is a frame somebody else's
// decoder reads back.
//
// ⛔ THE CARRY WALKS BACKWARDS THROUGH BYTES ALREADY WRITTEN. When `low`
// overflows, the increment has to ripple into output that has already been
// emitted, turning a run of 0xff into zeros. An encoder that ignores it
// produces a stream that decodes correctly for a while and then diverges — the
// failure appears far from the cause, in a later macroblock.
type boolEncoder struct {
	out     []byte
	rng     uint32 // kept in [128, 255] between calls
	low     uint32
	pending int // shifts remaining before a byte leaves

	// carries counts the ripples. It exists so that a test can show the carry
	// path was REACHED rather than assert that it was: a test that drives the
	// coder hard and never carries proves nothing about the hardest code in
	// the file, and looks exactly like one that does.
	carries int
}

func newBoolEncoder() *boolEncoder {
	return &boolEncoder{rng: 255, pending: 24}
}

// carry adds one to the bytes already written, clearing the 0xff run it
// crosses.
func (e *boolEncoder) carry() {
	e.carries++
	for i := len(e.out) - 1; i >= 0; i-- {
		if e.out[i] != 0xff {
			e.out[i]++
			return
		}
		e.out[i] = 0
	}
}

// writeBool writes one decision. prob is the chance IN 256 that bit is zero.
func (e *boolEncoder) writeBool(prob uint8, bit bool) {
	split := 1 + (((e.rng - 1) * uint32(prob)) >> 8)
	if bit {
		e.low += split
		e.rng -= split
	} else {
		e.rng = split
	}
	for e.rng < 128 {
		e.rng <<= 1
		if e.low&0x80000000 != 0 {
			e.carry()
		}
		e.low <<= 1
		e.pending--
		if e.pending == 0 {
			e.out = append(e.out, byte(e.low>>24))
			e.low &= 0x00ffffff
			e.pending = 8
		}
	}
}

// writeFlag writes a bit that is equally likely either way, which is how the
// headers carry their plain fields.
func (e *boolEncoder) writeFlag(bit bool) { e.writeBool(128, bit) }

// writeLiteral writes the n low bits of v, most significant first, each at
// even odds — the inverse of the decoder's readUint.
func (e *boolEncoder) writeLiteral(v uint32, n uint) {
	for i := int(n) - 1; i >= 0; i-- {
		e.writeFlag((v>>uint(i))&1 == 1)
	}
}

// writeAbsent writes the decoder's readOptionalInt for a value that is not
// there: one flag, and nothing after it.
//
// ⛔ The general form — a flag, a magnitude and a sign — is NOT written here,
// because nothing in this encoder has a non-zero quantiser delta to write. A
// function that can write what nothing calls for is a function no test
// reaches, and an untested branch in a bitstream writer is worse than a
// missing feature: it looks like the feature is there.
func (e *boolEncoder) writeAbsent() { e.writeFlag(false) }

// bytes finishes the stream.
func (e *boolEncoder) bytes() []byte {
	c := e.pending
	v := e.low
	if v&(1<<(32-uint(c))) != 0 {
		e.carry()
	}
	v <<= uint(c) & 7
	for c >>= 3; c > 0; c-- {
		v <<= 8
	}
	for i := 0; i < 4; i++ {
		e.out = append(e.out, byte(v>>24))
		v <<= 8
	}
	return e.out
}
