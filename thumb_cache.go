// =============================================================================
// thumb_cache.go — Persistent on-disk thumbnail cache
// =============================================================================
//
// Decoding a HEIC thumbnail is the single most expensive step of a scan (a WASM
// HEVC decode). The perceptual-hash phase already decodes every HEIC thumbnail,
// so it JPEG-encodes it once and persists it here (see computeDHashHEIC), and
// the UI then loads the ready-made JPEG instead of decoding again — across app
// restarts too.
//
// Layout: ~/.dedup-photos/thumbs/thumb_<hash>.jpg, where <hash> is the first 16
// hex chars of sha256(absolute path). Exactly ONE file exists per source image
// and is overwritten in place when the source changes. The source's size and
// modification time ride in a fixed 16-byte header in front of the JPEG rather
// than in the filename, so a stale entry is detected on read and replaced on the
// next write — there is never anything to hunt down and delete.
//
// The previous scheme put size and modtime in the filename, which made a changed
// file leave an orphan behind, so every write ran a filepath.Glob over the whole
// directory to clean up: 275 µs at 317 cached files, growing linearly, and now
// paid once per HEIC during a scan. pruneLegacyThumbs clears out its leftovers.
// =============================================================================

package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// thumbCacheStampSize is the length of the validation header written in front of
// every cached thumbnail: 8 bytes of source file size, then 8 bytes of source
// modification time in Unix nanoseconds, both little-endian.
const thumbCacheStampSize = 16

// Memoised so MkdirAll runs once per process, not on every read and write.
var (
	thumbDirOnce sync.Once
	thumbDirPath string
)

// thumbCacheDir returns (and on first call creates) the thumbnail cache
// directory, and clears out entries left by the previous naming scheme.
func thumbCacheDir() string {
	thumbDirOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		thumbDirPath = filepath.Join(home, ".dedup-photos", "thumbs")
		os.MkdirAll(thumbDirPath, 0755)
		pruneLegacyThumbs(thumbDirPath)
	})
	return thumbDirPath
}

// pruneLegacyThumbs deletes cache files written by the old
// thumb_<hash>_<size>_<modtime>.jpg scheme, which can never match a current
// lookup and would otherwise sit in the directory forever. Best-effort.
func pruneLegacyThumbs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		// Current names are thumb_<hash>.jpg — exactly one underscore. Legacy
		// names carry the size and modtime as two further underscore-separated
		// fields.
		if strings.HasPrefix(name, "thumb_") && strings.Count(name, "_") > 1 {
			os.Remove(filepath.Join(dir, name))
		}
	}
}

// thumbNameFor builds the cache filename for a source path. It depends only on
// the path, so a changed source file reuses (and overwrites) the same entry.
func thumbNameFor(path string) string {
	h := sha256.Sum256([]byte(path))
	return fmt.Sprintf("thumb_%x.jpg", h[:8])
}

// loadThumbCache returns the cached JPEG thumbnail bytes for path, if an entry
// exists whose stamp still matches the source file's size and modification time.
func loadThumbCache(path string) ([]byte, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(thumbCacheDir(), thumbNameFor(path)))
	if err != nil || len(data) <= thumbCacheStampSize {
		return nil, false
	}
	// Stale entry: the source file changed since this thumbnail was written.
	// Leave it in place — the next storeThumbCache overwrites it.
	size := int64(binary.LittleEndian.Uint64(data[0:8]))
	modTime := int64(binary.LittleEndian.Uint64(data[8:16]))
	if size != info.Size() || modTime != info.ModTime().UnixNano() {
		return nil, false
	}
	return data[thumbCacheStampSize:], true
}

// storeThumbCache writes JPEG bytes to the cache for path, stamped with the
// source file's current size and modification time. Best-effort: errors are
// ignored so a cache-write failure never breaks a scan or a thumbnail request.
func storeThumbCache(path string, jpegBytes []byte) {
	info, err := os.Stat(path)
	if err != nil || len(jpegBytes) == 0 {
		return
	}
	dir := thumbCacheDir()

	buf := make([]byte, thumbCacheStampSize+len(jpegBytes))
	binary.LittleEndian.PutUint64(buf[0:8], uint64(info.Size()))
	binary.LittleEndian.PutUint64(buf[8:16], uint64(info.ModTime().UnixNano()))
	copy(buf[thumbCacheStampSize:], jpegBytes)

	// Write to a unique temp file and rename over the target, so a concurrent
	// reader never sees a half-written entry. CreateTemp rather than a fixed
	// "<name>.tmp" so two goroutines storing the same path cannot collide.
	tmp, err := os.CreateTemp(dir, "thumb-*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, filepath.Join(dir, thumbNameFor(path))); err != nil {
		os.Remove(tmpName)
	}
}

// deleteThumbCache removes the cache entry for a source path. Called when the
// user deletes a file so its thumbnail doesn't linger on disk.
func deleteThumbCache(path string) {
	os.Remove(filepath.Join(thumbCacheDir(), thumbNameFor(path)))
}
