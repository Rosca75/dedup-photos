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
// EXIF is extracted via bep/imagemeta, which supports JPEG, TIFF, WebP, PNG,
// HEIF/HEIC, AVIF, DNG, CR2, NEF, ARW — and runs ~2.6× faster with ~40×
// fewer allocations than the previously used rwcarlsen/goexif.
// =============================================================================

package main

import (
	"bytes"         // bytes.NewReader — wraps []byte as io.Reader for in-buffer EXIF.
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
)

// =============================================================================
// Types
// =============================================================================

// ImageMetadata holds all the metadata we extract from a single image file.
// The `json:"..."` tags tell Go's JSON encoder what key names to use.
type ImageMetadata struct {
	Path         string  `json:"path"`          // Absolute filesystem path to the image.
	Filename     string  `json:"filename"`      // Just the filename (e.g., "photo.jpg").
	Size         int64   `json:"size"`          // File size in bytes.
	Width        int     `json:"width"`         // Image width in pixels.
	Height       int     `json:"height"`        // Image height in pixels.
	DateTaken    string  `json:"date_taken"`    // When the photo was taken, ISO 8601, or empty.
	GPSLat       float64 `json:"gps_lat"`       // GPS latitude (0 if not available).
	GPSLon       float64 `json:"gps_lon"`       // GPS longitude (0 if not available).
	Camera       string  `json:"camera"`        // Camera make + model (e.g., "Apple iPhone 15 Pro").
	Lens         string  `json:"lens"`          // Lens model, if available.
	ISO          int     `json:"iso"`           // ISO sensitivity (e.g., 100, 400, 3200).
	FocalLength  string  `json:"focal_length"`  // Focal length as a string (e.g., "50.0mm").
	Title        string  `json:"title"`         // Image title from EXIF/XMP, if present.
	Description  string  `json:"description"`   // Image description from EXIF/XMP, if present.
	QualityScore int     `json:"quality_score"` // Computed quality score, 0-100.
	Blockiness   float64 `json:"blockiness"`    // JPEG blockiness score (0=smooth).
	Blurring     float64 `json:"blurring"`      // Blur detection score (0=sharp).
	IsBest       bool    `json:"is_best"`       // True if this is the recommended "keep" image.
	XXHash       uint64  `json:"xxhash"`        // xxHash64 of raw file bytes (from scan).
	DHash        uint64  `json:"dhash"`         // Perceptual dHash of image content (from scan).
}

// =============================================================================
// ExtractMetadata — Read all available metadata from an image file
// =============================================================================

// ExtractMetadata reads an image file and extracts as much metadata as
// possible: file size, dimensions, EXIF data (date, camera, GPS, etc.), and
// computes a quality score. Never returns an error — fills in whatever it can.
func ExtractMetadata(path string) ImageMetadata {
	meta := ImageMetadata{
		Path:     path,
		Filename: filepath.Base(path),
	}

	// Step 1: Get basic file information (size).
	fileInfo, err := os.Stat(path)
	if err != nil {
		fmt.Printf("[metadata] Warning: cannot stat file %s: %v\n", path, err)
		return meta
	}
	meta.Size = fileInfo.Size()

	// Step 2: Get image dimensions by reading only the header.
	dimFile, err := os.Open(path)
	if err == nil {
		config, _, decErr := image.DecodeConfig(dimFile)
		dimFile.Close()
		if decErr == nil {
			meta.Width = config.Width
			meta.Height = config.Height
		} else {
			fmt.Printf("[metadata] Warning: cannot decode dimensions for %s: %v\n", path, decErr)
		}
	}

	// Step 3: Extract EXIF metadata using bep/imagemeta.
	// HEIC/HEIF uses extractHEICExif which handles the ISOBMFF container.
	// All other supported formats use extractExifInto with the appropriate format hint.
	ext := strings.ToLower(filepath.Ext(path))
	if isHEIC(path) {
		extractHEICExif(path, &meta)
	} else if format, ok := imageFormatForExt(ext); ok {
		exifFile, err := os.Open(path)
		if err == nil {
			defer exifFile.Close()
			extractExifInto(exifFile, format, &meta)
		}
	}

	// Step 4: Compute quality score.
	meta.QualityScore = ComputeQualityScore(&meta)

	// Blockiness and Blurring are left at 0.0. They are computed lazily via
	// GetImageQualityMetrics when the user expands a duplicate group.
	return meta
}

// metaBufPool holds reusable 128 KB buffers for ExtractMetadataFast.
// Instead of allocating a fresh 128 KB buffer on every call, we reuse buffers
// from this pool, reducing GC pressure when processing thousands of files.
var metaBufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 128*1024)
		return &buf
	},
}

// =============================================================================
// ExtractMetadataFast — Single-open metadata extraction
// =============================================================================

// ExtractMetadataFast is a faster version of ExtractMetadata that opens the
// file only ONCE and accepts pre-computed dimensions. On USB/NAS drives where
// each file open costs ~50ms, this is significantly faster than the original.
//
// Parameters:
//   - path:   Absolute file path.
//   - width:  Pre-computed image width (0 if unknown → will try DecodeConfig).
//   - height: Pre-computed image height (0 if unknown).
//   - size:   Pre-computed file size in bytes (0 → will call os.Stat).
func ExtractMetadataFast(path string, width, height int, size int64) ImageMetadata {
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
	// camera JPEGs.
	f, err := os.Open(path)
	if err != nil {
		meta.QualityScore = ComputeQualityScore(&meta)
		return meta
	}

	bufPtr := metaBufPool.Get().(*[]byte)
	defer metaBufPool.Put(bufPtr)
	buf := *bufPtr
	n, _ := io.ReadFull(f, buf)
	f.Close()

	if n == 0 {
		meta.QualityScore = ComputeQualityScore(&meta)
		return meta
	}
	buf = buf[:n]

	// Step 3: If dimensions are missing, try DecodeConfig from the buffer.
	if meta.Width == 0 || meta.Height == 0 {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(buf)); err == nil {
			meta.Width = cfg.Width
			meta.Height = cfg.Height
		}
	}

	// Step 4: Extract EXIF from the 128 KB buffer (no second file open).
	// HEIC/HEIF needs the full ISOBMFF container, so it opens the file itself.
	ext := strings.ToLower(filepath.Ext(path))
	if isHEIC(path) {
		extractHEICExif(path, &meta)
	} else if format, ok := imageFormatForExt(ext); ok {
		extractExifInto(bytes.NewReader(buf), format, &meta)
	}

	// Step 5: Compute quality score.
	meta.QualityScore = ComputeQualityScore(&meta)

	// Blockiness and Blurring are left at 0.0 — computed lazily on demand.
	return meta
}

// =============================================================================
// ComputeQualityScore — Rate an image's "quality" from 0 to 100
// =============================================================================

// ComputeQualityScore assigns a quality score (0-100) to an image based on
// its metadata. This score is used to rank duplicates: the highest-scoring
// image in a group is recommended as the one to keep.
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
func ComputeQualityScore(meta *ImageMetadata) int {
	score := 0.0

	// Resolution (0-30 points): log2(megapixels) × 7.5 with diminishing returns.
	if meta.Width > 0 && meta.Height > 0 {
		megapixels := float64(meta.Width) * float64(meta.Height) / 1_000_000.0
		if megapixels > 0 {
			resolutionScore := math.Min(math.Log2(megapixels)*7.5, 30.0)
			if resolutionScore > 0 {
				score += resolutionScore
			}
		}
	}

	// File size (0-15 points): log2(megabytes) × 3 with diminishing returns.
	if meta.Size > 0 {
		megabytes := float64(meta.Size) / (1024.0 * 1024.0)
		if megabytes > 0 {
			sizeScore := math.Min(math.Log2(megabytes)*3.0, 15.0)
			if sizeScore > 0 {
				score += sizeScore
			}
		}
	}

	// Date taken (0 or 20 points): EXIF date = almost certainly an original.
	if meta.DateTaken != "" {
		score += 20.0
	}

	// GPS coordinates (0 or 15 points): GPS = taken by a device with location services.
	if meta.GPSLat != 0 || meta.GPSLon != 0 {
		score += 15.0
	}

	// Camera information (0 or 5 points).
	if meta.Camera != "" {
		score += 5.0
	}

	// Lens information (0 or 5 points).
	if meta.Lens != "" {
		score += 5.0
	}

	// Title or description (0 or 10 points).
	if meta.Title != "" || meta.Description != "" {
		score += 10.0
	}

	return int(math.Max(0, math.Min(100, score)))
}

// =============================================================================
// ComputeImageQualityMetrics — Blockiness and blurring scores
// =============================================================================

// ComputeImageQualityMetrics computes two quality metrics for an image:
//
// Blockiness: Measures JPEG compression artifacts at 8-pixel block boundaries.
// Higher values = more visible JPEG artifacts.
//
// Blurring: Measures image sharpness via Laplacian variance. Higher = more blurry.
//
// Returns (blockiness, blurring). Both return 0.0 if the image can't be decoded.
func ComputeImageQualityMetrics(path string) (float64, float64) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer file.Close()

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
			gray[y][x] = float64(299*r+587*g+114*b) / 1000.0 / 256.0
		}
	}

	blockiness := computeBlockiness(gray, scaleW, scaleH)
	blurring := computeBlurring(gray, scaleW, scaleH)
	return blockiness, blurring
}

// computeBlockiness measures JPEG block boundary artifacts.
// Compares the average gradient at 8-pixel boundaries to the overall gradient.
func computeBlockiness(gray [][]float64, w, h int) float64 {
	var boundarySum, totalSum float64
	var boundaryCount, totalCount int

	for y := 1; y < h; y++ {
		for x := 1; x < w; x++ {
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
	return math.Max(0, (avgBoundary/avgTotal)-1.0) * 100.0
}

// computeBlurring estimates image sharpness via Laplacian variance.
// Lower variance = more blurry. We return the inverse so higher = more blurry.
func computeBlurring(gray [][]float64, w, h int) float64 {
	if w < 3 || h < 3 {
		return 0
	}

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

	if variance < 0.01 {
		return 100.0
	}
	blurScore := math.Max(0, math.Min(100, 100.0-variance*0.2))
	return math.Round(blurScore*100) / 100
}
