# webp

Write **WebP** images in pure Go, `CGO_ENABLED=0`. Both forms:

- **lossless** (VP8L) — what comes out is bit-for-bit the pixels that went in;
- **lossy** (VP8) — a quality from 0 to 100, for photographs.

```go
f, _ := os.Create("page.webp")
defer f.Close()
if err := webp.Encode(f, img); err != nil { … }        // lossless
if err := webp.EncodeLossy(f, img, 80); err != nil { … } // lossy

// or straight to bytes
b, err := webp.EncodeToBytes(img)
b, err = webp.EncodeLossyToBytes(img, 80)
```

Which to use is decided by the picture, not by a preference. On a rasterised
page of text the lossless form is **half the size of PNG** and exact. On a
photograph the lossy form is **26× smaller than the lossless one**:

| | lossless | lossy at 75 |
|---|---:|---:|
| a photograph, 128×128 | 25 722 | **974** bytes at 35.3 dB |

## Why it exists

The fleet could **read** WebP and could not write one, so every tool that
wanted to produce WebP had nothing to call. `go-avkit/vp8` parses frame headers
and does not encode.

## What it costs, measured

Against `image/png` on the same pixels — the other lossless encoder in the
standard library:

| | raw | PNG | this | of PNG |
|---|---:|---:|---:|---:|
| a rasterised page of text | 832 000 | 35 328 | **17 300** | 0.49 |
| flat colour | 360 000 | 1 445 | **82** | 0.06 |
| a 256×256 gradient | 262 144 | 1 185 | **114** | 0.10 |

The test holds those ratios, set just above what is measured rather than at a
round number anything would pass.

## How it compresses

- **Huffman over literals**, with the five prefix codes per group the format
  defines, each length-limited to the 15 bits a code length can express.
- **LZ77 backward references**, hash-chained, up to the 4 096-pixel copy the
  format allows. A copy may overlap its own source, which is how a run of one
  colour costs a few bits.
- **The predictor transform** — the thing PNG's filters do. Each 16×16 block
  picks one of the format's fourteen predictors and stores the residual.

⛔ **Both ways are tried and the smaller kept.** Neither wins everywhere, and
the difference is large in both directions: the predictor transform takes that
gradient from **194 666 bytes to 114**, and makes the page of text **worse** —
17 300 up to 27 124 — because prediction breaks the long exact matches repeated
glyphs give the match finder. Guessing from the image would be a heuristic to
maintain and to be wrong about; encoding twice costs one more pass and is never
worse than either.

## How it is checked

⛔ **The oracle is somebody else's decoder.** A round trip through a reader of
our own would agree with our own writer by construction — the two would share
every misreading of the specification, and the bit order is exactly the kind of
thing both halves get wrong together. Every test encodes, decodes with
[`golang.org/x/image/webp`](https://pkg.go.dev/golang.org/x/image/webp), and
compares **every pixel exactly**, because the format is lossless and a
tolerance would be an admission.

Coverage is **100% of statements with 0 uncovered blocks**, counted on the
profile rather than on a percentage that rounds. The fuzzer drives the
**pixels**, not a file: what has to hold is that for every image, what this
writes is read back as that image.

### Three defects fuzzing found that reading could not

**A one-symbol code costs ZERO bits.** When exactly one symbol occurs the
decoder reaches it having read nothing at all. An encoder that gives it a
one-bit code writes a bit per pixel that is never consumed, and the stream
desynchronises several symbols later — reported as a tree error about something
innocent further down. Hit on an image whose alpha channel was constant.

**The code-length code may not exceed SEVEN bits.** It describes the other
codes, and its own lengths are written as *three-bit* values, so a length of 8
is written as 0 — which says "absent". Nothing warns: the bit writer masks to
the width it is given. Found 12 million executions in, on an image whose red,
blue and alpha channels were almost entirely one value with a handful of
outliers, which is exactly the skew that drives a 19-symbol tree past seven
bits.

**A residual is subtracted channel by channel.** A single 32-bit subtraction
lets a borrow out of one channel corrupt the next, and the damage is invisible
on any image whose channels never underflow — which includes every test that
uses an opaque white background.

## The lossy half

A VP8 **key frame**: a still picture is one key frame, so none of the video
machinery — golden frames, motion vectors, the alternate reference — is
written. Each macroblock picks one of the four whole-block intra predictions
(DC, vertical, horizontal, TrueMotion) for luma and one for both chrominance
planes, by the sum of absolute differences; the residual goes through the
4×4 transform, the luma DC values through the second-order one, and the
coefficients are coded with the specification's default probabilities.

| quality | bytes | PSNR |
|---:|---:|---:|
| 20 | 112 | 29.9 dB |
| 50 | 142 | 31.2 dB |
| 80 | 228 | 37.9 dB |
| 95 | 448 | 43.1 dB |

⛔ **The forward transform is derived by inverting the decoder's inverse**, at
startup, in floating point — not copied from the usual integer constants.
Writing those from memory is how an encoder ends up *almost* right: the frames
still decode, the pictures are merely soft, and nothing says which of a dozen
magic numbers is wrong. A round trip through the decoder's own inverse is
accurate to **one level** on random blocks.

⛔ **The encoder reconstructs exactly as the decoder does**, because the next
macroblock is predicted from those pixels. Predicting from the original image
instead would drift, and the error would accumulate down the picture rather
than stay inside one block.

⛔ **The loop filter is off** (level 0), which is legal and costs some quality
at coarse settings. Writing a level this encoder does not itself apply would
be worse: the decoder would filter a picture the encoder never saw filtered,
so the two would disagree about what the frame looks like.

### What the lossy path cost to get right

**A skipped macroblock resets every context, the second-order one included.**
Keeping it — on the reasoning that the macroblock still has a second-order
block conceptually — left the two sides in step for a while and then diverging,
so most images and most quality settings looked fine. It showed as a gradient
at quality 75 decoding at 17.3 dB while the same picture at 50 and at 80 came
back above 31.

**The carry in the arithmetic coder is reached about once in ten million
decisions.** A test that ran five hundred decisions at the extreme
probabilities and asserted the round trip passed while carrying *zero* times;
a second that searched four hundred thousand found none either. The
accumulator gets to `0x7fffff14` and needs `0x80000000`. It is driven directly
now, and the counter makes "it was reached" an observation rather than a claim.

**Two of the quantiser rules are nearly invisible end to end.** The
chrominance DC factor is clamped at index 117 rather than 127, which only
bites below quality 8, where the levels are mostly 0 and ±1 — so a mutation
changing it survived the whole suite, *including* a test written specifically
to catch it by measuring the picture. Those rules are asserted directly now.

## What it does not do

- **The colour cache, the cross-colour and colour-indexing transforms, and the
  120 short distance codes** for a small two-dimensional neighbourhood.
  Distances are written in the plain scan-line form, which is correct for every
  distance and leaves a few per cent on the table against `libwebp`.
- **Decoding.** `golang.org/x/image/webp` reads WebP, and a second reader in
  the fleet would be a second answer to the same question.
- **The lossy form's finer machinery**: 4×4 intra modes (`B_PRED`), the loop
  filter, segmentation, multiple coefficient partitions, probability updates
  trained on the frame, and rate control toward a target size. Each would make
  the lossy files smaller at the same quality; none of them changes what the
  decoder sees as a valid frame.

## Licence

BSD-3-Clause.
