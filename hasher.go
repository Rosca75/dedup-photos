// =============================================================================
// hasher.go — Hash algorithms, types, and low-level utilities
// =============================================================================
//
// This file contains the core hashing primitives:
//
//   xxHash (exact match): A very fast non-cryptographic hash of raw file bytes.
//   Two files with the same xxHash are byte-for-byte identical.
//
//   dHash / pHash (perceptual match): A "difference hash" / "average hash" that
//   captures the visual structure of an image in 64 bits. Even slightly different
//   photos (resized, recompressed) will have similar hashes. Hamming distance
//   measures how visually different two images are.
//
// The parallel pipeline that coordinates these functions lives in
// hasher_pipeline.go. The shared worker-pool helper is in parallel.go.
//
// Key additions vs. the original file:
//   - ImageHash now carries Width, Height, Size (enables aspect-ratio bucketing
//     without re-opening files later).
//   - bufPool: sync.Pool of 64 KB buffers used for streaming partial reads and
//     the streaming xxHash path (#5 — reduces peak memory from ~40 MB to ~0.5 MB).
//   - computeXXHashStreaming: computes xxHash via streaming 64 KB chunks rather
//     than loading the entire file into RAM (#5).
//   - computeDHashFromHeader: reads only the first 128 KB of a file so that EXIF
//     thumbnails can be extracted cheaply, avoiding full-image decodes (#1/#3b).
//   - formatsNeedingFullDecode: the set of extensions whose images Go can decode
//     but which have no EXIF thumbnails (PNG, BMP, GIF, WebP, TIFF).
// =============================================================================

package main

import (
	"bytes"         // bytes.NewReader — wraps []byte as io.Reader for image/exif.
	"fmt"           // Formatted I/O (error messages, progress).
	"image"         // Standard image interface + DecodeConfig for dimensions.
	"io"            // io.ReadFull used in computeDHashFromHeader.
	"math/bits"     // bits.OnesCount64 for fast popcount (Hamming distance).
	"os"            // File operations (Open, ReadFile, Stat).
	"path/filepath" // filepath.Ext — extract extension in computeDHashFromHeader.
	"strings"       // strings.ToLower — normalise file extensions.
	"sync"          // sync.Pool for reusable 64 KB read buffers.

	// Image format decoders — blank imports register them with image.Decode.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	// goexif reads EXIF metadata from JPEG/TIFF files. We use it to extract
	// the embedded thumbnail for fast perceptual hashing.
	"github.com/rwcarlsen/goexif/exif"

	// xxhash is an extremely fast non-cryptographic hash (several GB/s).
	"github.com/cespare/xxhash/v2"
)

// =============================================================================
// Sentinel errors
// =============================================================================

// ErrNoThumbnail is returned when a file has no embedded EXIF thumbnail.
// The caller should fall back to a full-decode dHash or skip perceptual matching.
var ErrNoThumbnail = fmt.Errorf("no EXIF thumbnail available")

// =============================================================================
// Types
// =============================================================================

// ImageHash holds the results of hashing a single image file.
//
// CHANGE: Width, Height, and Size are now populated during the hash phase so
// that GroupDuplicates can bucket images by aspect ratio without re-opening
// files (optimisation #3 and #4).
//
// XXHash = 0 means the file was a singleton (unique file size) and therefore
// cannot be an exact duplicate. The grouper skips XXHash=0 in Pass 1.
type ImageHash struct {
	Path   string // Absolute filesystem path to the image.
	XXHash uint64 // xxHash64 of raw file bytes. 0 = singleton (no exact dup check).
	DHash  uint64 // Perceptual dHash. 0 = couldn't decode / skip perceptual matching.
	Width  int    // Image width in pixels (0 if unknown).
	Height int    // Image height in pixels (0 if unknown).
	Size   int64  // File size in bytes from os.Stat.
	Error  error  // Non-nil if this file failed to hash.
}

// ProgressCallback is called periodically to report scan progress.
// phase is a human-readable description; current and total are item counts.
type ProgressCallback func(phase string, current int, total int)

// =============================================================================
// bufPool — Reusable 64 KB read buffers (#5 streaming xxHash)
// =============================================================================

// bufPool holds pre-allocated 64 KB byte-slice pointers for reuse across
// goroutines. Using a pool avoids allocating a new 64 KB slab on every file
// read, reducing GC pressure when hashing thousands of files in parallel.
//
// Why 64 KB? It matches common OS page sizes and is large enough for one
// partial hash read. The pool stores *[]byte (pointer to slice) so that
// the slice header itself is heap-allocated and the pool can return a pointer.
var bufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 64*1024) // 64 KB
		return &buf
	},
}

// =============================================================================
// formatsNeedingFullDecode — Extensions where Go can fully decode the image
// but no EXIF thumbnail is embedded. computeDHashFromHeader falls back to a
// full os.ReadFile + image.Decode for these.
// =============================================================================

// formatsNeedingFullDecode lists file extensions whose files Go can decode
// via image.Decode but which typically lack EXIF thumbnails.
// JPEG and camera RAW formats are NOT in this map; they either carry an
// EXIF thumbnail or get dHash = 0 (skip perceptual matching).
var formatsNeedingFullDecode = map[string]bool{
	".png":  true,
	".bmp":  true,
	".gif":  true,
	".webp": true,
	".tiff": true,
	".tif":  true,
}

// =============================================================================
// ComputeXXHash — Full-file hash for exact duplicate detection
// =============================================================================

// ComputeXXHash reads the entire file and computes its xxHash64 digest.
// Two files with the same xxHash are byte-for-byte identical.
// Prefer computeXXHashStreaming when you want to avoid holding the full file
// in memory (useful for large RAW files).
func ComputeXXHash(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("failed to read file %s: %w", path, err)
	}
	return xxhash.Sum64(data), nil
}

// =============================================================================
// ComputePartialXXHash — Fast pre-filter using the first N bytes
// =============================================================================

// ComputePartialXXHash reads only the first 'size' bytes of a file and
// computes their xxHash. Files with different partial hashes cannot be exact
// duplicates, so we use this to eliminate non-candidates before doing
// expensive full-file reads.
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

// =============================================================================
// computeXXHashStreaming — Low-memory streaming xxHash (#5)
// =============================================================================

// computeXXHashStreaming computes xxHash64 by reading the file in 64 KB chunks
// rather than loading the entire file into memory. This reduces peak memory
// from ~5 MB/worker to ~64 KB/worker during the exact-hash phase.
//
// Buffers are borrowed from bufPool and returned after each file, so the same
// 64 KB slabs are reused across thousands of files.
func computeXXHashStreaming(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("failed to open %s: %w", path, err)
	}
	defer f.Close()

	// Borrow a 64 KB buffer from the pool; return it when done.
	bufPtr := bufPool.Get().(*[]byte)
	defer bufPool.Put(bufPtr)
	buf := *bufPtr

	// xxhash.New() returns a streaming hasher that accepts multiple writes.
	h := xxhash.New()

	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n]) // Feed the chunk to the hasher.
		}
		if err == io.EOF {
			break // Normal end of file.
		}
		if err != nil {
			return 0, fmt.Errorf("read error in %s: %w", path, err)
		}
	}

	return h.Sum64(), nil
}

// =============================================================================
// ComputeDHashFromEXIFThumbnail — Fast perceptual hash via embedded thumbnail
// =============================================================================

// ComputeDHashFromEXIFThumbnail extracts the EXIF thumbnail from a JPEG file
// and computes a dHash on it. This is ~50-100× faster than decoding the full
// image because the thumbnail is already a small JPEG (typically 10-20 KB).
//
// Returns ErrNoThumbnail if the file has no EXIF thumbnail.
func ComputeDHashFromEXIFThumbnail(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, ErrNoThumbnail
	}
	defer f.Close()

	x, err := exif.Decode(f)
	if err != nil {
		return 0, ErrNoThumbnail
	}

	thumb, err := x.JpegThumbnail()
	if err != nil || len(thumb) == 0 {
		return 0, ErrNoThumbnail
	}

	img, _, err := image.Decode(bytes.NewReader(thumb))
	if err != nil {
		return 0, ErrNoThumbnail
	}

	return computeDHashFromImage(img), nil
}

// =============================================================================
// computeDHashSmart — Buffer-based dHash with EXIF thumbnail fast-path
// =============================================================================

// computeDHashSmart computes a dHash from file bytes already in memory.
// It tries the EXIF thumbnail first (fast — ~0.5 ms) and falls back to
// full image decode (slow — ~30-50 ms) if no thumbnail is available.
func computeDHashSmart(data []byte) (uint64, error) {
	// Fast path: EXIF thumbnail (typical for camera/phone JPEGs).
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

	// Slow path: full decode (PNG, BMP, edited JPEG without thumbnail).
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, fmt.Errorf("failed to decode image: %w", err)
	}
	return computeDHashFromImage(img), nil
}

// =============================================================================
// computePHashFromData — Buffer-based pHash (average hash)
// =============================================================================

// computePHashFromData computes an average-based perceptual hash from bytes
// already in memory. The pHash compares each pixel to the image's average
// brightness rather than to its right neighbour (as dHash does). It can be
// more robust for certain types of edits.
func computePHashFromData(data []byte) (uint64, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, fmt.Errorf("failed to decode image: %w", err)
	}

	bounds := img.Bounds()
	srcW := bounds.Max.X - bounds.Min.X
	srcH := bounds.Max.Y - bounds.Min.Y

	const size = 8
	gray := make([][]uint32, size)
	for y := 0; y < size; y++ {
		gray[y] = make([]uint32, size)
	}

	var total uint64
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			srcX := bounds.Min.X + (x * srcW / size)
			srcY := bounds.Min.Y + (y * srcH / size)
			r, g, b, _ := img.At(srcX, srcY).RGBA()
			lum := (299*r + 587*g + 114*b) / 1000
			gray[y][x] = lum
			total += uint64(lum)
		}
	}

	avg := total / (size * size)
	var hash uint64
	bit := 0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if uint64(gray[y][x]) > avg {
				hash |= 1 << uint(bit)
			}
			bit++
		}
	}
	return hash, nil
}

// =============================================================================
// ComputeAllHashes — Single-read, cache-aware hash computation (utility)
// =============================================================================

// ComputeAllHashes computes both xxHash and dHash for a file with a single
// read, checking the cache first. This is kept as a utility for callers that
// don't use the split pipeline in hasher_pipeline.go.
func ComputeAllHashes(path string, info os.FileInfo, cache *HashCache, algorithm string) (xxHash uint64, dHash uint64, cacheHit bool, err error) {
	if xxh, dh, ok := cache.Lookup(path, info); ok {
		return xxh, dh, true, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false, fmt.Errorf("failed to read file %s: %w", path, err)
	}

	xxh := xxhash.Sum64(data)

	var dh uint64
	switch algorithm {
	case "phash":
		dh, err = computePHashFromData(data)
	default:
		dh, err = computeDHashSmart(data)
	}
	if err != nil {
		dh = 0
	}

	return xxh, dh, false, nil
}

// =============================================================================
// computeDHashFromImage — Core 9×8 dHash algorithm
// =============================================================================

// computeDHashFromImage computes a dHash from an already-decoded image.
// This is the shared implementation used by all dHash code paths.
//
// Algorithm:
//  1. Resize to 9×8 via nearest-neighbour (no library dependency).
//  2. Convert to grayscale via ITU-R 601 luminance weights.
//  3. For each row, compare each pixel to its right neighbour.
//     Bit = 1 if left > right, else 0.
//  4. Pack 64 comparison results into a uint64.
func computeDHashFromImage(img image.Image) uint64 {
	bounds := img.Bounds()
	srcW := bounds.Max.X - bounds.Min.X
	srcH := bounds.Max.Y - bounds.Min.Y

	const dstW, dstH = 9, 8

	gray := make([][]uint32, dstH)
	for y := 0; y < dstH; y++ {
		gray[y] = make([]uint32, dstW)
	}

	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			srcX := bounds.Min.X + (x * srcW / dstW)
			srcY := bounds.Min.Y + (y * srcH / dstH)
			r, g, b, _ := img.At(srcX, srcY).RGBA()
			gray[y][x] = (299*r + 587*g + 114*b) / 1000
		}
	}

	var hash uint64
	bit := 0
	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW-1; x++ {
			if gray[y][x] > gray[y][x+1] {
				hash |= 1 << uint(bit)
			}
			bit++
		}
	}
	return hash
}

// =============================================================================
// computeDHashFromHeader — EXIF-thumbnail dHash using only 128 KB (#1 / #3b)
// =============================================================================

// computeDHashFromHeader computes a dHash by reading only the first 128 KB of
// a file. For most camera/phone JPEGs this is enough to find the embedded EXIF
// thumbnail (~10-20 KB) and compute a dHash without a full-image decode.
//
// Also returns image dimensions (width, height) extracted from the 128 KB
// buffer, so the caller doesn't need to re-open the file later (#3).
//
// Decision tree after reading 128 KB:
//  1. Try EXIF thumbnail → compute dHash if found.
//  2. If not found and format is JPEG or RAW: return (0, w, h, ErrNoThumbnail).
//     These files get dHash=0 and are skipped in perceptual matching.
//  3. If not found and format is PNG/BMP/GIF/WebP/TIFF: do a full os.ReadFile
//     and decode (these formats lack EXIF but Go can decode them natively).
func computeDHashFromHeader(path, algorithm string) (dHash uint64, width, height int, err error) {
	ext := strings.ToLower(filepath.Ext(path))

	// Read the first 128 KB. io.ReadFull returns io.ErrUnexpectedEOF if the
	// file is shorter — that's fine, we use whatever bytes we got.
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, ErrNoThumbnail
	}
	buf := make([]byte, 128*1024)
	n, _ := io.ReadFull(f, buf)
	f.Close()
	if n == 0 {
		return 0, 0, 0, ErrNoThumbnail
	}
	buf = buf[:n]

	// Extract image dimensions from the header bytes (DecodeConfig is header-only).
	if cfg, _, decErr := image.DecodeConfig(bytes.NewReader(buf)); decErr == nil {
		width, height = cfg.Width, cfg.Height
	}

	// Try EXIF thumbnail for dHash computation.
	if x, exifErr := exif.Decode(bytes.NewReader(buf)); exifErr == nil {
		if thumb, thumbErr := x.JpegThumbnail(); thumbErr == nil && len(thumb) > 0 {
			if img, _, imgErr := image.Decode(bytes.NewReader(thumb)); imgErr == nil {
				return computeDHashFromImage(img), width, height, nil
			}
		}
	}

	// JPEG / RAW: no thumbnail found — skip perceptual matching for this file.
	// These formats need full decode which is too expensive for the fast path.
	if !formatsNeedingFullDecode[ext] {
		return 0, width, height, ErrNoThumbnail
	}

	// PNG / BMP / GIF / WebP / TIFF: fall back to full file read + decode.
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return 0, width, height, readErr
	}
	switch algorithm {
	case "phash":
		dHash, err = computePHashFromData(data)
	default:
		dHash, err = computeDHashSmart(data)
	}
	return dHash, width, height, err
}

// =============================================================================
// ComputeDHash / ComputePHash — Standalone file-path hash functions
// =============================================================================

// ComputeDHash computes a dHash by opening and decoding the full image.
// Use computeDHashFromHeader or computeDHashSmart for faster alternatives.
func ComputeDHash(path string) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("failed to open image %s: %w", path, err)
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return 0, fmt.Errorf("failed to decode image %s: %w", path, err)
	}
	return computeDHashFromImage(img), nil
}

// ComputePHash computes a pHash by opening and decoding the full image.
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
	srcW := bounds.Max.X - bounds.Min.X
	srcH := bounds.Max.Y - bounds.Min.Y

	const size = 8
	gray := make([][]uint32, size)
	for y := 0; y < size; y++ {
		gray[y] = make([]uint32, size)
	}

	var total uint64
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			srcX := bounds.Min.X + (x * srcW / size)
			srcY := bounds.Min.Y + (y * srcH / size)
			r, g, b, _ := img.At(srcX, srcY).RGBA()
			lum := (299*r + 587*g + 114*b) / 1000
			gray[y][x] = lum
			total += uint64(lum)
		}
	}

	avg := total / (size * size)
	var hash uint64
	bit := 0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if uint64(gray[y][x]) > avg {
				hash |= 1 << uint(bit)
			}
			bit++
		}
	}
	return hash, nil
}

// =============================================================================
// HammingDistance — Count differing bits between two 64-bit hashes
// =============================================================================

// HammingDistance returns the number of bit positions where a and b differ.
// For dHash values this measures how visually different two images are:
//
//	0     — identical visual structure
//	1-5   — very similar (resized / recompressed copy)
//	6-10  — somewhat similar (same scene, different settings)
//	>10   — probably different images
//
// Implementation: XOR the values (1 bit where they differ), then count the 1s
// (popcount). bits.OnesCount64 maps to a single hardware POPCNT instruction.
func HammingDistance(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}
