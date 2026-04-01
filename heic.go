// =============================================================================
// heic.go — Pure-Go HEIC/HEIF thumbnail and metadata extraction
// =============================================================================
//
// HEIC (High Efficiency Image Container) is Apple's default photo format since
// iOS 11. Standard Go image decoders cannot handle it, so this file provides
// a fallback path using a pure-Go ISOBMFF parser (github.com/jdeng/goheif/heif).
//
// No ImageMagick, no CGo, no external binaries — pure Go only.
//
// Functions exported from this file:
//   - extractHEICMeta(path)  → width, height, camera, date, thumbBase64, err
//   - isHEICPath(path)       → bool
// =============================================================================

package main

import (
	"bytes"           // bytes.NewReader — wraps []byte as io.Reader for exif.Decode
	"encoding/base64" // base64.StdEncoding.EncodeToString — build thumbnail data URI
	"os"              // os.Open — open the HEIC file for the heif parser
	"strings"         // strings.ToLower for extension check

	"github.com/jdeng/goheif/heif"    // Pure-Go ISOBMFF / HEIC container parser
	"github.com/rwcarlsen/goexif/exif" // EXIF metadata reader (already a project dependency)
)

// extractHEICMeta opens a HEIC/HEIF file and extracts:
//   - Spatial dimensions (width, height in pixels)
//   - Camera model string (from EXIF Make + Model tags)
//   - Date taken (ISO-8601 string from EXIF DateTimeOriginal)
//   - Embedded JPEG thumbnail as a data URI ("data:image/jpeg;base64,...")
//
// Returns an error if the file cannot be opened or parsed as a HEIC container.
// Individual fields may be empty/zero if the corresponding metadata is absent.
func extractHEICMeta(path string) (width, height int, camera, createdAt, thumbBase64 string, err error) {
	// Open the file. heif.Open() requires an io.ReaderAt; os.File satisfies that.
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, "", "", "", err
	}
	defer f.Close()

	// heif.Open parses the ISOBMFF box structure. It does NOT decode pixel data.
	// Returns *heif.File directly (no error return from Open).
	hf := heif.Open(f)

	// PrimaryItem returns the main image item (the one displayed by default).
	primary, err := hf.PrimaryItem()
	if err != nil {
		return 0, 0, "", "", "", err
	}

	// SpatialExtents reads declared dimensions from item properties (no pixel decode).
	// Returns (width, height int, ok bool).
	if w, h, ok := primary.SpatialExtents(); ok {
		width = w
		height = h
	}

	// Extract raw EXIF bytes from the HEIC container.
	// HEIC stores EXIF in a dedicated item separate from the pixel stream.
	exifBytes, exifErr := hf.EXIF()
	if exifErr == nil && len(exifBytes) > 0 {
		// Parse the raw EXIF bytes using the standard goexif library.
		x, decErr := exif.Decode(bytes.NewReader(exifBytes))
		if decErr == nil {
			// Combine Make + Model (e.g. "Apple" + "iPhone 15 Pro" → "Apple iPhone 15 Pro").
			if makeTag, e := x.Get(exif.Make); e == nil {
				if s, e2 := makeTag.StringVal(); e2 == nil {
					camera = s
				}
			}
			if modelTag, e := x.Get(exif.Model); e == nil {
				if s, e2 := modelTag.StringVal(); e2 == nil {
					if camera != "" {
						camera += " " + s
					} else {
						camera = s
					}
				}
			}

			// DateTimeOriginal is the EXIF tag for when the shutter was pressed.
			if dt, e := x.DateTime(); e == nil {
				// ISO-8601 format matches what the rest of the app expects.
				createdAt = dt.Format("2006-01-02T15:04:05")
			}
		}
	}

	// Scan for an embedded JPEG thumbnail inside the HEIC container.
	thumbBase64 = extractHEICThumb(hf, primary)

	return width, height, camera, createdAt, thumbBase64, nil
}

// extractHEICThumb scans item IDs 1–50 for a thumbnail item that references
// the primary image via a "thmb" item reference.
//
// Most HEIC files from iPhones include a small (~10–20 KB) JPEG thumbnail in
// the container alongside the full HEVC image. We extract and return that
// thumbnail without touching the HEVC stream at all.
//
// Returns "data:image/jpeg;base64,<data>" on success, or "" if no JPEG thumbnail
// is found (e.g. no embedded thumbnail, or thumbnail is HEVC-encoded).
func extractHEICThumb(hf *heif.File, primary *heif.Item) string {
	primaryID := primary.ID // ID is a uint32 field, not a method.

	// Iterate item IDs 1–50. HEIC containers typically have only a handful
	// of items (primary image, thumbnail, EXIF, XMP), so 50 is generous.
	for id := uint32(1); id <= 50; id++ {
		item, err := hf.ItemByID(id)
		if err != nil {
			continue // No item at this ID — skip.
		}

		// Reference("thmb") returns the "thmb" ItemReferenceEntry for this item,
		// or nil if this item has no thumbnail reference.
		ref := item.Reference("thmb")
		if ref == nil {
			continue // Not a thumbnail item.
		}

		// ref.ToItemIDs contains the IDs that this thumbnail item references.
		// We want the thumbnail that points to our primary image.
		pointsToPrimary := false
		for _, toID := range ref.ToItemIDs {
			if toID == primaryID {
				pointsToPrimary = true
				break
			}
		}
		if !pointsToPrimary {
			continue
		}

		// Read the raw item data (the thumbnail's encoded bytes).
		data, err := hf.GetItemData(item)
		if err != nil || len(data) < 3 {
			continue
		}

		// Validate JPEG magic bytes: SOI marker is FF D8 FF.
		// Some HEIC files embed HEVC-encoded thumbnails instead; skip those.
		if data[0] != 0xFF || data[1] != 0xD8 || data[2] != 0xFF {
			continue
		}

		// Return as a data URI so callers can embed it directly in img.src.
		return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
	}

	return "" // No JPEG thumbnail found in this container.
}

// isHEICPath returns true if the file extension indicates a HEIC or HEIF file.
// Used by GetThumbnail and ExtractMetadata to gate HEIC-specific code paths.
func isHEICPath(path string) bool {
	lower := strings.ToLower(path)
	if len(lower) >= 5 {
		tail := lower[len(lower)-5:]
		if tail == ".heic" || tail == ".heif" {
			return true
		}
	}
	return false
}
