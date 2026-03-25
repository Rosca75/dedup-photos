## Performance Optimization — DedupPhotos Scanning Pipeline

### Problem
Scanning 4,450 files on NAS: DedupPhotos takes 2m46s, AntiDupl.NET takes 3-5s.
The two slow phases are "Computing quick fingerprints" and "Computing visual fingerprints."

### Root cause analysis
After reverse-engineering AntiDupl.NET's C++ source and researching industry
best practices for large-scale image deduplication, five architectural problems
explain our 30x performance gap:

---

## The 5 performance patterns to implement

### Pattern 1: PERSISTENT HASH CACHE (biggest impact — 10-30x on re-scans)

**What AntiDupl does**: Stores all computed data (CRC32, reduced images) in `.adi`
files on disk. On re-scan, checks each file's size + modification time against cache.
If unchanged → zero I/O, zero decoding. This is why it takes 3-5 seconds on re-scan.

**What the industry does**: Every production dedup system (DupHunter, Google's SSCD,
enterprise backup dedup) uses persistent fingerprint caches. It's the #1 optimization.

**Implementation**: New file `cache.go` (~150 lines)
- Cache stored as binary file in user home: `~/.dedup-photos/cache.gob`
- Use Go's `encoding/gob` for fast serialization (much faster than JSON for binary data)
- Key: file path. Validity check: size + modTime match.
- Cache entries: `{Size, ModTime, XXHash, DHash}`
- Load cache at scan start, save at scan end
- Progress reports: "Loaded 4200/4450 from cache" so the user sees the benefit

### Pattern 2: SINGLE FILE READ (2-3x gain on first scan)

**Current problem**: Each file is opened and read 3 separate times:
1. `os.ReadFile` for xxHash (reads whole file)
2. `os.Open` + `image.Decode` for dHash (reads and decodes whole file)
3. `os.Open` + `exif.Decode` for EXIF metadata (reads file again)

Over NAS, each file open incurs ~1-5ms network round-trip. For 4,450 files,
that's 13-66 seconds just in connection overhead, times 3 = up to 3 minutes.

**Fix**: Read file ONCE into `[]byte`, then compute everything from the buffer:
```go
data, _ := os.ReadFile(path)        // ONE network read
xxHash := xxhash.Sum64(data)         // from buffer
img, _ := image.Decode(bytes.NewReader(data))  // from buffer
exifData, _ := exif.Decode(bytes.NewReader(data)) // from buffer
```

### Pattern 3: EXIF THUMBNAIL FAST-PATH (5-10x gain for JPEG/HEIC first scan)

**What the research shows**: Most JPEG and HEIC files embed a thumbnail (typically
160x120 or 320x240) in their EXIF metadata. This thumbnail is ~10-20KB. Decoding
it is 50-100x faster than decoding a 12MP full image.

**For dHash specifically**: We resize to 9x8 pixels anyway. Computing dHash from
a 160x120 thumbnail produces virtually identical results to computing it from
a 4032x3024 original — the resize to 9x8 destroys all that extra resolution.

**Implementation in `hasher.go`**:
```go
func computeDHashSmart(data []byte) (uint64, error) {
    // Try 1: Extract EXIF thumbnail (fast — ~0.5ms per image)
    x, err := exif.Decode(bytes.NewReader(data))
    if err == nil {
        thumb, err := x.JpegThumbnail()
        if err == nil && len(thumb) > 0 {
            img, _, err := image.Decode(bytes.NewReader(thumb))
            if err == nil {
                return computeDHashFromImage(img), nil
            }
        }
    }
    // Try 2: Full image decode (slow — ~30-50ms per image)
    img, _, err := image.Decode(bytes.NewReader(data))
    if err != nil {
        return 0, err
    }
    return computeDHashFromImage(img), nil
}
```

### Pattern 4: SCALED JPEG DECODING (3-5x gain when full decode is needed)

**What the research reveals**: Go's native `image/jpeg` decoder is 4-5x slower
than libjpeg-turbo. But more importantly, there is a pure-Go library
`go-scaled-jpeg` (github.com/m8rge/go-scaled-jpeg) that can decode JPEG at
1/8 resolution during the DCT step. For a 4032x3024 image, this decodes to
504x378 — still far more than we need for a 9x8 dHash, but uses ~1/64th the
memory and is much faster because it skips most of the IDCT computation.

**However**, this adds a CGo dependency (requires C compiler on Windows) which
conflicts with our "zero dependencies" goal. So we should:
- Use EXIF thumbnail (Pattern 3) as primary fast-path (covers ~80% of photos)
- Use `image.DecodeConfig` to get dimensions without full decode when we just need metadata
- Keep native Go decoder as fallback — it's "only" 30ms per image

**Alternative pure-Go approach**: Use `image.DecodeConfig` to read JPEG header
only (dimensions + format) without decoding pixels. For cases where we need
dHash and have no EXIF thumbnail, this doesn't help, but it avoids decoding
images that are filtered out by size/extension settings.

### Pattern 5: PROGRESSIVE MULTI-STAGE FILTERING (AntiDupl's spatial indexing)

**What AntiDupl does**: Instead of hashing everything then comparing, it uses a
two-level comparison during collection (streaming architecture):
1. **Fast 4x4 thumbnail** (16 bytes) — computed by averaging the reduced image
   into a 4x4 grid. Used as instant pre-filter: squared pixel difference.
   Rejects 95%+ of non-matching pairs in nanoseconds.
2. **Reduced NxN image** (default 16x16 = 256 bytes) — full pixel comparison
   with SIMD-accelerated `SimdSquaredDifferenceSum`. Only run on pairs that
   passed the fast filter.
3. **3D spatial indexing** — for large collections (>10K images), AntiDupl uses
   a 3D grid indexed by (brightness sum, horizontal gradient, vertical gradient)
   from the 4x4 fast thumbnail. This limits comparisons to nearby "buckets"
   only, avoiding O(n²) entirely.

**What Facebook/HuggingFace do at scale**: Use FAISS (Facebook AI Similarity Search)
with approximate nearest-neighbor (ANN) algorithms. Embeddings are indexed in a
high-dimensional space and queries find similar vectors in sub-linear time.

**For our scale (thousands, not millions)**: Our BK-Tree is adequate. But we should
add the "fast pre-filter" concept. Before inserting into the BK-Tree and running
Hamming distance comparisons, store a compact byte-level signature (like AntiDupl's
4x4 = 16 bytes) and use it as a first rejection pass.

**However**, this is a micro-optimization compared to Patterns 1-3. BK-Tree search
is already O(log n) per query. The real time is spent in I/O and decoding, not
comparison.

---

## Priority-ordered implementation plan

### Phase 1 (biggest gains — do first)

| Pattern | Expected gain | Effort | Files to create/modify |
|---------|--------------|--------|----------------------|
| 1. Cache | 10-30x re-scan | Medium | Create `cache.go`, modify `hasher.go`, `server.go` |
| 2. Single read | 2-3x first scan | Small | Modify `hasher.go` |
| 3. EXIF thumbnail | 5-10x first scan | Small | Modify `hasher.go` |

Combined Phase 1 expected result:
- **Re-scan**: 2m46s → **3-8 seconds**
- **First scan**: 2m46s → **20-40 seconds**

### Phase 2 (diminishing returns — do later if needed)

| Pattern | Expected gain | Effort | Notes |
|---------|--------------|--------|-------|
| 4. Scaled JPEG decode | 2-3x fallback path | Medium | Only benefits files without EXIF thumbnails |
| 5. Fast pre-filter | 10-20% comparison speed | Large | Only matters at 50K+ images |

---

## Detailed implementation for Phase 1

### Step 1: Create `cache.go`

```go
package main

import (
    "encoding/gob"
    "os"
    "path/filepath"
    "crypto/sha256"
    "fmt"
    "time"
)

// HashCache provides persistent storage of computed hashes.
// On re-scan, files whose size and modification time haven't changed
// are served from cache — zero I/O, zero image decoding.
type HashCache struct {
    Version int                       `json:"v"`
    Entries map[string]*CachedEntry   // key = absolute file path
}

// CachedEntry stores the precomputed hashes for one file.
type CachedEntry struct {
    Size    int64  // file size in bytes
    ModTime int64  // modification time (Unix nanoseconds)
    XXHash  uint64 // xxHash64 of raw bytes
    DHash   uint64 // perceptual dHash
}

// LoadCache loads the cache from disk. Returns empty cache on any error.
func LoadCache(scanPath string) *HashCache { ... }

// SaveCache writes the cache to disk atomically (write-then-rename).
func SaveCache(cache *HashCache, scanPath string) error { ... }

// cachePath returns ~/.dedup-photos/cache_<hash-of-scanpath>.gob
func cachePath(scanPath string) string {
    home, _ := os.UserHomeDir()
    dir := filepath.Join(home, ".dedup-photos")
    os.MkdirAll(dir, 0755)
    h := sha256.Sum256([]byte(scanPath))
    return filepath.Join(dir, fmt.Sprintf("cache_%x.gob", h[:8]))
}

// Lookup checks if a cached entry is still valid (size + modtime match).
func (c *HashCache) Lookup(path string, info os.FileInfo) (uint64, uint64, bool) {
    entry, ok := c.Entries[path]
    if !ok {
        return 0, 0, false
    }
    if entry.Size != info.Size() || entry.ModTime != info.ModTime().UnixNano() {
        return 0, 0, false  // file changed, cache invalid
    }
    return entry.XXHash, entry.DHash, true
}

// Store adds or updates a cache entry.
func (c *HashCache) Store(path string, info os.FileInfo, xxHash, dHash uint64) {
    c.Entries[path] = &CachedEntry{
        Size:    info.Size(),
        ModTime: info.ModTime().UnixNano(),
        XXHash:  xxHash,
        DHash:   dHash,
    }
}
```

### Step 2: Refactor `hasher.go` — unified hash function

Replace the current approach where xxHash and dHash are computed separately
with a single function that:
1. Checks the cache first
2. Reads the file ONCE
3. Computes xxHash from the buffer
4. Tries EXIF thumbnail dHash (fast path)
5. Falls back to full decode dHash (slow path)
6. Stores results in cache

```go
// ComputeAllHashes computes both xxHash and dHash for a single file.
// It uses the cache for speed and reads the file only once.
func ComputeAllHashes(path string, info os.FileInfo, cache *HashCache) (uint64, uint64, error) {
    // 1. Check cache
    if xxh, dh, ok := cache.Lookup(path, info); ok {
        return xxh, dh, nil  // cache hit — zero I/O!
    }
    
    // 2. Cache miss — read file once
    data, err := os.ReadFile(path)
    if err != nil {
        return 0, 0, err
    }
    
    // 3. xxHash from buffer
    xxh := xxhash.Sum64(data)
    
    // 4. dHash — try EXIF thumbnail first
    dh, err := computeDHashSmart(data)
    if err != nil {
        dh = 0  // dHash failed, xxHash still valid
    }
    
    // 5. Store in cache
    cache.Store(path, info, xxh, dh)
    
    return xxh, dh, nil
}
```

### Step 3: Refactor `HashAllImagesWithContext`

The function signature stays the same. Internally:
1. Load cache at start
2. `os.Stat` all paths first (collect `os.FileInfo` for cache validation)
3. Workers call `ComputeAllHashes` which uses cache + single-read + EXIF thumbnail
4. Save cache after all hashing completes
5. Report progress with cache hit count

### Step 4: Update `server.go` scan goroutine

Add progress phases:
```go
"Loading hash cache..."
"Computing fingerprints... (2400 cached, 2050 to compute)"
"Grouping duplicates..."
"Saving cache..."
```

---

## Files to create
| File      | ~Lines | Purpose                                    |
|-----------|--------|--------------------------------------------|
| cache.go  | 120    | Persistent hash cache (gob format)         |

## Files to modify
| File       | Changes                                              |
|------------|------------------------------------------------------|
| hasher.go  | Add `ComputeAllHashes`, `computeDHashSmart`, refactor `HashAllImagesWithContext` |
| server.go  | Load/save cache, update progress strings             |

## Files NOT to modify
- All frontend files, scanner.go, metadata.go, grouper.go, main.go

---

## Constraints
- **Pure Go only** — no CGo, no libjpeg-turbo, no C dependencies (Windows compat)
- **ImageHash struct unchanged** — grouper.go must not be touched
- **HashAllImagesWithContext signature unchanged** — server.go orchestration stays
- **go:embed unaffected** — cache.go is backend only
- **Cache must be atomic** — write to temp file then rename (no corruption on crash)
- **Cache location**: `~/.dedup-photos/cache_<hash>.gob` (one cache per scan path)

## Testing
1. First scan → creates cache, takes 20-40s (improved from 2m46s via single-read + EXIF)
2. Immediate re-scan → loads from cache, takes 3-8s
3. Modify 5 files → re-scan → only those 5 get re-hashed
4. Delete cache → back to first-scan behavior
5. Verify: same duplicate groups found before and after optimization
6. Log timing per phase to console for measurement
