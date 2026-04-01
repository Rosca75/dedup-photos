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

---

## 2. Repository Structure

```
dedup-photos/
├── main.go          107 lines   Wails app entry point (wails.Run)
├── app.go           601 lines   App struct — all public methods bound to the JS frontend
├── server.go        225 lines   Shared types, global scan state, ScanDirectoryFiltered
├── scanner.go       192 lines   Recursive filesystem walk
├── hasher.go        471 lines   xxHash (exact) + dHash/pHash (perceptual)
├── metadata.go      439 lines   EXIF extraction + quality scoring
├── grouper.go       583 lines   BK-Tree indexing + duplicate grouping
├── wails.json               Wails config (name, version, author)
├── go.mod / go.sum
├── build/
│   └── windows/
│       └── app.manifest
├── frontend/                ← canonical frontend (served by Wails AssetServer)
│   ├── index.html   113 lines
│   ├── style.css    256 lines
│   └── app.js       660 lines
└── static/                  ← legacy folder from pre-Wails era, IGNORE
```

> `static/` is the old HTTP-server era frontend. It still exists in the repo but is **not used**.
> All active frontend work happens exclusively in `frontend/`.

---

## 3. Architecture — Wails v2 Desktop App

### How it works

Wails replaces the old `net/http` server entirely. There is no TCP port, no `localhost:8080`,
no `fetch()` calls. Instead:

1. `main.go` embeds the `frontend/` directory with `//go:embed all:frontend`
2. Wails opens a native Windows window and loads `frontend/index.html` inside it
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
| `fetch('/api/browse', {method:'POST', body:...})` | `window.go.main.App.Browse(path)`                 |
| `fetch('/api/report-mismatch', ...)`              | `window.go.main.App.ReportMismatch(groupId)`      |

### Special cases

**Thumbnails** — `GetThumbnail(path)` returns a base64-encoded JPEG string.
The frontend sets: `img.src = "data:image/jpeg;base64," + result`

**Mismatch report download** — `ReportMismatch(groupID)` returns a JSON string.
The frontend creates a `Blob` and triggers a synthetic `<a>` click for the download.

---

## 4. Go Files Reference

### `main.go` — entry point

Calls `wails.Run()` with the App struct bound. Embeds `frontend/` via `//go:embed all:frontend`.
Window: 1280×900px, minimum 900×600px. Do not change window dimensions without a deliberate reason.

### `app.go` — Wails-bound methods

All public methods on `*App` are automatically callable from JavaScript.

| Method | Signature | Purpose |
|---|---|---|
| `StartScan` | `(path string, threshold int, algorithm string, extensions []string, minWidth int, maxHeight int) map[string]string` | Start background scan |
| `GetResults` | `() ScanResult` | Poll scan progress and results |
| `CancelScan` | `() map[string]string` | Cancel active scan |
| `DeleteFile` | `(path string) map[string]interface{}` | Soft-delete (rename to `.deleted`) |
| `GetThumbnail` | `(path string) string` | Returns base64 JPEG string |
| `Browse` | `(path string) BrowseResponse` | List child directories |
| `ReportMismatch` | `(groupID string) string` | Returns JSON diagnostic report string |

### `server.go` — types and state only

No HTTP handlers. Contains:
- Type definitions: `ScanRequest`, `BrowseRequest`, `BrowseResponse`, `ScanResult`, `ImageInfo`, etc.
- Global scan state: `scanMutex`, `scanResult`, `scanCancel`, `thumbnailCache`
- `ScanDirectoryFiltered()` — filesystem walk with extension/dimension filters

### Business logic files (do not modify)

| File | Lines | Purpose |
|---|---|---|
| `scanner.go` | 192 | Recursive filesystem walk |
| `hasher.go` | 471 | xxHash (exact) + dHash/pHash (perceptual) |
| `metadata.go` | 439 | EXIF extraction + quality scoring |
| `grouper.go` | 583 | BK-Tree indexing + duplicate grouping |

---

## 5. Frontend Architecture

### Current state

The frontend is a **single-file monolith** — all logic lives in `frontend/app.js` (660 lines)
wrapped in an IIFE (`(function() { ... })()`). It is **not** split into ES modules yet.
`frontend/index.html` loads `app.js` via a plain `<script src="/app.js">` (no `type="module"`).

CSS is a single file: `frontend/style.css` (256 lines).

Static assets are served by the Wails AssetServer from the root `/` — paths have **no `/static/` prefix**.
`style.css` is at `/style.css`, `app.js` is at `/app.js`.

### Target modular structure (not yet implemented)

When splitting `app.js`, the target is:

```
frontend/
├── index.html                  HTML shell with 4-zone grid layout
├── css/
│   ├── base.css        ~60 lines   CSS variables, reset, typography
│   ├── layout.css      ~80 lines   4-zone CSS Grid
│   ├── cards.css       ~100 lines  Group cards, image cards, quality bar
│   └── components.css  ~70 lines   Buttons, badges, toast, confirm, browse dialog
└── js/
    ├── app.js          ~40 lines   Entry point — imports all modules, wires init()
    ├── state.js        ~30 lines   Shared state object (single source of truth)
    ├── wails.js        ~60 lines   All window.go.main.App.* calls (the ONLY file that touches window.go)
    ├── helpers.js      ~55 lines   Pure utility functions (format, escape, color)
    ├── components.js   ~50 lines   showToast(), showConfirm()
    ├── scan.js         ~80 lines   startScan(), pollResults(), cancelScan(), updateProgress()
    ├── browse.js       ~80 lines   Folder browser dialog
    ├── sidebar.js      ~100 lines  Folder tree navigation within scan results
    ├── settings.js     ~60 lines   Settings panel read/write
    ├── render.js       ~150 lines  renderResults(), buildGroup(), buildImageCard()
    └── actions.js      ~50 lines   deleteFile(), reportMismatch(), batchDelete()
```

When the split is implemented:
- `index.html` loads only `js/app.js` via `<script type="module" src="/js/app.js">`
- `index.html` loads CSS via 4 `<link>` tags (no `/static/` prefix)
- **`wails.js` is the isolation layer** — it wraps all `window.go.main.App.*` calls; no other module touches `window.go`
- State lives exclusively in `state.js`

### State object (target, when split is implemented)

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
6. **Asset paths have no `/static/` prefix.** Wails serves `frontend/` at root `/`. A file at `frontend/style.css` loads as `/style.css`.
7. **`static/` is dead code.** Never read, reference, or modify the `static/` directory.
8. **State lives in `state.js` only** (when split). Never store shared state as module-level variables in other files.
9. **Wails calls live in `wails.js` only** (when split). No other module calls `window.go.*` directly.
10. **Do not touch business logic files** (`scanner.go`, `hasher.go`, `metadata.go`, `grouper.go`) unless explicitly requested.
11. **Comment all Go code.** The owner is not a Go expert. Explain every non-obvious construct.
12. **Test after every change.** Run `wails dev` and verify in the native window.
