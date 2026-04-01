# HEIC Decommission & Hasher Cleanup

> **Status:** Tasks 0, 1, and 2 from the original plan have been implemented and merged.
> This file replaces the original improvement plan with two new tasks:
> decommissioning HEIC support entirely and cleaning up the hasher.

---

## Background

HEIC support was added in the previous round (Task 1), but testing with real iPhone 15 files
revealed a fundamental limitation: modern iPhones embed their thumbnail as **HEVC-encoded
data**, not JPEG. The pure-Go `heif` sub-package can parse the container and extract metadata
(dimensions, camera, date), but **cannot decode HEVC pixels** — that requires CGo with
libde265, which causes GCC/MinGW build nightmares on Windows that could not be resolved.

Without thumbnails, the user cannot visually compare duplicates and decide which to keep or
delete — which is the core purpose of dedup-photos. Keeping HEIC in the supported list is
misleading: files are scanned and matched (via xxHash), but the user experience is broken.

**Decision:** Remove HEIC/HEIF from the supported formats. A future dedicated project may
handle HEIC+JPG deduplication using a different technology stack better suited to HEVC
decoding.

---

## Task 1 — Decommission HEIC support

### 1.1 Files to delete

**`heic.go`** — Delete the entire file. It contains `extractHEICMeta()`,
`extractHEICThumb()`, and `isHEICPath()`. All callers will be updated to remove references.

### 1.2 Go dependency to remove

Remove `github.com/jdeng/goheif` from `go.mod`. After deleting `heic.go` (the only file
that imports it), run:

```bash
go mod tidy
```

This will remove the `goheif` line from `go.mod` and clean up `go.sum`.

### 1.3 Backend changes

#### `scanner.go` — Remove HEIC from supported extensions

In `supportedExtensions` map (around line 46), delete these two entries:

```go
".heic": true,
".heif": true,
```

Also update the comment block above the map (around line 42–44) that mentions `.heic, .heif`.
Remove the lines about Apple's modern photo format.

#### `app.go` — Remove HEIC fallback in `GetThumbnail`

In `GetThumbnail` (around line 285–295), remove the entire `isHEICPath(path)` block:

```go
// DELETE this block:
if isHEICPath(path) {
    if _, _, _, _, thumbB64, heicErr := extractHEICMeta(path); heicErr == nil && thumbB64 != "" {
        raw := strings.TrimPrefix(thumbB64, "data:image/jpeg;base64,")
        if decoded, decErr := base64.StdEncoding.DecodeString(raw); decErr == nil {
            thumbnailCache.Store(path, decoded)
            return raw
        }
    }
}
```

The remaining fallback (`extractEmbeddedJPEG`) is still needed for DNG/ARW/CR2 RAW formats.

#### `metadata.go` — Remove HEIC fallback in `ExtractMetadata`

In `ExtractMetadata` (around line 142–155), remove the `else if isHEICPath(path)` block.
The `else` clause that logs a warning for unsupported formats should remain (or become the
only `else`):

```go
if decErr == nil {
    meta.Width = config.Width
    meta.Height = config.Height
} else {
    // DELETE the isHEICPath block, keep only the warning:
    fmt.Printf("[metadata] Warning: cannot decode dimensions for %s: %v\n", path, decErr)
}
```

#### `hasher.go` — Remove HEIC fast path in `computeDHashSmart`

In `computeDHashSmart` (around line 248–277), remove the entire "Fast path 2: HEIC embedded
thumbnail" block including the temp file logic:

```go
// DELETE this entire block:
if len(data) > 8 && string(data[4:8]) == "ftyp" {
    tmpFile, tmpErr := os.CreateTemp("", "dedup-heic-*.heic")
    // ... all the way to ...
    os.Remove(tmpPath)
}
```

After removing this block, the `encoding/base64` and `strings` imports in `hasher.go` are
no longer used (they were only needed for the HEIC thumbnail decoding). **Remove both imports**
from the import block (lines 33 and 39). Also remove the `os` import if it's only used for
`os.CreateTemp` and `os.Remove` in the deleted block — but check first, `os` is likely used
elsewhere in hasher.go (e.g. `os.ReadFile`, `os.Stat`).

Also update the comment on `computeDHashSmart` and `ComputeDHashFromEXIFThumbnail` that
mention "JPEG/HEIC files" — change to just "JPEG files".

#### `server.go` — Update comment on `extractEmbeddedJPEG`

The comment on `extractEmbeddedJPEG` (around line 229–233) mentions "HEIC, HEIF" in the
list of camera formats. Remove the HEIC/HEIF mentions:

```go
// Before:
// Many camera formats (HEIC, HEIF, ARW, DNG, CR2) contain a full-size JPEG

// After:
// Many camera RAW formats (ARW, DNG, CR2) contain a full-size JPEG
```

### 1.4 Frontend changes

#### `static/index.html` — Remove HEIC checkboxes

There are two HEIC checkbox instances:

**Line 57** (scan parameters, extension grid in the topbar):
```html
<!-- DELETE this line: -->
<label><input type="checkbox" value="heic" checked> HEIC</label>
```

**Line 163** (filter panel, extension grid):
```html
<!-- DELETE this line: -->
<label><input type="checkbox" value="heic" checked> HEIC</label>
```

### 1.5 Documentation changes

#### `CLAUDE.md`

Scan for any mention of HEIC support. As of the current version there are none, but after
these changes, add a note in the "Project Overview" or a suitable section:

> **Unsupported formats:** HEIC/HEIF files are not supported. Go has no pure-Go HEVC decoder,
> and CGo-based solutions (libde265) cause build issues on Windows. HEIC files are skipped
> during scanning.

#### `README.md`

If the README lists supported formats, ensure HEIC is not mentioned. If it doesn't list
formats explicitly, no change needed (current README has no HEIC references).

### 1.6 Acceptance criteria

- [ ] `heic.go` is deleted
- [ ] `github.com/jdeng/goheif` is removed from `go.mod` and `go.sum` (via `go mod tidy`)
- [ ] `.heic` and `.heif` are removed from `supportedExtensions` in `scanner.go`
- [ ] The `isHEICPath` block is removed from `GetThumbnail` in `app.go`
- [ ] The `isHEICPath` block is removed from `ExtractMetadata` in `metadata.go`
- [ ] The HEIC fast path (ftyp + temp file) is removed from `computeDHashSmart` in `hasher.go`
- [ ] Unused imports (`encoding/base64`, `strings`) are removed from `hasher.go`
- [ ] HEIC checkboxes are removed from both extension grids in `static/index.html`
- [ ] Comments mentioning HEIC are updated or removed in `scanner.go`, `hasher.go`, `server.go`
- [ ] `CLAUDE.md` notes that HEIC is unsupported and why
- [ ] `go build ./...` compiles without errors
- [ ] No references to `heic`, `heif`, `goheif`, `isHEICPath`, or `extractHEICMeta` remain
  in any `.go`, `.js`, or `.html` file

---

## Task 2 — Clean up `computeDHashSmart` (minor, optional)

### 2.1 Context

With the HEIC block removed from `computeDHashSmart`, the function becomes clean again:
EXIF thumbnail fast path → full image decode slow path. No further structural changes are
needed.

However, one small improvement remains from the earlier performance analysis: the function
still calls `exif.Decode(bytes.NewReader(data))` on every file regardless of format. For
formats that never have EXIF (e.g. PNG, BMP), this is a wasted ~0.1ms. This is negligible
and purely cosmetic — implement only if convenient.

### 2.2 Acceptance criteria

- [ ] `computeDHashSmart` has no dead code, no orphan imports, and clear comments
- [ ] Non-HEIC files continue to work exactly as before

---

## Implementation order

1. **Task 1** — HEIC decommission (the main change).
2. **Task 2** — optional hasher cleanup.

After Task 1, run `wails build -platform windows/amd64` to verify the full build.
