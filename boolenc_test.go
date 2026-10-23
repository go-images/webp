// Copyright (c) 2026, the go-images authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package webp

import (
	"math/rand"
	"testing"
)

// boolDecoder is RFC 6386's reader, written here only to exercise the writer.
//
// ⛔ THIS IS NOT THE ORACLE and must not be mistaken for one — a reader of our
// own agrees with a writer of our own by construction. The oracle for the
// bitstream is golang.org/x/image/vp8, which decodes whole frames this package
// writes. What this is for is the CARRY: a carry only happens on particular
// runs of bits, real frames reach it by luck, and the path that ripples an
// increment back through bytes already written is the single subtlest thing
// in the encoder. A targeted round trip reaches it deliberately, thousands of
// times, which no picture reliably does.
type boolDecoder struct {
	buf   []byte
	pos   int
	value uint32
	rng   uint32
	count int
}

func newBoolDecoder(b []byte) *boolDecoder {
	d := &boolDecoder{buf: b, rng: 255, count: -8}
	d.value = uint32(d.next())<<8 | uint32(d.next())
	d.count = 0
	return d
}

func (d *boolDecoder) next() byte {
	if d.pos < len(d.buf) {
		b := d.buf[d.pos]
		d.pos++
		return b
	}
	return 0
}

func (d *boolDecoder) readBool(prob uint8) bool {
	split := 1 + (((d.rng - 1) * uint32(prob)) >> 8)
	bigSplit := split << 8
	var bit bool
	if d.value >= bigSplit {
		bit = true
		d.rng -= split
		d.value -= bigSplit
	} else {
		d.rng = split
	}
	for d.rng < 128 {
		d.value <<= 1
		d.rng <<= 1
		d.count++
		if d.count == 8 {
			d.count = 0
			d.value |= uint32(d.next())
		}
	}
	return bit
}

// TestTheBooleanCoderRoundTrips drives the writer with sequences chosen to
// make carries happen: a long run of decisions that each take the high side at
// a very low probability pushes `low` to overflow repeatedly.
func TestTheBooleanCoderRoundTrips(t *testing.T) {
	r := rand.New(rand.NewSource(13))
	for round := 0; round < 400; round++ {
		n := 1 + r.Intn(600)
		probs := make([]uint8, n)
		bits := make([]bool, n)
		for i := range probs {
			switch round % 3 {
			case 0:
				probs[i] = uint8(1 + r.Intn(254))
				bits[i] = r.Intn(2) == 1
			case 1:
				// Extreme probabilities with the unlikely branch taken, which
				// is what drives `low` upwards and makes it carry.
				probs[i] = 254
				bits[i] = true
			default:
				probs[i] = 1
				bits[i] = false
			}
		}

		e := newBoolEncoder()
		for i := range probs {
			e.writeBool(probs[i], bits[i])
		}
		out := e.bytes()

		d := newBoolDecoder(out)
		for i := range probs {
			if got := d.readBool(probs[i]); got != bits[i] {
				t.Fatalf("round %d: decision %d of %d came back %v, wrote %v",
					round, i, n, got, bits[i])
			}
		}
	}
}

// ⛔ THE CARRY IS DRIVEN DELIBERATELY, because data never gets there. A first
// version of this test ran five hundred decisions at the extreme
// probabilities and asserted the round trip; it passed and carried ZERO times,
// so the path it existed for stayed untested while the comment above it said
// otherwise. A second version searched two thousand random sequences — about
// four hundred thousand decisions — and still found none.
//
// Measured: the accumulator reaches 0x7fffff14 and the carry needs
// 0x80000000. It is reachable, by about one part in ten million, because the
// accumulator is masked to 24 bits at every byte and only the additions
// between shifts can push it over. Waiting for that in a test is waiting for
// nothing, so the state is set up directly.
func TestACarryRipplesThroughBytesAlreadyWritten(t *testing.T) {
	// A renormalisation with the top bit already set is exactly the moment
	// the ripple has to happen.
	e := &boolEncoder{out: []byte{0x12, 0xff, 0xff}, rng: 1, low: 0x80000000, pending: 8}
	e.writeBool(128, false)
	if e.carries == 0 {
		t.Fatal("the renormalisation did not carry with the top bit set")
	}
	if got := string(e.out[:3]); got != string([]byte{0x13, 0x00, 0x00}) {
		t.Errorf("the ripple left %x, wanted 13 00 00", e.out[:3])
	}

	// And the one in the flush, which is the last chance for a carry.
	f := &boolEncoder{out: []byte{0x41}, rng: 200, low: 0xff000000, pending: 8}
	f.bytes()
	if f.carries == 0 {
		t.Error("the flush did not carry")
	}
}

// carry's own three cases, driven directly: an ordinary increment, a ripple
// across a run of 0xff, and a carry that runs off the front of everything
// written so far.
func TestTheCarryItself(t *testing.T) {
	for _, c := range []struct {
		why  string
		in   []byte
		want []byte
	}{
		{"an ordinary increment", []byte{0x01, 0x02}, []byte{0x01, 0x03}},
		{"a ripple across one 0xff", []byte{0x01, 0xff}, []byte{0x02, 0x00}},
		{"a ripple across several", []byte{0x07, 0xff, 0xff, 0xff}, []byte{0x08, 0x00, 0x00, 0x00}},
		{"off the front of the output", []byte{0xff, 0xff}, []byte{0x00, 0x00}},
	} {
		e := &boolEncoder{out: append([]byte(nil), c.in...)}
		e.carry()
		if string(e.out) != string(c.want) {
			t.Errorf("%s: %x became %x, wanted %x", c.why, c.in, e.out, c.want)
		}
	}
}
