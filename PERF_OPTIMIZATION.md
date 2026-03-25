## Performance Optimization: Multi-Pass Hashing Pipeline

### Problem
Scanning 4,450 files on a NAS takes ~2 minutes. AntiDupl.NET does the same in ~10 seconds.
Root cause: we decode every image (full JPEG/HEIC decompression) to compute dHash,
even though 90%+ of files are unique and don't need perceptual hashing at all.

### Current pipeline (slow)
```
For EVERY file:
  1. Read entire file from NAS         → slow network I/O
  2. Compute xxHash on full bytes      → reads whole file again
  3. Decode full image into pixels     → CPU-intensive (12MP JPEG = ~50ms)
  4. Resize to 9x8 + compute dHash    → trivial
```

### Optimized pipeline (target: <15 seconds)
```
Pass 0: File size grouping       → os.Stat only (zero file reads)
         Only files sharing a size move to Pass 1.
         Unique sizes → skip entirely for exact matching.

Pass 1: Partial xxHash (first 64KB) → tiny network read per candidate
         Only files sharing same size AND same partial hash move to Pass 2.

Pass 2: Full xxHash               → only the remaining candidates
         Groups with identical full hash = exact duplicates (100%).
         Already-matched files are excluded from perceptual passes.

Pass 3: EXIF thumbnail dHash      → extract embedded thumbnail (~10KB)
         Most JPEG/HEIC files contain a thumbnail in EXIF metadata.
         Compute dHash on the thumbnail instead of decoding the full image.
         This is 50-100x faster than full decode.

Pass 4: Full decode dHash (fallback) → only files without EXIF thumbnail
         PNG, BMP, edited images that stripped EXIF, etc.
```

### Expected performance gains on 4,450 files
- Pass 0: 4,450 stat calls (~1s over NAS)
- Assume 500 files share sizes with at least one other → Pass 1 reads 500 × 64KB = 32MB (~2s)
- Assume 100 files match partial hash → Pass 2 reads 100 full files (~3s)
- Pass 3: ~3,950 remaining files need EXIF thumbnail extraction (~5s, tiny reads)
- Pass 4: ~200 files without EXIF → full decode (~3s)
- Total: ~14 seconds

---

### Implementation plan

#### Step 1: Add `ComputePartialXXHash` to `hasher.go`

Add a new function that reads only the first 64KB of a file:

```go
// ComputePartialXXHash reads only the first 64KB of a file and computes
// its xxHash. This is used as a fast pre-filter: files with different
// partial hashes cannot be exact duplicates.
//
// Parameters:
//   - path: absolute file path
//   - size: number of bytes to read (recommended: 65536 = 64KB)
//
// Returns:
//   - uint64: partial xxHash digest
//   - error: if the file cannot be read
func ComputePartialXXHash(path string, size int) (uint64, error) {
    f, err := os.Open(path)
    if err != nil {
        return 0, err
    }
    defer f.Close()

    buf := make([]byte, size)
    n, err := f.Read(buf)
    if err != nil && n == 0 {
        return 0, err
    }
    return xxhash.Sum64(buf[:n]), nil
}
```

#### Step 2: Add `ComputeDHashFromEXIFThumbnail` to `hasher.go`

Add a function that extracts the EXIF thumbnail and computes dHash on it
instead of decoding the full image:

```go
// ComputeDHashFromEXIFThumbnail extracts the EXIF thumbnail embedded in
// JPEG/HEIC files and computes a dHash on it. This avoids decoding the
// full-resolution image (50-100x faster).
//
// Returns ErrNoThumbnail if the file has no EXIF thumbnail, in which case
// the caller should fall back to full-decode dHash.
func ComputeDHashFromEXIFThumbnail(path string) (uint64, error) {
    // Use goexif to read the EXIF data
    // Extract the thumbnail bytes from the EXIF IFD1
    // Decode the thumbnail (it's usually a small JPEG, ~10-20KB)
    // Compute dHash on the decoded thumbnail
    // ... implementation details below
}
```

To extract the EXIF thumbnail, use the `goexif` library already in go.mod:
```go
import "github.com/rwcarlsen/goexif/exif"

f, _ := os.Open(path)
defer f.Close()
x, err := exif.Decode(f)
if err != nil {
    return 0, ErrNoThumbnail
}
thumb, err := x.JpegThumbnail()
if err != nil {
    return 0, ErrNoThumbnail
}
// thumb is []byte of a small JPEG — decode it with image.Decode
img, _, err := image.Decode(bytes.NewReader(thumb))
// ... then compute dHash on img using the existing 9x8 resize logic
```

Define a sentinel error:
```go
var ErrNoThumbnail = fmt.Errorf("no EXIF thumbnail available")
```

#### Step 3: Refactor `HashAllImagesWithContext` in `hasher.go`

Replace the current single-pass approach with the multi-pass pipeline.
The function signature stays the same so `server.go` doesn't need changes.

New logic inside `HashAllImagesWithContext`:

```
Phase A: Get file sizes (parallel os.Stat)
  → Build map[int64][]string (file size → list of paths)
  → Files with unique sizes: store xxHash=0, will need perceptual hash later
  → Files sharing sizes: candidates for exact match

Phase B: Partial xxHash on size-collision candidates (parallel, 64KB reads)
  → Build map[partialHash][]string
  → Files with unique partial hash within their size group: not exact dupes
  → Files sharing partial hash: candidates for full xxHash

Phase C: Full xxHash on partial-hash-collision candidates (parallel)
  → Files with same full xxHash = exact duplicates
  → Store results

Phase D: Perceptual hash on ALL non-error files (parallel)
  → Try EXIF thumbnail dHash first (fast path)
  → Fall back to full decode dHash if no thumbnail
  → Store results

Return all ImageHash results (xxHash + dHash for each file)
```

**Critical**: the `ImageHash` struct doesn't change. Every file still gets
both an `XXHash` and a `DHash` in the result. The optimization is about
*how* we compute them — avoiding unnecessary full-file reads and full-image
decodes.

Files with unique sizes still get XXHash=0 (they can't have exact dupes,
but they still need dHash for perceptual matching). The grouper already
handles this: XXHash=0 won't collide because only files that share the
same XXHash form exact groups.

Wait — that's wrong. If we set XXHash=0 for unique-size files, they'll
all "match" each other in the exact pass. Instead:

**Option A**: Compute a fast hash for unique-size files too (partial xxHash
is enough — 64KB read, no full file read). This is still much faster than
reading the full file.

**Option B**: Use file size + partial xxHash as the exact-match key.

Go with **Option A**: compute partial xxHash for ALL files (fast — 64KB each),
then full xxHash only for files that collide on partial hash. For perceptual:
EXIF thumbnail first, full decode fallback.

Updated pipeline:

```
Phase A: Parallel os.Stat → get file sizes (for progress + size-group filter)
Phase B: Parallel 64KB partial xxHash for ALL files
Phase C: Parallel full xxHash ONLY for files sharing same (size + partial hash)
Phase D: Parallel perceptual hash for ALL files:
         → Try EXIF thumbnail dHash (fast)
         → Fallback: full image decode dHash (slow)
```

#### Step 4: Update progress reporting

The current progress reports one phase "Hashing images...". Update to
report each phase separately:

```go
scanResult.Progress.Phase = "Reading file sizes..."
scanResult.Progress.Phase = "Computing quick fingerprints..."  // partial xxHash
scanResult.Progress.Phase = "Verifying exact matches..."       // full xxHash
scanResult.Progress.Phase = "Computing visual fingerprints..." // perceptual
scanResult.Progress.Phase = "Grouping duplicates..."
```

Update `scanResult.Progress.Current` within each phase.

#### Step 5: Update the progress polling in the frontend

No changes needed — the frontend already shows `progress.phase` and
`progress.current / progress.total`. The new phase names will appear
automatically.

---

### Files to modify

| File       | Changes                                                    |
|------------|------------------------------------------------------------|
| hasher.go  | Add `ComputePartialXXHash`, `ComputeDHashFromEXIFThumbnail`, refactor `HashAllImagesWithContext` |
| server.go  | Update progress phase strings in the scan goroutine (lines ~390-460) |
| grouper.go | No changes needed                                          |

### Files NOT to modify
- All frontend files (js/, css/, index.html) — no changes needed
- scanner.go, metadata.go, main.go — no changes needed

---

### Testing

1. Run `go run .` and scan a local folder with ~100 images first
2. Verify exact duplicates are still found correctly
3. Verify perceptual duplicates are still found correctly
4. Then test on the NAS folder with 4,450 files — target is <15 seconds
5. Check the console logs — each phase should print timing info

### Constraints
- The `ImageHash` struct must NOT change (it would break grouper.go)
- The `HashAllImagesWithContext` function signature must NOT change (it would break server.go)
- All existing tests and functionality must continue to work
- Add `fmt.Printf` timing logs for each phase so we can measure the improvement
