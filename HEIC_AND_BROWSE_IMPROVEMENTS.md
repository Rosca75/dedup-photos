# HEIC Thumbnail Fix & Native Folder Picker — Implementation Plan

> **Purpose:** This file contains three tasks for Claude Code.
> Implement them in order (Task 0 → 1 → 2). Each section describes the problem, the reference
> implementation (from the sibling project [HEIC_converter](https://github.com/Rosca75/HEIC_converter)
> where applicable), the exact changes required, and acceptance criteria.

---

## Shared constraint

**Do NOT introduce an ImageMagick dependency.** The HEIC_converter project uses ImageMagick
for full-image conversion, but dedup-photos must remain free of any external binary dependency.
Every solution below must be **pure Go** (no `os/exec`, no `magick` CLI calls).

---

## Task 0 — Fix outdated CLAUDE.md (must be done first)

### 0.1 Problem

`CLAUDE.md` is significantly out of date. It describes `frontend/` as the canonical frontend
directory and `static/` as legacy dead code. **The opposite is true:**

- `main.go` line 38 embeds `static/` via `//go:embed all:static`, not `frontend/`.
- The `frontend/` directory **does not exist** in the repo.
- The modular split described as "target (not yet implemented)" in Section 5 **has already
  been done** — in `static/`, not `frontend/`.
- Line counts and method signatures in the doc are stale.

This is dangerous because rule 7 ("**`static/` is dead code.** Never read, reference, or
modify the `static/` directory.") will cause Claude Code to refuse to touch the actual
active frontend.

### 0.2 Changes required

Apply the following corrections to `CLAUDE.md`:

#### Section 2 — Repository Structure

Replace the entire tree block and the note below it with:

```
dedup-photos/
├── main.go           84 lines   Wails app entry point (wails.Run)
├── app.go           456 lines   App struct — all public methods bound to the JS frontend
├── server.go        307 lines   Shared types, global scan state, ScanDirectoryFiltered
├── scanner.go       204 lines   Recursive filesystem walk
├── hasher.go        815 lines   xxHash (exact) + dHash/pHash (perceptual)
├── metadata.go      584 lines   EXIF extraction + quality scoring
├── grouper.go       694 lines   BK-Tree indexing + duplicate grouping
├── cache.go         180 lines   Persistent hash cache
├── wails.json               Wails config (name, version, author)
├── go.mod / go.sum
└── static/                  ← active frontend (embedded by main.go via //go:embed all:static)
    ├── index.html   205 lines
    ├── css/
    │   ├── base.css        73 lines    CSS variables, reset, typography
    │   ├── layout.css     409 lines    Grid layout + left panel
    │   ├── table.css      157 lines    Data table styles
    │   └── components.css 319 lines    Buttons, badges, toast, browse dialog
    └── js/
        ├── app.js          27 lines    Entry point — imports modules, wires init()
        ├── state.js        47 lines    Shared state object
        ├── api.js          78 lines    All window.go.main.App.* calls (isolation layer)
        ├── helpers.js      70 lines    Pure utility functions
        ├── components.js   59 lines    showToast(), showConfirm()
        ├── scan.js        150 lines    startScan(), pollResults(), cancelScan()
        ├── browse.js      229 lines    Folder browser dialog
        ├── sidebar.js      96 lines    Folder tree navigation
        ├── preview.js     103 lines    Left panel image preview
        ├── filters.js     136 lines    Result filters
        ├── resize.js       96 lines    Panel resize handles
        ├── table.js       507 lines    Results data table
        └── actions.js     183 lines    deleteFile(), reportMismatch(), batch ops
```

Remove the note that says `static/` is legacy and `frontend/` is canonical.

#### Section 3.1 — "How it works" step 1

Change:
> `main.go` embeds the `frontend/` directory with `//go:embed all:frontend`

To:
> `main.go` embeds the `static/` directory with `//go:embed all:static`

Change step 2:
> Wails opens a native Windows window and loads `frontend/index.html` inside it

To:
> Wails opens a native Windows window and loads `static/index.html` inside it

#### Section 4 — `main.go` description

Change:
> Embeds `frontend/` via `//go:embed all:frontend`.

To:
> Embeds `static/` via `//go:embed all:static`.

#### Section 4 — `app.go` method table

The current table has a wrong signature for `StartScan` (it lists individual params but the
actual code takes a `ScanRequest` struct). It also lists `Browse` which will be removed by
Task 2. Replace the table with:

| Method | Signature | Purpose |
|---|---|---|
| `StartScan` | `(req ScanRequest) map[string]string` | Start background scan |
| `GetResults` | `() ScanResult` | Poll scan progress and results |
| `CancelScan` | `() map[string]string` | Cancel active scan |
| `DeleteFile` | `(path string) map[string]interface{}` | Permanently delete a file |
| `GetThumbnail` | `(path string) string` | Returns base64 JPEG string |
| `OpenFolderDialog` | `() (string, error)` | Native OS folder picker (added by Task 2) |
| `ReportMismatch` | `(groupID string) string` | Returns JSON diagnostic report string |

Also update the `server.go` description below the table — remove `BrowseRequest` and
`BrowseResponse` from the type list (those are removed by Task 2).

#### Section 4 — Business logic files table

Update line counts and add `cache.go`:

| File | Lines | Purpose |
|---|---|---|
| `scanner.go` | 204 | Recursive filesystem walk |
| `hasher.go` | 815 | xxHash (exact) + dHash/pHash (perceptual) |
| `metadata.go` | 584 | EXIF extraction + quality scoring |
| `grouper.go` | 694 | BK-Tree indexing + duplicate grouping |
| `cache.go` | 180 | Persistent hash cache |

#### Section 5 — Frontend Architecture

The "Current state" subsection describes a single-file monolith (`frontend/app.js`, 660 lines).
This is outdated. Replace the entire "Current state" subsection with:

> The frontend has been split into ES modules under `static/js/` and `static/css/`.
> `static/index.html` loads `js/app.js` via `<script type="module" src="/js/app.js">`.
> CSS is split into 4 files loaded via `<link>` tags.
>
> **`api.js` is the isolation layer** — it wraps all `window.go.main.App.*` calls;
> no other module touches `window.go` directly.

The "Target modular structure (not yet implemented)" subsection should be **removed entirely**
or renamed to "Current modular structure" and updated to reflect the actual file list above.
The tree in that subsection references a non-existent `wails.js`, `settings.js`, and
`render.js` — remove those. The actual modules are listed in the Section 2 tree.

#### Section 8 — Key Development Rules

**Rule 6:** Change:
> Asset paths have no `/static/` prefix. Wails serves `frontend/` at root `/`. A file at `frontend/style.css` loads as `/style.css`.

To:
> Asset paths have no `/static/` prefix. Wails embeds `static/` and strips the prefix via `fs.Sub`. A file at `static/css/base.css` loads as `/css/base.css`.

**Rule 7:** Delete this rule entirely:
> `static/` is dead code. Never read, reference, or modify the `static/` directory.

Replace it with:
> `static/` is the active frontend directory. All frontend work happens here. There is no `frontend/` directory.

**Rule 9:** Change the "when split" parenthetical:
> Wails calls live in `wails.js` only (when split).

To:
> Wails calls live in `api.js` only. No other module calls `window.go.*` directly.

**Rule 10:** Update to reflect the actual protected files. `hasher.go` and `metadata.go` may
need modification for HEIC support (see Task 1), so the blanket "do not touch" rule should be
softened to:
> Avoid modifying business logic files (`scanner.go`, `hasher.go`, `metadata.go`, `grouper.go`, `cache.go`) unless the change is explicitly scoped and described in an improvement plan.

### 0.3 Acceptance criteria

- [ ] All references to `frontend/` in CLAUDE.md are replaced with `static/`
- [ ] The "dead code" warning about `static/` is removed
- [ ] The repository structure tree matches the actual file layout
- [ ] Section 5 reflects the already-completed modular split
- [ ] Rule 7 no longer tells Claude Code to ignore the active frontend
- [ ] Line counts are updated to reflect the current state (approximate is fine — they drift)

---

## Task 1 — Fix HEIC thumbnail preview (pure-Go HEIC thumbnail extraction)

### 1.1 Problem

When the user clicks a HEIC file in the results table, the left-panel preview shows nothing.

**Root cause chain:**

1. `GetThumbnail(path)` in `app.go:265` calls `image.Decode(f)` — this fails because Go has
   no registered HEIC decoder.
2. It falls back to `extractEmbeddedJPEG(path)` in `server.go:254`, which does a brute-force
   byte scan for JPEG SOI/EOI markers across the first 50 MB. This is slow, unreliable, and
   often misidentifies unrelated JPEG segments inside the HEIC container — or returns nothing
   at all.

The same problem silently affects:
- **Metadata extraction** (`metadata.go:136`): `image.DecodeConfig` fails for HEIC, so
  `width`/`height` are always 0.
- **Perceptual hashing** (`hasher.go:232–251`): `computeDHashSmart` tries `exif.Decode` on
  the raw HEIC bytes. The `rwcarlsen/goexif` library doesn't understand the HEIC container,
  so it fails to find the EXIF block. It then falls back to `image.Decode` which also fails.
  Result: dHash = 0 for all HEIC files (they still get an xxHash, so exact duplicates are
  found, but perceptual matching is broken).

### 1.2 Reference implementation (HEIC_converter)

The last 2 commits on HEIC_converter solved this with a **pure-Go HEIC parser** —
no ImageMagick involved. The key files are:

- **`converter/thumb.go`** — two functions:
  - `extractMetaFast(path)` → opens the HEIC file, parses it with `heif.Open(f)`, reads
    spatial extents (width/height), extracts EXIF bytes for camera model + date, and calls
    `extractEmbeddedThumb`.
  - `extractEmbeddedThumb(hf, primary)` → scans HEIC item IDs 1–50 for a `"thmb"` reference
    pointing to the primary item, reads item data, validates JPEG magic bytes (`FF D8 FF`),
    and returns a `data:image/jpeg;base64,...` string.

- **`converter/meta.go`** — `getOneFileMeta(p)` calls `extractMetaFast` first. If the
  embedded thumbnail is empty (non-JPEG thumbnail, e.g. HEVC-encoded), it falls back to
  `generateThumb(p)` which uses ImageMagick. **We must NOT port the ImageMagick fallback.**

- **Go dependency:** `github.com/jdeng/goheif` — specifically the `heif` sub-package
  (`github.com/jdeng/goheif/heif`). This is a pure-Go ISOBMFF (ISO Base Media File Format)
  parser. No CGo, no external binaries. It's already used in HEIC_converter's `go.mod`:
  ```
  github.com/jdeng/goheif v0.0.0-20260309214039-46ce8d592019
  ```

### 1.3 Changes required

#### 1.3.1 Add Go dependency

```bash
go get github.com/jdeng/goheif@latest
```

This adds `github.com/jdeng/goheif` to `go.mod`. Only the `heif` sub-package is used
(`github.com/jdeng/goheif/heif`).

#### 1.3.2 Create `heic.go` — HEIC-specific thumbnail and metadata extraction

Create a new file `heic.go` in the project root (same package `main`). Port the following
from HEIC_converter's `converter/thumb.go`:

```go
package main

import (
    "bytes"
    "encoding/base64"
    "os"
    "strings"
    "time"

    "github.com/jdeng/goheif/heif"
    "github.com/rwcarlsen/goexif/exif"
)
```

**Function 1: `extractHEICMeta(path string) (width, height int, camera, createdAt, thumbBase64 string, err error)`**

This is a direct port of `extractMetaFast` from `converter/thumb.go:18-51`. Logic:

1. `os.Open(path)`, defer close
2. `heif.Open(f)` to parse the HEIC container
3. `hf.PrimaryItem()` to get the primary image item
4. `primary.SpatialExtents()` for width/height
5. `hf.EXIF()` → `exif.Decode(bytes.NewReader(exifBytes))` for camera model and date
6. Call `extractHEICThumb(hf, primary)` for the thumbnail
7. Return all values

**Function 2: `extractHEICThumb(hf *heif.File, primary *heif.Item) string`**

This is a direct port of `extractEmbeddedThumb` from `converter/thumb.go:55-81`. Logic:

1. Loop item IDs 1–50
2. For each item, check `item.Reference("thmb")`
3. If a thmb reference points to the primary item's ID, read item data with `hf.GetItemData(item)`
4. Validate JPEG magic bytes (`data[0]==0xFF && data[1]==0xD8 && data[2]==0xFF`)
5. Return `"data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)`
6. Return `""` if no valid JPEG thumbnail found

**Important:** Do NOT port `generateThumb` or `parseVerboseInfo` — those use ImageMagick.

#### 1.3.3 Modify `GetThumbnail` in `app.go`

Current fallback chain (line 282–289):
```go
img, _, err := image.Decode(f)
if err != nil {
    if embedded := extractEmbeddedJPEG(path); embedded != nil {
        thumbnailCache.Store(path, embedded)
        return base64.StdEncoding.EncodeToString(embedded)
    }
    return ""
}
```

New fallback chain — insert HEIC-specific extraction **before** the generic `extractEmbeddedJPEG`:

```go
img, _, err := image.Decode(f)
if err != nil {
    // Try HEIC-specific container parsing first (pure Go, no external deps).
    ext := strings.ToLower(filepath.Ext(path))
    if ext == ".heic" || ext == ".heif" {
        if _, _, _, _, thumbB64, heicErr := extractHEICMeta(path); heicErr == nil && thumbB64 != "" {
            // extractHEICMeta returns a full data URI; strip the prefix to get raw base64.
            raw := strings.TrimPrefix(thumbB64, "data:image/jpeg;base64,")
            if decoded, decErr := base64.StdEncoding.DecodeString(raw); decErr == nil {
                thumbnailCache.Store(path, decoded)
                return raw
            }
        }
    }
    // Generic fallback for other RAW formats (DNG, ARW, CR2).
    if embedded := extractEmbeddedJPEG(path); embedded != nil {
        thumbnailCache.Store(path, embedded)
        return base64.StdEncoding.EncodeToString(embedded)
    }
    return ""
}
```

Add `"path/filepath"` to the imports of `app.go` if not already present (it is already imported).

#### 1.3.4 Improve HEIC metadata in `metadata.go`

In `ExtractMetadata` (around line 130), after the `image.DecodeConfig` attempt that fails for
HEIC, add a HEIC-specific fallback:

```go
if decErr == nil {
    meta.Width = config.Width
    meta.Height = config.Height
} else {
    // HEIC fallback: use the pure-Go HEIC parser for dimensions + EXIF.
    ext := strings.ToLower(filepath.Ext(path))
    if ext == ".heic" || ext == ".heif" {
        w, h, cam, date, _, heicErr := extractHEICMeta(path)
        if heicErr == nil && w > 0 {
            meta.Width = w
            meta.Height = h
            if cam != "" && cam != "unknown" {
                meta.Camera = cam
            }
            if date != "" {
                meta.DateTaken = date
            }
        }
    }
}
```

This fills in width, height, camera, and date for HEIC files that currently show 0×0.

#### 1.3.5 Improve HEIC perceptual hashing in `hasher.go`

In `computeDHashSmart` (line 232), after the EXIF thumbnail extraction fails, add a
HEIC-specific path **before** the slow full-decode fallback. The HEIC embedded thumbnail
is a valid JPEG — we can decode it and compute dHash on it:

```go
func computeDHashSmart(data []byte) (uint64, error) {
    // Fast path 1: EXIF thumbnail (works for JPEG).
    x, err := exif.Decode(bytes.NewReader(data))
    if err == nil {
        thumb, err := x.JpegThumbnail()
        if err == nil && len(thumb) > 0 {
            img, _, err := image.Decode(bytes.NewReader(thumb))
            if err == nil {
                return computeDHashFromImage(img), nil
            }
        }
    }

    // Fast path 2: HEIC embedded thumbnail (pure-Go container parsing).
    // Write data to a temp file for the HEIC parser (it needs an io.ReadSeeker).
    // Only attempt if the buffer starts with an ISOBMFF signature (ftyp box).
    if len(data) > 8 && string(data[4:8]) == "ftyp" {
        tmpFile, tmpErr := os.CreateTemp("", "dedup-heic-*.heic")
        if tmpErr == nil {
            tmpPath := tmpFile.Name()
            _, writeErr := tmpFile.Write(data)
            tmpFile.Close()
            if writeErr == nil {
                _, _, _, _, thumbB64, heicErr := extractHEICMeta(tmpPath)
                if heicErr == nil && thumbB64 != "" {
                    raw := strings.TrimPrefix(thumbB64, "data:image/jpeg;base64,")
                    if decoded, decErr := base64.StdEncoding.DecodeString(raw); decErr == nil {
                        if img, _, imgErr := image.Decode(bytes.NewReader(decoded)); imgErr == nil {
                            os.Remove(tmpPath)
                            return computeDHashFromImage(img), nil
                        }
                    }
                }
            }
            os.Remove(tmpPath)
        }
    }

    // Slow path: full image decode (PNG, BMP, edited JPEG without thumbnail).
    img, _, err := image.Decode(bytes.NewReader(data))
    if err != nil {
        return 0, fmt.Errorf("failed to decode image: %w", err)
    }
    return computeDHashFromImage(img), nil
}
```

Add `"encoding/base64"` and `"strings"` to hasher.go imports if not already present.

**Note on the temp file approach:** The `heif.Open()` function requires an `io.ReadSeeker`.
Since `computeDHashSmart` receives a `[]byte`, we need to write it to a temp file. This is
still much faster than a full HEIC decode because we're only reading the small embedded
thumbnail (~10-20KB), not decoding the full HEVC image. An alternative (better) approach
would be to refactor `extractHEICMeta` to accept an `io.ReadSeeker` instead of a file path —
that way you can wrap the `[]byte` in a `bytes.Reader` directly. This is left as an
implementation choice.

### 1.4 Acceptance criteria

- [ ] Clicking a `.heic` file in the results table shows a thumbnail in the left preview panel
- [ ] HEIC files show correct width × height in metadata (not 0×0)
- [ ] HEIC files show camera model and date taken when available
- [ ] HEIC perceptual hashing produces non-zero dHash values (enabling perceptual duplicate detection)
- [ ] `go build ./...` compiles without errors
- [ ] No `magick`, `convert`, `os/exec`, or ImageMagick references are introduced
- [ ] The `extractEmbeddedJPEG` fallback in `server.go` is preserved for DNG/ARW/CR2 files

---

## Task 2 — Replace custom folder browser with native OS dialog

### 2.1 Problem

The "Browse..." button opens a custom-built folder browser dialog (a hand-coded modal with a
directory listing, navigation, and selection chips). This has several UX issues:

- **Not intuitive:** Single-click selects, double-click navigates — this is non-obvious.
- **No familiar navigation:** Missing favorites, breadcrumbs, network locations, Quick Access.
- **Painful multi-folder:** The only way to add multiple folders is to navigate inside the
  modal, click items, and manage chips — it's confusing.
- **~270 lines of unnecessary code:** `browse.js` (230 lines) + `Browse()` Go method +
  types in `server.go`.

### 2.2 Reference implementation (HEIC_converter)

HEIC_converter uses **Wails' built-in native folder dialog** — `runtime.OpenDirectoryDialog`.
This opens the OS-native folder picker (Windows Explorer dialog / macOS Finder / GTK on Linux).

From `HEIC_converter/app.go:55-59`:
```go
func (a *App) OpenFolderDialog() (string, error) {
    return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
        Title: "Select folder containing HEIC files",
    })
}
```

From `HEIC_converter/static/app.js:47-49`:
```js
document.getElementById('btnPickFolder').addEventListener('click', async () => {
    const dir = await window.go.main.App.OpenFolderDialog();
    if (dir) addFilesToBundle([dir]);
});
```

That's it — one Go method, three lines of JS. The OS handles everything else.

### 2.3 Changes required

#### 2.3.1 Add `OpenFolderDialog` method to `app.go`

Add the Wails runtime import and a new method:

```go
import (
    // ... existing imports ...
    wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// OpenFolderDialog opens the native OS folder picker and returns the selected path.
// Returns an empty string if the user cancels the dialog.
func (a *App) OpenFolderDialog() (string, error) {
    return wailsRuntime.OpenDirectoryDialog(a.ctx, wailsRuntime.OpenDialogOptions{
        Title: "Select folder to scan",
    })
}
```

**Note:** Import Wails runtime with an alias (`wailsRuntime`) to avoid collision with the
standard `"runtime"` package if it's used elsewhere. Check existing imports first.

#### 2.3.2 Add `apiOpenFolderDialog` to `static/js/api.js`

```js
/**
 * Open the native OS folder picker dialog.
 * Returns a Promise<string> with the selected folder path, or empty if cancelled.
 */
export function apiOpenFolderDialog() {
  return GoApp().OpenFolderDialog();
}
```

#### 2.3.3 Rewrite `static/js/browse.js`

Replace the entire file content. The new version is much simpler — no custom modal, no
directory listing API, no navigation. Instead:

- Click "Browse..." → open native dialog → get one folder path back.
- Multiple folders are supported by clicking "Browse..." again — each click opens a new
  native dialog and appends the result.
- Selected folders are shown as removable chips directly in the topbar (reuse the existing
  `.browse-selected-chip` CSS class from `static/css/components.css`).
- The `scan-path` input field becomes a display-only list of selected paths (or the user can
  still type/paste a path manually and press Enter to add it).

**New `browse.js` design:**

```js
import { apiOpenFolderDialog } from './api.js';
import { showToast } from './components.js';

const selectedPaths = new Set();

export function initBrowse() {
    document.getElementById('browse-btn').addEventListener('click', openNativeDialog);

    // Allow manual path entry via the text input (press Enter to add).
    const input = document.getElementById('scan-path');
    input.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') {
            const p = input.value.trim();
            if (p) {
                addPath(p);
                input.value = '';
            }
        }
    });
}

async function openNativeDialog() {
    try {
        const dir = await apiOpenFolderDialog();
        if (dir) addPath(dir);
    } catch (err) {
        showToast('Browse failed: ' + err.message, 'error');
    }
}

function addPath(p) {
    if (selectedPaths.has(p)) {
        showToast('Folder already added', 'info');
        return;
    }
    selectedPaths.add(p);
    syncUI();
}

function removePath(p) {
    selectedPaths.delete(p);
    syncUI();
}

/** Sync chips and hidden input value with the current selectedPaths set. */
function syncUI() {
    // Update the scan-path input (used by scan.js to build the ScanRequest).
    document.getElementById('scan-path').value = Array.from(selectedPaths).join('; ');

    // Update chip display.
    let container = document.getElementById('browse-chips');
    if (!container) {
        container = document.createElement('div');
        container.id = 'browse-chips';
        container.className = 'browse-selected-list';
        // Insert chips container after the input-group in the topbar.
        const inputGroup = document.getElementById('scan-path').closest('.input-group');
        inputGroup.parentNode.insertBefore(container, inputGroup.nextSibling);
    }

    container.innerHTML = '';
    if (selectedPaths.size === 0) {
        container.style.display = 'none';
        return;
    }
    container.style.display = 'flex';
    for (const p of selectedPaths) {
        const chip = document.createElement('span');
        chip.className = 'browse-selected-chip';
        chip.textContent = shortenPath(p);
        chip.title = p;
        const x = document.createElement('span');
        x.className = 'browse-chip-remove';
        x.textContent = '\u00D7';
        x.addEventListener('click', () => removePath(p));
        chip.appendChild(x);
        container.appendChild(chip);
    }
}

function shortenPath(fullPath) {
    if (!fullPath) return '/';
    const sep = fullPath.includes('\\') ? '\\' : '/';
    const parts = fullPath.split(sep).filter(Boolean);
    if (parts.length <= 2) return fullPath;
    return '...' + sep + parts.slice(-2).join(sep);
}

/** Expose for scan.js to read selected paths. */
export function getSelectedPaths() {
    return Array.from(selectedPaths);
}
```

#### 2.3.4 Update `static/js/scan.js`

In the `startScan` function, the scan path is currently read from `document.getElementById('scan-path').value`.
This should continue to work because `syncUI()` keeps that input updated with the semicolon-separated
paths. No change should be needed here, but verify that `scan.js` correctly splits on `; `
and passes the first path as `path` and the rest as `extra_paths` in the `ScanRequest`.

#### 2.3.5 Clean up dead code

After confirming the new browse works:

1. **Remove the `Browse` method** from `app.go` (the one that returns `BrowseResponse`).
2. **Remove `BrowseRequest`, `BrowseEntry`, `BrowseResponse`** types from `server.go`.
3. **Remove `apiBrowse`** from `static/js/api.js`.
4. **Remove the browse-overlay / browse-box CSS** from `static/css/components.css`
   (classes: `.browse-overlay`, `.browse-box`, `.browse-list`, `.browse-item`,
   `.browse-item-selected`, `.browse-path-row`, `.browse-path-input`,
   `.browse-current`, `.browse-subfolder-row`, `.browse-subfolder-label`,
   `.browse-actions`). **Keep** `.browse-selected-list`, `.browse-selected-chip`,
   `.browse-chip-remove` — those are reused by the new chips.

#### 2.3.6 Include subfolders

The current browse dialog has an "Include subfolders" checkbox. This is still needed.
The checkbox currently lives inside the modal and stores its state on `window._includeSubfolders`.

Move it: add a small checkbox next to the browse chips area in the topbar (either in HTML
or created dynamically by `browse.js`). Keep the `window._includeSubfolders` storage as-is
since `scan.js` already reads it.

### 2.4 Acceptance criteria

- [ ] Clicking "Browse..." opens the native OS folder picker (Windows Explorer dialog)
- [ ] Selecting a folder adds it as a chip in the topbar
- [ ] Clicking "Browse..." again opens a new dialog — multiple folders accumulate as chips
- [ ] Clicking the × on a chip removes that folder
- [ ] Typing a path in the input and pressing Enter adds it as a chip
- [ ] The scan still works correctly with one or multiple selected folders
- [ ] "Include subfolders" checkbox is accessible in the topbar (not buried in a modal)
- [ ] The old custom browse modal (`browse-overlay`) no longer appears
- [ ] `Browse()` method, `BrowseRequest/BrowseEntry/BrowseResponse` types, and `apiBrowse`
  are removed (dead code cleanup)
- [ ] `go build ./...` compiles without errors

---

## Implementation order

1. **Task 0 first** — fix CLAUDE.md so that all subsequent work targets the correct files.
   Without this, Claude Code may refuse to edit `static/` files due to rule 7.
2. **Task 1 second** — it's backend-only (Go) and doesn't affect the frontend structure.
3. **Task 2 third** — it touches both Go and JS but is a clean replacement (no interactions
   with Task 1).

After all three tasks, run `wails build -platform windows/amd64` to verify the full build.
