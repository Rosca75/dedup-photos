// =============================================================================
// cache.go — Persistent hash cache for fast re-scans
// =============================================================================
//
// This file implements a disk-based cache for computed image hashes (xxHash
// and dHash). On the first scan, computing hashes for 4,450 files takes
// minutes. On re-scan, if files haven't changed (same size + modification
// time), we load their hashes from cache — zero I/O, zero decoding.
//
// The cache is stored using Go's "gob" binary encoding, which is much faster
// than JSON for serializing binary data like uint64 hashes.
//
// Cache location: ~/.dedup-photos/cache_<hash-of-scanpath>.gob
// Each scan path gets its own cache file so caches don't collide.
//
// Cache validity: each entry stores the file's size and modification time.
// If either changes, the entry is considered stale and re-computed.
// =============================================================================

package main

import (
	"crypto/sha256" // For hashing the scan path into a cache filename.
	"encoding/gob"  // Binary serialization — fast and compact.
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
type CachedEntry struct {
	Size    int64  // File size in bytes (from os.FileInfo).
	ModTime int64  // Modification time as Unix nanoseconds.
	XXHash  uint64 // xxHash64 of the raw file bytes.
	DHash   uint64 // Perceptual dHash of the image content.
}

// cacheVersion is incremented when the cache format changes.
// Old caches with a different version are discarded and rebuilt.
const cacheVersion = 1

// cachePath returns the path to the cache file for a given scan path.
// Format: ~/.dedup-photos/cache_<first-16-hex-chars-of-sha256>.gob
// Using a hash of the scan path ensures each directory gets its own cache
// without filesystem-unsafe characters in the filename.
func cachePath(scanPath string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory if home can't be determined.
		home = "."
	}
	dir := filepath.Join(home, ".dedup-photos")
	// Create the cache directory if it doesn't exist (ignore errors —
	// if we can't create it, SaveCache will fail gracefully).
	os.MkdirAll(dir, 0755)
	// Hash the scan path so the filename is always filesystem-safe.
	h := sha256.Sum256([]byte(scanPath))
	return filepath.Join(dir, fmt.Sprintf("cache_%x.gob", h[:8]))
}

// LoadCache loads the hash cache from disk for the given scan path.
// Returns an empty, ready-to-use cache on any error (missing file,
// corrupt data, version mismatch, etc.). This makes it safe to always
// call LoadCache without checking errors — worst case is a cache miss.
func LoadCache(scanPath string) *HashCache {
	path := cachePath(scanPath)

	f, err := os.Open(path)
	if err != nil {
		// File doesn't exist yet (first scan) — return empty cache.
		return newEmptyCache()
	}
	defer f.Close()

	var cache HashCache
	decoder := gob.NewDecoder(f)
	if err := decoder.Decode(&cache); err != nil {
		// Corrupt or unreadable cache — start fresh.
		fmt.Printf("[cache] Failed to decode cache file, starting fresh: %v\n", err)
		return newEmptyCache()
	}

	// Check version — discard if format has changed.
	if cache.Version != cacheVersion {
		fmt.Printf("[cache] Cache version mismatch (got %d, want %d), starting fresh\n", cache.Version, cacheVersion)
		return newEmptyCache()
	}

	// Ensure Entries map is not nil (defensive).
	if cache.Entries == nil {
		cache.Entries = make(map[string]*CachedEntry)
	}

	fmt.Printf("[cache] Loaded %d entries from cache\n", len(cache.Entries))
	return &cache
}

// SaveCache writes the cache to disk atomically. It first writes to a
// temporary file, then renames it to the final path. This prevents
// corruption if the process is killed mid-write (the old cache remains
// intact until the rename succeeds).
func SaveCache(cache *HashCache, scanPath string) error {
	path := cachePath(scanPath)

	// Write to a temporary file in the same directory (so rename is atomic
	// on the same filesystem).
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

	// Atomic rename — replaces old cache file in one operation.
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename cache file: %w", err)
	}

	fmt.Printf("[cache] Saved %d entries to cache\n", len(cache.Entries))
	return nil
}

// Lookup checks if a cached entry exists and is still valid for the given file.
// An entry is valid if the file's size and modification time match what was
// stored when the hash was computed. Returns (xxHash, dHash, true) on hit,
// or (0, 0, false) on miss.
func (c *HashCache) Lookup(path string, info os.FileInfo) (uint64, uint64, bool) {
	entry, ok := c.Entries[path]
	if !ok {
		return 0, 0, false // Not in cache at all.
	}
	// Check if file has been modified since the cache entry was created.
	if entry.Size != info.Size() || entry.ModTime != info.ModTime().UnixNano() {
		return 0, 0, false // File changed — cache entry is stale.
	}
	return entry.XXHash, entry.DHash, true
}

// Store adds or updates a cache entry for the given file.
func (c *HashCache) Store(path string, info os.FileInfo, xxHash, dHash uint64) {
	c.Entries[path] = &CachedEntry{
		Size:    info.Size(),
		ModTime: info.ModTime().UnixNano(),
		XXHash:  xxHash,
		DHash:   dHash,
	}
}

// newEmptyCache creates a new, empty cache with the current version.
func newEmptyCache() *HashCache {
	return &HashCache{
		Version: cacheVersion,
		Entries: make(map[string]*CachedEntry),
	}
}
