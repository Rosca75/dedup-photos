# dedup-photos — Plan 2: Performance Hot Paths

> **Branch**: `preview` (continued from Plan 1)  •  **Goal**: cut scan time on the 3,000-file HEIC corpus.
> **Constraints**: pure Go (no CGo, no external binaries). API/UX stays stable. Internals can change freely.
> **Estimated session size**: 1 Claude Code session, ideally with a fresh context after Plan 1 is committed.
>
> **Prerequisite**: Plan 1 (`01-CLEANUP-AND-CONSOLIDATION.md`) must be committed. The dead-code removal and bep/imagemeta consolidation are assumed.

---

## Performance baseline & where time goes

User reports **>6 minutes for 3,000 HEIC files** on the `preview` branch. The samples in `samples/` show:

- **Average HEIC size in samples: 3.0 MB** (range 1.1–5.7 MB; matches typical iPhone HDR HEIC)
- All samples carry the iPhone HDR signature: `ftyp = heic + tmap` brand
- Each file declares **50+ HEVC items** in `iinf` (image grid tiles + tonemap items + thumbnail item)
- **Box layout is highly regular**: `meta` box ends at ~32 KB, `iloc` (item locations) lives at ~31 KB, `mdat` (image data) starts at ~32 KB, `Exif` payload sits at ~32.5 KB. The full 3 MB is mostly HEVC bitstream.

This means: **everything Plan 2 needs to do — find the EXIF, find the thumbnail item's byte range, locate the iref `thmb` link — can be done by reading the first ~64 KB of each file.** The only thing that requires later bytes is decoding the actual thumbnail HEVC tile, and that tile is typically 5–15 KB located early in `mdat`.

### Estimated I/O budget

| Phase | Current | Target | Mechanism |
|---|---|---|---|
| HEIC read for dHash | 3,000 × 3.0 MB = **9.0 GB** | 3,000 × ~150 KB = **440 MB** | byte-range read of header + thumbnail tile only |
| HEIC read for thumbnail (UI) | 3,000 × 3.0 MB | 3,000 × ~150 KB | same |
| EXIF for HEIC | 3,000 × 3.0 MB (currently re-read) | shared with above | reuse the 64 KB header buffer |

That's an order-of-magnitude reduction in I/O on the dominant file format. Even before counting decode-time savings, this alone should bring the scan well under 2 minutes on a typical SSD.

---

## Context Claude needs before starting

Read these in full before changing anything:

```
hasher_pipeline.go    ← runPartialHashPhase, runCollisionHashPhase, computePerceptualHashes
hasher.go             ← computeDHashFromHeader, bufPool, headerBufPool
heic_support.go       ← computeDHashHEIC, heicThumbnailJPEG, resizeImageToJPEG
app.go                ← GetThumbnail, GetResults, runScan
metadata.go           ← any remaining file-open paths after Plan 1
parallel.go           ← runParallel
raw_preview.go        ← extractEmbeddedJPEG (created in Plan 1)
```

Inspect the sample files to confirm the structure described above:

```bash
hexdump -C samples/IMG_2735.HEIC | head -4    # ftyp + brands
grep -aob "Exif\|thmb\|iloc\|mdat" samples/IMG_2735.HEIC | head    # box offsets
```

Expected: every sample shows `Exif` near offset 1554, `thmb` near 1677-1683, `iloc` near 30000-31000, `mdat` start near 31000-32000. If your test files differ, adapt the read-ahead size accordingly (still: aim for "header-only").

---

## Step 1 — HEIC fast path: byte-range thumbnail extraction

**This is the single biggest win.** Today:

```go
// heic_support.go: heicThumbnailJPEG and computeDHashHEIC both do this:
data, err := io.ReadAll(f)        // ~3 MB read
img, err := heic.DecodeThumbnail(bytes.NewReader(data))   // WASM decode
```

The `heic.DecodeThumbnail` call internally:
1. Parses the box structure to find the `thmb` iref
2. Resolves the thumbnail item's byte range via `iloc`
3. Decodes that small HEVC tile via WASM

Step 1 already requires only the first ~32 KB. Step 2 requires `iloc` (also in the first ~32 KB). Step 3 needs the actual tile bytes — typically 5–15 KB starting somewhere in `mdat` (offset ~32 KB+).

We can do this end-to-end by reading much less than the full file. There are two implementation paths:

### Path A (preferred) — extend the upstream heic library

Your fork already contains `DecodeThumbnail`. Add a new entry point that accepts an `io.ReaderAt` (random-access reader) instead of an `io.Reader`:

```go
// In your Rosca75/heic fork, add a new exported function:
func DecodeThumbnailAt(r io.ReaderAt, size int64) (image.Image, error)
```

Internally it parses boxes by issuing small `ReadAt` calls (typically: one `ReadAt(0, 32768)` is enough to parse `ftyp` + `meta`, then a second `ReadAt(thumbItemOffset, thumbItemLen)` for the HEVC payload). The WASM decoder accepts a byte slice — pass exactly the slice for the thumbnail tile.

In `dedup-photos`, call sites become:

```go
f, err := os.Open(path)
if err != nil { return nil, err }
defer f.Close()
info, _ := f.Stat()
img, err := heic.DecodeThumbnailAt(f, info.Size())
```

This is much cleaner than the current `io.ReadAll` and avoids the OS having to bring 3 MB into memory per file.

**Submitting upstream**: gen2brain has indicated openness to discussion-first PRs. Open an issue first describing the use case (batch metadata pipelines), reference your existing PR conversation, and propose the API. Keep it tiny: one new exported function, no new dependencies, no `CLAUDE.md`, no `PLAN.md`, no binaries. This may take a few iterations to land — for the dedup-photos work, point your `go.mod` at your fork until it merges.

### Path B (fallback if Path A is delayed) — buffered slice approach

If extending the heic library upstream is going to take too long, do the parse in dedup-photos itself using `bep/imagemeta`'s ISOBMFF box walker (same library you're already on after Plan 1). Steps:

1. Read the first 64 KB of the file into a pooled buffer (extend `headerBufPool` from 128 KB to 64 KB and rename to `headerBuf64Pool`, or add a separate pool).
2. Use `bep/imagemeta` to enumerate items and find the one with `thmb` reference type (the library exposes the iref relationships).
3. Get the thumbnail item's offset and length from the parsed `iloc` table.
4. If the thumbnail bytes are within the 64 KB already read, slice them out. Otherwise issue a second `ReadAt(offset, length)` for just that range.
5. Pass the resulting byte slice to `heic.Decode` (which decodes a single HEVC stream — the existing WASM decoder works fine on a thumbnail tile).

This is more code in dedup-photos but ships immediately without depending on upstream merges. Document the choice in a comment at the top of `heic_support.go`.

### Apply to both call sites

Both `heicThumbnailJPEG` (UI thumbnail) and `computeDHashHEIC` (perceptual hash during scan) currently call `io.ReadAll` + `heic.DecodeThumbnail`. Both must use the new fast path.

`computeDHashHEIC` additionally extracts dimensions via `imagemeta.Decode(... Sources: imagemeta.CONFIG)`. After Plan 1 introduces the unified `extractExifInto`, you can fold the dimensions read into the same single 64 KB header parse — `bep/imagemeta` returns `ImageConfig.Width/Height` from the ISOBMFF `ispe` (image spatial extent) property, which is in the first 32 KB. Avoid two separate decodes of the same buffer.

### Acceptance for Step 1

After Step 1, on the 8 sample HEICs:

```bash
# Total bytes read by the HEIC path should be ~150 KB per file, not ~3 MB.
# Add a debug print:
log.Printf("[heic] %s: header=%d bytes thumb=%d bytes", path, headerBytes, thumbBytes)
```

Expected: `header=65536` (or less if the file is shorter), `thumb` between 5,000 and 20,000.

---

## Step 2 — Eliminate mutex-guarded result maps

Every parallel phase currently does:

```go
var mu sync.Mutex
results := make(map[string]X, len(paths))

runParallel(ctx, paths, numWorkers, func(path string) {
    // ... compute ...
    mu.Lock()
    results[path] = computed
    mu.Unlock()
})
```

This shows up in:
- `statAllFiles` (hasher_pipeline.go:325)
- `runPartialHashPhase` (hasher_pipeline.go:105)
- `runCollisionHashPhase` (hasher_pipeline.go:160)
- `computePerceptualHashes` (hasher_pipeline.go:257)
- `parallelExtractMetadata` (grouper.go:318)

On a 16-core machine processing 3,000 files, each map holds 3,000 entries and incurs 3,000 lock acquisitions. The lock itself is fast, but the map access plus pointer-rehashing under contention adds measurable overhead — and worse, it blocks workers during `map[path] = result` even though the work is purely independent.

**Fix**: change `runParallel` to take an indexed worker function and have each phase pre-allocate slices keyed by index. The result-map is built sequentially after all workers finish.

### 2a. New worker primitive in `parallel.go`

Add alongside the existing `runParallel`:

```go
// runParallelIndexed is like runParallel but passes the slice index too,
// so callers can write results into a pre-allocated, lock-free slice.
func runParallelIndexed(ctx context.Context, n int, numWorkers int, fn func(i int)) {
    if n == 0 { return }
    var idx atomic.Int64
    var wg sync.WaitGroup
    for w := 0; w < numWorkers; w++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for {
                if ctx.Err() != nil { return }
                i := int(idx.Add(1)) - 1
                if i >= n { return }
                fn(i)
            }
        }()
    }
    wg.Wait()
}
```

Atomic counter beats a buffered channel for this many small jobs — no allocation per item, no channel scheduling overhead.

### 2b. Refactor each phase

For each of the five phases listed above, the pattern becomes:

```go
// Before:
results := make(map[string]X)
var mu sync.Mutex
runParallel(ctx, paths, n, func(p string) {
    v := compute(p)
    mu.Lock(); results[p] = v; mu.Unlock()
})

// After:
out := make([]X, len(paths))
runParallelIndexed(ctx, len(paths), n, func(i int) {
    out[i] = compute(paths[i])
})
// Build the map once, sequentially, if downstream needs map access:
results := make(map[string]X, len(paths))
for i, p := range paths {
    results[p] = out[i]
}
```

For `runCollisionHashPhase` the result is three parallel slices (`fullHashes`, `fullData`, `dims`) — same pattern, three slices instead of three maps.

For `runPartialHashPhase` the secondary `partialGroups` map is built sequentially anyway after the parallel section — unchanged.

### 2c. Keep `runParallel` for genuinely path-based phases

Some phases really do iterate over a `[]string` and don't need indexed access. Keep `runParallel` for those; both helpers can coexist in `parallel.go`.

### Acceptance for Step 2

After Step 2, run with `GOMAXPROCS=8 go test -race ./...` (or equivalent build) and confirm no data-race warnings. The five hot-path mutexes should be gone — `grep -n "sync.Mutex\|mu.Lock" hasher_pipeline.go grouper.go` should show zero hits in the parallel sections.

---

## Step 3 — Stop double-reading cache-miss files

`computePerceptualHashes` (hasher_pipeline.go:257) handles cache-miss files in two branches:

```go
if data, ok := fullData[path]; ok {
    // Reuse bytes from runCollisionHashPhase
} else {
    // Header-only path
    dh, w, h, _ = computeDHashFromHeader(path, algorithm)   // opens file again
}
```

The `else` branch opens the file *again* even though `runPartialHashPhase` opened it earlier in the same scan to read 64 KB. We threw those bytes away.

**Fix**: have `runPartialHashPhase` retain its 64 KB header buffer for paths that are NOT in any collision group (i.e., singletons-by-partial-hash). These bytes can be passed to `computeDHashFromHeader` as a pre-read buffer.

### 3a. Change `runPartialHashPhase` return shape

Add a third return value:

```go
func runPartialHashPhase(...) (
    partials map[string]uint64,
    partialGroups map[uint64][]string,
    headerBytes map[string][]byte,   // NEW: 64 KB buffers for non-collision paths
)
```

Inside the worker, after computing the partial hash, decide whether to keep the buffer:
- If `partialGroups[h]` will end up with 2+ entries → drop (collision phase will full-read anyway)
- Otherwise → keep the slice in `headerBytes[path]`

You can't know the group sizes during the parallel section, so retain ALL header buffers from this phase, then prune in a sequential post-pass:

```go
for path, h := range partials {
    if len(partialGroups[h]) >= 2 {
        delete(headerBytes, path)   // collision phase will full-read
    }
}
```

### 3b. Pass header bytes through the pipeline

Thread `headerBytes` from `runExactHashPhase` → `HashAllImagesWithProgress` → `computePerceptualHashes`. In `computePerceptualHashes`:

```go
if data, ok := fullData[path]; ok {
    // existing collision branch
} else if header, ok := headerBytes[path]; ok {
    dh, w, h, _ = computeDHashFromHeaderBuffer(path, header, algorithm)   // NEW
} else {
    dh, w, h, _ = computeDHashFromHeader(path, algorithm)   // singleton-by-size, never read
}
```

### 3c. Add `computeDHashFromHeaderBuffer`

In `hasher.go`, alongside `computeDHashFromHeader`, add a sibling that takes a pre-read buffer:

```go
func computeDHashFromHeaderBuffer(path string, buf []byte, algorithm string) (dHash uint64, width, height int, err error)
```

Body is identical to `computeDHashFromHeader` minus the `os.Open` + `io.ReadFull` block. Refactor `computeDHashFromHeader` to call the buffer variant after reading.

### 3d. Memory pressure check

Retaining 64 KB per path × ~3,000 paths = 180 MB worst case. That's higher than current peak (~5 MB for the `bufPool`) but still well under any reasonable RAM budget. If you want to be cautious, cap retention to ~1,000 buffers (so ~64 MB) and let the rest fall through to the file-open path.

### Acceptance for Step 3

Re-run the scan. The `[perf] Perceptual (3b):` log line should drop noticeably (this is where the duplicated I/O lived). Add a debug counter for `header-buf hits` vs `re-open` and verify the ratio matches expectations.

---

## Step 4 — Rewrite the resize loop in `GetThumbnail` and `resizeImageToJPEG`

Both `app.go:GetThumbnail` and `heic_support.go:resizeImageToJPEG` use the slowest possible nearest-neighbour resize:

```go
for y := 0; y < newH; y++ {
    for x := 0; x < newW; x++ {
        thumb.Set(x, y, img.At(srcX, srcY))
    }
}
```

`img.At()` allocates per call (returns a `color.Color` interface), `Set()` does the same — for a 400×300 thumbnail that's 120,000 interface allocations per file × 3,000 files = 360 million allocations during a full UI browse session.

### Fix

Use `golang.org/x/image/draw` (already a transitive dep — no new modules) for nearest-neighbour resize:

```go
import "golang.org/x/image/draw"

dstRect := image.Rect(0, 0, newW, newH)
dst := image.NewRGBA(dstRect)
draw.NearestNeighbor.Scale(dst, dstRect, img, img.Bounds(), draw.Src, nil)
```

`draw.NearestNeighbor` uses fast pixel-format-specific paths and avoids the `color.Color` interface entirely.

For thumbnail quality (since you're decoding HEIC HEVC tiles which are often blocky on iPhone HDR images), consider `draw.ApproxBiLinear` or `draw.BiLinear` instead — they're much higher quality and only ~2× slower than nearest-neighbour, which is irrelevant here because the source images are already small (HEIC thumbnails are 320×240; JPEG EXIF thumbs are similar). For full-image fallbacks (the rare PNG/TIFF case), keep `NearestNeighbor` to avoid quality vs speed regressions.

Apply the change in both `app.go:GetThumbnail` (around line 353) and `heic_support.go:resizeImageToJPEG` (around line 90).

### Acceptance for Step 4

Time `GetThumbnail` on a single HEIC sample before and after — should drop from tens of milliseconds to single-digit milliseconds. Run a quick pprof CPU profile during a UI browse and confirm `(*RGBA).Set` and `(*color.NRGBA64).RGBA` no longer dominate.

---

## Step 5 — Trim `extractEmbeddedJPEG`

`raw_preview.go:extractEmbeddedJPEG` (moved from server.go in Plan 1) validates each candidate JPEG with a full `jpeg.Decode`. For a 50 MB ARW that may scan 50 MB looking for SOI/EOI pairs and decode each candidate — extremely expensive.

**Fix**: validate by parsing the JPEG header markers only (SOI → SOF0/2 → confirm width × height plausible → accept). The `jpeg.DecodeConfig` function does exactly this without decoding pixels:

```go
cfg, err := jpeg.DecodeConfig(bytes.NewReader(jpegData))
if err == nil && cfg.Width > 200 && cfg.Height > 200 {
    if len(jpegData) > len(bestJPEG) { bestJPEG = jpegData }
}
```

This is what the comment "Validate by attempting a full JPEG decode" should have always said. `DecodeConfig` parses headers only and returns instantly.

Additionally, the byte-by-byte scan loop can be replaced with `bytes.Index(data[i:], []byte{0xFF, 0xD8})` which uses an optimised assembly implementation — far faster than the manual loop.

### Acceptance for Step 5

Time `extractEmbeddedJPEG` on one of the iPhone HEIC files (just to see worst-case behaviour on a 5 MB file) — should drop from ~hundreds of ms to single-digit ms.

---

## Step 6 — Make `GetResults` cheap to poll

`app.go:GetResults` returns the full `ScanResult` — including the entire `[]DuplicateGroup` — under `scanMutex`. The frontend polls this **every 500ms** during a scan. After scan completion, the frontend may still poll briefly before noticing `Status == "complete"`. Each poll deep-copies the entire results struct (Wails serialises it to JSON), and on a large scan with thousands of duplicate groups this can be tens of MB serialised twice per second.

### Fix

Split into two methods:

```go
// Lightweight progress-only call. Returns just status + progress, no groups.
func (a *App) GetProgress() ScanProgress { ... }

// Full results, called once at completion.
func (a *App) GetResults() ScanResult { ... }
```

Update the JS poller to call `GetProgress` while `status == "scanning"` and only call `GetResults` once when status becomes `"complete"`.

The change is small in Go (one new method, ~5 lines) and small in JS (one conditional in the polling loop). It preserves all existing behaviour — the heavy serialisation just stops happening every 500 ms.

If touching the JS feels out of scope, the alternative is to keep `GetResults` but have it return `nil` for `Groups` when status is `"scanning"`. The JS frontend already has to wait for `"complete"` to render groups, so this is safe.

### Acceptance for Step 6

During a long scan, watch CPU usage in the running app. Before: one core stays warm purely from poll serialisation. After: idle between polls.

---

## Step 7 — End-to-end measurement

Re-run the same 3,000-file scan that previously took >6 minutes. Capture the `[perf]` log lines. Expected shape:

```
[perf] Directory walk:    XX s
[perf] File stat:         XX s
[perf] Cache load:        XX s
[perf] Cache split:       XX s
[perf] Exact hash (3a):   XX s   ← partial + collision
[perf] Perceptual (3b):   XX s   ← biggest drop expected (HEIC fast path + buffer reuse)
[perf] Grouping:          XX s
[perf] TOTAL:             XX s
```

Target with all six steps applied: well under 2 minutes on a typical SSD with mostly-HEIC content. If any single phase still dominates, that's the next thing to attack.

---

## Step 8 — Final cleanup

Re-run `go build ./...`, `go vet ./...`, and the smoke test from Plan 1's Step 7 (scan `samples/`, verify the 4 IMG_136*.JPG files still group correctly).

Commit message suggestion:

```
perf: HEIC byte-range fast path, lock-free worker results, cheaper polling

- HEIC dHash and thumbnail now read ~150 KB per file instead of full 3 MB
  (24× I/O reduction on iPhone HDR HEIC; uses ReaderAt + iloc parsing)
- runParallelIndexed: lock-free per-worker slice writes; 5 mutex-guarded
  result maps eliminated
- Cache-miss files no longer re-opened in computePerceptualHashes (header
  bytes from partial-hash phase are reused)
- Resize loops switched to golang.org/x/image/draw (no more img.At/Set)
- extractEmbeddedJPEG validates with DecodeConfig + bytes.Index (no pixel decode)
- GetProgress/GetResults split: 500ms poller no longer serialises full groups

3,000-file scan: 6m20s → XmYs on test corpus.
```

---

## Acceptance criteria (whole plan)

- [ ] HEIC scan path reads ≤200 KB per file in the common case (verified by debug log)
- [ ] No `sync.Mutex` in any parallel result-collection inside `hasher_pipeline.go` or `grouper.go`
- [ ] `cache-miss header re-read` count is zero (verified by counter)
- [ ] `golang.org/x/image/draw` is the only resize implementation in the codebase
- [ ] `GetResults` is not called during the scanning phase by the frontend
- [ ] `go build ./...`, `go vet ./...`, `go test -race ./...` (if tests exist) all pass
- [ ] 3,000-file scan completes meaningfully faster than the pre-Plan-2 baseline (target: well under 2 minutes)
- [ ] Same number of duplicate groups detected as the pre-Plan-2 baseline (no regressions in detection accuracy)

---

## What's intentionally out of scope

- **GPU-accelerated HEVC decode** — would require CGo or a compiled WASM SIMD path. Out of bounds.
- **Persistent thumbnail cache on disk** — current in-memory `sync.Map` is fine for a single session. A disk cache would help across sessions but is a separate feature, not a perf bug.
- **Multi-machine / NAS-aware scanning** — `ConcurrentScanDirectory` already addresses NAS latency moderately.
- **Switch to AV1/AVIF** — irrelevant; iPhones still produce HEVC HEIC.

If after Plan 2 any single phase still takes more than 30% of total time, open a follow-up note rather than expanding scope here.
