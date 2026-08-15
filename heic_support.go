package main

// HEIC fast path (Plan 2 / Step 1, window sized by Plan 4 / Step 3):
// Instead of reading the whole 3 MB HEIC file to get its embedded thumbnail,
// we read only the first 128 KB. libheif's WASM decoder walks the ISOBMFF
// header, finds the thumbnail item's absolute file offset in the `iloc` box,
// and reads its bytes directly from the buffer. For iPhone HDR HEICs the
// header + thumbnail tile all live within that window. If decode fails (rare
// corpus), we retry once with a wider byte range.
//
// This is Path B from 02-PERFORMANCE-HOT-PATHS.md: no upstream heic library
// changes, no manual ISOBMFF parsing — we just hand the WASM decoder a
// truncated buffer and trust iloc-based tile lookup.

import (
	"bytes"
	"errors"
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
// decodeHEICFromHeader tries three "rungs" in order until one decodes an image.
// To find out WHERE the scan spends its HEIC time we count how often each rung
// succeeds, plus the total time and bytes spent in the expensive full-file
// os.ReadFile fallback. Everything below is aggregate-only (a handful of atomic
// counters), so it adds no per-file logging and negligible overhead.
//
// These are package-level so every worker goroutine bumps the same counters.
// atomic.Int64 makes concurrent Add/Load safe without a mutex.
var (
	heicLadderThumbHeader   atomic.Int64 // Rung 1: embedded thumbnail from the header window.
	heicLadderPrimaryHeader atomic.Int64 // Rung 2: primary image from the header window.
	heicLadderFullThumb     atomic.Int64 // Rung 3: thumbnail after the widened 1 MB read.
	heicLadderFullPrimary   atomic.Int64 // Retired rung 4 (full primary decode); must stay 0 — see decodeHEICFromHeader.
	heicLadderFail          atomic.Int64 // No rung succeeded — the file has no embedded thumbnail.
	heicLadderFullReadNs    atomic.Int64 // Total nanoseconds spent in the widened-retry branch.
	heicLadderFullReadBytes atomic.Int64 // Total bytes read by the widened-retry branch.
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
// It covers the ftyp + meta + iloc + thumbnail tile on every iPhone HEIC tested.
// If a file needs more, rung 3 retries once at heicWideRetryReadSize.
//
// 128 KB rather than the 192 KB used until Plan 4. On an SMB share with
// rsize=65536, dropping the third read request is worth far more than the third
// of the bytes it saves: a cold read of 221 files took 0.94s at 128 KB against
// 2.27s at 192 KB. End to end on two independent iPhone corpora, two runs each:
//
//	1,038 HEIC : hash 30.5-31.1s at 192 KB -> 26.0-27.0s at 128 KB  (-14%)
//	1,645 HEIC : hash 57.2s      at 192 KB -> 44.7s      at 128 KB  (-22%)
//
// The window still reaches the same files: rung-1 hits were 1016/1038 at both
// sizes on the first corpus, and 1439 vs 1438 on the second — the one file that
// moved still decodes, via the rung-3 retry. Both corpora produced identical
// duplicate groups at both sizes.
//
// Do not shrink this further: at 96 KB both corpora start missing files (981 of
// 1038, 289 of 297), and every miss costs a 1 MB re-read.
const heicHeaderReadSize = 128 * 1024

// heicWideRetryReadSize is the widened window used by rung 3 of the decode
// ladder, for the rare file whose thumbnail tile sits past the header window.
// Measured on a 1,038-file corpus: 22 files missed the header window and 2 of
// them were recovered at 1 MB. The rest have no thumbnail item at all, so no
// window size would help them.
const heicWideRetryReadSize = 1024 * 1024

// initHEIC configures the heic package at startup, choosing between the two
// backends: the system libheif loaded dynamically via purego, or the Rust HEVC
// decoder compiled to WASM.
//
// The deciding factor is whether the backend can decode the deliberately
// TRUNCATED header buffer that the byte-range fast path hands it
// (decodeHEICFromHeader, rung 1). A backend that rejects truncated containers
// collapses every HEIC onto a full os.ReadFile, the ~100× slower path.
// Measured on the samples/ corpus, 96 concurrent thumbnail decodes from a
// truncated header buffer (192 KB at the time of measurement):
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
		log.Println("[heic] Dynamic libheif < 1.18; using WASM decoder (HDR/tmap correctness + byte-range fast path)")
		return
	}
	log.Println("[heic] Using dynamic libheif >= 1.18 (faster than WASM, handles the byte-range fast path)")
}

// isHEIC reports whether path has a .heic or .heif extension.
func isHEIC(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".heic" || ext == ".heif"
}

// readHEICPrefix reads up to n bytes from the front of path.
// Returns the bytes actually read (fewer than n if the file is shorter) and any
// I/O error. The decode ladder uses this twice with different window sizes: the
// standard heicHeaderReadSize pass, and one widened retry.
func readHEICPrefix(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	got, err := io.ReadFull(f, buf)
	// io.ReadFull returns ErrUnexpectedEOF for short files — that's fine,
	// we just use however many bytes we got.
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:got], nil
}

// readHEICHeader reads the standard byte-range window used by the fast path.
func readHEICHeader(path string) ([]byte, error) {
	return readHEICPrefix(path, heicHeaderReadSize)
}

// Decode through the package-level heic.Decode / heic.DecodeThumbnail: as of
// heic v0.4.0 the package pools WASM modules internally, so there is nothing to
// gain from holding a decoder per worker (measured within noise over 96
// concurrent decodes) and heic.Decoder is deprecated upstream.

// decodeHEICFromHeader runs the thumbnail → primary → widened-retry ladder
// against an already-read header buffer. It is the shared decode core for the UI
// thumbnail path and the perceptual-hash path, so both agree on which image is
// produced whenever one is produced at all.
//
// When no rung succeeds it returns ErrNoThumbnail rather than decoding the full
// primary image. The two callers then diverge deliberately: the hash path treats
// that as "skip perceptual matching" (as it already does for JPEG), while
// heicThumbnailJPEG falls back to a full decode because a user is waiting on
// that one specific image.
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
	// Rung 3 — Fallback 2: widen the byte range to 1 MB and retry the thumbnail.
	// This recovers files whose thumbnail tile sits just past the header window
	// without paying to read the whole file. Timed and byte-counted because this
	// branch is the expensive one.
	fullReadStart := time.Now()
	if wide, wideErr := readHEICPrefix(path, heicWideRetryReadSize); wideErr == nil {
		heicLadderFullReadBytes.Add(int64(len(wide)))
		if img, err := heic.DecodeThumbnail(bytes.NewReader(wide)); err == nil {
			heicLadderFullReadNs.Add(int64(time.Since(fullReadStart)))
			heicLadderFullThumb.Add(1)
			return img, nil
		}
	}

	// No embedded thumbnail anywhere in the file. There used to be a fourth rung
	// here that read the whole file and decoded the PRIMARY image as a last
	// resort. Under the WASM decoder (the only backend on Windows) that costs
	// 3-13 seconds for a single 12 MP iPhone image: measured at 125s of decode
	// time for the 22 files out of 1,038 that reached it — 2% of the corpus
	// consuming ~30% of the entire scan, on every scan, because a file with no
	// thumbnail item never gains one.
	//
	// A JPEG with no EXIF thumbnail already returns ErrNoThumbnail from
	// computeDHashFromHeaderBuffer in hasher.go and is simply skipped for
	// perceptual matching. This is the same policy applied to the same situation.
	// The affected files still participate in exact-duplicate detection via
	// xxHash; they only lose perceptual matching.
	//
	// heicLadderFullPrimary is deliberately left in place and is now always zero.
	// A non-zero value in the [perf] line means this decision was reverted.
	heicLadderFullReadNs.Add(int64(time.Since(fullReadStart)))
	heicLadderFail.Add(1)
	return nil, ErrNoThumbnail
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
		// The ladder gives up on files with no embedded thumbnail rather than
		// decoding the full primary image, because in a scan that costs 3-13 s
		// per file across the whole corpus (see decodeHEICFromHeader).
		//
		// Here that reasoning does not apply: this is one file the user is
		// actively looking at, on demand. Showing nothing at all would be worse
		// than making them wait, so the interactive path keeps the full decode as
		// a last resort. GetThumbnail caches the result to disk, so the cost is
		// paid once per file rather than on every hover.
		//
		// Deliberately not counted in the heicLadder* counters: those describe
		// scan behaviour, and heicLadderFullPrimary staying at zero is the
		// tripwire for the scan-path decision.
		if !errors.Is(err, ErrNoThumbnail) {
			return nil, err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, err
		}
		img, err = heic.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
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
// Uses the byte-range fast path: one header read covers the ISOBMFF header
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
