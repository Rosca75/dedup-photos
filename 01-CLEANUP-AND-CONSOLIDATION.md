# dedup-photos — Plan 1: Cleanup, Consolidation & Dead Code Removal

> **Branch**: `preview`  •  **Goal**: shrink and rationalise the codebase before the perf rewrite.
> **Constraints**: pure Go (no CGo, no external binaries). API/UX stays stable. Internals can change freely.
> **Estimated session size**: 1 Claude Code session.
>
> **Run order**: this file MUST be completed and committed before starting `02-PERFORMANCE-HOT-PATHS.md`. Plan 2 assumes the cleanup is done.

---

## Why this plan exists

The audit of `preview` found:

- **Two EXIF libraries** doing overlapping work: `rwcarlsen/goexif` (JPEG/TIFF) and `bep/imagemeta` (HEIC). The latter is ~2.6× faster, allocates ~40× less, and supports every format in `supportedExtensions` (JPEG, TIFF, PNG, WebP, HEIF, AVIF, DNG, CR2, NEF, PEF, ARW). Maintaining two parsers is unnecessary and the slower one is in the hot path.
- **Significant dead code** in `hasher.go`, `cache.go`, `scanner.go`, and `app.go` left over from the historical pipeline (pre-split, pre-aspect-bucket).
- **`server.go` is misnamed** — it contains shared types and two utility functions, no server. The HTTP server it once held was removed (see the file's own header comment) but the filename was never updated. New contributors will look in vain for `ScanDirectoryFiltered` (it's in `server.go`, not `scanner.go`).
- **`runScan` and `GetThumbnail` carry obsolete fallbacks** for code paths that no longer exist.

This plan rationalises the structure so Plan 2's performance work has a clean target.

---

## Context Claude needs before starting

Read these files in full before making any change:

```
app.go            ← orchestrator + Wails bindings
hasher.go         ← hash primitives + dead code candidates
hasher_pipeline.go ← active pipeline, nothing to delete here
cache.go          ← contains 2 unused methods
scanner.go        ← contains one unused function
server.go         ← misnamed, will be split
metadata.go       ← uses goexif; will be migrated to bep/imagemeta
heic_support.go   ← uses bep/imagemeta already; will become the model
grouper.go        ← no changes in this plan
parallel.go       ← no changes in this plan
go.mod            ← `goexif` dependency will be removed at the end
```

Do NOT touch `static/`, `wails.json`, `main.go`, or the `heic_version_*.go` files in this plan.

---

## Step 1 — Rename and split `server.go`

`server.go` contains three unrelated concerns. Split them along clean lines.

### 1a. Move shared types to `types.go`

Create a new file `types.go` containing **only**:
- `Action` struct
- `ScanRequest`, `ReportMismatchRequest`, `DeleteRequest` structs
- `ScanProgress`, `ScanStats`, `ScanResult` structs

The file should have a focused header comment: "Shared request/response types serialised between Go backend and JS frontend via Wails."

### 1b. Move global state to `state.go`

Create a new file `state.go` containing **only**:
- `scanMutex`, `scanResult`, `thumbnailCache`, `scanCancel` globals
- `actionMutex`, `undoStack`, `redoStack`, `maxUndoActions`

Header comment: "Global scan state and undo/redo stacks. All access is mutex-guarded."

### 1c. Move `ScanDirectoryFiltered` into `scanner.go`

`ScanDirectoryFiltered` is the actively-used filtered walker. It belongs next to `ConcurrentScanDirectory` in `scanner.go`. Move it there.

### 1d. Move `extractEmbeddedJPEG` into a new `raw_preview.go`

`extractEmbeddedJPEG` is an isolated utility for camera RAW thumbnail extraction. Give it its own file so its purpose is obvious from the filename.

### 1e. Delete `server.go`

Once 1a–1d are done, `server.go` is empty. Delete it.

After this step the file layout is:

```
app.go            ← Wails App struct + methods
state.go          ← global state (NEW)
types.go          ← shared types (NEW)
scanner.go        ← directory walkers (now includes ScanDirectoryFiltered)
hasher.go         ← hash primitives
hasher_pipeline.go ← pipeline
cache.go          ← persistent cache
grouper.go        ← BK-tree + grouping
metadata.go       ← EXIF + quality scoring
heic_support.go   ← HEIC-specific helpers
raw_preview.go    ← extractEmbeddedJPEG (NEW)
parallel.go       ← worker pool
main.go           ← entry point
heic_version_*.go ← unchanged
```

After 1a–1e, run `go build ./...` and confirm the build still passes.

---

## Step 2 — Delete dead code in `hasher.go`

Several public/private functions in `hasher.go` are no longer called anywhere except by tests-that-don't-exist or by `ReportMismatch` (which itself is wasteful — see Step 5).

Run `grep -rn "FunctionName" *.go` for each candidate before deleting to confirm no callers. Currently expected results:

| Function | Where it's defined | Where it's called | Action |
|---|---|---|---|
| `ComputePartialXXHash` | hasher.go:166 | nowhere | **DELETE** |
| `computeXXHashStreaming` | hasher.go:191 | nowhere (the inline `runPartialHashPhase` does its own read) | **DELETE** |
| `ComputeAllHashes` | hasher.go:341 | nowhere | **DELETE** |
| `ComputeDHashFromEXIFThumbnail` | hasher.go:231 | nowhere (inlined into `computeDHashSmart`) | **DELETE** |
| `ComputeDHash` | hasher.go:500 | `app.go:ReportMismatch` only | **KEEP for now** — Step 5 will remove the caller |
| `ComputePHash` | hasher.go:515 | nowhere | **DELETE** |
| `ComputeXXHash` | hasher.go:150 | `app.go:ReportMismatch` only | **KEEP for now** — Step 5 will remove the caller |

**Verify** before deleting each one with:
```
grep -rn "\bFunctionName\b" --include="*.go"
```

After deletions, `hasher.go` should drop from ~580 lines to roughly ~330. The `bufPool` block stays (used by `runPartialHashPhase`) and `headerBufPool` stays (used by `computeDHashFromHeader`).

Re-run `go build ./...` after deletions.

---

## Step 3 — Delete dead code in `cache.go` and `scanner.go`

### 3a. `cache.go`

| Function | Action |
|---|---|
| `(c *HashCache) Lookup` | **DELETE** — only `LookupAll` is used by `presplitByCache` |
| `(c *HashCache) Store` | **DELETE** — only `StoreAll` is used by `saveUpdatedCache` |

These are described in the source as "backwards-compatible wrappers" but the codebase has no v1 callers left.

### 3b. `scanner.go`

| Function | Where it's called | Action |
|---|---|---|
| `ScanDirectory` | only by `ConcurrentScanDirectory` for sub-tree walks | **KEEP** (genuine internal use) |

So `scanner.go` keeps `ScanDirectory`, `ConcurrentScanDirectory`, and the moved-in `ScanDirectoryFiltered`.

Re-run `go build ./...`.

---

## Step 4 — Migrate EXIF extraction to `bep/imagemeta`

`bep/imagemeta` is already a dependency (used in `heic_support.go`). It supports JPEG, TIFF, PNG, WebP, HEIF/HEIC, AVIF, DNG, CR2, NEF, PEF, and ARW — a superset of what `rwcarlsen/goexif` supports. Benchmark numbers from the upstream README: **JPEG all-tags decode is 19µs / 4 KB / 188 allocs vs. goexif's 50µs / 175 KB / 812 allocs** (~2.6× faster, ~40× less memory).

### 4a. Build a unified `extractExif` helper

Add a new file `exif_extract.go` that exposes one function:

```go
// extractExifInto populates meta.{DateTaken, GPSLat, GPSLon, Camera, Lens,
// ISO, FocalLength, Title, Description} from the EXIF in the given reader.
// The caller is responsible for passing the right ImageFormat hint
// (use imagemeta.JPEG / TIFF / WebP / PNG / HEIF based on file extension).
func extractExifInto(r io.Reader, format imagemeta.ImageFormat, meta *ImageMetadata)
```

Internally it uses `imagemeta.Decode(imagemeta.Options{R: r, ImageFormat: format, HandleTag: ...})`. Move the `HandleTag` switch from the existing `extractHEICExif` into this new helper — the tag names are identical for HEIF and JPEG (`DateTimeOriginal`, `Make`, `Model`, `LensModel`, `ISO`, `FocalLength`, `GPSLatitudeRef`, etc.). The HEIC wrapper in `heic_support.go` becomes a one-liner that just selects `imagemeta.HEIF`.

### 4b. Map extension → `imagemeta.ImageFormat`

Add an internal helper:

```go
func imageFormatForExt(ext string) (imagemeta.ImageFormat, bool) {
    switch strings.ToLower(ext) {
    case ".jpg", ".jpeg":
        return imagemeta.JPEG, true
    case ".tiff", ".tif", ".dng", ".arw", ".cr2", ".nef", ".orf", ".rw2", ".raf":
        return imagemeta.TIFF, true   // RAW formats are TIFF-based containers
    case ".webp":
        return imagemeta.WebP, true
    case ".png":
        return imagemeta.PNG, true
    case ".heic", ".heif":
        return imagemeta.HEIF, true
    }
    return 0, false
}
```

`bep/imagemeta` reads RAW formats via the embedded TIFF EXIF block, which is exactly what we need. Verify the constant names by running `grep -r "imagemeta.JPEG\|imagemeta.TIFF" $(go env GOMODCACHE)/github.com/bep/imagemeta@*/` if uncertain — the package exposes the format enum publicly.

### 4c. Replace all `goexif` calls

Find every call site and replace:

```
grep -rn "rwcarlsen/goexif\|exif.Decode\|x.JpegThumbnail\|exif.DateTime\|exif.Latitude\|exif.Camera" --include="*.go"
```

Expected hits: `metadata.go` (multiple — DateTaken, GPS, Camera, ISO, FocalLength, Lens, Title, Description) and `hasher.go` (one — `computeDHashFromHeader` reads `x.JpegThumbnail()`).

For `metadata.go`:
- Delete the goexif import.
- Replace each `exif.Decode(f)` + tag-walk block with a single call to `extractExifInto(f, format, &meta)`.

For `hasher.go` (`computeDHashFromHeader`):
- The EXIF-thumbnail extraction needs more care. `bep/imagemeta` does NOT expose the JPEG thumbnail bytes directly (it focuses on metadata, not image payload extraction). Two options:
  1. **Preferred**: keep a tiny inline helper that finds the JPEG SOI in the EXIF APP1 segment manually. The thumbnail in JFIF/EXIF JPEGs lives in IFD1 (second IFD) at the `ThumbnailOffset` (tag 0x0201) / `ThumbnailLength` (tag 0x0202) offsets. `bep/imagemeta` exposes both via its tag handler — see the field names in their `metadecoder_exif_fields.go`. So you can register a HandleTag callback that captures both, then slice `buf[thumbOffset : thumbOffset+thumbLen]` from the file bytes.
  2. **Fallback**: keep `goexif` *only* for `JpegThumbnail()` extraction in this one call site. This still removes ~80% of goexif usage but keeps the dependency. This is the safer choice if the bep/imagemeta thumbnail-offset path proves fragile.

Implement option 1 first; if the resulting dHash values for the same JPEG don't match what goexif produces (run a small comparison harness against `samples/false_duplicates/IMG_136*.JPG`), fall back to option 2 and document why.

### 4d. Remove `goexif` from `go.mod`

Once all `rwcarlsen/goexif` imports are gone (run `grep -rn "rwcarlsen/goexif" --include="*.go"` to verify zero hits), run:

```
go mod tidy
```

This drops `github.com/rwcarlsen/goexif` from `go.mod` / `go.sum`. Confirm by checking those files diff.

---

## Step 5 — Trim `ReportMismatch` in `app.go`

`ReportMismatch` (app.go:414) re-reads each file in the group to recompute xxHash and dHash for the JSON report. These values are **already in `scanResult`** (the `ImageHash` slice that produced the groups). The current code does ~2N file reads + 2N decodes per report — pointlessly.

**Action**: change the report builder to read xxHash and dHash from the existing `ImageMetadata` / `ImageHash` data carried in `scanResult.Groups`. The current `DuplicateGroup.Images` carries `ImageMetadata`, which doesn't have hash fields — so add `XXHash uint64` and `DHash uint64` fields to `ImageMetadata` and populate them in `buildGroup` (grouper.go:362) by looking up the hash from `metaMap`'s sibling map. (`buildGroup` already receives the hashMap context indirectly via path→metadata.) Pass `hashMap` through to `buildGroup` and copy `h.XXHash`, `h.DHash` into the result.

Once `ReportMismatch` no longer calls `ComputeXXHash` or `ComputeDHash`, **delete** both functions from `hasher.go`.

After this step the only active hash entry points are:
- `runExactHashPhase` (in hasher_pipeline.go) — uses inline reads, no `Compute*` helpers
- `computeDHashFromHeader` (in hasher.go) — header-only fast path
- `computeDHashSmart` / `computePHashFromData` (in hasher.go) — buffer-based, called from `computePerceptualHashes`
- `computeDHashHEIC` (in heic_support.go) — HEIC-specific (rewritten in Plan 2)

---

## Step 6 — Trim `runScan` and `GetThumbnail` in `app.go`

### 6a. `runScan` (app.go:95)

The conditional in `runScan` that picks between `ConcurrentScanDirectory` and `ScanDirectoryFiltered`:

```go
noExtraFilters := req.MinWidth == 0 && req.MaxHeight == 0 && req.MinFileSize == 0 && req.MaxFileSize == 0
if len(allowedExts) == 0 && noExtraFilters && req.IncludeSubfolders {
    paths, err = ConcurrentScanDirectory(req.Path)
} else {
    paths, err = ScanDirectoryFiltered(...)
}
```

This is fine functionally but the `ConcurrentScanDirectory` branch only fires when ALL filters are empty AND the user wants subfolders — rare in practice. Rather than maintain two walkers, **delete** `ConcurrentScanDirectory` and have `ScanDirectoryFiltered` do the parallel-per-subdirectory walk internally when `len(allowedExts) == 0 && minWidth == 0 && maxHeight == 0 && minFileSize == 0 && maxFileSize == 0 && includeSubfolders`. Plan 2 will further optimise the walker.

For now, the simplest move: keep both walkers but simplify the call site to a single helper `walkScanRoot(path, req)` that encapsulates the branching. This makes Plan 2's walker consolidation a one-file change.

### 6b. `GetThumbnail` (app.go:293)

The function body is fine for now (Plan 2 rewrites the resize loop) but it has a stale comment referencing `extractEmbeddedJPEG` "Slower but works for many camera formats" — keep the call, just verify the import after Step 1d (the file moved).

---

## Step 7 — Verify and commit

After every step, the build must pass:

```bash
go build ./...
go vet ./...
```

Then run a manual smoke test against the `samples/` directory:

```bash
# from the project root, if a CLI exists, otherwise via the Wails dev mode
# Build the binary and run a scan on samples/ to confirm:
#   - All 8 HEIC files are detected
#   - All 4 false_duplicates JPGs are detected
#   - No groups are produced for the HEIC files (they're all distinct)
#   - The 4 IMG_136*.JPG files MAY group as a series (they are visually similar burst frames)
```

Commit message suggestion:

```
refactor: consolidate EXIF on bep/imagemeta, drop goexif, remove dead code

- Split server.go into types.go + state.go + raw_preview.go
- Move ScanDirectoryFiltered into scanner.go where it belongs
- Migrate all EXIF reading to bep/imagemeta (2.6× faster, supports HEIC natively)
- Remove rwcarlsen/goexif dependency
- Delete unused: ComputePartialXXHash, computeXXHashStreaming, ComputeAllHashes,
  ComputeDHashFromEXIFThumbnail, ComputePHash, HashCache.Lookup/Store
- ReportMismatch now reuses cached hashes instead of re-reading files
- Net: -1 dependency, -~700 lines, single EXIF code path

No behaviour change. Plan 2 (performance) follows.
```

---

## Acceptance criteria

- [ ] `go build ./...` and `go vet ./...` pass
- [ ] `grep -rn "rwcarlsen/goexif" --include="*.go"` returns zero hits
- [ ] `grep -rn "func ScanDirectoryFiltered" --include="*.go"` shows it in `scanner.go`, not `server.go`
- [ ] `server.go` no longer exists
- [ ] `go.mod` no longer lists `github.com/rwcarlsen/goexif`
- [ ] `wc -l *.go` shows ~700 fewer lines than before (cleanup target)
- [ ] Manual scan of `samples/` produces the same group results as the pre-refactor branch
- [ ] No new TODO/FIXME comments left behind

---

## What this plan does NOT do

These are intentionally deferred to Plan 2:

- HEIC fast path (still does full `io.ReadAll` + WASM decode of every file)
- Mutex-guarded result maps in parallel phases
- Pure-Go nearest-neighbour resize in `GetThumbnail` and `heic_support.resizeImageToJPEG`
- `extractEmbeddedJPEG` validating with full `jpeg.Decode`
- `GetResults()` copying the entire scanResult struct on every 500ms poll
- Walker consolidation (kept as a single helper here; properly merged in Plan 2)

If you finish Plan 1 with budget remaining in the same session, **stop and commit**. Plan 2 needs a clean baseline to measure against.
