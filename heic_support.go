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

	"github.com/Rosca75/heic"
	"github.com/bep/imagemeta"
	"golang.org/x/image/draw"
)

// heicHeaderReadSize is the byte-range size used by the HEIC fast path.
// 192 KB comfortably covers the ftyp + meta + iloc + thumbnail tile on
// every iPhone HEIC sample tested. If a file needs more, we fall back to
// a full read.
const heicHeaderReadSize = 192 * 1024

// initHEIC configures the heic package at startup.
// If dynamic libheif is in use but its version is < 1.18, force WASM mode so
// that HDR / tmap-brand HEIC files (common on iPhone) decode correctly.
func initHEIC() {
	if heic.Dynamic() != nil {
		// Dynamic libheif not available; WASM will be used automatically.
		return
	}
	if !heicDynamicVersionAtLeast(1, 18) {
		heic.ForceWasmMode = true
		log.Println("[heic] Dynamic libheif < 1.18; using WASM decoder for full compatibility")
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

// decodeHEICThumbnail decodes the embedded thumbnail from a HEIC file using
// the fast path: read first ~192 KB, hand to the WASM decoder. Falls back to
// a full file read on failure.
func decodeHEICThumbnail(path string) (image.Image, error) {
	// Fast path: read only the header + thumbnail tile region.
	header, err := readHEICHeader(path)
	if err != nil {
		return nil, err
	}
	if img, thumbErr := heic.DecodeThumbnail(bytes.NewReader(header)); thumbErr == nil {
		return img, nil
	}
	// Fallback 1: decode the primary image from the header (for files without
	// an embedded thumbnail iref).
	if img, decErr := heic.Decode(bytes.NewReader(header)); decErr == nil {
		return img, nil
	}
	// Fallback 2: full file read. Rare on typical iPhone HEIC but safe for
	// unusual files whose thumbnail tile sits past the 192 KB window.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if img, thumbErr := heic.DecodeThumbnail(bytes.NewReader(data)); thumbErr == nil {
		return img, nil
	}
	return heic.Decode(bytes.NewReader(data))
}

// heicThumbnailJPEG returns a JPEG-encoded, max-400px thumbnail for a HEIC
// file. Uses the byte-range fast path to avoid reading the full 3 MB file.
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

	img, thumbErr := heic.DecodeThumbnail(bytes.NewReader(header))
	if thumbErr != nil {
		// No embedded thumbnail in the 192 KB window — try primary decode
		// from the same buffer before falling back to full file read.
		var decErr error
		img, decErr = heic.Decode(bytes.NewReader(header))
		if decErr != nil {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return 0, width, height, ErrNoThumbnail
			}
			img, decErr = heic.DecodeThumbnail(bytes.NewReader(data))
			if decErr != nil {
				img, decErr = heic.Decode(bytes.NewReader(data))
				if decErr != nil {
					return 0, width, height, ErrNoThumbnail
				}
			}
		}
	}

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
