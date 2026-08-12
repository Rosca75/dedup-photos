// =============================================================================
// thumb_cache.go — Persistent on-disk thumbnail cache
// =============================================================================
//
// Decoding a HEIC thumbnail is the single most expensive step of a scan (a WASM
// HEVC decode). During the perceptual-hash phase we already decode every HEIC
// thumbnail, so we JPEG-encode it once and persist it here. Afterwards the UI
// (GetThumbnail) and any app restart can load the ready-made JPEG straight from
// disk instead of decoding the HEIC again — halving decode work across the
// scan-then-review workflow.
//
// Layout: ~/.dedup-photos/thumbs/thumb_<hash>_<size>_<modtime>.jpg
//   - <hash>    : first 16 hex chars of sha256(absolute path) — filesystem-safe.
//   - <size>    : source file size in bytes.
//   - <modtime> : source file modification time (Unix nanoseconds).
//
// Encoding the size + modtime into the filename gives free cache invalidation:
// if the source file changes, the computed filename changes and the old entry
// simply never matches. On write we first delete any prior entry for the same
// path hash, so at most one thumbnail file exists per source image.
// =============================================================================

package main

import (
	"crypto/sha256"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"time" // time.Now/time.Since — measure the HEIC thumb-store cost (perf Trace 2).
)

// thumbCacheDir returns (and lazily creates) the thumbnail cache directory.
func thumbCacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	dir := filepath.Join(home, ".dedup-photos", "thumbs")
	os.MkdirAll(dir, 0755)
	return dir
}

// thumbHashPrefix returns the "thumb_<hash>_" filename prefix for a path.
// All cache entries for the same source path share this prefix regardless of
// the file's current size/modtime, so we can find and delete stale entries.
func thumbHashPrefix(path string) string {
	h := sha256.Sum256([]byte(path))
	return fmt.Sprintf("thumb_%x_", h[:8])
}

// thumbNameFor builds the full cache filename for a path + its file info.
func thumbNameFor(path string, info os.FileInfo) string {
	return fmt.Sprintf("%s%d_%d.jpg", thumbHashPrefix(path), info.Size(), info.ModTime().UnixNano())
}

// loadThumbCache returns the cached JPEG thumbnail bytes for path, if a valid
// (matching size + modtime) entry exists on disk.
func loadThumbCache(path string) ([]byte, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	name := filepath.Join(thumbCacheDir(), thumbNameFor(path, info))
	data, err := os.ReadFile(name)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

// storeThumbCache writes JPEG bytes to the cache for path, first removing any
// stale entry for the same source path. Best-effort: errors are ignored so a
// cache-write failure never breaks a scan or a thumbnail request.
func storeThumbCache(path string, jpegBytes []byte) {
	info, err := os.Stat(path)
	if err != nil || len(jpegBytes) == 0 {
		return
	}
	dir := thumbCacheDir()

	// Remove any prior entry for this path (different size/modtime) so the
	// cache holds at most one thumbnail per source file.
	if matches, _ := filepath.Glob(filepath.Join(dir, thumbHashPrefix(path)+"*.jpg")); matches != nil {
		for _, m := range matches {
			os.Remove(m)
		}
	}

	// Write atomically: temp file then rename, so a concurrent reader never
	// sees a half-written JPEG.
	final := filepath.Join(dir, thumbNameFor(path, info))
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, jpegBytes, 0644); err != nil {
		return
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
	}
}

// storeHEICThumbCache JPEG-encodes a decoded HEIC image (max 400px, quality 85 —
// identical to heicThumbnailJPEG so the UI reuses byte-for-byte the same output)
// and persists it. Called from the hash phase after a HEIC thumbnail is decoded.
func storeHEICThumbCache(path string, img image.Image) {
	if img == nil {
		return
	}
	// Perf tracing (Trace 2): time the encode + write so Phase 3b can report how
	// much of its wall time is spent persisting HEIC thumbnails vs. decoding.
	// phase3bThumbStoreNs is a package-level atomic declared in hasher_pipeline.go.
	start := time.Now()
	defer func() { phase3bThumbStoreNs.Add(int64(time.Since(start))) }()

	if jpegBytes := resizeImageToJPEG(img, 400, 85); jpegBytes != nil {
		storeThumbCache(path, jpegBytes)
	}
}

// deleteThumbCache removes all cache entries for a source path. Called when the
// user deletes a file so its stale thumbnail doesn't linger on disk.
func deleteThumbCache(path string) {
	if matches, _ := filepath.Glob(filepath.Join(thumbCacheDir(), thumbHashPrefix(path)+"*.jpg")); matches != nil {
		for _, m := range matches {
			os.Remove(m)
		}
	}
}
