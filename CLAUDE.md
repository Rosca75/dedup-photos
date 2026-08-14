# CLAUDE.md — DedupPhotos

> This file is the single source of truth for Claude Code working on this project.
> Read it fully before making any change. Follow every rule without exception.

---

## 1. Project Overview

**DedupPhotos** is a Go-based duplicate photo finder packaged as a **native desktop application**
using [Wails v2](https://wails.io). It opens a native Windows window (via WebView2), scans a
photo library for exact and perceptual duplicates, and presents a web-based review UI embedded
directly in the binary — no browser, no localhost port.

**Owner profile:**
- Running on **Windows 11**, Go installed via `winget install GoLang.Go`
- Comfortable with Python, TypeScript/JS, web frontends — not a Go expert
- **Go code must be heavily commented** — explain every function and non-obvious block
- Build command: `wails build -platform windows/amd64`
- Dev mode (live reload): `wails dev`
- Prerequisites: Go 1.25+ (`go.mod` declares `go 1.25.0`), Wails CLI v2.13.0
  (`go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0` — keep the CLI and the
  `wails/v2` module in `go.mod` on the same version), Node.js 16+ (required by the Wails
  toolchain), WebView2 (pre-installed on Windows 10/11)

**Both commands must be run from the directory containing `wails.json`.** On the owner's
Linux box the repo is nested one level down, at
`~/Documents/dedup-photos/dedup-photos/` — running `wails dev` from the parent fails with
`ERROR open .../wails.json: no such file or directory`.

**On Linux, add `-tags webkit2_41`** — `wails dev -tags webkit2_41`,
`wails build -platform linux/amd64 -tags webkit2_41`. Wails links `webkit2gtk-4.0` by
default, but Ubuntu 24.04 ships only `webkit2gtk-4.1`, so an untagged build dies with
`Package 'webkit2gtk-4.0', required by 'virtual:world', not found`. The tag activates
Wails' built-in 4.1 support. `.github/workflows/release.yml` already does this. Note that
`wails doctor` reports `libwebkit: Not Found` on 24.04 even when 4.1 is correctly
installed — that warning is expected and does not block a tagged build.

**HEIC/HEIF: fully supported.** `.heic` and `.heif` are first-class scanned formats
(see `supportedExtensions` in `scanner.go`), decoded via `github.com/Rosca75/heic` — a
pure-Go library with **no CGo**, so the Windows build stays a single static binary. It has
two backends: a Rust HEVC decoder compiled to WASM (always available, works everywhere),
or the system `libheif` loaded dynamically via purego when present.

**This app always uses the WASM backend.** `initHEIC()` in `heic_support.go` sets
`heic.ForceWasmMode` whenever a dynamic libheif is detected, regardless of its version, for
two independent reasons: libheif rejects the truncated buffer the 192 KB fast path depends
on (measured 0/8 on `samples/`, versus 8/8 under WASM), and libheif < 1.18 mis-decodes
iPhone HDR/`tmap` files. `heic.ForceWasmMode` is a package-level global and the hash
pipeline decodes concurrently, so the backend cannot be chosen per call — it is all or
nothing. Upgrading the system libheif does not and should not change this.

HEIC has a dedicated fast path, since these files are ~3 MB each and a full decode is
~100× slower than the embedded thumbnail. `heic_support.go` reads only the first **192 KB**
of the file and runs a four-rung ladder (`decodeHEICFromHeader`): embedded thumbnail from
that header window → primary image from it → thumbnail after a full read → primary after a
full read. Rung 1 hits for essentially every iPhone photo. `printAndResetHEICLadder()`
prints a `[perf] HEIC ladder:` line at the end of a scan showing how often each rung won —
if `thumbHdr` ever collapses toward zero, the fast path has broken and scans will crawl.

**Genuinely unsupported:** RAW formats have preview extraction only (`raw_preview.go`),
not full decode.

---

## 2. Repository Structure

> Line counts are a rough guide to weight, not a contract. Regenerate this block when
> files are added, split or deleted — it had drifted badly once already (it listed a
> `server.go` that no longer exists and omitted eight files).

```
dedup-photos/
│
│  ── Entry point & Wails surface ─────────────────────────────────────────────
├── main.go               84   Wails v2 entry point; //go:embed all:static
├── app.go               503   App struct — every method bound to the JS frontend
├── types.go              61   Request/response types serialised across the Wails bridge
├── state.go              35   Global scan state + undo/redo stacks (mutex-guarded)
│
│  ── Scan pipeline ────────────────────────────────────────────────────────────
├── scanner.go           385   Filesystem walk; supportedExtensions lives here
├── hasher.go            366   Hash algorithms (xxHash exact, dHash/pHash perceptual)
├── hasher_pipeline.go   671   Parallel hash pipeline with file-size bucketing
├── parallel.go          168   Shared worker-pool helper used by hasher + grouper
├── grouper.go           682   BK-Tree, Union-Find, duplicate grouping
├── cache.go             186   Persistent hash cache for fast re-scans
├── thumb_cache.go       109   Persistent on-disk thumbnail cache (lazy, on first view)
│
│  ── Format support ───────────────────────────────────────────────────────────
├── heic_support.go      352   HEIC/HEIF: 192 KB byte-range fast path, 4-rung decode
│                              ladder, per-worker reusable decoder, [perf] counters
├── heic_version_linux.go  28  Probe the system libheif version via purego dlopen, so
├── heic_version_darwin.go 34  initHEIC() can force WASM below 1.18.
├── heic_version_other.go  11  The third is the Windows/BSD stub (always returns true)
├── raw_preview.go        76   extractEmbeddedJPEG — pull JPEG previews out of RAW files
├── exif_extract.go      169   Unified EXIF extraction via bep/imagemeta (all formats)
├── metadata.go          467   EXIF-driven quality scoring
│
│  ── Config & docs ────────────────────────────────────────────────────────────
├── wails.json                 Wails config (name, output filename, author)
├── go.mod / go.sum
├── CLAUDE.md / README.md
├── docs/                      Improvement plans, numbered NN-NAME.md
├── samples/                   Test images (incl. HEIC) used for manual verification
│
└── static/                    ← active frontend, embedded into the binary
    ├── index.html       211
    ├── css/
    │   ├── base.css      73   CSS variables, reset, typography
    │   ├── layout.css   409   Grid layout + left panel
    │   ├── table.css    157   Data table styles
    │   └── components.css 261 Buttons, badges, toast, browse dialog
    └── js/
        ├── app.js        27   Entry point — imports modules, wires init()
        ├── state.js      47   Shared state object
        ├── api.js        97   All window.go.main.App.* calls (isolation layer)
        ├── helpers.js    70   Pure utility functions
        ├── components.js 59   showToast(), showConfirm()
        ├── scan.js      156   startScan(), pollResults(), cancelScan()
        ├── browse.js    138   Folder browser dialog
        ├── sidebar.js    96   Folder tree navigation
        ├── preview.js   103   Left panel image preview
        ├── filters.js   136   Result filters
        ├── resize.js     96   Panel resize handles
        ├── table.js     532   Results data table
        └── actions.js   183   deleteFile(), reportMismatch(), batch ops
```

`frontend/` also exists but holds only Wails-generated `wailsjs` bindings (`runtime/` and
`go/main/App.js`) — it is not the UI source and is untracked in git. Every `wails dev` and
`wails build` regenerates it, so it reappears after deletion; that is normal. The UI lives
in `static/`.

---

## 3. Architecture — Wails v2 Desktop App

### How it works

Wails replaces the old `net/http` server entirely. There is no TCP port, no `localhost:8080`,
no `fetch()` calls. Instead:

1. `main.go` embeds the `static/` directory with `//go:embed all:static`
2. Wails opens a native Windows window and loads `static/index.html` inside it
3. Wails injects `window.go` into the page — a JS object with one method per bound Go function
4. The frontend calls `window.go.main.App.MethodName(args)` which returns a **Promise**
5. Go return values (structs, maps) are automatically serialised to JS objects

### Go ↔ JavaScript bridge

| Old (HTTP)                                        | New (Wails)                                       |
|---------------------------------------------------|---------------------------------------------------|
| `fetch('/api/scan', {method:'POST', body:...})`   | `window.go.main.App.StartScan(...)`               |
| `fetch('/api/results')`                           | `window.go.main.App.GetResults()`                 |
| `fetch('/api/cancel', {method:'POST'})`           | `window.go.main.App.CancelScan()`                 |
| `fetch('/api/delete', {method:'POST', body:...})` | `window.go.main.App.DeleteFile(path)`             |
| `img.src = '/api/thumbnail?path=...'`             | `App.GetThumbnail(path).then(b64 => img.src=...)` |
| `fetch('/api/report-mismatch', ...)`              | `window.go.main.App.ReportMismatch(groupId)`      |

### Special cases

**Thumbnails** — `GetThumbnail(path)` returns a base64-encoded JPEG string.
The frontend sets: `img.src = "data:image/jpeg;base64," + result`

**Mismatch report download** — `ReportMismatch(groupID)` returns a JSON string.
The frontend creates a `Blob` and triggers a synthetic `<a>` click for the download.

---

## 4. Go Files Reference

### `main.go` — entry point

Calls `wails.Run()` with the App struct bound. Embeds `static/` via `//go:embed all:static`.
Window: 1280×900px, minimum 900×600px. Do not change window dimensions without a deliberate reason.

### `app.go` — Wails-bound methods

All public methods on `*App` are automatically callable from JavaScript.

| Method | Signature | Purpose |
|---|---|---|
| `StartScan` | `(req ScanRequest) map[string]string` | Start background scan |
| `GetResults` | `() ScanResult` | Poll scan progress and results |
| `CancelScan` | `() map[string]string` | Cancel active scan |
| `DeleteFile` | `(path string) map[string]interface{}` | Permanently delete a file |
| `GetThumbnail` | `(path string) string` | Returns base64 JPEG string |
| `OpenFolderDialog` | `() (string, error)` | Native OS folder picker |
| `ReportMismatch` | `(groupID string) string` | Returns JSON diagnostic report string |

### Types and state — no HTTP layer

There are no HTTP handlers; Wails binds Go methods directly. What an older version of this
document called `server.go` was split up in `8890c49` and that file no longer exists:

- **`types.go`** — `ScanRequest`, `ScanResult`, `ScanProgress`, `ScanStats`, `Action`,
  `ReportMismatchRequest`, `DeleteRequest`
- **`state.go`** — global scan state, all mutex-guarded: `scanMutex`, `scanResult`,
  `scanCancel`, `thumbnailCache`, plus `actionMutex` and the `undoStack` / `redoStack`
- **`scanner.go`** — `ScanDirectoryFiltered()`, the filesystem walk with extension,
  dimension and file-size filters

### Business logic files

Avoid modifying these unless the change is explicitly scoped and described in an
improvement plan.

| File | Lines | Purpose |
|---|---|---|
| `scanner.go` | 385 | Filesystem walk; `supportedExtensions` |
| `hasher.go` | 366 | Hash algorithms — xxHash (exact), dHash/pHash (perceptual) |
| `hasher_pipeline.go` | 671 | Parallel hash pipeline with file-size bucketing |
| `grouper.go` | 682 | BK-Tree indexing + Union-Find duplicate grouping |
| `metadata.go` | 467 | EXIF-driven quality scoring |
| `cache.go` | 186 | Persistent hash cache |
| `thumb_cache.go` | 109 | Persistent on-disk thumbnail cache |
| `heic_support.go` | 352 | HEIC fast path + decode ladder (see §1) |

---

## 5. Frontend Architecture

The frontend has been split into ES modules under `static/js/` and `static/css/`.
`static/index.html` loads `js/app.js` via `<script type="module" src="/js/app.js">`.
CSS is split into 4 files loaded via `<link>` tags.

**`api.js` is the isolation layer** — it wraps all `window.go.main.App.*` calls;
no other module touches `window.go` directly.

### Current modular structure

```
static/
├── index.html
├── css/
│   ├── base.css        CSS variables, reset, typography
│   ├── layout.css      Grid layout + left panel
│   ├── table.css       Data table styles
│   └── components.css  Buttons, badges, toast, browse dialog
└── js/
    ├── app.js          Entry point — imports modules, wires init()
    ├── state.js        Shared state object (single source of truth)
    ├── api.js          All window.go.main.App.* calls (isolation layer)
    ├── helpers.js      Pure utility functions
    ├── components.js   showToast(), showConfirm()
    ├── scan.js         startScan(), pollResults(), cancelScan()
    ├── browse.js       Folder browser (native dialog wrapper)
    ├── sidebar.js      Folder tree navigation
    ├── preview.js      Left panel image preview
    ├── filters.js      Result filters
    ├── resize.js       Panel resize handles
    ├── table.js        Results data table
    └── actions.js      deleteFile(), reportMismatch(), batch ops
```

### State object

```js
export const state = {
    scanResult: null,          // Last full response from GetResults()
    pollTimer: null,           // setInterval handle during active scan
    selectedFolder: null,      // Folder filter active in sidebar (null = All)
    settingsOpen: false,       // Whether the settings pane is visible
    expandedGroups: new Set()  // IDs of groups currently expanded
};
```

---

## 6. UI Layout — 4-Zone Interface

```
┌──────────────────────────────────────────────────────────────────┐
│  ZONE B — Top Bar (full width, fixed)                            │
│  [path input] [Browse] [Scan] [Cancel]             [Settings ▸] │
│  [━━━━━━━━━━━━━━━━ progress bar (during scan) ━━━━━━━━━━━━━━━] │
├────────────┬─────────────────────────────────┬───────────────────┤
│            │                                 │                   │
│  ZONE A    │  ZONE D — Main Area             │  ZONE C           │
│  Sidebar   │  (fills remaining space)        │  Settings Pane    │
│  ~220px    │                                 │  ~260px           │
│            │  Duplicate group cards,         │  (hidden by       │
│  Folder    │  collapsible / expandable       │   default)        │
│  tree with │                                 │                   │
│  dupe      │                                 │                   │
│  counts    │                                 │                   │
│            │                                 │                   │
├────────────┴─────────────────────────────────┴───────────────────┤
│  STATUS BAR: 1,234 files │ 15 groups │ 234 MB savings │ 4.5s    │
└──────────────────────────────────────────────────────────────────┘
```

### Zone A — Sidebar (~220px)

Folder tree built from scan results. Extract unique parent directories from all duplicate image paths.
"All" at the top (default). Each folder shows duplicate image count. Folders with subfolders collapse/expand.
Clicking sets `state.selectedFolder` and re-renders Zone D filtered to that folder.

### Zone B — Top Bar

Path input, Browse, Scan, Cancel (visible only during scan), Settings toggle (right side).
Progress bar appears below the input row during an active scan. Single-row height when idle.

### Zone C — Settings Pane (~260px, hidden by default)

Toggled by the Settings button. Contains: algorithm selector (dHash/pHash/both), threshold,
extensions filter, min/max dimension inputs. Settings are read at scan start — no live-apply.

CSS Grid column collapses to `0px` when hidden, expands to `260px` when open:

```css
.app-layout {
    display: grid;
    grid-template-areas:
        "topbar  topbar   topbar"
        "sidebar main     settings"
        "status  status   status";
    grid-template-columns: 220px 1fr 0px;
    grid-template-rows: auto 1fr auto;
    height: 100vh;
}
.app-layout.settings-open {
    grid-template-columns: 220px 1fr 260px;
}
```

### Zone D — Main Area

Duplicate group cards. Each group is collapsible:
- **Collapsed**: match type badge · confidence % · image count · wasted space · expand button (▶)
- **Expanded**: thumbnails side-by-side · metadata grids · quality bars · delete buttons · collapse (▼)

Default on new results: first 3 groups expanded, rest collapsed.
Sort: exact matches first, then perceptual descending by confidence.
Expand/collapse toggles group ID in `state.expandedGroups` and re-renders that group only (not the full list).

---

## 7. Design Tokens

Light professional theme. Do not change values without a deliberate design decision.

```css
:root {
    /* Primary */
    --primary:          #1A3A5C;   /* Deep blue — buttons, links, interactive */
    --primary-light:    #4A90E2;   /* Sky blue — accents, active states */

    /* Secondary */
    --success:          #50C878;   /* Mint green — success, validation */

    /* Neutrals */
    --text:             #2D2D2D;   /* Dark grey — main text */
    --text-light:       #6B6B6B;   /* Medium grey — secondary text */
    --border:           #E0E0E0;   /* Light grey — borders */
    --bg-subtle:        #F5F5F5;   /* Very light grey — subtle backgrounds */
    --bg:               #FFFFFF;   /* White — main background */
    --black:            #121212;   /* Deep black — headers */

    /* Semantic */
    --danger:           #E74C3C;   /* Red — delete, errors */
    --warning:          #F5A623;   /* Orange — warnings, perceptual badge */
    --exact:            #1A3A5C;   /* Deep blue — exact match badge */
    --perceptual:       #F5A623;   /* Orange — perceptual match badge */
    --original:         #50C878;   /* Green — "Original" badge */
    --duplicate:        #E74C3C;   /* Red — "Duplicate" badge */

    /* Typography */
    --font:             'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;

    /* Spacing (8px grid) */
    --space-xs:         4px;
    --space-sm:         8px;
    --space-md:         16px;
    --space-lg:         24px;
    --space-xl:         32px;

    /* Elements */
    --radius:           8px;
    --shadow:           0 2px 4px rgba(0,0,0,0.05);
    --shadow-md:        0 4px 8px rgba(0,0,0,0.08);
    --transition:       200ms ease;
}
```

`index.html` loads **Inter** via Google Fonts and **Feather Icons** via unpkg.
`js/app.js` calls `feather.replace()` on init to activate icon sprites.

---

## 8. Key Development Rules

1. **Read before writing.** Before modifying any file, read it first. Never assume its current contents.
2. **One file at a time.** Change one module, verify it is correct, then move on.
3. **No file over 150 lines.** If a file approaches this limit, split it.
4. **No function over 50 lines.** Extract helpers when functions grow.
5. **No HTTP, no `fetch()`.** All Go ↔ JS communication goes through `window.go.main.App.*` Promises.
6. **Asset paths have no `/static/` prefix.** Wails embeds `static/` and strips the prefix via `fs.Sub`. A file at `static/css/base.css` loads as `/css/base.css`.
7. **`static/` is the active frontend directory.** All frontend work happens here. A `frontend/` directory does exist, but it holds only Wails-generated `wailsjs` bindings — never edit it, and never put UI source there.
8. **State lives in `state.js` only.** Never store shared state as module-level variables in other files.
9. **Wails calls live in `api.js` only.** No other module calls `window.go.*` directly.
10. **Avoid modifying business logic files** (`scanner.go`, `hasher.go`, `metadata.go`, `grouper.go`, `cache.go`) unless the change is explicitly scoped and described in an improvement plan.
11. **Comment all Go code.** The owner is not a Go expert. Explain every non-obvious construct.
12. **Test after every change.** Run `wails dev` (on Linux: `wails dev -tags webkit2_41`) from the directory holding `wails.json`, and verify in the native window.
