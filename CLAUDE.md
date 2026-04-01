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
- Prerequisites: Go 1.21+, Wails CLI v2 (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`),
  Node.js 16+ (required by Wails toolchain), WebView2 (pre-installed on Windows 10/11)

**Unsupported formats:** HEIC/HEIF files are not supported. Go has no pure-Go HEVC decoder,
and CGo-based solutions (libde265) cause build issues on Windows. HEIC files are skipped
during scanning.

---

## 2. Repository Structure

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

### `server.go` — types and state only

No HTTP handlers. Contains:
- Type definitions: `ScanRequest`, `ScanResult`, `ImageInfo`, etc.
- Global scan state: `scanMutex`, `scanResult`, `scanCancel`, `thumbnailCache`
- `ScanDirectoryFiltered()` — filesystem walk with extension/dimension filters

### Business logic files

Avoid modifying these files (`scanner.go`, `hasher.go`, `metadata.go`, `grouper.go`, `cache.go`)
unless the change is explicitly scoped and described in an improvement plan.

| File | Lines | Purpose |
|---|---|---|
| `scanner.go` | 204 | Recursive filesystem walk |
| `hasher.go` | 815 | xxHash (exact) + dHash/pHash (perceptual) |
| `metadata.go` | 584 | EXIF extraction + quality scoring |
| `grouper.go` | 694 | BK-Tree indexing + duplicate grouping |
| `cache.go` | 180 | Persistent hash cache |

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

Dark navy/teal theme. Do not change values without a deliberate design decision.

```css
:root {
    --bg:           #0f1419;
    --bg-card:      #1a2332;
    --bg-hover:     #253345;
    --border:       #2a3a4e;
    --text:         #e2e8f0;
    --muted:        #5a6a7a;
    --accent:       #2dd4bf;    /* teal — primary accent */
    --danger:       #ef4444;    /* red — delete actions */
    --success:      #22c55e;    /* green — confirmations */
    --warning:      #f59e0b;    /* amber — warnings */
    --exact:        #8b5cf6;    /* purple — exact match badge */
    --perceptual:   #06b6d4;    /* cyan — perceptual match badge */
    --font: 'SF Mono', 'Fira Code', 'JetBrains Mono', 'Consolas', monospace;
}
```

---

## 8. Key Development Rules

1. **Read before writing.** Before modifying any file, read it first. Never assume its current contents.
2. **One file at a time.** Change one module, verify it is correct, then move on.
3. **No file over 150 lines.** If a file approaches this limit, split it.
4. **No function over 50 lines.** Extract helpers when functions grow.
5. **No HTTP, no `fetch()`.** All Go ↔ JS communication goes through `window.go.main.App.*` Promises.
6. **Asset paths have no `/static/` prefix.** Wails embeds `static/` and strips the prefix via `fs.Sub`. A file at `static/css/base.css` loads as `/css/base.css`.
7. **`static/` is the active frontend directory.** All frontend work happens here. There is no `frontend/` directory.
8. **State lives in `state.js` only.** Never store shared state as module-level variables in other files.
9. **Wails calls live in `api.js` only.** No other module calls `window.go.*` directly.
10. **Avoid modifying business logic files** (`scanner.go`, `hasher.go`, `metadata.go`, `grouper.go`, `cache.go`) unless the change is explicitly scoped and described in an improvement plan.
11. **Comment all Go code.** The owner is not a Go expert. Explain every non-obvious construct.
12. **Test after every change.** Run `wails dev` and verify in the native window.
