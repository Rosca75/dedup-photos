# PERF_FIX_V3.md — Kill the Real Bottleneck: ComputeImageQualityMetrics

> **Purpose:** Claude Code implementation plan. Execute in order.
> **Branch:** `preview`
> **Problem:** Grouping takes 514s for 2 771 files = 186 ms/file.
> **Root cause found:** `ComputeImageQualityMetrics` does `image.Decode(file)` —
> a **full pixel decode** of every image — for blockiness/blurring scores.
> On a Z: USB drive, reading a 5 MB JPEG + decompressing ~12 MP = ~150-180 ms.
> This single function call (metadata.go line 372) accounts for **99% of grouping time**.

---

## The Smoking Gun

`ExtractMetadataFast` (metadata.go:285) was designed to open the file only once (128 KB read).
It does that correctly for EXIF, dimensions, and quality score.

**But at line 372, it calls:**
```go
meta.Blockiness, meta.Blurring = ComputeImageQualityMetrics(path)
```

`ComputeImageQualityMetrics` (line 576) does:
1. `os.Open(path)` — another file open (~50 ms on USB)
2. `image.Decode(file)` — **full JPEG decode** (~100-130 ms for 12 MP on USB)
3. Builds a 512×512 grayscale grid from decoded pixels
4. Computes blockiness (gradient analysis) and blurring (Laplacian variance)

**Cost: ~150-180 ms per file. For 2 771 files: ~480 s. That's the entire bottleneck.**

The EXIF parsing, DecodeConfig, quality score — all the work `ExtractMetadataFast` does
correctly in its single 128 KB read — costs only ~10 ms/file combined. The "fast path"
isn't slow. The full-image decode hiding at the bottom is.

---

## Fix #1 — Make Blockiness/Blurring Lazy (Immediate Fix, Biggest Win)

**Estimated savings: ~480 s on Scan 2 (2 771 files), reducing grouping from 514 s to ~30 s**

Blockiness and blurring scores are **cosmetic quality indicators** used to help the user
decide which duplicate to keep. They do NOT affect duplicate detection, grouping, series
classification, or the quality score ranking. They are displayed in the UI but are
never the deciding factor.

### Option A (recommended): Compute on-demand in GetThumbnail

Move blockiness/blurring computation to the `GetThumbnail` method in `app.go`.
When the user expands a group and thumbnails are loaded, compute the metrics then.
This means:
- Scanning is fast (no full decode during grouping)
- Metrics are computed only for images the user actually views
- Most duplicate groups are never expanded → most images never get decoded

### Option B (simpler): Remove from ExtractMetadataFast, compute later

Simply remove the `ComputeImageQualityMetrics` call from `ExtractMetadataFast`.
Set Blockiness and Blurring to 0. The UI shows them as "N/A" or hides them.
Add a button "Compute quality metrics" per group if the user wants them.

### Implementation (Option A — recommended)

#### Step 1: Remove ComputeImageQualityMetrics from ExtractMetadataFast

In `metadata.go`, in `ExtractMetadataFast` (around line 370-373), **delete these lines:**

```go
// Step 6: Compute blockiness and blurring scores.
// NOTE: This still opens the file for a full image decode...
meta.Blockiness, meta.Blurring = ComputeImageQualityMetrics(path)
```

Also remove the same call from the old `ExtractMetadata` (around line 264-266):

```go
meta.Blockiness, meta.Blurring = ComputeImageQualityMetrics(path)
```

Blockiness and Blurring will now be 0.0 by default (Go zero values).

#### Step 2: Add a new Wails-bound method in app.go

```go
// GetImageQualityMetrics computes blockiness and blurring scores for a single
// image. Called lazily by the frontend when the user expands a duplicate group,
// so the expensive full-image decode only happens for images the user views.
//
// This was moved out of the scan pipeline because ComputeImageQualityMetrics
// does a full JPEG decode (~150 ms/file on USB), which was causing the grouping
// phase to take 8+ minutes for large scans on external drives.
func (a *App) GetImageQualityMetrics(path string) map[string]interface{} {
	blockiness, blurring := ComputeImageQualityMetrics(path)
	return map[string]interface{}{
		"blockiness": blockiness,
		"blurring":   blurring,
	}
}
```

#### Step 3: Update the frontend to fetch metrics lazily

In `static/js/table.js`, when a group is expanded and thumbnails are rendered,
add an async call for each image:

```javascript
// After rendering the thumbnail for an image in an expanded group:
window.go.main.App.GetImageQualityMetrics(img.path).then(metrics => {
    // Update the displayed blockiness/blurring values for this image.
    const el = document.querySelector(`[data-path="${CSS.escape(img.path)}"] .quality-metrics`);
    if (el) {
        el.textContent = `Blockiness: ${metrics.blockiness.toFixed(1)} | Blur: ${metrics.blurring.toFixed(1)}`;
    }
});
```

**Note:** If the UI currently doesn't display blockiness/blurring prominently,
this step can be deferred. The key win is Step 1.

---

## Fix #2 — Also Remove from Old ExtractMetadata

The old `ExtractMetadata` (line 266) also calls `ComputeImageQualityMetrics`.
Even though `parallelExtractMetadata` now calls `ExtractMetadataFast`, the old
function is still used as a fallback (grouper.go line 371) and potentially in
other code paths. Remove the call there too.

---

## Fix #3 — Pre-Filter More Series Groups (Strengthen Criterion A)

The logs show `Pre-filtered 24 series groups. Remaining perceptual: 2616`.
Only 24 groups were caught by the filename pre-filter. This is because the
current `isFilenameSeriesFromPaths` requires:
- Confidence >= 95% threshold check happens later (in `detectSeriesGroups`)
- The pre-filter only runs on groups with `percMinDist[root] <= 3`

### Fix: Lower the distance threshold for pre-filtering

In grouper.go, in the pre-filtering section, change the threshold from 3 to the
actual scan threshold (which is already passed as `threshold` parameter):

```go
// Current (too strict):
if percMinDist[root] <= 3 {

// Better — use the actual scan threshold (typically 10):
// Any high-confidence group (distance <= threshold/2) is a strong candidate
if dist, ok := percMinDist[root]; ok && dist <= threshold/2 {
```

Also, the pre-filter should check confidence >= 85% (not just distance <= 3),
since many burst shots have distance 2-5 but are obviously series.

### Also: Check that the pre-filter actually removes paths from allPaths

Currently the pre-filter deletes from `percGroups` but `collectUniquePaths`
still sees all exact groups. Verify that removed groups' paths don't end up
in `allPaths` sent to `parallelExtractMetadata`.

---

## Fix #4 — Buffer Pool for ExtractMetadataFast

`ExtractMetadataFast` allocates a fresh 128 KB buffer on every call (line 312):
```go
buf := make([]byte, 128*1024)
```

For 2 771 files called in parallel, this is 2 771 allocations of 128 KB.
Use the same pool pattern as the hasher:

```go
// metaBufPool holds reusable 128 KB buffers for ExtractMetadataFast.
var metaBufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 128*1024)
		return &buf
	},
}
```

In `ExtractMetadataFast`:
```go
bufPtr := metaBufPool.Get().(*[]byte)
buf := *bufPtr
n, _ := io.ReadFull(f, buf)
f.Close()
buf = buf[:n]  // Note: need a local copy of the slice header
// ... use buf ...
// At the end of the function:
metaBufPool.Put(bufPtr)
```

**Important:** Make sure `metaBufPool.Put` is called on ALL return paths.
Use a pattern like:
```go
bufPtr := metaBufPool.Get().(*[]byte)
defer metaBufPool.Put(bufPtr)
buf := (*bufPtr)[:0]  // Reset length
```

Add `"sync"` to metadata.go imports if not already present.

---

## Expected Impact

| Change | Files affected | Per-file savings | Total savings (Scan 2) |
|--------|---------------|-----------------|----------------------|
| Fix #1: Remove full decode | 2 771 | ~170 ms → ~15 ms | **~430 s** |
| Fix #3: Better pre-filter | ~500 fewer files | skip entirely | **~8 s** |
| Fix #4: Buffer pool | 2 771 | ~1-2 ms GC | **~3-5 s** |

**Projected Scan 2 results:**

| Phase | Before | After |
|-------|--------|-------|
| Hashing (cached) | 1.91 s | 1.91 s |
| Grouping | 514.41 s | **~40 s** |
| **Total** | **518.87 s** | **~45 s** |

That's a **~12× speedup** on the grouping phase, bringing the cached rescan
from 8 min 39 s down to under 1 minute.

---

## Implementation Order

1. **Fix #1** — Remove `ComputeImageQualityMetrics` calls (5 min, massive win)
2. **Fix #2** — Same removal from old `ExtractMetadata` (1 min)
3. **Fix #4** — Buffer pool for 128 KB reads (5 min, minor win)
4. **Fix #3** — Strengthen series pre-filter (10 min, moderate win)
5. *Optional:* Add `GetImageQualityMetrics` Wails method + lazy frontend loading

---

## Testing Checklist

- [ ] `go build ./...` succeeds
- [ ] Rescan on Z: drive: grouping phase < 60 s (was 514 s)
- [ ] Duplicate groups still display correctly
- [ ] Quality scores still work (resolution, size, EXIF-based scoring unaffected)
- [ ] Blockiness/Blurring show as 0 in the UI (acceptable — cosmetic only)
- [ ] Series detection still works (the improved version from previous commit)
- [ ] No data races: `go build -race ./...`
