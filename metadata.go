// =============================================================================
// metadata.go — EXIF extraction and quality scoring
// =============================================================================
//
// This file reads metadata from image files — things like the date the photo
// was taken, the camera used, GPS coordinates, image dimensions, etc. This
// information is stored in the image file's EXIF (Exchangeable Image File
// Format) data, which is a standard way cameras embed settings into photos.
//
// Not every image has EXIF data. Screenshots, downloaded images, and some
// edited photos may have had their EXIF stripped. Our code handles this
// gracefully — missing EXIF just means we have less information.
//
// We also compute a "quality score" (0-100) for each image based on its
// metadata. This helps us recommend which duplicate to keep: the one with
// the highest resolution, most metadata, largest file size, etc.
//
// Key Go concepts:
//   - struct:      A collection of named fields (like a class without methods).
//   - interface:   A set of method signatures that a type can implement.
//   - json tags:   Tell Go's JSON encoder what key names to use in JSON output.
//   - math.Log2:   Logarithm base 2 — used for diminishing-returns scoring.
// =============================================================================

package main

import (
	"bytes"         // bytes.NewReader — wraps []byte as io.Reader for EXIF from buffer.
	"fmt"           // Formatted I/O.
	"image"         // Standard image interface for getting dimensions.
	"io"            // io.ReadFull — read exactly N bytes from a file.
	"math"          // Math functions like Log2, Min, Max.
	"os"            // File operations (stat, open).
	"path/filepath" // File path manipulation (extracting filenames).
	"strings"       // String manipulation.
	"sync"          // sync.Pool — reusable buffer pool for ExtractMetadataFast.

	// Standard image decoders (registered via blank imports in hasher.go,
	// but we list them here too for clarity about what formats we support).
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	// Extended format decoders from the Go team.
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	// goexif is a third-party library for reading EXIF metadata from images.
	// EXIF is the standard format that cameras use to store settings like
	// shutter speed, ISO, GPS coordinates, date/time, etc. inside the image.
	"github.com/rwcarlsen/goexif/exif"
)

// =============================================================================
// Types
// =============================================================================

// ImageMetadata holds all the metadata we extract from a single image file.
// The `json:"..."` tags after each field tell Go's JSON encoder what key name
// to use when converting this struct to JSON. For example, "date_taken" means
// the JSON output will have {"date_taken": "2024-01-15T..."} instead of
// {"DateTaken": "2024-01-15T..."}.
type ImageMetadata struct {
	Path         string  `json:"path"`          // Absolute filesystem path to the image.
	Filename     string  `json:"filename"`      // Just the filename (e.g., "photo.jpg").
	Size         int64   `json:"size"`          // File size in bytes.
	Width        int     `json:"width"`         // Image width in pixels.
	Height       int     `json:"height"`        // Image height in pixels.
	DateTaken    string  `json:"date_taken"`    // When the photo was taken, ISO 8601 format, or empty.
	GPSLat       float64 `json:"gps_lat"`       // GPS latitude (0 if not available).
	GPSLon       float64 `json:"gps_lon"`       // GPS longitude (0 if not available).
	Camera       string  `json:"camera"`        // Camera make + model (e.g., "Apple iPhone 15 Pro").
	Lens         string  `json:"lens"`          // Lens model, if available.
	ISO          int     `json:"iso"`           // ISO sensitivity (e.g., 100, 400, 3200).
	FocalLength  string  `json:"focal_length"`  // Focal length as a string (e.g., "50/1" for 50mm).
	Title        string  `json:"title"`         // Image title from EXIF/XMP, if present.
	Description  string  `json:"description"`   // Image description from EXIF/XMP, if present.
	QualityScore int     `json:"quality_score"` // Computed quality score, 0-100.
	Blockiness   float64 `json:"blockiness"`    // JPEG blockiness score (0=smooth, higher=more blocky artifacts).
	Blurring     float64 `json:"blurring"`      // Blur detection score (0=sharp, higher=more blurry).
	IsBest       bool    `json:"is_best"`       // True if this is the recommended "keep" image in a group.
}

// =============================================================================
// ExtractMetadata — Read all available metadata from an image file
// =============================================================================

// ExtractMetadata reads an image file and extracts as much metadata as
// possible: file size, dimensions, EXIF data (date, camera, GPS, etc.), and
// computes a quality score.
//
// This function never returns an error — it fills in whatever it can and
// leaves the rest at zero/empty values. Missing EXIF is extremely common
// (screenshots, web images, edited photos), so we treat it as normal.
//
// Parameters:
//   - path: Absolute path to the image file.
//
// Returns:
//   - ImageMetadata: A struct with all the extracted metadata.
func ExtractMetadata(path string) ImageMetadata {
	// Start building the metadata struct with the path and filename.
	// filepath.Base extracts just the filename from a full path.
	// For example, filepath.Base("/home/user/Photos/sunset.jpg") = "sunset.jpg".
	meta := ImageMetadata{
		Path:     path,
		Filename: filepath.Base(path),
	}

	// -------------------------------------------------------------------------
	// Step 1: Get basic file information (size)
	// -------------------------------------------------------------------------
	//
	// os.Stat returns a FileInfo interface with the file's size, permissions,
	// modification time, etc. This always works unless the file was deleted
	// between scanning and now.
	fileInfo, err := os.Stat(path)
	if err != nil {
		// If we can't even stat the file, return what we have (just path/name).
		fmt.Printf("[metadata] Warning: cannot stat file %s: %v\n", path, err)
		return meta
	}
	// Size() returns the file size in bytes as an int64.
	meta.Size = fileInfo.Size()

	// -------------------------------------------------------------------------
	// Step 2: Get image dimensions (width × height)
	// -------------------------------------------------------------------------
	//
	// We open the file and use image.DecodeConfig to read just the header
	// (not the full pixel data). This is much faster than image.Decode
	// because it doesn't decompress the entire image.
	dimFile, err := os.Open(path)
	if err == nil {
		// defer ensures the file is closed when we leave this block.
		// image.DecodeConfig reads the image header to get dimensions and
		// color model without decoding all the pixels.
		config, _, decErr := image.DecodeConfig(dimFile)
		dimFile.Close() // Close immediately — we don't need it anymore.

		if decErr == nil {
			meta.Width = config.Width
			meta.Height = config.Height
		} else {
			// If we can't decode dimensions (unsupported format),
			// just log it and continue. We'll still have file size and
			// possibly EXIF data.
			fmt.Printf("[metadata] Warning: cannot decode dimensions for %s: %v\n", path, decErr)
		}
	}

	// -------------------------------------------------------------------------
	// Step 3: Extract EXIF metadata
	// -------------------------------------------------------------------------
	//
	// EXIF data is embedded in JPEG and TIFF files by cameras and phones.
	// It contains a wealth of information: when the photo was taken, what
	// camera was used, GPS coordinates, etc.
	//
	// Not all images have EXIF data, and even when present, individual
	// fields may be missing. We handle each field independently.
	exifFile, err := os.Open(path)
	if err == nil {
		defer exifFile.Close()

		// exif.Decode reads and parses the EXIF data from the file.
		// It returns an *exif.Exif object that we can query for specific tags.
		exifData, exifErr := exif.Decode(exifFile)
		if exifErr == nil {
			// --- Date/Time the photo was taken ---
			// DateTimeOriginal is the standard EXIF tag for when the shutter
			// was pressed. It's the most reliable date for when a photo was
			// actually taken (as opposed to when the file was modified).
			dateTime, dtErr := exifData.DateTime()
			if dtErr == nil {
				// Format as ISO 8601 (the international standard for date/time).
				// "2006-01-02T15:04:05" is Go's reference time — you use this
				// specific date to define the format. It looks weird, but it's
				// how Go works (the reference time is Mon Jan 2 15:04:05 MST 2006).
				meta.DateTaken = dateTime.Format("2006-01-02T15:04:05")
			}

			// --- GPS coordinates ---
			// LatLong() extracts the GPS latitude and longitude from EXIF.
			// Not all photos have GPS data — phones usually do, but DSLRs
			// often don't (unless they have a built-in GPS or paired phone).
			lat, lon, gpsErr := exifData.LatLong()
			if gpsErr == nil {
				meta.GPSLat = lat
				meta.GPSLon = lon
			}

			// --- Camera make and model ---
			// We combine the make (manufacturer) and model into one string.
			// For example: make="Apple", model="iPhone 15 Pro" → "Apple iPhone 15 Pro".
			cameraMake := getExifString(exifData, exif.Make)
			cameraModel := getExifString(exifData, exif.Model)
			// strings.TrimSpace removes leading/trailing whitespace.
			meta.Camera = strings.TrimSpace(cameraMake + " " + cameraModel)

			// --- Lens model ---
			// LensModel is the EXIF tag for the lens used. This is most
			// useful for cameras with interchangeable lenses (DSLRs, mirrorless).
			meta.Lens = getExifString(exifData, exif.LensModel)

			// --- ISO sensitivity ---
			// ISO measures the sensor's light sensitivity. Lower ISO (100-400)
			// means less noise; higher ISO (1600-12800) is used in low light.
			isoTag, isoErr := exifData.Get(exif.ISOSpeedRatings)
			if isoErr == nil {
				// Int(0) gets the first integer value from the EXIF tag.
				// Some tags can have multiple values; we just want the first.
				isoVal, isoIntErr := isoTag.Int(0)
				if isoIntErr == nil {
					meta.ISO = isoVal
				}
			}

			// --- Focal length ---
			// Focal length determines the field of view: 14mm = ultra-wide,
			// 50mm = "normal" (similar to human eye), 200mm = telephoto.
			focalTag, focalErr := exifData.Get(exif.FocalLength)
			if focalErr == nil {
				// Focal length is stored as a rational number (numerator/denominator).
				// We convert it to a string representation.
				numer, denom, ratErr := focalTag.Rat2(0)
				if ratErr == nil {
					if denom != 0 {
						// Show as a clean number if it divides evenly, otherwise
						// as a fraction.
						focalMM := float64(numer) / float64(denom)
						meta.FocalLength = fmt.Sprintf("%.1fmm", focalMM)
					}
				}
			}

			// --- Title and Description ---
			// ImageDescription is a standard EXIF field for a short description.
			// Some cameras and photo editors set this.
			meta.Description = getExifString(exifData, exif.ImageDescription)

			// Note: "Title" isn't a standard EXIF tag — it's typically stored
			// in XMP or IPTC metadata, which goexif doesn't parse. We leave
			// Title empty for now; a future version could use an XMP library.
		}
		// If EXIF decoding failed, that's fine — we just won't have EXIF data.
		// This is normal for PNGs, BMPs, screenshots, etc.
	}

	// -------------------------------------------------------------------------
	// Step 4: Compute quality score
	// -------------------------------------------------------------------------
	//
	// The quality score helps us decide which image in a duplicate group to
	// keep. Higher score = more "valuable" image. See ComputeQualityScore
	// for the scoring algorithm.
	meta.QualityScore = ComputeQualityScore(&meta)

	// Blockiness and Blurring are left at 0.0 (Go zero values).
	// ComputeImageQualityMetrics was removed from the scan pipeline because it
	// does a full JPEG decode (~150 ms/file on USB), which made the grouping
	// phase take 8+ minutes for large scans. These metrics are now computed
	// lazily via the GetImageQualityMetrics Wails method when the user
	// expands a duplicate group in the UI.

	return meta
}

// metaBufPool holds reusable 128 KB buffers for ExtractMetadataFast.
// Instead of allocating a fresh 128 KB buffer on every call (2 771 allocations
// when running in parallel), we reuse buffers from this pool, reducing GC
// pressure and memory churn. The pool stores *[]byte pointers to avoid
// interface boxing overhead.
var metaBufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 128*1024)
		return &buf
	},
}

// =============================================================================
// ExtractMetadataFast — Single-open metadata extraction (Optimization A)
// =============================================================================

// ExtractMetadataFast is a faster version of ExtractMetadata that opens the
// file only ONCE and accepts pre-computed dimensions. On USB/NAS drives where
// each file open costs ~50ms, this is 3× faster than the original which opens
// the file 3 times (stat + DecodeConfig + EXIF).
//
// Parameters:
//   - path:   Absolute file path.
//   - width:  Pre-computed image width (0 if unknown → will try DecodeConfig).
//   - height: Pre-computed image height (0 if unknown).
//   - size:   Pre-computed file size in bytes (0 → will call os.Stat).
func ExtractMetadataFast(path string, width, height int, size int64) ImageMetadata {
	// Start with pre-computed values.
	meta := ImageMetadata{
		Path:     path,
		Filename: filepath.Base(path),
		Width:    width,
		Height:   height,
		Size:     size,
	}

	// Step 1: If size is missing, stat the file to get it.
	if meta.Size == 0 {
		if info, err := os.Stat(path); err == nil {
			meta.Size = info.Size()
		}
	}

	// Step 2: Single file open → read first 128 KB → extract everything.
	// 128 KB is enough to contain the full EXIF header for virtually all
	// camera JPEGs. This avoids reading the entire file.
	f, err := os.Open(path)
	if err != nil {
		// Can't open → compute quality from what we have and return.
		meta.QualityScore = ComputeQualityScore(&meta)
		return meta
	}

	// Grab a reusable 128 KB buffer from the pool instead of allocating
	// a new one each time. This reduces GC pressure when processing
	// thousands of files in parallel.
	bufPtr := metaBufPool.Get().(*[]byte)
	defer metaBufPool.Put(bufPtr)
	buf := *bufPtr
	n, _ := io.ReadFull(f, buf)
	f.Close() // Close immediately — we have the bytes we need.

	if n == 0 {
		meta.QualityScore = ComputeQualityScore(&meta)
		return meta
	}
	buf = buf[:n]

	// Step 3: If dimensions are missing, try DecodeConfig from the buffer.
	// DecodeConfig reads only the image header (not full pixel data).
	if meta.Width == 0 || meta.Height == 0 {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(buf)); err == nil {
			meta.Width = cfg.Width
			meta.Height = cfg.Height
		}
	}

	// Step 4: Extract EXIF from the buffer (no second file open!).
	exifData, exifErr := exif.Decode(bytes.NewReader(buf))
	if exifErr == nil {
		// --- Date/Time the photo was taken ---
		if dt, err := exifData.DateTime(); err == nil {
			meta.DateTaken = dt.Format("2006-01-02T15:04:05")
		}
		// --- GPS coordinates ---
		if lat, lon, err := exifData.LatLong(); err == nil {
			meta.GPSLat = lat
			meta.GPSLon = lon
		}
		// --- Camera make + model ---
		cameraMake := getExifString(exifData, exif.Make)
		cameraModel := getExifString(exifData, exif.Model)
		meta.Camera = strings.TrimSpace(cameraMake + " " + cameraModel)
		// --- Lens model ---
		meta.Lens = getExifString(exifData, exif.LensModel)
		// --- ISO sensitivity ---
		if isoTag, err := exifData.Get(exif.ISOSpeedRatings); err == nil {
			if v, err := isoTag.Int(0); err == nil {
				meta.ISO = v
			}
		}
		// --- Focal length ---
		if focalTag, err := exifData.Get(exif.FocalLength); err == nil {
			if num, den, err := focalTag.Rat2(0); err == nil && den != 0 {
				meta.FocalLength = fmt.Sprintf("%.1fmm", float64(num)/float64(den))
			}
		}
		// --- Description ---
		meta.Description = getExifString(exifData, exif.ImageDescription)
	}

	// Step 5: Compute quality score (same logic as original, uses all fields).
	meta.QualityScore = ComputeQualityScore(&meta)

	// Blockiness and Blurring are left at 0.0 (Go zero values).
	// ComputeImageQualityMetrics was removed from the scan pipeline because it
	// does a full JPEG decode (~150 ms/file on USB), which made the grouping
	// phase take 8+ minutes for large scans. These metrics are now computed
	// lazily via the GetImageQualityMetrics Wails method when the user
	// expands a duplicate group in the UI.

	return meta
}

// =============================================================================
// getExifString — Helper to safely extract a string from an EXIF tag
// =============================================================================

// getExifString tries to read a string value from the given EXIF tag.
// If the tag doesn't exist or can't be read, it returns an empty string.
// This avoids repetitive error-checking code in ExtractMetadata.
//
// Parameters:
//   - x:   The parsed EXIF data object.
//   - tag: The EXIF tag to read (e.g., exif.Make, exif.Model).
//
// Returns:
//   - string: The tag's value, or "" if not found/readable.
func getExifString(x *exif.Exif, tag exif.FieldName) string {
	// x.Get(tag) looks up the tag in the EXIF data. It returns the raw tag
	// value and an error (nil if found, non-nil if not present).
	t, err := x.Get(tag)
	if err != nil {
		return ""
	}
	// StringVal() converts the raw tag value to a Go string.
	// It returns an error if the tag isn't a string type.
	val, err := t.StringVal()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(val)
}

// =============================================================================
// ComputeQualityScore — Rate an image's "quality" from 0 to 100
// =============================================================================

// ComputeQualityScore assigns a quality score (0-100) to an image based on
// its metadata. This score is used to rank duplicates: the highest-scoring
// image in a group is recommended as the one to keep.
//
// The scoring algorithm considers multiple factors, each contributing a
// portion of the maximum 100 points. The idea is:
//   - Higher resolution = probably a better copy (not resized/thumbnailed).
//   - Larger file size = probably less compressed (more quality preserved).
//   - Has date/GPS/camera info = probably an original from a real camera
//     (not a screenshot or downloaded copy).
//
// SCORING BREAKDOWN:
//
//	Factor          Max Points  Rationale
//	─────────────   ──────────  ─────────────────────────────────────────────
//	Resolution      30          Higher resolution = more detail preserved.
//	File size       15          Larger files usually mean less compression.
//	Date taken      20          Originals almost always have date EXIF data.
//	GPS coords      15          Only originals from cameras/phones have GPS.
//	Camera info      5          Indicates it came straight from a camera.
//	Lens info        5          Indicates a serious camera (not a phone edit).
//	Title/Desc      10          Metadata-rich images are more "curated."
//	─────────────   ──────────
//	TOTAL          100
//
// Parameters:
//   - meta: Pointer to the ImageMetadata struct to score. We use a pointer
//     (&meta) so we don't copy the whole struct (efficiency).
//
// Returns:
//   - int: The quality score, clamped to the range [0, 100].
func ComputeQualityScore(meta *ImageMetadata) int {
	score := 0.0 // We use float64 for intermediate calculations.

	// -------------------------------------------------------------------------
	// Factor 1: Resolution (0-30 points)
	// -------------------------------------------------------------------------
	// We use log2(megapixels) × 7.5 as the formula. The logarithm provides
	// "diminishing returns" — going from 1MP to 4MP matters a lot, but going
	// from 48MP to 50MP doesn't matter much. This is how human perception
	// works with resolution.
	//
	// Examples:
	//   1 MP  → log2(1) × 7.5 = 0 × 7.5 = 0 points
	//   2 MP  → log2(2) × 7.5 = 1 × 7.5 = 7.5 points
	//   8 MP  → log2(8) × 7.5 = 3 × 7.5 = 22.5 points
	//   16 MP → log2(16) × 7.5 = 4 × 7.5 = 30 points (capped)
	if meta.Width > 0 && meta.Height > 0 {
		// Calculate megapixels: total pixels divided by 1,000,000.
		megapixels := float64(meta.Width) * float64(meta.Height) / 1_000_000.0

		// Only compute log if megapixels > 0 (log of 0 is negative infinity).
		if megapixels > 0 {
			// math.Log2 returns log base 2. math.Min caps the result at 30.
			resolutionScore := math.Min(math.Log2(megapixels)*7.5, 30.0)

			// Ensure we don't add negative points (if megapixels < 1, log2 is negative).
			if resolutionScore > 0 {
				score += resolutionScore
			}
		}
	}

	// -------------------------------------------------------------------------
	// Factor 2: File size (0-15 points)
	// -------------------------------------------------------------------------
	// Similar diminishing-returns approach. A 1MB JPEG is probably fine; a
	// 20MB JPEG is probably high-quality; a 100MB file isn't 5× better than
	// 20MB. Log2 captures this nicely.
	//
	// Examples:
	//   0.5 MB → log2(0.5) × 3 = -1 × 3 = -3 → clamped to 0
	//   1 MB   → log2(1) × 3 = 0 × 3 = 0 points
	//   4 MB   → log2(4) × 3 = 2 × 3 = 6 points
	//   32 MB  → log2(32) × 3 = 5 × 3 = 15 points (capped)
	if meta.Size > 0 {
		// Convert bytes to megabytes (1 MB = 1,048,576 bytes).
		megabytes := float64(meta.Size) / (1024.0 * 1024.0)

		if megabytes > 0 {
			sizeScore := math.Min(math.Log2(megabytes)*3.0, 15.0)
			if sizeScore > 0 {
				score += sizeScore
			}
		}
	}

	// -------------------------------------------------------------------------
	// Factor 3: Date taken (0 or 20 points)
	// -------------------------------------------------------------------------
	// If the image has a "date taken" in its EXIF data, it's almost certainly
	// an original photo from a camera/phone. Downloaded images, screenshots,
	// and re-saved copies usually lose this information. This is a strong
	// signal that we have the "original" file.
	if meta.DateTaken != "" {
		score += 20.0
	}

	// -------------------------------------------------------------------------
	// Factor 4: GPS coordinates (0 or 15 points)
	// -------------------------------------------------------------------------
	// GPS data means the photo was taken by a device with location services.
	// This is very common on phones and some high-end cameras. A copy of the
	// photo (from messaging apps, social media, etc.) usually has GPS stripped
	// for privacy. So GPS presence = likely the original.
	if meta.GPSLat != 0 || meta.GPSLon != 0 {
		score += 15.0
	}

	// -------------------------------------------------------------------------
	// Factor 5: Camera information (0 or 5 points)
	// -------------------------------------------------------------------------
	// If we know what camera took the photo, it's more likely to be the
	// original file. Editing tools sometimes strip this info.
	if meta.Camera != "" {
		score += 5.0
	}

	// -------------------------------------------------------------------------
	// Factor 6: Lens information (0 or 5 points)
	// -------------------------------------------------------------------------
	// Lens info is usually only present on the original file from a camera.
	// It's especially common with interchangeable-lens cameras (DSLRs,
	// mirrorless).
	if meta.Lens != "" {
		score += 5.0
	}

	// -------------------------------------------------------------------------
	// Factor 7: Title or description (0 or 10 points)
	// -------------------------------------------------------------------------
	// If someone took the time to add a title or description, this is
	// probably a curated/organized copy. It suggests the user values this
	// particular file.
	if meta.Title != "" || meta.Description != "" {
		score += 10.0
	}

	// -------------------------------------------------------------------------
	// Clamp the final score to [0, 100]
	// -------------------------------------------------------------------------
	// math.Max ensures the score isn't negative.
	// math.Min ensures it doesn't exceed 100.
	// int() truncates the float to an integer (e.g., 72.8 → 72).
	finalScore := int(math.Max(0, math.Min(100, score)))

	return finalScore
}

// =============================================================================
// ComputeImageQualityMetrics — Blockiness and blurring scores
// =============================================================================

// ComputeImageQualityMetrics computes two quality metrics for an image:
//
// Blockiness: Measures JPEG compression artifacts. JPEG encodes images in 8x8
// blocks. Over-compressed images show visible block boundaries. We detect
// these by measuring the average gradient at 8-pixel intervals and comparing
// it to the overall gradient. Higher values = more visible artifacts.
//
// Blurring: Measures image sharpness using the Laplacian operator (second
// derivative). Sharp images have high variance in the Laplacian, blurry
// images have low variance. We compute on a downscaled version for speed.
//
// Returns (blockiness, blurring). Both return 0.0 if the image can't be decoded.
func ComputeImageQualityMetrics(path string) (float64, float64) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer file.Close()

	// Use DecodeConfig first to check dimensions, then decode.
	img, _, err := image.Decode(file)
	if err != nil {
		return 0, 0
	}

	bounds := img.Bounds()
	w := bounds.Max.X - bounds.Min.X
	h := bounds.Max.Y - bounds.Min.Y
	if w < 16 || h < 16 {
		return 0, 0
	}

	// Cap at 512px for performance — we don't need full resolution.
	// Use nearest-neighbor sampling for speed.
	maxDim := 512
	scaleW, scaleH := w, h
	if w > maxDim || h > maxDim {
		ratio := float64(maxDim) / math.Max(float64(w), float64(h))
		scaleW = int(float64(w) * ratio)
		scaleH = int(float64(h) * ratio)
	}

	// Build grayscale grid.
	gray := make([][]float64, scaleH)
	for y := 0; y < scaleH; y++ {
		gray[y] = make([]float64, scaleW)
		for x := 0; x < scaleW; x++ {
			srcX := bounds.Min.X + x*w/scaleW
			srcY := bounds.Min.Y + y*h/scaleH
			r, g, b, _ := img.At(srcX, srcY).RGBA()
			// Luminance in [0, 255] range.
			gray[y][x] = float64(299*r+587*g+114*b) / 1000.0 / 256.0
		}
	}

	blockiness := computeBlockiness(gray, scaleW, scaleH)
	blurring := computeBlurring(gray, scaleW, scaleH)
	return blockiness, blurring
}

// computeBlockiness measures JPEG block boundary artifacts.
// It compares the average gradient at 8-pixel boundaries to the overall gradient.
func computeBlockiness(gray [][]float64, w, h int) float64 {
	var boundarySum, totalSum float64
	var boundaryCount, totalCount int

	for y := 1; y < h; y++ {
		for x := 1; x < w; x++ {
			// Horizontal gradient.
			dx := math.Abs(gray[y][x] - gray[y][x-1])
			totalSum += dx
			totalCount++
			if x%8 == 0 {
				boundarySum += dx
				boundaryCount++
			}
		}
	}

	if totalCount == 0 || boundaryCount == 0 {
		return 0
	}

	avgBoundary := boundarySum / float64(boundaryCount)
	avgTotal := totalSum / float64(totalCount)

	if avgTotal < 0.01 {
		return 0
	}
	// Ratio of boundary gradient to overall gradient.
	// Values close to 1.0 = no blockiness, higher = more blocky.
	return math.Max(0, (avgBoundary/avgTotal)-1.0) * 100.0
}

// computeBlurring estimates image sharpness via Laplacian variance.
// Lower variance = more blurry. We return the inverse so higher = more blurry.
func computeBlurring(gray [][]float64, w, h int) float64 {
	if w < 3 || h < 3 {
		return 0
	}

	// Apply 3x3 Laplacian kernel: [0,-1,0; -1,4,-1; 0,-1,0]
	var sum, sumSq float64
	count := 0
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			laplacian := 4*gray[y][x] - gray[y-1][x] - gray[y+1][x] - gray[y][x-1] - gray[y][x+1]
			sum += laplacian
			sumSq += laplacian * laplacian
			count++
		}
	}

	if count == 0 {
		return 0
	}

	mean := sum / float64(count)
	variance := sumSq/float64(count) - mean*mean

	// Higher variance = sharper. We invert so higher = more blurry.
	// Scale to a readable range. Typical sharp images have variance 50-500.
	if variance < 0.01 {
		return 100.0 // Extremely blurry.
	}
	// Map: variance 500+ → ~0 blur, variance ~1 → ~100 blur.
	blurScore := math.Max(0, math.Min(100, 100.0-variance*0.2))
	return math.Round(blurScore*100) / 100
}
