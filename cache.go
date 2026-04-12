// =============================================================================
// cache.go — Persistent hash cache for fast re-scans
// =============================================================================
//
// This file implements a disk-based cache for computed image hashes (xxHash
// and dHash). On the first scan, computing hashes for thousands of files takes
// many seconds. On re-scan, if files haven't changed (same size + modification
// time), we load their hashes from cache — zero I/O, zero decoding.
//
// The cache is stored using Go's "gob" binary encoding, which is much faster
// than JSON for serialising binary data like uint64 hashes.
//
// Cache location: ~/.dedup-photos/cache_<hash-of-scanpath>.gob
// Each scan path gets its own cache file so caches don't collide.
//
// Cache validity: each entry stores the file's size and modification time.
// If either changes, the entry is considered stale and re-computed.
//
// Version history:
//   v1 — initial release (xxHash + dHash)
//   v2 — added Width + Height fields to CachedEntry (enables aspect-ratio
//         pre-grouping without re-opening files on rescan)
// =============================================================================

package main

import (
	"crypto/sha256" // For hashing the scan path into a cache filename.
	"encoding/gob"  // Binary serialisation — fast and compact.
	"fmt"           // Formatted I/O.
	"os"            // File operations.
	"path/filepath" // Cross-platform path manipulation.
)

// HashCache provides persistent storage of computed hashes.
// On re-scan, files whose size and modification time haven't changed
// are served from cache — zero I/O, zero image decoding.
type HashCache struct {
	// Version allows future cache format changes without corruption.
	// If the version doesn't match, the cache is discarded.
	Version int

	// Entries maps absolute file path → cached hash data.
	Entries map[string]*CachedEntry
}

// CachedEntry stores the precomputed hashes for one file, along with the
// file metadata used to check if the cache entry is still valid.
//
// CHANGE IN v2: Width and Height are stored so that aspect-ratio pre-grouping
// (optimisation #4) works without re-opening files on subsequent scans.
type CachedEntry struct {
	Size    int64  // File size in bytes (from os.FileInfo).
	ModTime int64  // Modification time as Unix nanoseconds.
	XXHash  uint64 // xxHash64 of the raw file bytes.
	DHash   uint64 // Perceptual dHash of the image content.
	Width   int    // Image width in pixels (0 if unknown). Added in v2.
	Height  int    // Image height in pixels (0 if unknown). Added in v2.
}

// cacheVersion is incremented when the cache format changes.
// Old caches with a different version are discarded and rebuilt.
const cacheVersion = 2

// cachePath returns the path to the cache file for a given scan path.
// Format: ~/.dedup-photos/cache_<first-16-hex-chars-of-sha256>.gob
// Using a hash of the scan path ensures each directory gets its own cache
// without filesystem-unsafe characters in the filename.
func cachePath(scanPath string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	dir := filepath.Join(home, ".dedup-photos")
	os.MkdirAll(dir, 0755)
	h := sha256.Sum256([]byte(scanPath))
	return filepath.Join(dir, fmt.Sprintf("cache_%x.gob", h[:8]))
}

// LoadCache loads the hash cache from disk for the given scan path.
// Returns an empty, ready-to-use cache on any error (missing file,
// corrupt data, version mismatch, etc.).
func LoadCache(scanPath string) *HashCache {
	path := cachePath(scanPath)

	f, err := os.Open(path)
	if err != nil {
		return newEmptyCache()
	}
	defer f.Close()

	var cache HashCache
	decoder := gob.NewDecoder(f)
	if err := decoder.Decode(&cache); err != nil {
		fmt.Printf("[cache] Failed to decode cache file, starting fresh: %v\n", err)
		return newEmptyCache()
	}

	if cache.Version != cacheVersion {
		fmt.Printf("[cache] Cache version mismatch (got %d, want %d), starting fresh\n", cache.Version, cacheVersion)
		return newEmptyCache()
	}

	if cache.Entries == nil {
		cache.Entries = make(map[string]*CachedEntry)
	}

	fmt.Printf("[cache] Loaded %d entries from cache\n", len(cache.Entries))
	return &cache
}

// SaveCache writes the cache to disk atomically. It first writes to a
// temporary file, then renames it to the final path. This prevents
// corruption if the process is killed mid-write.
func SaveCache(cache *HashCache, scanPath string) error {
	path := cachePath(scanPath)

	tmpPath := path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to create cache temp file: %w", err)
	}

	encoder := gob.NewEncoder(f)
	if err := encoder.Encode(cache); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to encode cache: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to close cache temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename cache file: %w", err)
	}

	fmt.Printf("[cache] Saved %d entries to cache\n", len(cache.Entries))
	return nil
}

// =============================================================================
// LookupAll / StoreAll — Full cache API including image dimensions (v2)
// =============================================================================

// LookupAll checks if a cached entry exists and is still valid.
// Returns (xxHash, dHash, width, height, true) on a cache hit.
// Returns (0, 0, 0, 0, false) on a miss or stale entry.
//
// Width/Height are 0 if the entry was written before v2 or if dimensions
// couldn't be determined at hash time; callers must handle the zero case.
func (c *HashCache) LookupAll(path string, info os.FileInfo) (uint64, uint64, int, int, bool) {
	entry, ok := c.Entries[path]
	if !ok {
		return 0, 0, 0, 0, false
	}
	// Invalidate the entry if the file was modified since caching.
	if entry.Size != info.Size() || entry.ModTime != info.ModTime().UnixNano() {
		return 0, 0, 0, 0, false
	}
	return entry.XXHash, entry.DHash, entry.Width, entry.Height, true
}

// StoreAll adds or updates a cache entry, including image dimensions.
// Call this instead of Store whenever width/height are known.
func (c *HashCache) StoreAll(path string, info os.FileInfo, xxHash, dHash uint64, width, height int) {
	c.Entries[path] = &CachedEntry{
		Size:    info.Size(),
		ModTime: info.ModTime().UnixNano(),
		XXHash:  xxHash,
		DHash:   dHash,
		Width:   width,
		Height:  height,
	}
}

// =============================================================================
// Lookup / Store — Backwards-compatible wrappers (width/height default to 0)
// =============================================================================

// Lookup checks if a cached entry exists and is still valid.
// Returns (xxHash, dHash, true) on a cache hit; (0, 0, false) on a miss.
// Use LookupAll to also retrieve cached image dimensions.
func (c *HashCache) Lookup(path string, info os.FileInfo) (uint64, uint64, bool) {
	xxh, dh, _, _, ok := c.LookupAll(path, info)
	return xxh, dh, ok
}

// Store adds or updates a cache entry. Width and Height are not stored (0).
// Use StoreAll when image dimensions are available.
func (c *HashCache) Store(path string, info os.FileInfo, xxHash, dHash uint64) {
	c.StoreAll(path, info, xxHash, dHash, 0, 0)
}

// newEmptyCache creates a new, empty cache with the current version.
func newEmptyCache() *HashCache {
	return &HashCache{
		Version: cacheVersion,
		Entries: make(map[string]*CachedEntry),
	}
}
