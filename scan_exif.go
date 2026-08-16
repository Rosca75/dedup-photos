// =============================================================================
// scan_exif.go — EXIF carried forward from the hash phase
// =============================================================================
//
// WHY THIS EXISTS (docs/05-GROUPING-METADATA-REUSE.md)
//
// Grouping used to be ~99% metadata I/O. The BK-Tree and cluster refinement
// finish in well under a second; everything else was parallelExtractMetadata
// re-opening every file that landed in a group and letting bep/imagemeta seek
// around the container for EXIF. Measured over SMB that is ~253 ms per file,
// and it scales with how many files the matcher groups — so the better the
// matching gets, the more it hurts.
//
// The bytes were already read once. The hash phase reads the first 128 KB of
// every file (heicHeaderReadSize) and, for HEIC, already hands that buffer to
// imagemeta for dimensions. Then it throws the buffer away. This file lets the
// hash phase parse EXIF out of the buffer it is holding — no extra file open,
// no extra byte read — and carry the result forward through ImageHash and the
// on-disk cache to the metadata phase.
//
// WHAT THIS IS NOT: it is NOT "extract EXIF for the whole library" by opening
// files. That was tried in plan 04 Step 1 and reverted, because opening 8,000
// files to serve the ~5% that reach a result cost 17.9 s of a 28 s scan. The
// distinction is file OPENS, not parsing: plan 04 removed the opens, and there
// are no new opens here. Parsing bytes already in memory costs 0.15 ms per file
// (measured, Config→Config+Exif on the same buffer).
//
// The retention trap is also avoided: 128 KB × 8,000 files would be 1 GB, so
// the raw buffers cannot be kept. Only the handful of parsed fields below are
// retained — roughly 100 bytes per file, about 1 MB for a 10,000-image library.
// =============================================================================

package main

import (
	"bytes"         // bytes.NewReader — wraps the header buffer as the io.ReadSeeker imagemeta needs.
	"path/filepath" // Extension sniffing to pick the imagemeta format hint.
	"strings"       // Lower-casing that extension.
)

// ScanExif is the flat, cacheable subset of ImageMetadata that comes from EXIF.
//
// Deliberately NOT ImageMetadata itself: that struct carries derived values —
// QualityScore above all — which must stay computed at group time from the
// stored inputs. Caching a score would mean a change to the scoring rules
// needed a cache wipe to take effect, silently.
//
// The field list is exactly what something downstream reads back out:
// ComputeQualityScore scores DateTaken (20 pts), GPS (15), Camera (5), Lens (5)
// and Description (10); ISO and FocalLength are shown in the UI's metadata grid.
// Dropping any of them would change QualityScore, and QualityScore decides which
// image is marked "best" — i.e. which file the user is offered to delete. Title
// is absent because extractExifInto never populates it.
type ScanExif struct {
	DateTaken   string  // ISO 8601, from DateTimeOriginal.
	Camera      string  // Make + Model, joined.
	Lens        string  // LensModel.
	FocalLength string  // e.g. "4.2mm".
	Description string  // ImageDescription.
	GPSLat      float64 // Signed decimal degrees (negative = South).
	GPSLon      float64 // Signed decimal degrees (negative = West).
	ISO         int     // ISO sensitivity.

	// OK reports that this EXIF was genuinely parsed, rather than being an
	// all-zero struct because nothing ran or the parse failed.
	//
	// This flag is the whole safety mechanism. EXIF absence is not an error —
	// extractExifInto simply leaves fields empty — so without an explicit
	// "yes, this was read" marker a file whose EXIF fell outside the header
	// window would cache blank fields, and the metadata phase would serve those
	// blanks instead of falling back to a real read. That would silently zero
	// the file's QualityScore and change which duplicate is recommended for
	// deletion. When OK is false the caller MUST fall back to reading the file.
	OK bool
}

// parseScanExifBuffer parses EXIF out of a buffer that is already in memory.
//
// It delegates to the same extractExifInto used by the file-handle path, with
// the only difference being the reader: a bytes.Reader over the header buffer
// instead of an *os.File. Sharing the function is what guarantees the two paths
// produce identical fields — a separate parser here could drift from the one
// the fallback uses, and the drift would show up as a changed "best image"
// rather than as an error.
//
// Returns OK=false when the buffer yielded no identifying EXIF, which is the
// signal to fall back. Measured over 4,265 real iPhone HEICs, 99.23% of files
// are fully recoverable from the standard 128 KB window, and the failures are
// clean all-or-nothing misses — no file produced partial or wrong values.
func parseScanExifBuffer(path string, buf []byte) ScanExif {
	if len(buf) == 0 {
		return ScanExif{}
	}
	format, ok := imageFormatForExt(strings.ToLower(filepath.Ext(path)))
	if !ok {
		return ScanExif{} // Not a format imagemeta can read EXIF from.
	}

	// extractExifInto fills an ImageMetadata; only the EXIF-derived fields of
	// that struct are kept. The rest (Path, Size, dimensions, scores) are the
	// caller's business and are never sourced from here.
	var meta ImageMetadata
	extractExifInto(bytes.NewReader(buf), format, &meta)

	return ScanExif{
		DateTaken:   meta.DateTaken,
		Camera:      meta.Camera,
		Lens:        meta.Lens,
		FocalLength: meta.FocalLength,
		Description: meta.Description,
		GPSLat:      meta.GPSLat,
		GPSLon:      meta.GPSLon,
		ISO:         meta.ISO,

		// A camera-produced file always carries at least a capture date or a
		// camera name. Requiring one of those — rather than "the parse did not
		// error" — keeps a truncated container that parsed to nothing from
		// being mistaken for a genuinely EXIF-less file. The cost of being
		// wrong in the conservative direction is one file re-read; the cost of
		// being wrong the other way is silently blank metadata.
		OK: meta.DateTaken != "" || meta.Camera != "",
	}
}

// applyTo copies the cached EXIF fields into a freshly built ImageMetadata.
//
// Only non-empty values are written, mirroring extractExifInto's own "do not
// overwrite what is already set" contract, so a caller that has filled a field
// from another source keeps it.
func (e ScanExif) applyTo(meta *ImageMetadata) {
	if meta.DateTaken == "" {
		meta.DateTaken = e.DateTaken
	}
	if meta.Camera == "" {
		meta.Camera = e.Camera
	}
	if meta.Lens == "" {
		meta.Lens = e.Lens
	}
	if meta.FocalLength == "" {
		meta.FocalLength = e.FocalLength
	}
	if meta.Description == "" {
		meta.Description = e.Description
	}
	if meta.GPSLat == 0 {
		meta.GPSLat = e.GPSLat
	}
	if meta.GPSLon == 0 {
		meta.GPSLon = e.GPSLon
	}
	if meta.ISO == 0 {
		meta.ISO = e.ISO
	}
}
