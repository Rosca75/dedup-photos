## Project: DedupPhotos — High-Performance Duplicate Photo Finder

### Context
DedupPhotos is a **Wails v2 desktop application** (not a web server) that finds and removes duplicate photos. It runs as a native Windows/Mac/Linux window powered by the system WebView. The Go backend exposes methods directly to the JavaScript frontend via Wails bindings — no HTTP server, no fetch() calls.

### Owner
- GitHub username: Rosca75
- Running on Windows 11, Go installed via `winget install GoLang.Go`
- Wails CLI installed via `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- Does not know Go in depth — code must be heavily commented
- Comfortable with Python, TypeScript/JS, web frontends
- **Build command**: `wails build` (production) or `wails dev` (live reload)
- The project uses `//go:embed all:static` to embed the frontend into the binary

---

## Workflow — Branch Rules

- **Always develop on the `preview` branch.** Never create a new branch for changes unless explicitly asked.
- When the owner is satisfied with `preview`, they will ask you to push to `main`. Until then, stay on `preview`.
- Commit and push to `origin/preview` after every meaningful change.

---

## Architecture

### Wails v2 bridge

In Wails v2, Go methods are called directly from JavaScript — no HTTP required:

```
// Go side (app.go):
func (a *App) StartScan(req ScanRequest) ScanResult { ... }

// JS side:
const result = await window.go.main.App.StartScan({ path: "/photos", ... });
```

Every public method on `*App` is automatically exposed. Wails serialises Go structs ↔ JS objects using the `json:` struct tags.

### File layout

```
dedup-photos/
├── main.go          ~60 lines  — wails.Run() entry point, embeds static/
├── app.go           ~300 lines — *App struct: all methods exposed to JS
├── server.go        ~500 lines — shared types, global state, ScanDirectoryFiltered
├── scanner.go       ~192 lines — Recursive filesystem walk
├── hasher.go        ~471 lines — xxHash (exact) + dHash/pHash (perceptual)
├── metadata.go      ~439 lines — EXIF extraction + quality scoring
├── grouper.go       ~583 lines — BK-Tree indexing + duplicate grouping
├── static/
│   ├── index.html          — HTML skeleton (4-zone layout)
│   ├── css/
│   │   ├── base.css        — Design tokens, reset, light theme
│   │   ├── layout.css      — 3-zone grid: topbar, left panel, main table
│   │   ├── table.css       — Duplicate groups table, rows, badges
│   │   └── components.css  — Buttons, toast, dialogs, hover preview
│   └── js/
│       ├── app.js          — Entry point: imports all modules
│       ├── state.js        — Shared application state
│       ├── api.js          — Wails binding wrappers (window.go.main.App.*)
│       ├── helpers.js      — Pure formatting utilities
│       ├── components.js   — showToast(), showConfirm()
│       ├── scan.js         — Scan lifecycle: start, poll, cancel
│       ├── browse.js       — Folder browser dialog
│       ├── sidebar.js      — Left panel folder tree
│       ├── actions.js      — Delete, batch, mismatch report
│       ├── table.js        — Main results table rendering
│       ├── preview.js      — Left panel image preview
│       ├── filters.js      — Dynamic result filtering
│       └── resize.js       — Drag-to-resize panel handles
```

### Wails App methods (app.go)

| JS call | Go method | Returns |
|---------|-----------|---------|
| `App.StartScan(req)` | `StartScan(req ScanRequest)` | `map[string]string` |
| `App.GetResults()` | `GetResults()` | `ScanResult` |
| `App.CancelScan()` | `CancelScan()` | `map[string]string` |
| `App.DeleteFile(path)` | `DeleteFile(path string)` | `map[string]interface{}` |
| `App.GetThumbnail(path)` | `GetThumbnail(path string)` | `string` (base64 JPEG) |
| `App.Browse(path)` | `Browse(path string)` | `BrowseResponse` |
| `App.ReportMismatch(id)` | `ReportMismatch(id string)` | `string` (JSON) |

### Thumbnail loading (async)
Thumbnails are loaded asynchronously via `GetThumbnail()` which returns a base64-encoded JPEG string. In JS:
```js
const b64 = await window.go.main.App.GetThumbnail(img.path);
if (b64) imgEl.src = 'data:image/jpeg;base64,' + b64;
```

### Static file paths
With `//go:embed all:static`, Wails serves the `static/` directory at `/`. So:
- `static/css/base.css` → `/css/base.css`
- `static/js/app.js`   → `/js/app.js`
- `static/index.html`  → served as the root page

**Do NOT use `/static/` prefix in HTML/CSS/JS file references.**

---

## Key constraints
- **No frameworks** — vanilla HTML/CSS/JS only (no React, Vue, Svelte)
- **ES modules** — use `import`/`export`, loaded via `<script type="module">`
- **No file exceeds 150 lines** — hard constraint for token efficiency
- **Every function is documented** with a short JSDoc comment
- **api.js is the ONLY file that calls window.go.main.App.*** — other modules import from api.js
- **state.js is the single source of truth** — shared state object used by all modules
- **Go code must be heavily commented** — the owner is not a Go expert

---

## ScanRequest struct (shared between Go and JS)
```json
{
  "path": "/photos",
  "threshold": 10,
  "algorithm": "dhash",
  "extensions": ["jpg", "png"],
  "extra_paths": ["/more/photos"],
  "normalised_size": 32,
  "include_subfolders": true,
  "min_width": 0,
  "max_height": 0,
  "min_file_size": 0,
  "max_file_size": 0
}
```

## ScanResult shape (from GetResults)
```json
{
  "status": "complete",
  "progress": { "current": 100, "total": 100, "phase": "Complete" },
  "stats": {
    "total_files": 1234, "duplicate_groups": 15,
    "wasted_bytes": 245366784, "duration_ms": 4521
  },
  "groups": [{
    "id": "uuid", "match_type": "exact", "confidence": 100.0,
    "images": [{
      "path": "/photos/img.jpg", "filename": "img.jpg",
      "size": 4521984, "width": 4032, "height": 3024,
      "date_taken": "2024-06-15T14:30:00Z",
      "gps_lat": 49.749, "gps_lon": 6.637,
      "camera": "iPhone 15 Pro", "quality_score": 78, "is_best": true
    }]
  }]
}
```
