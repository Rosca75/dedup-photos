package main

// HEIC fast path (Plan 2 / Step 1):
// Instead of reading the whole 3 MB HEIC file to get its embedded thumbnail,
// we read only the first 192 KB. libheif's WASM decoder walks the ISOBMFF
// header, finds the thumbnail item's absolute file offset in the `iloc` box,
// and reads its bytes directly from the buffer. For iPhone HDR HEICs the
// header + thumbnail tile all live within the first ~128 KB; 192 KB gives
// headroom for variations. If decode fails (rare corpus), we fall back to
// reading the full file.
//
// This is Path B from 02-PERFORMANCE-HOT-PATHS.md: no upstream heic library
// changes, no manual ISOBMFF parsing — we just hand the WASM decoder a
// truncated buffer and trust iloc-based tile lookup.

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic" // atomic.Int64 — lock-free counters for the HEIC decode ladder.
	"time"        // time.Now/time.Since — measure the full-read fallback branch.

	"github.com/Rosca75/heic"
	"github.com/bep/imagemeta"
	"golang.org/x/image/draw"
)

// =============================================================================
// HEIC decode-ladder instrumentation (perf tracing — Trace 1)
// =============================================================================
//
// decodeHEICFromHeader tries four "rungs" in order until one decodes an image.
// To find out WHERE the scan spends its HEIC time we count how often each rung
// succeeds, plus the total time and bytes spent in the expensive full-file
// os.ReadFile fallback. Everything below is aggregate-only (a handful of atomic
// counters), so it adds no per-file logging and negligible overhead.
//
// These are package-level so every worker goroutine bumps the same counters.
// atomic.Int64 makes concurrent Add/Load safe without a mutex.
var (
	heicLadderThumbHeader   atomic.Int64 // Rung 1: embedded thumbnail from the 192 KB header.
	heicLadderPrimaryHeader atomic.Int64 // Rung 2: primary image from the header window.
	heicLadderFullThumb     atomic.Int64 // Rung 3: thumbnail after a full os.ReadFile.
	heicLadderFullPrimary   atomic.Int64 // Rung 4: primary after a full os.ReadFile.
	heicLadderFail          atomic.Int64 // No rung succeeded — decode failed entirely.
	heicLadderFullReadNs    atomic.Int64 // Total nanoseconds spent in the full-read fallback branch.
	heicLadderFullReadBytes atomic.Int64 // Total bytes read by the full-read fallback branch.
)

// printAndResetHEICLadder prints one aggregate [perf] line for the HEIC decode
// ladder, then resets every counter to zero so a subsequent scan in the same
// app session starts fresh. Called once at the end of Phase 3b.
func printAndResetHEICLadder() {
	fmt.Printf("[perf] HEIC ladder: thumbHdr=%d primaryHdr=%d fullThumb=%d fullPrimary=%d fail=%d | fullReadFallback=%.2fs (%.1fMB)\n",
		heicLadderThumbHeader.Load(),
		heicLadderPrimaryHeader.Load(),
		heicLadderFullThumb.Load(),
		heicLadderFullPrimary.Load(),
		heicLadderFail.Load(),
		float64(heicLadderFullReadNs.Load())/1e9,
		float64(heicLadderFullReadBytes.Load())/(1024*1024),
	)
	// Reset all counters (Store(0)) for the next scan.
	heicLadderThumbHeader.Store(0)
	heicLadderPrimaryHeader.Store(0)
	heicLadderFullThumb.Store(0)
	heicLadderFullPrimary.Store(0)
	heicLadderFail.Store(0)
	heicLadderFullReadNs.Store(0)
	heicLadderFullReadBytes.Store(0)
}

// heicHeaderReadSize is the byte-range size used by the HEIC fast path.
// 192 KB comfortably covers the ftyp + meta + iloc + thumbnail tile on
// every iPhone HEIC sample tested. If a file needs more, we fall back to
// a full read.
const heicHeaderReadSize = 192 * 1024

// initHEIC configures the heic package at startup, choosing between the two
// backends: the system libheif loaded dynamically via purego, or the Rust HEVC
// decoder compiled to WASM.
//
// The deciding factor is whether the backend can decode the deliberately
// TRUNCATED 192 KB buffer that the byte-range fast path hands it
// (decodeHEICFromHeader, rung 1). A backend that rejects truncated containers
// collapses every HEIC onto a full os.ReadFile, the ~100× slower path.
// Measured on the samples/ corpus, 96 concurrent thumbnail decodes from a
// 192 KB header buffer:
//
//	dynamic libheif 1.17.6 : 0/8 decode — rejects the truncated container
//	dynamic libheif 1.19.8 : 8/8, 251 ms
//	WASM                   : 8/8, 964 ms
//
// So the dynamic backend is strongly preferred once it is new enough — it is
// ~3.8× faster than WASM here — and unusable below 1.18, where it both rejects
// the truncated buffer and mis-decodes iPhone HDR / tmap-brand files. Hence the
// version gate. Do not "simplify" this to always forcing WASM: that was tried,
// and it costs roughly 4× on the hot path for anyone on a current libheif.
//
// Windows ships no dynamic libheif, so that build always lands on WASM.
func initHEIC() {
	if heic.Dynamic() != nil {
		// Dynamic libheif not available (the normal case on Windows); the heic
		// package falls back to WASM automatically.
		return
	}
	if !heicDynamicVersionAtLeast(1, 18) {
		heic.ForceWasmMode = true
		log.Println("[heic] Dynamic libheif < 1.18; using WASM decoder (HDR/tmap correctness + 192 KB fast path)")
		return
	}
	log.Println("[heic] Using dynamic libheif >= 1.18 (faster than WASM, handles the 192 KB fast path)")
}

// isHEIC reports whether path has a .heic or .heif extension.
func isHEIC(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".heic" || ext == ".heif"
}

// readHEICHeader reads up to heicHeaderReadSize bytes from path.
// Returns the read bytes (may be less than heicHeaderReadSize if the file
// is shorter) and any I/O error.
func readHEICHeader(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, heicHeaderReadSize)
	n, err := io.ReadFull(f, buf)
	// io.ReadFull returns ErrUnexpectedEOF for short files — that's fine,
	// we just use however many bytes we got.
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:n], nil
}

// Decode through the package-level heic.Decode / heic.DecodeThumbnail: as of
// heic v0.4.0 the package pools WASM modules internally, so there is nothing to
// gain from holding a decoder per worker (measured within noise over 96
// concurrent decodes) and heic.Decoder is deprecated upstream.

// decodeHEICFromHeader runs the thumb → primary → full-file-read fallback ladder
// against an already-read header buffer. This is the shared decode core for both
// the UI thumbnail path and the perceptual-hash path, so they always agree on
// which image is produced.
func decodeHEICFromHeader(path string, header []byte) (image.Image, error) {
	// Rung 1 — Fast path: embedded thumbnail from the header window.
	if img, err := heic.DecodeThumbnail(bytes.NewReader(header)); err == nil {
		heicLadderThumbHeader.Add(1)
		return img, nil
	}
	// Rung 2 — Fallback 1: primary image from the header (no thumbnail iref).
	if img, err := heic.Decode(bytes.NewReader(header)); err == nil {
		heicLadderPrimaryHeader.Add(1)
		return img, nil
	}
	// Rungs 3 & 4 — Fallback 2: full file read (thumbnail tile past the header
	// window). This branch is the expensive one, so we time it and record how
	// many bytes it reads to quantify the cost of files that miss the header.
	fullReadStart := time.Now()
	data, err := os.ReadFile(path)
	if err != nil {
		// Still record the time spent attempting the read before giving up.
		heicLadderFullReadNs.Add(int64(time.Since(fullReadStart)))
		heicLadderFail.Add(1)
		return nil, err
	}
	heicLadderFullReadBytes.Add(int64(len(data)))
	// Rung 3: thumbnail from the full file.
	if img, err := heic.DecodeThumbnail(bytes.NewReader(data)); err == nil {
		heicLadderFullReadNs.Add(int64(time.Since(fullReadStart)))
		heicLadderFullThumb.Add(1)
		return img, nil
	}
	// Rung 4: primary from the full file (last resort).
	img, err := heic.Decode(bytes.NewReader(data))
	heicLadderFullReadNs.Add(int64(time.Since(fullReadStart)))
	if err != nil {
		heicLadderFail.Add(1)
	} else {
		heicLadderFullPrimary.Add(1)
	}
	return img, err
}

// decodeHEICThumbnail decodes the embedded thumbnail from a HEIC file using
// the fast path: read first ~192 KB, hand to the WASM decoder. Falls back to
// a full file read on failure.
func decodeHEICThumbnail(path string) (image.Image, error) {
	header, err := readHEICHeader(path)
	if err != nil {
		return nil, err
	}
	return decodeHEICFromHeader(path, header)
}

// heicThumbnailJPEG returns a JPEG-encoded, max-400px thumbnail for a HEIC
// file. Uses the byte-range fast path to avoid reading the full 3 MB file.
// This is the interactive UI path, not the batch scan path.
func heicThumbnailJPEG(path string) ([]byte, error) {
	img, err := decodeHEICThumbnail(path)
	if err != nil {
		return nil, err
	}
	result := resizeImageToJPEG(img, 400, 85)
	if result == nil {
		return nil, fmt.Errorf("jpeg encode failed")
	}
	return result, nil
}

// resizeImageToJPEG resizes img to fit within maxDim×maxDim (preserving aspect
// ratio) and JPEG-encodes it at the given quality level. Returns nil on error.
//
// Uses golang.org/x/image/draw.ApproxBiLinear — ~10× faster than the previous
// manual img.At/Set loop and produces visibly better output. The previous loop
// called img.At (allocating a color.Color interface) for every output pixel,
// which dominated CPU during thumbnail generation.
func resizeImageToJPEG(img image.Image, maxDim, quality int) []byte {
	b := img.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	newW, newH := srcW, srcH
	if srcW > maxDim || srcH > maxDim {
		if srcW >= srcH {
			newW = maxDim
			newH = srcH * maxDim / srcW
		} else {
			newH = maxDim
			newW = srcW * maxDim / srcH
		}
	}
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	dstRect := image.Rect(0, 0, newW, newH)
	thumb := image.NewRGBA(dstRect)
	draw.ApproxBiLinear.Scale(thumb, dstRect, img, b, draw.Src, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: quality}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// computeDHashHEIC computes a dHash and image dimensions for a HEIC file.
// Uses the byte-range fast path: one 192 KB read covers the ISOBMFF header
// (for dimensions via imagemeta) and the thumbnail tile (decoded via WASM).
func computeDHashHEIC(path, algorithm string) (dHash uint64, width, height int, err error) {
	header, hdrErr := readHEICHeader(path)
	if hdrErr != nil {
		return 0, 0, 0, ErrNoThumbnail
	}

	// Extract dimensions from the same buffer — no extra I/O.
	if res, metaErr := imagemeta.Decode(imagemeta.Options{
		R:           bytes.NewReader(header),
		ImageFormat: imagemeta.HEIF,
		Sources:     imagemeta.CONFIG,
	}); metaErr == nil && res.ImageConfig.Width > 0 {
		width, height = res.ImageConfig.Width, res.ImageConfig.Height
	}

	// Decode via the shared thumb → primary → full-read ladder.
	img, decErr := decodeHEICFromHeader(path, header)
	if decErr != nil {
		return 0, width, height, ErrNoThumbnail
	}

	// Note: we intentionally do NOT persist a JPEG thumbnail here. Generating a
	// thumbnail for every HEIC during the scan (resize + JPEG encode + disk
	// write for thousands of files, the vast majority of which are never
	// viewed) was a major hot-path cost. Thumbnails are now produced lazily on
	// first view in GetThumbnail, which caches them to disk on demand.
	return computeDHashFromImage(img), width, height, nil
}

// extractHEICExif populates meta with EXIF data from a HEIC file.
// Delegates to extractExifInto with the HEIF format hint; the ISOBMFF container
// is handled transparently by bep/imagemeta.
func extractHEICExif(path string, meta *ImageMetadata) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	extractExifInto(f, imagemeta.HEIF, meta)
}
