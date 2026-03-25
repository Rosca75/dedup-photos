// =============================================================================
// hasher.go — Hashing engine with parallel processing
// =============================================================================
//
// This file computes two types of hashes for each image:
//
// 1. xxHash (exact match): A very fast non-cryptographic hash of the raw file
//    bytes. If two files have the same xxHash, they are byte-for-byte identical.
//    Think of it like a fingerprint — identical files produce identical hashes.
//
// 2. dHash (perceptual match): A "difference hash" that captures the visual
//    structure of an image. Even if two photos are slightly different (resized,
//    recompressed, color-adjusted), their dHashes will be very similar. This
//    lets us find "visually similar" duplicates, not just exact copies.
//
// The file also implements a parallel processing pipeline using goroutines
// (Go's lightweight threads) and channels (Go's way of communicating between
// goroutines). This lets us hash many images simultaneously, taking advantage
// of multi-core CPUs.
//
// Key Go concepts used here:
//   - goroutine:    A lightweight thread. Started with the "go" keyword.
//   - channel:      A typed pipe for sending data between goroutines.
//   - sync.WaitGroup: A counter that lets you wait for multiple goroutines.
//   - math/bits:    Standard library for bit manipulation (popcount, etc.).
// =============================================================================

package main

import (
	"bytes"     // For wrapping byte slices as io.Reader (used for EXIF thumbnails).
	"context"   // For cancellation support.
	"fmt"       // Formatted I/O (printing).
	"image"     // Go's standard image interface and decoding registry.
	"math/bits" // Bit counting functions (like popcount).
	"os"        // File operations.
	"runtime"   // Access to Go runtime info like number of CPUs.
	"sync"      // Synchronization primitives (WaitGroup, Mutex).
	"sync/atomic" // Atomic counters for lock-free progress tracking.
	"time"      // For timing each phase of the pipeline.

	// Standard image format decoders. Importing these with "_" (blank import)
	// registers them with the image package so image.Decode can recognize them.
	// You don't call any functions from these packages directly — the import
	// side effect is what matters.
	_ "image/gif"  // Registers GIF decoder.
	_ "image/jpeg" // Registers JPEG decoder.
	_ "image/png"  // Registers PNG decoder.

	// Extended image format decoders from the Go "x" (experimental) packages.
	// These aren't in the standard library but are maintained by the Go team.
	_ "golang.org/x/image/bmp"  // Registers BMP decoder.
	_ "golang.org/x/image/tiff" // Registers TIFF decoder.
	_ "golang.org/x/image/webp" // Registers WebP decoder.

	// goexif extracts EXIF metadata from JPEG/TIFF files. We use it to
	// pull out embedded thumbnails for fast perceptual hashing.
	"github.com/rwcarlsen/goexif/exif"

	// xxhash is an extremely fast non-cryptographic hash function.
	// "v2" means we're using version 2 of the package.
	"github.com/cespare/xxhash/v2"
)

// =============================================================================
// Sentinel errors
// =============================================================================

// ErrNoThumbnail is returned when a file has no embedded EXIF thumbnail.
// The caller should fall back to full-decode dHash when this occurs.
var ErrNoThumbnail = fmt.Errorf("no EXIF thumbnail available")

// =============================================================================
// Types
// =============================================================================

// ImageHash holds the results of hashing a single image file.
// Each image gets two hashes computed: an xxHash for exact matching and a
// dHash for perceptual (visual similarity) matching.
type ImageHash struct {
	Path   string // The absolute filesystem path to the image file.
	XXHash uint64 // xxHash64 of the raw file bytes — for exact duplicate detection.
	DHash  uint64 // Perceptual difference hash — for visual similarity detection.
	Error  error  // If something went wrong hashing this file, the error is stored here.
}

// ProgressCallback is called by HashAllImagesWithContext to report progress.
// The hasher calls this whenever the phase changes or the count of processed
// files updates. This lets the server update the scan progress for the UI.
//
// Parameters:
//   - phase: human-readable phase description (e.g., "Computing quick fingerprints...")
//   - current: number of items processed in the current phase
//   - total: total number of items in the current phase
type ProgressCallback func(phase string, current int, total int)

// =============================================================================
// ComputeXXHash — Fast file-level hash for exact duplicate detection
// =============================================================================

// ComputeXXHash reads the entire file at the given path and computes its
// xxHash64 digest. xxHash is one of the fastest non-cryptographic hash
// functions available — it can process several GB/s on modern hardware.
//
// Two files with the same xxHash are (with overwhelming probability) byte-for-
// byte identical. This is our first-pass duplicate check: fast and definitive.
//
// Parameters:
//   - path: Absolute path to the file to hash.
//
// Returns:
//   - uint64: The 64-bit xxHash digest.
//   - error:  Non-nil if the file couldn't be read.
func ComputeXXHash(path string) (uint64, error) {
	// os.ReadFile reads the entire file into memory as a byte slice ([]byte).
	// For very large files this could use a lot of RAM, but photos are
	// typically 1-30 MB, which is fine.
	data, err := os.ReadFile(path)
	if err != nil {
		// If we can't read the file (permissions, missing, etc.), return the
		// error. The caller will handle it gracefully.
		return 0, fmt.Errorf("failed to read file %s: %w", path, err)
	}

	// xxhash.Sum64 computes the xxHash64 digest of the byte slice in a single
	// call. This is the simplest API — there's also a streaming API if you
	// don't want to load the whole file into memory.
	hash := xxhash.Sum64(data)

	return hash, nil
}

// =============================================================================
// ComputePartialXXHash — Fast pre-filter using only the first N bytes
// =============================================================================

// ComputePartialXXHash reads only the first 'size' bytes of a file and computes
// its xxHash. This is used as a fast pre-filter: files with different partial
// hashes cannot be exact duplicates, so we can skip reading the full file.
//
// For a 64KB read over a network (NAS), this is ~100x faster than reading a
// typical 6MB photo file. We use this to quickly eliminate non-duplicates
// before doing expensive full-file reads.
//
// Parameters:
//   - path: absolute file path to read
//   - size: number of bytes to read (recommended: 65536 = 64KB)
//
// Returns:
//   - uint64: partial xxHash digest of the first 'size' bytes
//   - error: if the file cannot be opened or read
func ComputePartialXXHash(path string, size int) (uint64, error) {
	// Open the file — we only need to read the beginning, not the whole thing.
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	// Allocate a buffer of the requested size and read into it.
	// f.Read may return fewer bytes than requested (e.g., if the file is
	// smaller than 'size'), and that's fine — we hash whatever we got.
	buf := make([]byte, size)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		// Only treat it as an error if we read zero bytes.
		// io.EOF with n > 0 just means the file was smaller than 'size'.
		return 0, err
	}

	// Hash only the bytes we actually read (buf[:n]).
	return xxhash.Sum64(buf[:n]), nil
}

// =============================================================================
// ComputeDHashFromEXIFThumbnail — Fast perceptual hash via embedded thumbnail
// =============================================================================

// ComputeDHashFromEXIFThumbnail extracts the EXIF thumbnail embedded in
// JPEG/HEIC files and computes a dHash on it. This avoids decoding the
// full-resolution image, which is 50-100x faster.
//
// Most JPEG files from cameras/phones embed a small thumbnail (~10-20KB JPEG)
// in the EXIF metadata. We decode that tiny image instead of the full 12MP one.
//
// Returns ErrNoThumbnail if the file has no EXIF thumbnail, in which case
// the caller should fall back to full-decode dHash (ComputeDHash).
func ComputeDHashFromEXIFThumbnail(path string) (uint64, error) {
	// Open the file to read EXIF data.
	f, err := os.Open(path)
	if err != nil {
		return 0, ErrNoThumbnail
	}
	defer f.Close()

	// Parse the EXIF metadata from the file header.
	x, err := exif.Decode(f)
	if err != nil {
		// No EXIF data at all — not an error, just means no thumbnail.
		return 0, ErrNoThumbnail
	}

	// Extract the embedded JPEG thumbnail from the EXIF IFD1 block.
	// This is typically a small JPEG (160x120 to 320x240 pixels).
	thumb, err := x.JpegThumbnail()
	if err != nil || len(thumb) == 0 {
		// EXIF exists but no thumbnail embedded (common in edited images).
		return 0, ErrNoThumbnail
	}

	// Decode the thumbnail JPEG into an image.Image.
	// bytes.NewReader wraps the []byte so it satisfies io.Reader.
	img, _, err := image.Decode(bytes.NewReader(thumb))
	if err != nil {
		return 0, ErrNoThumbnail
	}

	// Compute dHash on the thumbnail image using the same 9x8 algorithm.
	return computeDHashFromImage(img), nil
}

// computeDHashFromImage computes a dHash from an already-decoded image.
// This is the shared core logic used by both ComputeDHash (full decode)
// and ComputeDHashFromEXIFThumbnail (thumbnail decode).
func computeDHashFromImage(img image.Image) uint64 {
	bounds := img.Bounds()
	srcWidth := bounds.Max.X - bounds.Min.X
	srcHeight := bounds.Max.Y - bounds.Min.Y

	const dstWidth = 9
	const dstHeight = 8

	// Build the 9x8 grayscale grid via nearest-neighbor sampling.
	gray := make([][]uint32, dstHeight)
	for y := 0; y < dstHeight; y++ {
		gray[y] = make([]uint32, dstWidth)
	}

	for y := 0; y < dstHeight; y++ {
		for x := 0; x < dstWidth; x++ {
			srcX := bounds.Min.X + (x * srcWidth / dstWidth)
			srcY := bounds.Min.Y + (y * srcHeight / dstHeight)
			r, g, b, _ := img.At(srcX, srcY).RGBA()
			gray[y][x] = (299*r + 587*g + 114*b) / 1000
		}
	}

	// Compute the difference hash: compare each pixel to its right neighbor.
	var hash uint64
	bitPosition := 0
	for y := 0; y < dstHeight; y++ {
		for x := 0; x < dstWidth-1; x++ {
			if gray[y][x] > gray[y][x+1] {
				hash |= 1 << uint(bitPosition)
			}
			bitPosition++
		}
	}

	return hash
}

// =============================================================================
// ComputeDHash — Perceptual "difference hash" for visual similarity
// =============================================================================

// ComputeDHash computes a "difference hash" (dHash) for the image at the
// given path. This is a perceptual hashing algorithm that captures the
// visual structure of an image in a 64-bit integer.
//
// HOW THE dHash ALGORITHM WORKS (step by step):
//
//  1. LOAD the image and decode it into pixels.
//
//  2. RESIZE to 9 columns × 8 rows. Why 9×8? Because we'll compare each
//     pixel to its right neighbor, producing 8 comparisons per row.
//     8 rows × 8 comparisons = 64 bits = one uint64.
//
//  3. CONVERT TO GRAYSCALE. We only care about brightness differences, not
//     color. Each pixel becomes a single brightness value (0-255).
//
//  4. COMPUTE DIFFERENCES. For each pixel, compare it to the pixel to its
//     right. If the left pixel is brighter, set the bit to 1; otherwise 0.
//
//  5. PACK INTO 64 BITS. The 64 comparison results become a 64-bit integer.
//
// WHY THIS WORKS:
//   - The resize step removes fine detail, keeping only the overall structure.
//   - Comparing neighbors instead of using absolute values makes the hash
//     invariant to brightness/contrast changes.
//   - Two visually similar images will have very similar dHash values.
//   - The "Hamming distance" (number of differing bits) between two dHashes
//     tells you how visually different the images are. 0 = identical structure,
//     64 = completely different.
//
// Parameters:
//   - path: Absolute path to an image file.
//
// Returns:
//   - uint64: The 64-bit difference hash.
//   - error:  Non-nil if the image couldn't be decoded.
func ComputeDHash(path string) (uint64, error) {
	// Open the file for reading.
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("failed to open image %s: %w", path, err)
	}
	defer file.Close()

	// Decode the full image (JPEG, PNG, etc. — auto-detected via magic bytes).
	img, _, err := image.Decode(file)
	if err != nil {
		return 0, fmt.Errorf("failed to decode image %s: %w", path, err)
	}

	// Delegate to the shared 9x8 dHash computation.
	return computeDHashFromImage(img), nil
}

// =============================================================================
// ComputePHash — Average-based perceptual hash (alternative algorithm)
// =============================================================================

// ComputePHash computes a simple average-based perceptual hash. Unlike dHash
// which compares adjacent pixels, pHash compares each pixel to the overall
// average brightness. This can be more robust for certain types of edits.
func ComputePHash(path string) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("failed to open image %s: %w", path, err)
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return 0, fmt.Errorf("failed to decode image %s: %w", path, err)
	}

	bounds := img.Bounds()
	srcWidth := bounds.Max.X - bounds.Min.X
	srcHeight := bounds.Max.Y - bounds.Min.Y

	const size = 8
	gray := make([][]uint32, size)
	for y := 0; y < size; y++ {
		gray[y] = make([]uint32, size)
	}

	var total uint64
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			srcX := bounds.Min.X + (x * srcWidth / size)
			srcY := bounds.Min.Y + (y * srcHeight / size)
			r, g, b, _ := img.At(srcX, srcY).RGBA()
			luminance := (299*r + 587*g + 114*b) / 1000
			gray[y][x] = luminance
			total += uint64(luminance)
		}
	}

	avg := total / (size * size)

	var hash uint64
	bitPosition := 0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if uint64(gray[y][x]) > avg {
				hash |= 1 << uint(bitPosition)
			}
			bitPosition++
		}
	}

	return hash, nil
}

// =============================================================================
// HashAllImagesWithContext — Parallel hashing with cancellation support
// =============================================================================

// HashAllImagesWithContext hashes all images using a multi-pass pipeline that
// avoids unnecessary work. Instead of reading every full file and decoding
// every image, it uses increasingly expensive passes:
//
//	Phase A: Get file sizes (os.Stat only — zero file reads)
//	Phase B: Partial xxHash (64KB read) for ALL files — fast pre-filter
//	Phase C: Full xxHash ONLY for files sharing same (size + partial hash)
//	Phase D: Perceptual hash for ALL files (EXIF thumbnail first, full decode fallback)
//
// The function signature is unchanged from the original single-pass version,
// so server.go doesn't need modifications.
//
// The optional progressFn callback reports phase changes and progress counts
// to the caller (typically the server, which forwards it to the UI).
func HashAllImagesWithContext(ctx context.Context, paths []string, numWorkers int, algorithm string) []ImageHash {
	return HashAllImagesWithProgress(ctx, paths, numWorkers, algorithm, nil)
}

// HashAllImagesWithProgress is the same as HashAllImagesWithContext but accepts
// a ProgressCallback for reporting phase/progress updates to the UI.
func HashAllImagesWithProgress(ctx context.Context, paths []string, numWorkers int, algorithm string, progressFn ProgressCallback) []ImageHash {
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}

	totalImages := len(paths)
	if totalImages == 0 {
		return []ImageHash{}
	}

	// Helper to report progress (no-op if callback is nil).
	reportProgress := func(phase string, current, total int) {
		if progressFn != nil {
			progressFn(phase, current, total)
		}
	}

	fmt.Printf("[hasher] Starting multi-pass pipeline for %d images (%d workers, algorithm: %s)\n", totalImages, numWorkers, algorithm)

	// =======================================================================
	// Phase A: Get file sizes via os.Stat (no file content reads)
	// =======================================================================
	phaseStart := time.Now()
	reportProgress("Reading file sizes...", 0, totalImages)

	// fileSize maps each path to its file size in bytes.
	fileSize := make(map[string]int64, totalImages)
	// sizeGroups maps file size → list of paths with that size.
	sizeGroups := make(map[int64][]string)

	var statCount atomic.Int32
	var mu sync.Mutex
	runParallel(ctx, paths, numWorkers, func(path string) {
		info, err := os.Stat(path)
		if err != nil {
			return // Skip files we can't stat.
		}
		sz := info.Size()
		mu.Lock()
		fileSize[path] = sz
		sizeGroups[sz] = append(sizeGroups[sz], path)
		mu.Unlock()
		cur := int(statCount.Add(1))
		if cur%200 == 0 || cur == totalImages {
			reportProgress("Reading file sizes...", cur, totalImages)
		}
	})

	if ctx.Err() != nil {
		return nil
	}

	// Count how many files share a size with at least one other file.
	var sizeCollisionPaths []string
	for _, group := range sizeGroups {
		if len(group) > 1 {
			sizeCollisionPaths = append(sizeCollisionPaths, group...)
		}
	}

	fmt.Printf("[hasher] Phase A (file sizes): %d files, %d share sizes → %.1fs\n",
		totalImages, len(sizeCollisionPaths), time.Since(phaseStart).Seconds())

	// =======================================================================
	// Phase B: Partial xxHash (64KB) for ALL files — fast pre-filter
	// =======================================================================
	phaseStart = time.Now()
	reportProgress("Computing quick fingerprints...", 0, totalImages)

	const partialSize = 65536 // 64KB — enough to distinguish most files.

	// partialHash maps each path to its partial xxHash.
	partialHash := make(map[string]uint64, totalImages)
	var partialCount atomic.Int32
	var muPartial sync.Mutex

	runParallel(ctx, paths, numWorkers, func(path string) {
		ph, err := ComputePartialXXHash(path, partialSize)
		if err != nil {
			return
		}
		muPartial.Lock()
		partialHash[path] = ph
		muPartial.Unlock()
		cur := int(partialCount.Add(1))
		if cur%200 == 0 || cur == totalImages {
			reportProgress("Computing quick fingerprints...", cur, totalImages)
		}
	})

	if ctx.Err() != nil {
		return nil
	}

	fmt.Printf("[hasher] Phase B (partial xxHash): %d files × 64KB → %.1fs\n",
		totalImages, time.Since(phaseStart).Seconds())

	// =======================================================================
	// Phase C: Full xxHash ONLY for files that share (size + partial hash)
	// =======================================================================
	phaseStart = time.Now()

	// Build groups keyed by (size, partial hash). Only groups with 2+ files
	// need full xxHash — those are the exact-duplicate candidates.
	type sizePartialKey struct {
		size    int64
		partial uint64
	}
	spGroups := make(map[sizePartialKey][]string)
	for _, path := range paths {
		sz, ok1 := fileSize[path]
		ph, ok2 := partialHash[path]
		if !ok1 || !ok2 {
			continue
		}
		key := sizePartialKey{size: sz, partial: ph}
		spGroups[key] = append(spGroups[key], path)
	}

	// Collect only the paths that need full xxHash (collision candidates).
	var fullHashPaths []string
	for _, group := range spGroups {
		if len(group) > 1 {
			fullHashPaths = append(fullHashPaths, group...)
		}
	}

	reportProgress("Verifying exact matches...", 0, len(fullHashPaths))

	// fullXXHash maps each path to its full-file xxHash.
	fullXXHash := make(map[string]uint64, len(fullHashPaths))
	var fullCount atomic.Int32
	var muFull sync.Mutex

	runParallel(ctx, fullHashPaths, numWorkers, func(path string) {
		xxh, err := ComputeXXHash(path)
		if err != nil {
			return
		}
		muFull.Lock()
		fullXXHash[path] = xxh
		muFull.Unlock()
		cur := int(fullCount.Add(1))
		total := len(fullHashPaths)
		if cur%50 == 0 || cur == total {
			reportProgress("Verifying exact matches...", cur, total)
		}
	})

	if ctx.Err() != nil {
		return nil
	}

	fmt.Printf("[hasher] Phase C (full xxHash): %d candidates out of %d → %.1fs\n",
		len(fullHashPaths), totalImages, time.Since(phaseStart).Seconds())

	// =======================================================================
	// Phase D: Perceptual hash for ALL files
	// =======================================================================
	// Try EXIF thumbnail dHash first (fast), fall back to full decode (slow).
	phaseStart = time.Now()
	reportProgress("Computing visual fingerprints...", 0, totalImages)

	perceptualHash := make(map[string]uint64, totalImages)
	perceptualErr := make(map[string]error, 0)
	var perceptualCount atomic.Int32
	var thumbCount, fullDecodeCount atomic.Int32
	var muPerceptual sync.Mutex

	// Choose the perceptual hash function based on algorithm setting.
	perceptualHashFn := func(path string) (uint64, error) {
		switch algorithm {
		case "phash":
			return ComputePHash(path)
		default: // "dhash" or "both" — use dHash for grouping
			return ComputeDHash(path)
		}
	}

	runParallel(ctx, paths, numWorkers, func(path string) {
		var dh uint64
		var err error

		// Fast path: try EXIF thumbnail (only for dHash, not pHash).
		if algorithm != "phash" {
			dh, err = ComputeDHashFromEXIFThumbnail(path)
			if err == nil {
				thumbCount.Add(1)
				muPerceptual.Lock()
				perceptualHash[path] = dh
				muPerceptual.Unlock()
				cur := int(perceptualCount.Add(1))
				if cur%200 == 0 || cur == totalImages {
					reportProgress("Computing visual fingerprints...", cur, totalImages)
				}
				return
			}
		}

		// Slow path: full image decode.
		fullDecodeCount.Add(1)
		dh, err = perceptualHashFn(path)
		muPerceptual.Lock()
		if err != nil {
			perceptualErr[path] = err
		} else {
			perceptualHash[path] = dh
		}
		muPerceptual.Unlock()
		cur := int(perceptualCount.Add(1))
		if cur%200 == 0 || cur == totalImages {
			reportProgress("Computing visual fingerprints...", cur, totalImages)
		}
	})

	if ctx.Err() != nil {
		return nil
	}

	fmt.Printf("[hasher] Phase D (perceptual): %d via EXIF thumbnail, %d via full decode → %.1fs\n",
		int(thumbCount.Load()), int(fullDecodeCount.Load()), time.Since(phaseStart).Seconds())

	// =======================================================================
	// Assemble final results
	// =======================================================================
	// Combine all phases into ImageHash results. Each file gets:
	//   - XXHash: full xxHash if it was a collision candidate, else partial xxHash
	//   - DHash: perceptual hash (from thumbnail or full decode)
	//   - Error: set if perceptual hashing failed
	allResults := make([]ImageHash, 0, totalImages)
	for _, path := range paths {
		result := ImageHash{Path: path}

		// Use full xxHash if available (collision candidate), else partial hash.
		// Partial hash is sufficient for non-collision files: they won't match
		// anything anyway since no other file shares their (size, partial) key.
		if xxh, ok := fullXXHash[path]; ok {
			result.XXHash = xxh
		} else if ph, ok := partialHash[path]; ok {
			result.XXHash = ph
		}

		// Set perceptual hash.
		if dh, ok := perceptualHash[path]; ok {
			result.DHash = dh
		}

		// Set error if perceptual hashing failed.
		if err, ok := perceptualErr[path]; ok {
			result.Error = err
		}

		allResults = append(allResults, result)
	}

	fmt.Printf("[hasher] Done! %d images hashed via multi-pass pipeline.\n", len(allResults))
	return allResults
}

// runParallel executes fn for each item in paths using numWorkers goroutines.
// It respects context cancellation — workers stop early if ctx is cancelled.
// This is a reusable helper to avoid duplicating the worker-pool pattern
// in every phase of the pipeline.
func runParallel(ctx context.Context, paths []string, numWorkers int, fn func(string)) {
	if len(paths) == 0 {
		return
	}

	jobs := make(chan string, len(paths))
	var wg sync.WaitGroup

	// Start worker goroutines.
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				if ctx.Err() != nil {
					return
				}
				fn(path)
			}
		}()
	}

	// Send jobs, checking for cancellation.
	for _, path := range paths {
		if ctx.Err() != nil {
			break
		}
		jobs <- path
	}
	close(jobs)

	wg.Wait()
}

// HammingDistance computes the Hamming distance between two 64-bit integers.
// The Hamming distance is the number of bit positions where the two values
// differ. For dHash values:
//
//   - Distance 0:  The images have identical visual structure.
//   - Distance 1-5: Very similar (probably the same photo, resized/recompressed).
//   - Distance 6-10: Somewhat similar (maybe same scene, different angle).
//   - Distance >10: Probably different images.
//
// HOW IT WORKS:
//  1. XOR the two values. XOR produces a 1 bit wherever the inputs differ
//     and a 0 bit wherever they match. So the XOR result has a 1 for every
//     position where a and b are different.
//  2. Count the number of 1 bits (called "popcount" or "population count").
//     This gives us the total number of differing positions.
//
// Example:
//
//	a = 0b1010  (binary)
//	b = 0b1100
//	a ^ b = 0b0110  (two bits differ)
//	popcount(0b0110) = 2
//	So the Hamming distance is 2.
//
// Parameters:
//   - a, b: Two 64-bit hash values to compare.
//
// Returns:
//   - int: The number of differing bits (0 to 64).
func HammingDistance(a, b uint64) int {
	// "^" is the XOR (exclusive or) operator. It produces a 1 bit for every
	// position where a and b differ.
	xor := a ^ b

	// bits.OnesCount64 counts the number of 1 bits in a uint64. This is also
	// known as "popcount" (population count). Modern CPUs have a dedicated
	// hardware instruction for this (POPCNT), so it's extremely fast.
	return bits.OnesCount64(xor)
}
