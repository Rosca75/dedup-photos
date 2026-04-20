// exif_extract.go — Unified EXIF extraction using bep/imagemeta.
//
// extractExifInto replaces all rwcarlsen/goexif call sites. bep/imagemeta
// supports JPEG, TIFF, WebP, PNG, HEIF/HEIC, AVIF, DNG, CR2, NEF, ARW —
// a superset of goexif's JPEG/TIFF-only coverage, and runs ~2.6× faster
// with ~40× fewer allocations.
package main

import (
	"fmt"
	"io"     // io.ReadSeeker required by imagemeta.Options.
	"strings"
	"time"

	"github.com/bep/imagemeta"
)

// imageFormatForExt maps a lowercase file extension to the bep/imagemeta format
// constant needed by imagemeta.Decode. RAW formats (DNG, ARW, CR2, etc.) are
// TIFF-based containers, so imagemeta.TIFF is correct for them.
func imageFormatForExt(ext string) (imagemeta.ImageFormat, bool) {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg":
		return imagemeta.JPEG, true
	case ".tiff", ".tif", ".dng", ".arw", ".cr2", ".nef", ".orf", ".rw2", ".raf":
		return imagemeta.TIFF, true // RAW formats embed EXIF in a TIFF container.
	case ".webp":
		return imagemeta.WebP, true
	case ".png":
		return imagemeta.PNG, true
	case ".heic", ".heif":
		return imagemeta.HEIF, true
	}
	return 0, false
}

// extractExifInto populates metadata fields from the EXIF stream in r.
// format must match the file's actual image format (use imageFormatForExt).
// Fields already set in meta are not overwritten (HEIC path may call this
// after a partial extraction). r must implement io.ReadSeeker (bytes.NewReader
// and os.File both satisfy this; plain io.Reader does not).
func extractExifInto(r io.ReadSeeker, format imagemeta.ImageFormat, meta *ImageMetadata) {
	var make_, model string
	var latRef, lonRef string
	var lat, lon float64
	var hasLat, hasLon bool

	_, _ = imagemeta.Decode(imagemeta.Options{
		R:           r,
		ImageFormat: format,
		HandleTag: func(ti imagemeta.TagInfo) error {
			switch ti.Tag {
			case "DateTimeOriginal":
				if meta.DateTaken == "" {
					switch v := ti.Value.(type) {
					case string:
						if t, err := time.Parse("2006:01:02 15:04:05", v); err == nil {
							meta.DateTaken = t.Format("2006-01-02T15:04:05")
						}
					case time.Time:
						if !v.IsZero() {
							meta.DateTaken = v.Format("2006-01-02T15:04:05")
						}
					}
				}
			case "Make":
				if s, ok := ti.Value.(string); ok {
					make_ = strings.TrimSpace(s)
				}
			case "Model":
				if s, ok := ti.Value.(string); ok {
					model = strings.TrimSpace(s)
				}
			case "LensModel":
				if s, ok := ti.Value.(string); ok && meta.Lens == "" {
					meta.Lens = strings.TrimSpace(s)
				}
			case "ISO":
				if meta.ISO == 0 {
					switch v := ti.Value.(type) {
					case uint16:
						meta.ISO = int(v)
					case uint32:
						meta.ISO = int(v)
					}
				}
			case "FocalLength":
				if meta.FocalLength == "" {
					switch v := ti.Value.(type) {
					case imagemeta.Rat[uint32]:
						meta.FocalLength = fmt.Sprintf("%.1fmm", v.Float64())
					case imagemeta.Rat[int32]:
						meta.FocalLength = fmt.Sprintf("%.1fmm", v.Float64())
					case float64:
						if v > 0 {
							meta.FocalLength = fmt.Sprintf("%.1fmm", v)
						}
					}
				}
			case "GPSLatitudeRef":
				if s, ok := ti.Value.(string); ok {
					latRef = s
				}
			case "GPSLongitudeRef":
				if s, ok := ti.Value.(string); ok {
					lonRef = s
				}
			case "GPSLatitude":
				if fv, ok := ti.Value.(float64); ok {
					lat = fv
					hasLat = true
				}
			case "GPSLongitude":
				if fv, ok := ti.Value.(float64); ok {
					lon = fv
					hasLon = true
				}
			case "ImageDescription":
				if s, ok := ti.Value.(string); ok && meta.Description == "" {
					meta.Description = strings.TrimSpace(s)
				}
			}
			return nil
		},
	})

	if meta.Camera == "" && (make_ != "" || model != "") {
		meta.Camera = strings.TrimSpace(make_ + " " + model)
	}
	if hasLat && meta.GPSLat == 0 {
		meta.GPSLat = lat
		if strings.HasPrefix(strings.ToUpper(latRef), "S") {
			meta.GPSLat = -lat
		}
	}
	if hasLon && meta.GPSLon == 0 {
		meta.GPSLon = lon
		if strings.HasPrefix(strings.ToUpper(lonRef), "W") {
			meta.GPSLon = -lon
		}
	}
}

// extractJPEGThumbnailFromBuffer scans the first 128 KB of buf for an embedded
// JPEG thumbnail. Camera and phone JPEGs store a small preview in the EXIF APP1
// segment, which is always near the start of the file. Scanning for the JPEG
// SOI/EOI markers is cheaper than parsing the full TIFF IFD structure.
//
// Starts at byte 2 to skip the main image's own SOI marker (if buf is a JPEG).
// Returns nil if no embedded thumbnail is found.
func extractJPEGThumbnailFromBuffer(buf []byte) []byte {
	// Only scan the first 128 KB — EXIF thumbnails are always in this range.
	limit := len(buf)
	if limit > 128*1024 {
		limit = 128 * 1024
	}
	for i := 2; i < limit-3; i++ {
		if buf[i] != 0xFF || buf[i+1] != 0xD8 {
			continue
		}
		// Found a potential JPEG SOI — search for the matching EOI within the window.
		for j := i + 2; j < limit-1; j++ {
			if buf[j] == 0xFF && buf[j+1] == 0xD9 {
				return buf[i : j+2]
			}
		}
	}
	return nil
}
