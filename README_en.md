# go-cad

<p align="center">
  <a href="README.md">简体中文</a> | <a href="README_en.md">English</a>
</p>

**Pure-Go DWG/DXF parser, renderer and writer — R9 through R2018, zero cgo, aligned with LibreDWG.**

## What is go-cad ?

`go-cad` parses Autodesk DWG drawings at the bit level (R9–R2018), renders them
to PNG/SVG, extracts text, and writes DWG files back in six container
generations. It also reads and writes DXF, and consumes LibreDWG-style JSON.
No cgo, no external binaries — a standalone module embeddable in any Go service.

Decoding correctness is **verified key-by-key against
[LibreDWG](https://github.com/LibreDWG/libredwg)** (the reference
implementation): 37 corpus drawings, 34 of them strict hard-gated, all at
100% key-level alignment. The implementation is an independent pure-Go
expression, aligned bit-by-bit against the LibreDWG specification.

## Features

- 🗂️ **Full-version DWG reading** — R9/R10/R11 (dedicated pre-R13 path) plus R13/R14/R2000/R2004/R2007/R2010/R2013/R2018
- ✍️ **DWG writing** — six container generations (R2000→R2018), round-trip verified by re-parsing and by LibreDWG `dwgread`
- 🖼️ **PNG rendering** — real CJK glyph engine (rotation/mirroring/alignment/width-factor), auto resolution that guarantees text legibility
- 📐 **SVG output** — true vector with per-layer groups, text elements and AI-friendly metadata (layer names, entity types, handles, sheet summary)
- 🔪 **Smart sheet splitting** *(unique)* — auto-detects title-block frames (including non-standard extended formats), extracts sheet numbers and names, renders each sheet separately; orphan content areas (notes pages, legend tables, system diagrams) are clustered and split too; a full-drawing overview is always first
- 📝 **Text extraction** — 14k+ texts from a single drawing, GBK/UTF-16 codepage decoding, pre-R13 codepage support
- 🔄 **DXF both ways** — ASCII + binary reading, 23+ entity writing, gold-verified against LibreDWG `dxf2dwg` (59/59)
- 📥 **JSON input** — consumes LibreDWG-style gold JSON directly (41 entity types)
- 🧱 **Structured forward writing** — any in-memory `Document` (from JSON/DXF/your own code) can be written to DWG without a source file
- 🛡️ **Industrial reliability** — 147-file official corpus throughput with zero bugs, 1000+ mutation-fuzz variants with zero panics, auto-clarity retry built into rendering
- 💻 **CLI included** — `dwg2png` renders DWG/DXF to PNG or SVG, with per-sheet splitting

## Multilingual drawing support

CJK drawings are first-class citizens, not an afterthought:

- **GBK/UTF-16 codepage decoding** for TEXT/MTEXT/ATTRIB/ATTDEF (R13+) and
  **pre-R13 codepages** from the R9–R11 header variable stream
- Bigfont handling and `\U+XXXX` escape processing
- CJK glyph engine — Chinese annotations render crisply in PNG/SVG
- Verified on 4 real Chinese engineering drawings (incl. an R2000 drawing with
  505 Chinese TEXT entities) at 100% value-level alignment

## Comparison with LibreDWG

| Capability | LibreDWG | go-cad |
|---|---|---|
| DWG reading | R13–R2018 + partial pre-R13 | **R9–R2018 full** (pre-R13 with per-type count verification) |
| Value-level correctness | — (it *is* the baseline) | **100% on 37 corpus drawings** (62 strict gate rows) |
| DWG writing | R13–R2018 | **R2000–R2018** (six generations, verified by `dwgread`) |
| DXF read/write | ✅ | ✅ (gold-verified 59/59) |
| JSON in/out | ✅ | ✅ in (41 types) / structured dump out |
| Rendering | ⚠️ debug-grade `dwg2SVG` only | ✅ PNG glyph rendering + AI-friendly SVG + smart sheet splitting |
| Language | C (GPL) | **Go (Apache-2.0)** — embeddable, no license contamination |

### What go-cad cannot do (honest limits)

- **ACIS geometry kernels** (3DSOLID/REGION SAT/SAB reconstruction) — LibreDWG
  also only stores the blob; no reference exists to verify against
- **pre-R13 writing** — LibreDWG's encoder does not support it either
- **R2.x–R8 legacy formats** — outside LibreDWG's supported range as well
- A few spec branches with **zero real-world samples found** after two
  independent web-wide searches (photometric LIGHT, pre-R13 CJK) — implemented
  per spec with synthetic verification
- ~5% of DumpEntities keys (long-tail table records) — tracked, coverage gate
  at 15923/15923 verified keys

## Supported formats

| Format | Read | Write |
|---|---|---|
| DWG R9/R10/R11 (AC1004/06/09) | ✅ | ➖ (LibreDWG doesn't either) |
| DWG R13/R14 (AC1012/14) | ✅ | ✅ (R2000 layout) |
| DWG R2000–R2018 (AC1015–AC1032) | ✅ | ✅ (six containers) |
| DXF ASCII / binary (R12–R2018) | ✅ | ✅ |
| JSON (LibreDWG gold style) | ✅ | ✅ (entity dump) |

## Quickstart

### 1. Install

```bash
go get github.com/unitedrhino/go-cad
```

### 2. Parse, render, write

```go
package main

import (
    "bytes"
    "os"

    "github.com/unitedrhino/go-cad"
)

func main() {
    data, _ := os.ReadFile("drawing.dwg")

    doc, err := cad.Parse(data) // also: ParseDXF, ParseJSON
    if err != nil { panic(err) }

    texts := doc.Texts() // 14k+ texts from a real 4MB drawing

    png, err := cad.RenderPNG(doc, cad.RenderOptions{Width: 4096})

    var buf bytes.Buffer
    err = cad.WriteDwg(doc, &buf) // six-container writer, auto-dispatched

    svg, err := cad.RenderSVG(doc, cad.RenderOptions{Width: 4096})
    _ = png; _ = svg
}
```

### 3. CLI

```bash
go run ./cmd/dwg2png drawing.dwg -o out.png            # PNG (2048)
go run ./cmd/dwg2png drawing.dwg -o out.svg            # SVG (vector, zoom forever)
go run ./cmd/dwg2png drawing.dwg -sheets -o s.png      # smart sheet split:
                                                       # one PNG per title-block frame,
                                                       # auto width per sheet text size,
                                                       # orphan content areas (notes, legends) included,
                                                       # full-drawing overview always first
```

## Example one-command demo

```bash
go run ./example
```

Runs the full pipeline with zero arguments (parse → text → render → DWG
write-back → re-parse check → DXF both ways → JSON dump), dropping
PNG/DWG/DXF artifacts in the current directory. `-f` selects any sample,
`-o` the output directory, `-width` the render width. See
[example/main.go](example/main.go) — each of the 6 steps is a minimal use of
a public API.

## Real-world validation

Every release is validated on real production drawings, not just synthetic fixtures:

- **4.3 MB intelligent-building construction set** (108,850 objects):
  2.5M entity keys verified 100% against LibreDWG gold; rendered at 8192px
  with readable CJK annotations; written back and re-parsed losslessly
- **Fire-protection & weak-current construction sets**: 9+9 title-block
  sheets auto-split with correct sheet numbers and names extracted from
  title bars (e.g. `RD(XF)-05-地下一层消防平面图`), color-legend table fully
  restored (16 colored line samples + cable specs)
- **Outdoor site plan**: outlier-robust bounds fixed a blank render
  (0.04% → 2.45% ink coverage)
- **147-file LibreDWG official corpus**: full consumption pipeline
  (parse → render → write → re-parse), arbitered by `dwgread` — 0 bugs
- **Fuzzing**: 1000+ mutation variants + random inputs, zero panics
  (4 panics found and fixed during development)

## Quality assurance

| Dimension | Gate | Status |
|---|---|---|
| Value-level alignment | 37 drawings × object/entity, key-by-key vs `dwgread -O JSON` | **all strict rows 100%** |
| Round-trip | object+entity replay re-decode | fail=0 |
| DWG writing | six-container readback invariants + `dwgread` cross-check | all PASS |
| DXF | same-source pairs + `dxf2dwg` gold roundtrip | 59/59 |
| JSON input | gold → Document → vs DWG-sourced Document | 0 diff on 9 drawings |
| Reliability | 147-file corpus + mutation fuzzing | 0 bugs / 0 panics |
| Coverage | statement coverage | 83.5% |

External verification resources (LibreDWG gold JSON) are env-gated; the
built-in testdata suite runs everywhere.

## Performance

Pure-parse throughput is **2.3–6.4× faster** than LibreDWG `dwgread`
(full-pipeline comparison, C implementation):

| Sample | go-cad | dwgread (net) |
|---|---|---|
| 117 KB | 32.9 MB/s | 5.1 MB/s |
| 8.2 MB | 9.1 MB/s | 3.4 MB/s |

Rendering a 4.3 MB / 108k-object drawing: parse 691 ms, PNG@2048 4.1 s,
DWG write 785 ms, full sheet split (26 SVG) in seconds.

## Package layout

| Path | Role |
|---|---|
| `cad.go` | Public facade: type aliases + API forwarding (Parse/Document/render/write); external usage unchanged |
| `internal/bitstream/` | DWG bit-stream primitives (symmetric read/write; bottom layer, no internal deps) |
| `internal/container/` | container layer (R2000 sections / R2004 paged / R2007 RS-interleaved) + LZ77/R21 compressors + raw-material capture for replay writing |
| `internal/objrec/` | object record layer: object map / records / type-code naming (shared lowest-level record primitives) |
| `internal/entity/` | entity decoding (common-head scan framework + per-family files) + shared entity-side helpers (tessellation, palette) |
| `internal/object/` | internal object decoding (spec-driven `gfRead` framework) |
| `internal/drawing/` | document model & decode orchestration (Document/Parse) + pre-R13 path + DXF both ways + JSON input + tessellation & bounding boxes |
| `internal/render/` | rendering (PNG glyphs / SVG vector / sheet splitting) |
| `internal/writer/` | DWG writing (bit-level replay + structured forward) |
| `internal/testsupport/` | test support: test bit-writer / gold path helpers / comparators (no business deps, importable by all sub-package tests) |
| `example/` / `cmd/dwg2png/` | one-command demo and CLI (facade-only) |
| `testdata/` | official corpus samples (built in, CI runs the full suite) |

One-way dependency flow: `bitstream → objrec → container → entity/object → drawing → render/writer`.
All implementation lives under `internal/`; external consumers only use the `cad.go` facade.

## Known limitations

See [Comparison](#comparison-with-libredwg) — the short list: ACIS geometry
kernels (blob only, like LibreDWG), pre-R13 writing (LibreDWG doesn't either),
R2.x–R8 legacy formats, and a handful of spec branches pending real-world
samples (implemented per spec, synthetically verified).

## References

- Alignment gold standard: [LibreDWG](https://github.com/LibreDWG/libredwg)
  `dwgread -O JSON` and `dxf2dwg`

## Contributing

PRs welcome. `go test ./...` must pass — the suite includes value-level
alignment gates that keep decoding honest.

## License

Apache-2.0. See [LICENSE](LICENSE).
