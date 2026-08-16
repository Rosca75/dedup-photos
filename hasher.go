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
// Key types:
//   - ImageHash now carries Width, Height, Size (enables aspect-ratio bucketing
//     without re-opening files later).
//   - bufPool: sync.Pool of 64 KB buffers used for streaming partial reads.
//   - computeDHashFromHeader: reads only the first 128 KB of a file so that
//     embedded JPEG thumbnails can be extracted cheaply, avoiding full-image
//     decodes.
//   - formatsNeedingFullDecode: the set of extensions whose images Go can decode
//     but which have no EXIF thumbnails (PNG, BMP, GIF, WebP, TIFF).
// =============================================================================

package main

import (
	"bytes"         // bytes.NewReader — wraps []byte as io.Reader for image decoding.
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
// Width, Height, and Size are populated during the hash phase so that
// GroupDuplicates can bucket images by aspect ratio without re-opening files.
//
// XXHash = 0 means the file was a singleton (unique file size) and therefore
// cannot be an exact duplicate. The grouper skips XXHash=0 in Pass 1.
type ImageHash struct {
	Path   string // Absolute filesystem path to the image.
	XXHash uint64 // xxHash64 of raw file bytes. 0 = singleton (no exact dup check).
	DHash  uint64 // Difference hash. 0 = couldn't decode / skip perceptual matching.
	PHash  uint64 // DCT perceptual hash, computed from the same decode as DHash.
	Width  int    // Image width in pixels (0 if unknown).
	Height int    // Image height in pixels (0 if unknown).
	Size   int64  // File size in bytes from os.Stat.
	Error  error  // Non-nil if this file failed to hash.
}

// ProgressCallback is called periodically to report scan progress.
// phase is a human-readable description; current and total are item counts.
type ProgressCallback func(phase string, current int, total int)

// =============================================================================
// bufPool — Reusable 64 KB read buffers
// =============================================================================

// bufPool holds pre-allocated 64 KB byte-slice pointers for reuse across
// goroutines. Using a pool avoids allocating a new 64 KB slab on every file
// read, reducing GC pressure when hashing thousands of files in parallel.
//
// The pool stores *[]byte (pointer to slice) so that the slice header itself
// is heap-allocated and the pool can return a pointer.
var bufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 64*1024) // 64 KB
		return &buf
	},
}

// headerBufPool holds reusable 128 KB buffers for computeDHashFromHeader.
// This eliminates ~6000 allocations of 128 KB each when hashing thousands of
// files, significantly reducing GC pressure.
var headerBufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 128*1024) // 128 KB
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
// computeDHashSmart — Buffer-based dHash with embedded-thumbnail fast-path
// =============================================================================

// computeDHashSmart computes both fingerprints from file bytes already in
// memory. It tries an embedded JPEG thumbnail first (fast — ~0.5 ms) and falls
// back to full image decode (slow — ~30-50 ms) if no thumbnail is found.
//
// Both hashes are always computed, never one or the other. The algorithm
// setting is applied at MATCH time, in the grouper, not at hash time. That
// removes a whole class of bug: the cache stores one entry per file regardless
// of the setting, so switching between dHash, pHash and Both no longer reuses
// hashes computed under a different algorithm, and no longer needs a re-scan.
// The second fingerprint costs ~240 µs per image against I/O measured in
// milliseconds per file.
func computeDHashSmart(data []byte) (dHash, pHash uint64, err error) {
	// Fast path: scan for embedded JPEG thumbnail (typical for camera/phone JPEGs).
	if thumb := extractJPEGThumbnailFromBuffer(data); thumb != nil {
		if img, _, decErr := image.Decode(bytes.NewReader(thumb)); decErr == nil {
			return computeDHashFromImage(img), computePHashFromImage(img), nil
		}
	}

	// Slow path: full decode (PNG, BMP, edited JPEG without thumbnail).
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, 0, fmt.Errorf("failed to decode image: %w", err)
	}
	return computeDHashFromImage(img), computePHashFromImage(img), nil
}

// =============================================================================
// =============================================================================
// grayGrid — Area-average downsampling to a small luminance grid
// =============================================================================

// grayGrid reduces img to a cols×rows grid where each cell holds the AVERAGE
// luminance of every source pixel that falls inside it.
//
// This replaces the nearest-neighbour point sampling this file used to do, and
// the difference is the whole reason perceptual matching was unreliable. Point
// sampling read one physical pixel per cell — 72 pixels out of a 416×312
// thumbnail, 0.055% of it — so sensor noise and JPEG ringing landed directly in
// the hash. Measured over 386 photos from a real library put through seven
// realistic edits (re-save at q90/q75/q60, downscale to 50%/25%, and
// combinations), averaging instead of sampling raised the share of duplicates
// found at a 6-bit threshold from 64.3% to 97.8%.
//
// Two implementation details keep this cheap enough to run on every file:
//
//   - Every image the scan hashes decodes to *image.YCbCr (HEIC thumbnails via
//     the byte-range ladder, and JPEG EXIF thumbnails alike). Its Y plane is
//     already BT.601 luminance — exactly the quantity the old code recomputed
//     from RGB — so that path reads bytes directly and skips both the
//     per-pixel interface call and the colour conversion.
//   - The destination column depends only on x, so it is computed once per
//     image instead of once per pixel. Leaving that integer division in the
//     inner loop cost 1.39 ms per image; hoisting it brings the whole function
//     to ~244 µs, against ~3.4 µs for the old point-sampling version. Across a
//     library of 8,000 photos that is roughly two seconds of extra CPU, spread
//     over every core.
//
// A resampling kernel (draw.CatmullRom) scores marginally better still, but
// costs 4.6 ms per image — its kernel support grows with the downscale ratio —
// which is not worth ~1 percentage point of recall.
func grayGrid(img image.Image, cols, rows int) []uint32 {
	sum := make([]uint64, cols*rows)
	cnt := make([]uint32, cols*rows)

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return make([]uint32, cols*rows)
	}

	// Destination column for each source x, computed once.
	colOf := make([]int, w)
	for x := 0; x < w; x++ {
		colOf[x] = x * cols / w
	}

	if yc, ok := img.(*image.YCbCr); ok {
		// Fast path: the luma plane is the luminance we want.
		for y := 0; y < h; y++ {
			base := (y * rows / h) * cols
			off := yc.YOffset(bounds.Min.X, bounds.Min.Y+y)
			row := yc.Y[off : off+w]
			for x := 0; x < w; x++ {
				c := base + colOf[x]
				sum[c] += uint64(row[x])
				cnt[c]++
			}
		}
	} else {
		// Generic path for any other image type.
		for y := 0; y < h; y++ {
			base := (y * rows / h) * cols
			for x := 0; x < w; x++ {
				r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
				c := base + colOf[x]
				// >>8 puts the 16-bit RGBA() result back into 0-255, matching
				// the scale of the YCbCr fast path above.
				sum[c] += uint64((299*r + 587*g + 114*b) / 1000 >> 8)
				cnt[c]++
			}
		}
	}

	out := make([]uint32, cols*rows)
	for i := range out {
		if cnt[i] > 0 {
			out[i] = uint32(sum[i] / uint64(cnt[i]))
		}
	}
	return out
}

// =============================================================================
// computeDHashFromImage — Core 9×8 dHash algorithm
// =============================================================================

// computeDHashFromImage computes a dHash from an already-decoded image.
// This is the shared implementation used by all dHash code paths.
//
// Algorithm:
//  1. Reduce to a 9×8 grid of average luminance (see grayGrid).
//  2. For each row, compare each cell to its right neighbour.
//     Bit = 1 if left > right, else 0.
//  3. Pack 64 comparison results into a uint64.
//
// The bit ordering is unchanged from the point-sampling version, but the values
// being compared are not, so hashes computed by older builds are incompatible —
// cacheVersion was raised to discard them.
func computeDHashFromImage(img image.Image) uint64 {
	const dstW, dstH = 9, 8
	gray := grayGrid(img, dstW, dstH)

	var hash uint64
	bit := 0
	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW-1; x++ {
			if gray[y*dstW+x] > gray[y*dstW+x+1] {
				hash |= 1 << uint(bit)
			}
			bit++
		}
	}
	return hash
}

// =============================================================================
// computeDHashFromHeader — Embedded-thumbnail dHash using only 128 KB
// =============================================================================

// computeDHashFromHeader computes a dHash by reading only the first 128 KB of
// a file. For most camera/phone JPEGs this is enough to find the embedded JPEG
// thumbnail and compute a dHash without a full-image decode.
//
// Also returns image dimensions (width, height) extracted from the 128 KB
// buffer, so the caller doesn't need to re-open the file later.
//
// Decision tree after reading 128 KB:
//  1. Try to find an embedded JPEG thumbnail → compute both fingerprints.
//  2. If not found and format is JPEG or RAW: return (0, 0, w, h, ErrNoThumbnail).
//     These files get dHash=0 and are skipped in perceptual matching.
//  3. If not found and format is PNG/BMP/GIF/WebP/TIFF: do a full os.ReadFile
//     and decode (these formats lack embedded thumbnails but Go can decode them).
//
// Both fingerprints are always produced; the algorithm setting is applied when
// matching, not when hashing. See computeDHashSmart for why.
func computeDHashFromHeader(path string) (dHash, pHash uint64, width, height int, err error) {
	ext := strings.ToLower(filepath.Ext(path))

	// HEIC/HEIF: the container requires full file access; use dedicated decoder.
	if ext == ".heic" || ext == ".heif" {
		return computeDHashHEIC(path)
	}

	// Read the first 128 KB. io.ReadFull returns io.ErrUnexpectedEOF if the
	// file is shorter — that's fine, we use whatever bytes we got.
	// Borrow a 128 KB buffer from the pool to avoid per-call allocation.
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, 0, ErrNoThumbnail
	}
	bufPtr := headerBufPool.Get().(*[]byte)
	buf := *bufPtr
	n, _ := io.ReadFull(f, buf)
	f.Close()
	if n == 0 {
		headerBufPool.Put(bufPtr)
		return 0, 0, 0, 0, ErrNoThumbnail
	}
	// Return the buffer to the pool when we're done with it.
	// defer is safe here — all remaining code paths only read from buf.
	defer headerBufPool.Put(bufPtr)

	return computeDHashFromHeaderBuffer(path, buf[:n])
}

// computeDHashFromHeaderBuffer runs the same decision tree as
// computeDHashFromHeader but skips the file-open/read step because the header
// bytes are already in buf. Used by the pipeline to reuse 64 KB buffers read
// during the partial-hash phase instead of re-opening the file.
func computeDHashFromHeaderBuffer(path string, buf []byte) (dHash, pHash uint64, width, height int, err error) {
	ext := strings.ToLower(filepath.Ext(path))

	// HEIC/HEIF: defer to the dedicated HEIC fast path — it does its own read
	// at heicHeaderReadSize, larger than our 64 KB partial-hash buffer anyway.
	if ext == ".heic" || ext == ".heif" {
		return computeDHashHEIC(path)
	}

	if len(buf) == 0 {
		return 0, 0, 0, 0, ErrNoThumbnail
	}

	// Extract image dimensions from the header bytes (DecodeConfig is header-only).
	if cfg, _, decErr := image.DecodeConfig(bytes.NewReader(buf)); decErr == nil {
		width, height = cfg.Width, cfg.Height
	}

	// Try to find an embedded JPEG thumbnail for cheap fingerprinting.
	// Camera and phone JPEGs store a small thumbnail in the EXIF APP1 segment.
	if thumb := extractJPEGThumbnailFromBuffer(buf); thumb != nil {
		if img, _, imgErr := image.Decode(bytes.NewReader(thumb)); imgErr == nil {
			return computeDHashFromImage(img), computePHashFromImage(img), width, height, nil
		}
	}

	// JPEG / RAW: no thumbnail found — skip perceptual matching for this file.
	// These formats need full decode which is too expensive for the fast path.
	if !formatsNeedingFullDecode[ext] {
		return 0, 0, width, height, ErrNoThumbnail
	}

	// PNG / BMP / GIF / WebP / TIFF: fall back to full file read + decode.
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return 0, 0, width, height, readErr
	}
	dHash, pHash, err = computeDHashSmart(data)
	return dHash, pHash, width, height, err
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
