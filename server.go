// =============================================================================
// server.go — Shared types, global state, and utility functions.
// =============================================================================
//
// This file contains:
//   - Type definitions shared between the Go backend and JS frontend
//     (serialised via JSON struct tags).
//   - Global state variables protected by mutexes (scan result, thumbnail
//     cache, cancel function, undo/redo stacks).
//   - ScanDirectoryFiltered: walks a directory tree with optional filters.
//   - extractEmbeddedJPEG: extracts embedded JPEG previews from camera files.
//
// All HTTP handler code has been removed — the app now uses Wails v2 bindings
// (see app.go). Types remain here because scanner.go, hasher.go, grouper.go,
// and metadata.go all reference them.
// =============================================================================

package main

import (
	"bytes"         // bytes.NewReader for JPEG validation in extractEmbeddedJPEG.
	"context"       // context.CancelFunc type for scan cancellation.
	"image"         // image.DecodeConfig for dimension filtering.
	"image/jpeg"    // jpeg.Decode for validating extracted JPEG segments.
	"os"            // File operations (Open, ReadFile, Stat, WalkDir).
	"path/filepath" // filepath.WalkDir, filepath.Abs, filepath.Ext.
	"sort"          // sort.Strings for deterministic path ordering.
	"strings"       // String helpers (HasPrefix, ToLower, TrimSpace).
	"sync"          // sync.Mutex for scanMutex; sync.Map for thumbnailCache.

	// Image format decoders — blank imports register decoders via init().
	// These must be imported somewhere in the binary to enable decoding.
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
	_ "image/gif"
	_ "image/png"
)

// =============================================================================
// Global state — protected by mutexes
// =============================================================================

// scanMutex protects scanResult and scanCancel from concurrent access.
// The background scan goroutine writes these; GetResults() reads them.
var scanMutex sync.Mutex

// scanResult holds the latest scan state (idle / scanning / complete).
// Written by the scan goroutine; read by GetResults().
var scanResult ScanResult

// thumbnailCache stores computed JPEG thumbnails keyed by file path.
// sync.Map is safe for concurrent reads and writes without a mutex.
var thumbnailCache sync.Map

// scanCancel is the cancel function for the currently running scan.
// Calling it stops the background scan goroutine via context cancellation.
var scanCancel context.CancelFunc

// actionMutex protects the undo/redo stacks below.
var actionMutex sync.Mutex

// undoStack records reversible delete operations (max 20 entries).
var undoStack []Action

// redoStack records undone actions so they can be re-applied.
var redoStack []Action

// maxUndoActions is the maximum number of actions kept in the undo stack.
const maxUndoActions = 20

// =============================================================================
// Types — JSON request/response structures (shared with JS via Wails)
// =============================================================================

// Action represents a reversible delete (soft-delete via rename to .deleted).
type Action struct {
	Type      string `json:"type"`       // "delete"
	Path      string `json:"path"`       // Original file path.
	TrashPath string `json:"trash_path"` // Renamed path (.deleted suffix).
	Timestamp int64  `json:"timestamp"`  // Unix milliseconds.
}

// ScanRequest is passed by the JS frontend when starting a scan.
// Wails automatically deserialises the JS object into this struct.
type ScanRequest struct {
	Path              string   `json:"path"`               // Primary directory to scan.
	Threshold         int      `json:"threshold"`           // Hamming distance threshold (0–100).
	Algorithm         string   `json:"algorithm"`           // "dhash", "phash", or "both".
	MinWidth          int      `json:"min_width"`           // Minimum image width in px (0=no limit).
	MaxHeight         int      `json:"max_height"`          // Maximum image height in px (0=no limit).
	Extensions        []string `json:"extensions"`          // e.g. ["jpg","png"] (empty=all).
	NormalisedSize    int      `json:"normalised_size"`     // Hash grid size: 16, 32, 64, or 128.
	IncludeSubfolders bool     `json:"include_subfolders"`  // Recurse into subdirectories.
	MinFileSize       int64    `json:"min_file_size"`       // Minimum file size in bytes.
	MaxFileSize       int64    `json:"max_file_size"`       // Maximum file size in bytes.
	ExtraPaths        []string `json:"extra_paths"`         // Additional directories to scan.
}

// BrowseRequest is the argument to the Browse method.
type BrowseRequest struct {
	Path string `json:"path"` // Directory to list (empty = home directory).
}

// BrowseEntry represents one subdirectory in a browse result.
type BrowseEntry struct {
	Name  string `json:"name"`   // Directory name (last segment only).
	Path  string `json:"path"`   // Full absolute path.
	IsDir bool   `json:"is_dir"` // Always true (we only return directories).
}

// BrowseResponse is returned by Browse().
type BrowseResponse struct {
	Current string        `json:"current"` // Absolute path currently being listed.
	Parent  string        `json:"parent"`  // Parent directory (empty if at root).
	Entries []BrowseEntry `json:"entries"` // Subdirectory entries.
}

// ReportMismatchRequest is the argument to ReportMismatch().
type ReportMismatchRequest struct {
	GroupID string `json:"group_id"` // UUID of the duplicate group.
}

// DeleteRequest is the argument to DeleteFile().
type DeleteRequest struct {
	Path string `json:"path"` // Absolute path of the file to delete.
}

// ScanProgress reports how far along the current scan is.
type ScanProgress struct {
	Current int    `json:"current"` // Items processed so far.
	Total   int    `json:"total"`   // Total items to process.
	Phase   string `json:"phase"`   // Human-readable current activity.
}

// ScanStats contains summary statistics for a completed scan.
type ScanStats struct {
	TotalFiles      int   `json:"total_files"`      // Image files found.
	DuplicateGroups int   `json:"duplicate_groups"` // Duplicate groups detected.
	WastedBytes     int64 `json:"wasted_bytes"`     // Bytes that could be freed.
	DurationMs      int64 `json:"duration_ms"`      // Scan duration in milliseconds.
}

// ScanResult is the full response returned by GetResults().
// Status is "idle", "scanning", "complete", or "cancelled".
type ScanResult struct {
	Status   string           `json:"status"`
	Progress ScanProgress     `json:"progress"`
	Stats    ScanStats        `json:"stats"`
	Groups   []DuplicateGroup `json:"groups"`
}

// =============================================================================
// ScanDirectoryFiltered — Walk a directory tree with optional filters
// =============================================================================

// ScanDirectoryFiltered walks rootPath and returns image file paths that match
// all active filters. Used by both the primary path and extra paths in StartScan.
//
// Parameters:
//   - rootPath:          Directory to walk.
//   - allowedExts:       Extension filter map (empty = all supported types).
//   - minWidth:          Minimum image width in pixels (0 = no limit).
//   - maxHeight:         Maximum image height in pixels (0 = no limit).
//   - includeSubfolders: Whether to recurse into subdirectories.
//   - minFileSize:       Minimum file size in bytes (0 = no limit).
//   - maxFileSize:       Maximum file size in bytes (0 = no limit).
func ScanDirectoryFiltered(rootPath string, allowedExts map[string]bool, minWidth, maxHeight int, includeSubfolders bool, minFileSize, maxFileSize int64) ([]string, error) {
	var imagePaths []string

	err := filepath.WalkDir(rootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip unreadable entries.
		}
		// Skip hidden directories (e.g. .thumbnails, .DS_Store).
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if !includeSubfolders && path != rootPath {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))

		// Extension filter: check against allowedExts, or all supported types.
		if len(allowedExts) > 0 {
			if !allowedExts[ext] {
				return nil
			}
		} else {
			if !supportedExtensions[ext] {
				return nil
			}
		}

		absPath, err2 := filepath.Abs(path)
		if err2 != nil {
			absPath = path
		}

		// File size filter.
		if minFileSize > 0 || maxFileSize > 0 {
			if info, err3 := d.Info(); err3 == nil {
				if minFileSize > 0 && info.Size() < minFileSize {
					return nil
				}
				if maxFileSize > 0 && info.Size() > maxFileSize {
					return nil
				}
			}
		}

		// Dimension filter — opens the file to read image config (no full decode).
		if minWidth > 0 || maxHeight > 0 {
			f, err3 := os.Open(absPath)
			if err3 != nil {
				return nil
			}
			cfg, _, err3 := image.DecodeConfig(f)
			f.Close()
			if err3 == nil {
				if minWidth > 0 && cfg.Width < minWidth {
					return nil
				}
				if maxHeight > 0 && cfg.Height > maxHeight {
					return nil
				}
			}
			// If we can't read dimensions, include the file anyway.
		}

		imagePaths = append(imagePaths, absPath)
		return nil
	})

	if err != nil {
		return nil, err
	}

	sort.Strings(imagePaths) // Deterministic order for reproducible grouping.
	return imagePaths, nil
}

// =============================================================================
// extractEmbeddedJPEG — Extract JPEG preview from camera RAW / HEIC files
// =============================================================================

// extractEmbeddedJPEG scans filePath for embedded JPEG data (SOI/EOI markers).
// Many camera formats (HEIC, HEIF, ARW, DNG, CR2) contain a full-size JPEG
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
