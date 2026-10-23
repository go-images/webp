# webp

Write **WebP** images in pure Go, `CGO_ENABLED=0`.

It encodes the lossless form (**VP8L**): what comes out is bit-for-bit the
pixels that went in.

```go
f, _ := os.Create("page.webp")
defer f.Close()
if err := webp.Encode(f, img); err != nil { … }

// or straight to bytes
b, err := webp.EncodeToBytes(img)
```

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

## What it does not do

- **Lossy (VP8).** The quality slider belongs to a different codec and is the
  next piece of work; a page of text is better served losslessly anyway.
- **The colour cache, the cross-colour and colour-indexing transforms, and the
  120 short distance codes** for a small two-dimensional neighbourhood.
  Distances are written in the plain scan-line form, which is correct for every
  distance and leaves a few per cent on the table against `libwebp`.
- **Decoding.** `golang.org/x/image/webp` reads WebP, and a second reader in
  the fleet would be a second answer to the same question.

## Licence

BSD-3-Clause.
