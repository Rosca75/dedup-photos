// =============================================================================
// hasher_pipeline.go — Parallel hash pipeline with file-size bucketing
// =============================================================================
//
// This file contains the orchestration of the hash pipeline that replaced the
// original single-pass ComputeAllHashes loop. Each phase is a separate function
// so that profiling and reasoning about performance is straightforward.
//
// PIPELINE OVERVIEW
// ─────────────────
// Phase 1 : loadMergedCache   — load + merge persistent caches from all scan paths.
// Phase 2 : statAllFiles      — parallel os.Stat for cache validation + sizes.
// Phase 3  : presplitByCache  — sequential split into cache-hits vs. misses.
//              (eliminates muCache mutex contention — optimisation #6)
// Phase 3a : runExactHashPhase
//              buildFileSizeBuckets  → singletons cannot be exact duplicates
//              runPartialHashPhase   → 64 KB partial xxHash for same-size files
//              runCollisionHashPhase → full read only for same-partial-hash groups
//              (optimisation #1: ~85% of files skip full reads for exact matching)
// Phase 3b : computePerceptualHashes
//              files with full data  → computeDHashSmart (reuse in-memory bytes)
//              all other files       → computeDHashFromHeader (128 KB EXIF thumb)
//              also extracts Width/Height from the same read (optimisation #3)
// Phase 4  : saveUpdatedCache — write enriched entries (now with Width/Height) to disk.
//
// MEMORY MODEL
// ────────────
// Singletons (~85% of corpus)   : one 128 KB header read, then discarded.
// Collision candidates (~15%)   : one full file read, held during Phase 3b, then freed.
// Cache hits                    : zero file I/O.
//
// OPTIMISATIONS IMPLEMENTED
// ─────────────────────────
// #1 File-size bucketing + partial xxHash fast lane
// #3 Pre-extract Width/Height during hash phase
// #5 sync.Pool streaming buffers (64 KB, reused across goroutines)
// #6 Lock-free cache access (presplitByCache is sequential; parallel phases
//    write only to local maps, never to cache.Entries)
// =============================================================================

package main

import (
	"bytes"       // bytes.NewReader — wraps in-memory data for image decoding.
	"context"     // context.Context for cancellation support.
	"fmt"         // fmt.Printf for [perf] timing lines.
	"image"       // image.DecodeConfig for extracting dimensions from buffers.
	"os"          // os.FileInfo, os.ReadFile, os.Stat.
	"runtime"     // runtime.NumCPU — default worker count.
	"sync/atomic" // atomic.Int32 for lock-free progress counters.
	"time"        // time.Now / time.Since for [perf] phase timing.

	"github.com/cespare/xxhash/v2" // xxHash: fast non-cryptographic hash.
)

// headerBufRetentionLimit caps how many 64 KB header buffers we retain for
// reuse across the exact/perceptual phases. At 64 KB × 1000 buffers this
// keeps peak extra memory below 64 MB — well under any reasonable RAM
// budget — while still covering the typical singleton-by-partial-hash case
// for corpora of a few thousand files.
const headerBufRetentionLimit = 1000

// =============================================================================
// buildFileSizeBuckets — Identify singleton files vs. same-size groups (#1)
// =============================================================================

// buildFileSizeBuckets groups paths by their file size.
//
// Singletons (unique sizes) cannot have exact duplicates — no other file has
// the same number of bytes. We skip full-file reads for them in the exact-hash
// phase, saving ~85% of expensive I/O.
//
// Returns:
//   - singletons: paths whose file size is unique in the scan set.
//   - buckets:    map from file size → list of paths sharing that size.
func buildFileSizeBuckets(paths []string, fileInfo map[string]os.FileInfo) (singletons []string, buckets map[int64][]string) {
	// Group all paths by their file size.
	sizeGroups := make(map[int64][]string, len(paths))
	for _, path := range paths {
		info, ok := fileInfo[path]
		if !ok {
			continue
		}
		sz := info.Size()
		sizeGroups[sz] = append(sizeGroups[sz], path)
	}

	// Separate unique-size files (singletons) from multi-file-size groups.
	buckets = make(map[int64][]string)
	for sz, group := range sizeGroups {
		if len(group) == 1 {
			singletons = append(singletons, group[0])
		} else {
			buckets[sz] = group
		}
	}
	return
}

// =============================================================================
// runPartialHashPhase — 64 KB partial xxHash for same-size files (#1, #5)
// =============================================================================

// runPartialHashPhase computes a 64 KB partial xxHash for every path in
// same-size groups.
//
// Workers write results into index-aligned slices (no mutex). The 64 KB
// header bytes are also retained per path so that the later perceptual-hash
// phase can reuse them without re-opening the file.
//
// Returns:
//   - partials:      path → 64 KB partial xxHash value.
//   - partialGroups: partial hash → list of paths (for collision detection).
//   - headerBytes:   path → 64 KB header buffer (only for paths whose partial
//                    hash is unique across the batch; buffers for collision
//                    candidates are dropped since those files are re-read in
//                    full during the collision phase).
func runPartialHashPhase(ctx context.Context, sameSizePaths []string, numWorkers int) (
	partials map[string]uint64,
	partialGroups map[uint64][]string,
	headerBytes map[string][]byte,
) {
	n := len(sameSizePaths)
	hashSlice := make([]uint64, n)
	bufSlice := make([][]byte, n) // owned copies of the header bytes

	runParallelIndexed(ctx, n, numWorkers, func(i int) {
		path := sameSizePaths[i]

		// Borrow a 64 KB buffer from the pool — used only for the hash read.
		// We then allocate a fresh, path-owned copy of the bytes actually read
		// so the pool buffer can be returned immediately.
		bufPtr := bufPool.Get().(*[]byte)
		buf := *bufPtr

		f, err := os.Open(path)
		if err != nil {
			bufPool.Put(bufPtr)
			return
		}
		nb, _ := f.Read(buf) // Read up to 64 KB; fewer bytes on small files.
		f.Close()
		if nb == 0 {
			bufPool.Put(bufPtr)
			return
		}

		hashSlice[i] = xxhash.Sum64(buf[:nb])

		// Keep a private copy of the header bytes for possible reuse.
		owned := make([]byte, nb)
		copy(owned, buf[:nb])
		bufSlice[i] = owned
		bufPool.Put(bufPtr)
	})

	// Reassemble results into maps (sequential, no contention).
	partials = make(map[string]uint64, n)
	partialGroups = make(map[uint64][]string, n)
	headerBytes = make(map[string][]byte, n)
	for i, path := range sameSizePaths {
		h := hashSlice[i]
		if h == 0 && bufSlice[i] == nil {
			continue // Open failed or empty file.
		}
		partials[path] = h
		partialGroups[h] = append(partialGroups[h], path)
		if bufSlice[i] != nil {
			headerBytes[path] = bufSlice[i]
		}
	}

	// Prune header buffers for collision-group paths: those files get a full
	// read in runCollisionHashPhase so their header bytes are redundant.
	for _, group := range partialGroups {
		if len(group) >= 2 {
			for _, path := range group {
				delete(headerBytes, path)
			}
		}
	}

	// Cap retention so peak memory stays bounded on very large scans.
	// 64 KB × 1000 = ~64 MB; the cap rarely fires in practice (most scans
	// have a few thousand singleton-size files at most).
	if len(headerBytes) > headerBufRetentionLimit {
		kept := 0
		for path := range headerBytes {
			kept++
			if kept > headerBufRetentionLimit {
				delete(headerBytes, path)
			}
		}
	}
	return
}

// =============================================================================
// runCollisionHashPhase — Full reads for same-partial-hash groups (#1, #3)
// =============================================================================

// runCollisionHashPhase reads the full content of files that share a partial
// xxHash (meaning they could be exact duplicates). For each file it computes:
//   - The full xxHash (for definitive exact-duplicate detection).
//   - The image dimensions via DecodeConfig (for aspect-ratio grouping, #4).
//
// The in-memory file bytes are returned in fullData so Phase 3b
// (computePerceptualHashes) can compute dHash without re-opening the file.
//
// Returns:
//   - fullHashes: path → full xxHash64.
//   - fullData:   path → raw file bytes (reused for dHash in Phase 3b).
//   - dims:       path → [width, height] extracted during the read.
func runCollisionHashPhase(ctx context.Context, partialGroups map[uint64][]string, numWorkers int) (
	fullHashes map[string]uint64,
	fullData map[string][]byte,
	dims map[string][2]int,
) {
	// Only process groups where 2+ files share the same partial hash.
	var needFull []string
	for _, group := range partialGroups {
		if len(group) >= 2 {
			needFull = append(needFull, group...)
		}
	}

	n := len(needFull)
	hashSlice := make([]uint64, n)
	dataSlice := make([][]byte, n)
	dimSlice := make([][2]int, n)

	runParallelIndexed(ctx, n, numWorkers, func(i int) {
		path := needFull[i]
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		hashSlice[i] = xxhash.Sum64(data)

		// Extract dimensions while the bytes are in memory — no extra I/O (#3).
		var w, ht int
		if cfg, _, decErr := image.DecodeConfig(bytes.NewReader(data)); decErr == nil {
			w, ht = cfg.Width, cfg.Height
		}
		dataSlice[i] = data // Kept for Phase 3b dHash reuse.
		dimSlice[i] = [2]int{w, ht}
	})

	fullHashes = make(map[string]uint64, n)
	fullData = make(map[string][]byte, n)
	dims = make(map[string][2]int, n)
	for i, path := range needFull {
		if dataSlice[i] == nil {
			continue // Read failed.
		}
		fullHashes[path] = hashSlice[i]
		fullData[path] = dataSlice[i]
		dims[path] = dimSlice[i]
	}
	return
}

// =============================================================================
// runExactHashPhase — Coordinates Phase 3a (file-size bucketing) (#1)
// =============================================================================

// runExactHashPhase implements the exact-duplicate fast lane (Phase 3a).
// It groups cache-miss paths by file size, uses 64 KB partial hashing to
// identify candidates, then does full reads only for collision groups.
//
// Returns:
//   - xxHashes: path → full xxHash (only for collision-group files; 0 elsewhere).
//   - fullData: path → file bytes for collision files (reused in Phase 3b).
//   - dims:     path → [width, height] for collision files.
func runExactHashPhase(ctx context.Context, misses []string, fileInfo map[string]os.FileInfo, numWorkers int) (
	xxHashes map[string]uint64,
	fullData map[string][]byte,
	dims map[string][2]int,
	headerBytes map[string][]byte,
) {
	// Separate singleton sizes from same-size groups.
	_, sameSizeBuckets := buildFileSizeBuckets(misses, fileInfo)

	// Collect all paths from same-size groups for partial hashing.
	var sameSizePaths []string
	for _, group := range sameSizeBuckets {
		sameSizePaths = append(sameSizePaths, group...)
	}

	if len(sameSizePaths) == 0 {
		// No same-size files in this batch — nothing to check for exact dups.
		return make(map[string]uint64), make(map[string][]byte), make(map[string][2]int), make(map[string][]byte)
	}

	// Phase 3a-i: 64 KB partial hash for all same-size files.
	// headerBytes carries the 64 KB reads forward to Phase 3b for reuse.
	_, partialGroups, headerBytes := runPartialHashPhase(ctx, sameSizePaths, numWorkers)
	if ctx.Err() != nil {
		return nil, nil, nil, nil
	}

	// Phase 3a-ii: Full reads for files sharing a partial hash.
	xxHashes, fullData, dims = runCollisionHashPhase(ctx, partialGroups, numWorkers)
	return
}

// =============================================================================
// computePerceptualHashes — Phase 3b: dHash for all cache-miss files (#1, #3)
// =============================================================================

// computePerceptualHashes computes the perceptual hash (dHash or pHash) for
// every cache-miss file. Files already in fullData reuse their in-memory bytes
// (no extra I/O). All other files are read as a 128 KB header so that the EXIF
// thumbnail can be extracted cheaply.
//
// Also returns image dimensions (width, height) extracted during the same read,
// so the aspect-ratio bucketer in GroupDuplicates doesn't need to re-open files.
//
// Returns:
//   - dHashes: path → dHash (omitted when 0 — skip perceptual matching).
//   - dims:    path → [width, height] (omitted when 0×0 — unknown dimensions).
func computePerceptualHashes(ctx context.Context, misses []string, fullData map[string][]byte, headerBytes map[string][]byte, numWorkers int, algorithm string) (
	dHashes map[string]uint64,
	dims map[string][2]int,
) {
	n := len(misses)
	hashSlice := make([]uint64, n)
	dimSlice := make([][2]int, n)

	// Lock-free counters for visibility into the header-buffer reuse ratio.
	// These inform whether the retention cap needs tuning on larger corpora.
	var hitsFullData, hitsHeaderBuf, reopenCount atomic.Int64

	runParallelIndexed(ctx, n, numWorkers, func(i int) {
		path := misses[i]
		var dh uint64
		var w, h int

		if data, ok := fullData[path]; ok {
			// Reuse in-memory bytes from the full-read (collision) phase.
			hitsFullData.Add(1)
			if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
				w, h = cfg.Width, cfg.Height
			}
			switch algorithm {
			case "phash":
				dh, _ = computePHashFromData(data)
			default:
				dh, _ = computeDHashSmart(data)
			}
		} else if header, ok := headerBytes[path]; ok {
			// Reuse the 64 KB header read from runPartialHashPhase — no new I/O.
			hitsHeaderBuf.Add(1)
			dh, w, h, _ = computeDHashFromHeaderBuffer(path, header, algorithm)
		} else {
			// Singleton-by-size (never opened before) or retention-capped:
			// header-only path reads ~128 KB for EXIF thumbnail + dimensions.
			reopenCount.Add(1)
			dh, w, h, _ = computeDHashFromHeader(path, algorithm)
		}

		hashSlice[i] = dh
		dimSlice[i] = [2]int{w, h}
	})

	dHashes = make(map[string]uint64, n)
	dims = make(map[string][2]int, n)
	for i, path := range misses {
		if hashSlice[i] != 0 {
			dHashes[path] = hashSlice[i]
		}
		if dimSlice[i][0] > 0 && dimSlice[i][1] > 0 {
			dims[path] = dimSlice[i]
		}
	}
	fmt.Printf("[perf] dHash reuse:       fullData=%d headerBuf=%d reopen=%d\n",
		hitsFullData.Load(), hitsHeaderBuf.Load(), reopenCount.Load())
	return
}

// =============================================================================
// Pipeline helpers — cache, stat, split, merge, save
// =============================================================================

// loadMergedCache loads the primary cache and merges entries from any extra
// scan-path caches. The primary cache takes precedence on key conflicts.
func loadMergedCache(scanPaths []string) *HashCache {
	if len(scanPaths) == 0 || scanPaths[0] == "" {
		return newEmptyCache()
	}
	cache := LoadCache(scanPaths[0])
	for _, sp := range scanPaths[1:] {
		if sp == "" {
			continue
		}
		extra := LoadCache(sp)
		for k, v := range extra.Entries {
			if _, exists := cache.Entries[k]; !exists {
				cache.Entries[k] = v
			}
		}
		fmt.Printf("[hasher] Merged %d entries from extra cache (%s)\n", len(extra.Entries), sp)
	}
	return cache
}

// statAllFiles runs os.Stat on every path in parallel and returns a map of
// path → os.FileInfo. Needed for cache validation and file-size bucketing.
//
// Workers write into an index-aligned slice (no mutex). The map is built
// sequentially once all stats complete.
func statAllFiles(ctx context.Context, paths []string, numWorkers int, reportFn ProgressCallback, total int) map[string]os.FileInfo {
	n := len(paths)
	infos := make([]os.FileInfo, n)
	var count atomic.Int32

	runParallelIndexed(ctx, n, numWorkers, func(i int) {
		info, err := os.Stat(paths[i])
		if err != nil {
			return
		}
		infos[i] = info
		cur := int(count.Add(1))
		if (cur%500 == 0 || cur == total) && reportFn != nil {
			reportFn("Reading file info...", cur, total)
		}
	})

	fileInfo := make(map[string]os.FileInfo, n)
	for i, path := range paths {
		if infos[i] != nil {
			fileInfo[path] = infos[i]
		}
	}
	return fileInfo
}

// presplitByCache separates paths into cache hits (with stored results) and
// cache misses (paths that need hashing). Runs sequentially — no mutex needed.
// This is where optimisation #6 is realised: cache access is never done inside
// a parallel loop, eliminating muCache contention entirely.
func presplitByCache(paths []string, fileInfo map[string]os.FileInfo, cache *HashCache) (hits []ImageHash, misses []string) {
	for _, path := range paths {
		info, ok := fileInfo[path]
		if !ok {
			continue // File disappeared between stat and hash phase.
		}
		xxh, dh, w, h, hit := cache.LookupAll(path, info)
		if hit {
			hits = append(hits, ImageHash{
				Path: path, XXHash: xxh, DHash: dh,
				Width: w, Height: h, Size: info.Size(),
			})
		} else {
			misses = append(misses, path)
		}
	}
	return
}

// buildFinalResults assembles the final []ImageHash in input-path order,
// merging cache hits with the newly computed hashes and dimensions.
func buildFinalResults(paths []string, hits []ImageHash, xxHashes, dHashes map[string]uint64, dims map[string][2]int, fileInfo map[string]os.FileInfo) []ImageHash {
	hitSet := make(map[string]ImageHash, len(hits))
	for _, h := range hits {
		hitSet[h.Path] = h
	}

	results := make([]ImageHash, 0, len(paths))
	for _, path := range paths {
		if h, ok := hitSet[path]; ok {
			results = append(results, h)
			continue
		}
		result := ImageHash{Path: path}
		if info, ok := fileInfo[path]; ok {
			result.Size = info.Size()
		}
		// xxHash = 0 for singletons and non-collision same-size files.
		// The grouper skips XXHash=0 in Pass 1 (no exact-dup grouping).
		result.XXHash = xxHashes[path]
		result.DHash = dHashes[path]
		if d, ok := dims[path]; ok {
			result.Width = d[0]
			result.Height = d[1]
		}
		results = append(results, result)
	}
	return results
}

// saveUpdatedCache writes all computed results back into the cache and
// persists it to disk. Width and Height are now stored per entry (v2).
func saveUpdatedCache(cache *HashCache, results []ImageHash, fileInfo map[string]os.FileInfo, scanPaths []string) {
	if len(scanPaths) == 0 || scanPaths[0] == "" {
		return
	}
	for i := range results {
		r := &results[i]
		if r.Error != nil {
			continue
		}
		info, ok := fileInfo[r.Path]
		if !ok {
			continue
		}
		cache.StoreAll(r.Path, info, r.XXHash, r.DHash, r.Width, r.Height)
	}
	if err := SaveCache(cache, scanPaths[0]); err != nil {
		fmt.Printf("[hasher] Warning: failed to save cache: %v\n", err)
	}
}

// =============================================================================
// HashAllImagesWithContext / HashAllImagesWithProgress — Public entry points
// =============================================================================

// HashAllImagesWithContext hashes all images using the split pipeline.
// Convenience wrapper around HashAllImagesWithProgress with no progress callback.
func HashAllImagesWithContext(ctx context.Context, paths []string, numWorkers int, algorithm string) []ImageHash {
	return HashAllImagesWithProgress(ctx, paths, numWorkers, algorithm, nil, nil)
}

// HashAllImagesWithProgress is the main entry point for the hash pipeline.
// It coordinates all phases and returns one ImageHash per input path.
//
// The function is called from app.go's runScan goroutine. The progressFn
// callback keeps the UI's progress bar updated throughout the scan.
func HashAllImagesWithProgress(ctx context.Context, paths []string, numWorkers int, algorithm string, progressFn ProgressCallback, scanPaths []string) []ImageHash {
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}
	total := len(paths)
	if total == 0 {
		return []ImageHash{}
	}

	report := func(phase string, cur, tot int) {
		if progressFn != nil {
			progressFn(phase, cur, tot)
		}
	}
	fmt.Printf("[hasher] Optimised pipeline: %d images, %d workers, alg=%s\n", total, numWorkers, algorithm)

	// Phase 1: Load + merge persistent caches.
	t0 := time.Now()
	report("Loading hash cache...", 0, total)
	cache := loadMergedCache(scanPaths)
	fmt.Printf("[perf] Cache load:       %.2fs  (%d entries)\n", time.Since(t0).Seconds(), len(cache.Entries))

	// Phase 2: Stat all files (parallel) for cache validation + size bucketing.
	t0 = time.Now()
	report("Reading file info...", 0, total)
	fileInfo := statAllFiles(ctx, paths, numWorkers, progressFn, total)
	if ctx.Err() != nil {
		return nil
	}
	fmt.Printf("[perf] File stat:        %.2fs  (%d files)\n", time.Since(t0).Seconds(), len(fileInfo))

	// Phase 3 (sequential): split cache hits from misses — zero lock contention.
	t0 = time.Now()
	hits, misses := presplitByCache(paths, fileInfo, cache)
	fmt.Printf("[perf] Cache split:      %.2fs  (%d hits, %d misses)\n", time.Since(t0).Seconds(), len(hits), len(misses))

	report(fmt.Sprintf("Computing fingerprints... (%d cached, %d to compute)", len(hits), len(misses)), 0, total)

	// Phase 3a: Exact-duplicate fast lane (file-size bucketing + partial xxHash).
	t0 = time.Now()
	xxHashes, fullData, exactDims, headerBytes := runExactHashPhase(ctx, misses, fileInfo, numWorkers)
	if ctx.Err() != nil {
		return nil
	}
	fmt.Printf("[perf] Exact hash (3a):  %.2fs  (%d full reads, %d header buffers retained)\n",
		time.Since(t0).Seconds(), len(fullData), len(headerBytes))

	// Phase 3b: Perceptual hash for all cache misses (EXIF thumbnail fast-path).
	t0 = time.Now()
	dHashes, percDims := computePerceptualHashes(ctx, misses, fullData, headerBytes, numWorkers, algorithm)
	if ctx.Err() != nil {
		return nil
	}
	fmt.Printf("[perf] Perceptual (3b):  %.2fs\n", time.Since(t0).Seconds())

	// Merge dimension maps: full-read dims take precedence over header dims.
	for path, d := range percDims {
		if _, ok := exactDims[path]; !ok {
			exactDims[path] = d
		}
	}

	// Assemble the final results slice.
	allResults := buildFinalResults(paths, hits, xxHashes, dHashes, exactDims, fileInfo)

	// Phase 4: Persist updated cache (now includes Width/Height per entry).
	t0 = time.Now()
	report("Saving cache...", total, total)
	saveUpdatedCache(cache, allResults, fileInfo, scanPaths)
	fmt.Printf("[perf] Cache save:       %.2fs\n", time.Since(t0).Seconds())
	fmt.Printf("[hasher] Done: %d images processed.\n", len(allResults))

	return allResults
}
