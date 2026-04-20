// raw_preview.go — extractEmbeddedJPEG extracts JPEG previews from camera RAW files.
package main

import (
	"bytes"      // bytes.Index for optimised SOI marker search.
	"image/jpeg" // jpeg.DecodeConfig for header-only JPEG validation.
	"os"         // os.ReadFile to load the raw file bytes.
)

// extractEmbeddedJPEG scans filePath for embedded JPEG data (SOI/EOI markers).
// Many camera RAW formats (ARW, DNG, CR2) contain a full-size JPEG preview.
// Returns the largest valid JPEG found, or nil if none.
//
// Validation uses jpeg.DecodeConfig (header-only parse) instead of jpeg.Decode
// (full pixel decode): on a 50 MB ARW with multiple preview candidates, this
// cuts validation from hundreds of milliseconds to single digits.
//
// SOI search uses bytes.Index (assembly-optimised) instead of a manual byte-
// by-byte loop.
func extractEmbeddedJPEG(filePath string) []byte {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}

	// Limit scan to first 50 MB to avoid hanging on very large RAW files.
	maxScan := len(data)
	if maxScan > 50*1024*1024 {
		maxScan = 50 * 1024 * 1024
	}

	soi := []byte{0xFF, 0xD8}
	eoi := []byte{0xFF, 0xD9}

	var bestJPEG []byte

	// Start search at byte 2 — if the file starts with FFD8 it's already a
	// plain JPEG and would have decoded via image.Decode in the caller.
	pos := 2
	for pos < maxScan-1 {
		rel := bytes.Index(data[pos:maxScan], soi)
		if rel < 0 {
			break
		}
		i := pos + rel

		// Find matching EOI within the remaining scan window.
		relEoi := bytes.Index(data[i+2:maxScan], eoi)
		if relEoi < 0 {
			break
		}
		eoiIdx := i + 2 + relEoi + 2 // +2 past EOI marker bytes
		jpegData := data[i:eoiIdx]

		// Keep tiny (<10 KB) segments as a fallback but don't bother validating —
		// they are typically EXIF thumbnails we'd rather skip if anything better exists.
		if len(jpegData) < 10*1024 {
			if bestJPEG == nil {
				bestJPEG = jpegData
			}
			pos = eoiIdx
			continue
		}

		// Header-only validation — no pixel decode.
		if cfg, err := jpeg.DecodeConfig(bytes.NewReader(jpegData)); err == nil && cfg.Width > 200 && cfg.Height > 200 {
			if len(jpegData) > len(bestJPEG) {
				bestJPEG = jpegData
			}
		}

		pos = eoiIdx
	}

	return bestJPEG
}
