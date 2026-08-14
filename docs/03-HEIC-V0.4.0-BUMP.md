# 03 — Bump `Rosca75/heic` v0.2.0 → v0.4.0

> Status: ready to execute. Every claim below was verified on Linux against the real
> library and the eight `samples/` HEIC files before this plan was written — see
> §7 for what was measured and how to reproduce it.

## 1. Why

`Rosca75/heic` v0.4.0 syncs the fork onto upstream `gen2brain/heic`, which replaced the
libheif/libde265 C-to-WASM decoder with the **pure-Rust `heic` crate** (run under wazero,
or transpiled to Go with the `wasm2go` build tag). The fork's `DecodeThumbnail`,
`DecodeThumbnailConfig` and the reusable `Decoder` were re-implemented on top of it.

What this app gets:

- **~17% faster thumbnail decode on the exact hot path this app uses** (see §7).
- A **real** embedded-thumbnail decode in the WASM path. v0.2.0's WASM thumbnail was
  genuine too, so this is not a correctness change here — but the Rust implementation is
  the one upstream now maintains.
- Upstream's `DecodeAll` (image sequences) and `DecodeExif`, neither of which this app
  needs (see §6).

## 2. The headline: this is a one-line change

**No Go source file needs editing.** The bump builds, vets and cross-compiles to Windows
with zero code changes. Verified on a full copy of this repo.

```bash
go get github.com/Rosca75/heic@v0.4.0
go mod tidy
```

Resulting `go.mod` deltas — all four are expected:

| Module | Before | After |
|---|---|---|
| `github.com/Rosca75/heic` | v0.2.0 | **v0.4.0** |
| `github.com/ebitengine/purego` | v0.10.0 | v0.10.1 |
| `github.com/tetratelabs/wazero` (indirect) | v1.9.0 | **v1.12.0** |
| `golang.org/x/sys` (indirect) | v0.30.0 | v0.44.0 |
| `go` directive | `1.25` | `1.25.0` |

The `go` directive gains its patch component because heic v0.4.0 declares `go 1.25.0`.
Harmless — do not hand-revert it, `go mod tidy` will just put it back.

## 3. Risks that were checked and ruled out

Three things looked dangerous on paper. All three were tested; none apply.

### 3.1 The `*image.YCbCr` → `*image.NRGBA` return-type change — DOES NOT APPLY

The WASM backend now returns `*image.NRGBA` (the Rust crate emits RGBA8) where the old one
returned YCbCr planes. That breaks any caller doing a concrete type assertion.

**This repo does none.** A search for `.(*image.`, `image.YCbCr`, `image.NRGBA`, `image.RGBA`
and `image.Gray` across all `.go` files returns **zero** hits. Every consumer of a decoded
HEIC goes through `resizeImageToJPEG`, which uses `img.Bounds()` and
`draw.ApproxBiLinear.Scale` — both interface-level, both type-agnostic.

Nothing to do. But if you later add code that type-switches on a decoded HEIC, remember it
can be `*image.NRGBA` (WASM path) **or** `*image.YCbCr` (dynamic libheif path).

### 3.2 The 192 KB truncated-header fast path — STILL WORKS

This is the app's most important optimisation and the thing most at risk. `heic_support.go`
hands `DecodeThumbnail` a **truncated 192 KB buffer** (`decodeHEICFromHeader`, rung 1) and
relies on the decoder walking the ISOBMFF `iloc` box to find the thumbnail tile inside that
window. A stricter decoder would reject the truncated input and collapse the whole ladder
onto the expensive full-file read.

Tested on all eight `samples/` files, header-only, forced WASM:

| | v0.2.0 (current) | v0.4.0 (new) |
|---|---|---|
| Thumbnail from 192 KB header | **8/8 ok** | **8/8 ok** |
| Primary from 192 KB header | 0/8 (expected) | 0/8 (expected) |

Identical. Rung 1 keeps hitting; the ladder is unaffected.

### 3.3 `heic.Decoder` semantics — UNCHANGED

v0.4.0 reimplements `Decoder` as a thin shim because the package now pools WASM modules
internally. Its doc comment says it "always uses the WASM backend", which could have been a
behaviour change for the batch scan path.

It isn't: **v0.2.0's `NewDecoder` also always used the WASM backend** (its own comment says
so). `heicDecoderWorker`, `heicDecodeThumb` and `heicDecodePrimary` in `heic_support.go`
keep working exactly as before. `Close()` is still a no-op-safe call.

## 4. A pre-existing quirk worth knowing (NOT caused by this bump)

When dynamic libheif *is* in use, **the 192 KB fast path always fails** — libheif rejects
truncated containers outright, so rungs 1 and 2 both miss and every HEIC falls through to
the full `os.ReadFile`. Measured identically on v0.2.0 and v0.4.0, so the bump neither
causes nor fixes it.

Why you probably have not noticed: `initHEIC()` sets `heic.ForceWasmMode = true` whenever
dynamic libheif is older than 1.18, and that is the common case (this Linux box has 1.17.6).
Under forced WASM the fast path works. On a machine with libheif ≥ 1.18 the gate lets the
dynamic path through and HEIC scanning silently gets slower.

**No action required for this bump.** If you want to chase it later, the fix is a one-liner
in `initHEIC()` — force WASM whenever the byte-range fast path is in play, not only below
1.18 — but that is a separate change with its own perf testing, out of scope here.

## 5. Execution

```bash
cd /home/oscar/Documents/dedup-photos/dedup-photos
git checkout preview            # already the current branch
git switch -c chore/heic-v0.4.0

go get github.com/Rosca75/heic@v0.4.0
go mod tidy

go build ./...                  # must succeed with no source edits
go vet ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...   # this app ships on Windows
```

All four commands were confirmed green on a copy of this repo at its current HEAD
(`1558685`).

Then the real check — a scan against `samples/`, watching the perf line that
`printAndResetHEICLadder()` emits at the end of Phase 3b:

```
[perf] HEIC ladder: thumbHdr=N primaryHdr=… fullThumb=… fullPrimary=… fail=0 | fullReadFallback=…
```

**Acceptance: `thumbHdr` must account for essentially every HEIC file, and `fail` must be 0.**
If `thumbHdr` collapses to ~0 and `fullThumb` takes over, the truncated-header path has
regressed — stop and investigate rather than shipping, because that is the whole point of
the 192 KB read.

Commit only `go.mod` and `go.sum`:

```bash
git add go.mod go.sum
git commit -m "Bump Rosca75/heic to v0.4.0 (upstream Rust/WASM backend)"
```

Rollback if anything looks wrong: `go get github.com/Rosca75/heic@v0.2.0 && go mod tidy`.
v0.2.0 is untouched and still tagged.

## 6. Explicitly NOT doing: switching EXIF to `heic.DecodeExif`

v0.4.0 exposes upstream's new `DecodeExif`. **Do not adopt it here.**

This app already extracts EXIF through `github.com/bep/imagemeta` (`exif_extract.go`,
`extractHEICExif` in `heic_support.go` with `imagemeta.HEIF`), and that is the *unified*
path across JPEG, TIFF, WebP, PNG, HEIF, AVIF and RAW. Replacing the HEIC branch with
`heic.DecodeExif` would add a second EXIF code path for one format and buy nothing.
`computeDHashHEIC` also reuses the same 192 KB buffer for `imagemeta.Decode` with
`Sources: imagemeta.CONFIG` to get dimensions with no extra I/O — worth preserving.

(For contrast: `geo-photo-tagger` *should* adopt `heic.DecodeExif`, because it currently
pulls a whole extra dependency, `jdeng/goheif`, just for HEIC EXIF. Different repo,
different plan.)

## 7. What was measured, and how to reproduce

Environment: Linux, Go 1.26.5, libheif 1.17.6 present, the eight `IMG_*.HEIC` files from
`samples/`.

**Throughput on this app's hot path** — `DecodeThumbnail` against a 192 KB header buffer,
forced WASM, 5 rounds over 8 files after warm-up:

| Version | Total | Per file |
|---|---|---|
| v0.2.0 | 1.916 s | 47.9 ms |
| **v0.4.0** | **1.601 s** | **40.0 ms** |

≈17% faster, 40/40 decodes successful in both.

Treat this as indicative, not a benchmark: single machine, small corpus, wall-clock. The
number that matters for acceptance is the ladder counter in §5, not this figure.

To reproduce, decode `samples/*.HEIC` through `heic.DecodeThumbnail` with
`heic.ForceWasmMode = true`, passing only the first 192 KB of each file, and compare against
the same program built with `@v0.2.0`.

## 8. CLAUDE.md staleness — HEIC part already fixed

`CLAUDE.md` used to claim, in the Project Overview:

> **Unsupported formats:** HEIC/HEIF files are not supported. Go has no pure-Go HEVC decoder,
> and CGo-based solutions (libde265) cause build issues on Windows. HEIC files are skipped
> during scanning.

Flatly wrong — HEIC is a first-class scanned format with a dedicated fast path. Since
CLAUDE.md bills itself as "the single source of truth", this actively misled every session
that read it.

**Already corrected** (uncommitted, sitting in the working tree alongside this plan). The
replacement describes the two backends, the `initHEIC()` libheif < 1.18 WASM gate, the
192 KB fast path and the decode ladder, and points at the `[perf] HEIC ladder:` line as the
health signal. The four HEIC source files were also added to the Repository Structure map,
which had omitted them entirely.

Commit that together with the bump, or separately — but do not lose it.

### Remaining CLAUDE.md staleness — NOT fixed, out of scope

Found while editing, deliberately left alone since it is unrelated to HEIC:

- **Seven source files are still missing** from the Repository Structure map:
  `exif_extract.go`, `hasher_pipeline.go`, `parallel.go`, `raw_preview.go`, `state.go`,
  `thumb_cache.go`, `types.go`.
- **The line counts are wrong**, in both directions — `scanner.go` is listed as 204 lines
  but is 385; `hasher.go` is listed as 815 but is 366 (it was split into
  `hasher_pipeline.go` at some point and the doc never caught up). Treat every number in
  that block as untrustworthy until someone regenerates it.

`static/` *is* correct, incidentally — `main.go` really does `//go:embed all:static`. The
untracked `frontend/` directory only holds Wails-generated `wailsjs` bindings, so it being
untracked is probably intentional.
