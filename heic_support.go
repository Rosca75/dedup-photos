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

// initHEIC configures the heic package at startup.
//
// When a dynamic libheif is present we deliberately do NOT use it, for two
// independent reasons:
//
//  1. Speed. The 192 KB byte-range fast path (decodeHEICFromHeader, rung 1)
//     hands the decoder a deliberately truncated container. libheif rejects
//     truncated input outright — measured 0/8 on the samples/ corpus — which
//     collapses every HEIC onto a full os.ReadFile, the ~100× slower path.
//     The WASM backend walks the ISOBMFF iloc box and decodes 8/8 from that
//     same buffer.
//  2. Correctness. libheif < 1.18 mis-decodes HDR / tmap-brand files, which
//     are common on iPhone.
//
// heic.ForceWasmMode is a package-level global and the hash pipeline decodes
// concurrently, so the backend cannot be chosen per call — it is all or
// nothing, and the fast path is what this app is built around.
func initHEIC() {
	if heic.Dynamic() != nil {
		// Dynamic libheif not available; WASM will be used automatically.
		return
	}
	heic.ForceWasmMode = true
	if heicDynamicVersionAtLeast(1, 18) {
		log.Println("[heic] Dynamic libheif >= 1.18 found; using WASM decoder anyway to keep the 192 KB fast path")
	} else {
		log.Println("[heic] Dynamic libheif < 1.18 found; using WASM decoder (correctness + 192 KB fast path)")
	}
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

// heicDecoderWorker holds a per-goroutine reusable HEIC decoder for the
// perceptual-hash phase. The underlying WASM module instance is expensive to
// create, so we build it lazily on the first HEIC file a worker encounters —
// corpora with no HEIC files never instantiate one. It is NOT concurrency-safe:
// exactly one heicDecoderWorker per worker goroutine.
type heicDecoderWorker struct {
	dec    *heic.Decoder // Reusable decoder; nil until first use (or on failure).
	inited bool          // Whether we've already attempted to create dec.
}

// decoder returns this worker's reusable decoder, creating it on first use.
// Returns nil if creation failed, in which case callers fall back to the
// package-level per-call decode functions (correct, just slower).
func (w *heicDecoderWorker) decoder() *heic.Decoder {
	if w == nil {
		return nil
	}
	if !w.inited {
		w.inited = true
		d, err := heic.NewDecoder()
		if err != nil {
			// Log once per worker; the scan continues via package-level decode.
			log.Printf("[heic] NewDecoder failed, using per-call decode: %v", err)
			return nil
		}
		w.dec = d
	}
	return w.dec
}

// close releases the worker's decoder. Safe to call when no decoder was built.
func (w *heicDecoderWorker) close() {
	if w != nil && w.dec != nil {
		_ = w.dec.Close()
		w.dec = nil
	}
}

// heicDecodeThumb decodes an embedded thumbnail from r. When dec is non-nil the
// reusable decoder is used (fast batch path); otherwise the package-level
// per-call function is used. Both produce byte-identical output.
func heicDecodeThumb(dec *heic.Decoder, r io.Reader) (image.Image, error) {
	if dec != nil {
		return dec.DecodeThumbnail(r)
	}
	return heic.DecodeThumbnail(r)
}

// heicDecodePrimary decodes the primary image from r, using dec when non-nil.
func heicDecodePrimary(dec *heic.Decoder, r io.Reader) (image.Image, error) {
	if dec != nil {
		return dec.Decode(r)
	}
	return heic.Decode(r)
}

// decodeHEICFromHeader runs the thumb → primary → full-file-read fallback ladder
// against an already-read header buffer, using dec (when non-nil) for every
// decode. This is the shared decode core for both the UI thumbnail path and the
// perceptual-hash path, so they always agree on which image is produced.
func decodeHEICFromHeader(dec *heic.Decoder, path string, header []byte) (image.Image, error) {
	// Rung 1 — Fast path: embedded thumbnail from the header window.
	if img, err := heicDecodeThumb(dec, bytes.NewReader(header)); err == nil {
		heicLadderThumbHeader.Add(1)
		return img, nil
	}
	// Rung 2 — Fallback 1: primary image from the header (no thumbnail iref).
	if img, err := heicDecodePrimary(dec, bytes.NewReader(header)); err == nil {
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
	if img, err := heicDecodeThumb(dec, bytes.NewReader(data)); err == nil {
		heicLadderFullReadNs.Add(int64(time.Since(fullReadStart)))
		heicLadderFullThumb.Add(1)
		return img, nil
	}
	// Rung 4: primary from the full file (last resort).
	img, err := heicDecodePrimary(dec, bytes.NewReader(data))
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
// a full file read on failure. dec may be nil (UI path) to use per-call decode.
func decodeHEICThumbnail(dec *heic.Decoder, path string) (image.Image, error) {
	header, err := readHEICHeader(path)
	if err != nil {
		return nil, err
	}
	return decodeHEICFromHeader(dec, path, header)
}

// heicThumbnailJPEG returns a JPEG-encoded, max-400px thumbnail for a HEIC
// file. Uses the byte-range fast path to avoid reading the full 3 MB file.
// Uses per-call decoding (dec=nil) — this is the interactive UI path, not the
// batch scan path, so a reusable decoder isn't warranted here.
func heicThumbnailJPEG(path string) ([]byte, error) {
	img, err := decodeHEICThumbnail(nil, path)
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
//
// dec is the calling worker's reusable decoder (may be nil to fall back to
// per-call decoding). Reusing one WASM module instance per worker avoids the
// dominant per-call instantiation cost across a batch of HEIC files.
func computeDHashHEIC(dec *heic.Decoder, path, algorithm string) (dHash uint64, width, height int, err error) {
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

	// Decode via the shared thumb → primary → full-read ladder, reusing the
	// worker's decoder. Byte-identical to the previous per-call ladder.
	img, decErr := decodeHEICFromHeader(dec, path, header)
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
