// raw_preview.go — extractEmbeddedJPEG extracts JPEG previews from camera RAW files.
package main

import (
	"bytes"      // bytes.NewReader for JPEG validation.
	"image/jpeg" // jpeg.Decode for validating extracted JPEG segments.
	"os"         // os.ReadFile to load the raw file bytes.
)

// extractEmbeddedJPEG scans filePath for embedded JPEG data (SOI/EOI markers).
// Many camera RAW formats (ARW, DNG, CR2) contain a full-size JPEG
// preview. Returns the largest valid JPEG found, or nil if none.
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

	var bestJPEG []byte

	// Scan for JPEG SOI markers (0xFF 0xD8). Skip byte 0 — if the file starts
	// with FFD8, it's already a plain JPEG and would have decoded normally.
	for i := 2; i < maxScan-1; i++ {
		if data[i] != 0xFF || data[i+1] != 0xD8 {
			continue
		}
		// Found SOI — search for the matching EOI (0xFF 0xD9).
		eoiIdx := -1
		for j := i + 2; j < maxScan-1; j++ {
			if data[j] == 0xFF && data[j+1] == 0xD9 {
				eoiIdx = j + 2
				break
			}
		}
		if eoiIdx <= 0 {
			continue
		}

		jpegData := data[i:eoiIdx]

		// Skip tiny EXIF thumbnails (< 10 KB); keep as fallback if nothing better.
		if len(jpegData) < 10*1024 {
			if bestJPEG == nil {
				bestJPEG = jpegData
			}
			continue
		}

		// Validate by attempting a full JPEG decode.
		if _, err := jpeg.Decode(bytes.NewReader(jpegData)); err == nil {
			if len(jpegData) > len(bestJPEG) {
				bestJPEG = jpegData
			}
		}

		i = eoiIdx - 1 // Advance past this JPEG segment.
	}

	return bestJPEG
}
