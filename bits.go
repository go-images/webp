// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

// A bitWriter writes VP8L's bitstream.
//
// ⛔ LEAST SIGNIFICANT BIT FIRST, which is the opposite of what a reader of the
// JPEG or PNG specifications expects. The lossless specification says it
// plainly: "within a byte, bits are consumed least-significant first", and a
// multi-bit value is written so that the FIRST bit written is the LOW bit of
// the value — ReadBits(2) is ReadBits(1) | (ReadBits(1) << 1).
//
// A writer that packed the other way produces a file that is the right length,
// has the right signature, and decodes to noise.
type bitWriter struct {
	out  []byte
	acc  uint64 // bits not yet flushed, low bits first
	nbit uint   // how many bits are in acc
}

// writeBits writes the low n bits of v, low bit first.
func (w *bitWriter) writeBits(v uint32, n uint) {
	if n == 0 {
		return
	}
	w.acc |= uint64(v&((1<<n)-1)) << w.nbit
	w.nbit += n
	for w.nbit >= 8 {
		w.out = append(w.out, byte(w.acc))
		w.acc >>= 8
		w.nbit -= 8
	}
}

// writeCode writes a canonical prefix code.
//
// ⛔ THE CODE IS WRITTEN MOST SIGNIFICANT BIT FIRST, into a stream that is
// otherwise least significant bit first. Canonical prefix codes are assigned
// by walking lengths upwards, which only forms a prefix-free set when the
// codes are READ from the top bit down — the same convention Deflate uses, and
// the reason both specifications describe a bit-reversal. The bits of the code
// are therefore reversed before they go into the writer.
//
// Getting this wrong gives a stream whose first symbol often decodes and whose
// later symbols do not, which reads like a corrupt image rather than like a
// packing mistake.
func (w *bitWriter) writeCode(code uint32, length uint8) {
	var rev uint32
	for i := uint8(0); i < length; i++ {
		rev |= ((code >> (length - 1 - i)) & 1) << i
	}
	w.writeBits(rev, uint(length))
}

// bytes finishes the stream, padding the last byte with zeros.
func (w *bitWriter) bytes() []byte {
	if w.nbit > 0 {
		w.out = append(w.out, byte(w.acc))
		w.acc, w.nbit = 0, 0
	}
	return w.out
}
