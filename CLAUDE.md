## Project: DedupPhotos — High-Performance Duplicate Photo Finder

### Context
You are working on DedupPhotos, a Go-based duplicate photo finder with a web UI. The project exists on GitHub at https://github.com/Rosca75/dedup-photos and has a working backend. Your task is to **refactor the frontend architecture** for maintainability and token-efficient development, and **redesign the UI layout** to a professional 4-zone interface.

### Owner
- GitHub username: Rosca75
- Running on Windows 11, Go installed via `winget install GoLang.Go`
- Does not know Go in depth — code must be heavily commented
- Comfortable with Python, TypeScript/JS, web frontends
- Build command: `go run .` from the project root
- The project uses `//go:embed static` to embed all static files in the binary — this works recursively with subdirectories, no server.go changes needed

---

## TASK 1: Frontend file refactoring

### Problem
The current `static/app.js` is 612 lines containing 6 unrelated concerns in one file. Every small UI change requires reading/rewriting the entire file, wasting tokens and increasing error risk.

### Current structure
```
static/
├── index.html    (102 lines — HTML skeleton)
├── style.css     (255 lines — all styles in one file)
└── app.js        (612 lines — ALL JavaScript in one file)
```

### Target structure
Split into focused modules. Each JS file uses ES modules (`export`/`import`). Each file stays under 150 lines so that any single change only requires reading one small file.

```
static/
├── index.html              ~120 lines — HTML skeleton with 4-zone layout
├── css/
│   ├── base.css            ~60 lines  — CSS variables, reset, typography, body
│   ├── layout.css          ~80 lines  — 4-zone grid: sidebar, topbar, settings pane, main area
│   ├── cards.css           ~100 lines — group-card, image-card, meta-grid, quality bar, expand/collapse
│   └── components.css      ~70 lines  — buttons, badges, toast, confirm dialog, browse dialog
├── js/
│   ├── app.js              ~40 lines  — entry point: imports all modules, runs init()
│   ├── state.js            ~30 lines  — shared state object (scanResult, pollTimer, settings, selectedFolder)
│   ├── api.js              ~80 lines  — all fetch() calls: scan, delete, browse, results, cancel, mismatch
│   ├── helpers.js          ~55 lines  — formatBytes, formatDate, formatGPS, escapeHtml, qualityColor, formatDuration
│   ├── components.js       ~50 lines  — showToast(), showConfirm()
│   ├── scan.js             ~80 lines  — startScan(), pollResults(), cancelScan(), updateProgress()
│   ├── browse.js           ~80 lines  — folder browser dialog logic
│   ├── sidebar.js          ~100 lines — left pane: folder tree navigation within scan results
│   ├── settings.js         ~60 lines  — right pane: settings panel (algorithm, threshold, extensions, dimensions)
│   ├── render.js           ~150 lines — central zone: renderResults(), buildGroup(), buildImageCard()
│   └── actions.js          ~50 lines  — deleteFile(), reportMismatch(), batchDelete()
```

### Refactoring rules
1. **Use ES modules**: every JS file exports its public functions, `app.js` imports and wires them together
2. **`index.html` loads only `app.js`** with `<script type="module" src="/static/js/app.js"></script>`
3. **`index.html` loads CSS** via multiple `<link>` tags: `base.css`, `layout.css`, `cards.css`, `components.css`
4. **No function should exceed 50 lines** — if it does, split into smaller functions
5. **`state.js` is the single source of truth** — it exports a `state` object that other modules read/write
6. **`api.js` is the only file that calls `fetch()`** — other modules call api functions, not fetch directly
7. **Do NOT change any Go backend code** unless strictly necessary for the new static file paths
8. **Preserve all existing functionality**: scan, poll, cancel, delete, browse, mismatch report, settings, batch operations

### Important: server.go static serving
The current server.go serves static files via:
```go
//go:embed static
var staticFiles embed.FS
http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
```
This automatically serves any file under `static/` including subdirectories. So `static/js/app.js` is served at `/static/js/app.js` and `static/css/base.css` at `/static/css/base.css`. No Go changes needed.

---

## TASK 2: UI redesign — 4-zone layout

### Layout specification

```
┌──────────────────────────────────────────────────────────────────┐
│  TOP BAR (zone B)                                                │
│  [scan path input] [Browse] [Scan] [Cancel]          [Settings ▸]│
├────────────┬─────────────────────────────────┬───────────────────┤
│            │                                 │                   │
│  SIDEBAR   │     MAIN AREA (zone D)          │  SETTINGS PANE   │
│  (zone A)  │                                 │  (zone C)        │
│            │  ┌─ Group 1 (Exact, 100%) ──┐   │                   │
│  Folders:  │  │ ▼ [expand/collapse]       │   │  Algorithm:      │
│  ├─ 2024/  │  │  ┌────┐  ┌────┐         │   │  [dHash ▼]       │
│  │ ├─ Jan/ │  │  │img1│  │img2│         │   │                   │
│  │ ├─ Feb/ │  │  └────┘  └────┘         │   │  Threshold:      │
│  │ └─ Mar/ │  └──────────────────────────┘   │  [10]            │
│  ├─ 2023/  │                                 │                   │
│  └─ misc/  │  ┌─ Group 2 (Perceptual, 92%) ┐│  Extensions:     │
│            │  │ ▶ [collapsed: 3 images]     ││  [jpg,png,...]   │
│  (3 dupes) │  └──────────────────────────────┘│                   │
│  (7 dupes) │                                 │  Min width:      │
│            │                                 │  [0]             │
│            │                                 │                   │
├────────────┴─────────────────────────────────┴───────────────────┤
│  STATUS BAR: 1,234 files scanned │ 15 groups │ 234 MB savings   │
└──────────────────────────────────────────────────────────────────┘
```

### Zone A — Left sidebar: folder navigation
- Displays a tree of subfolders where duplicates were found
- Built from scan results: extract unique parent directories from all duplicate image paths
- Clicking a folder filters the main area to show only groups containing images in that folder
- "All" option at the top to show everything (default)
- Each folder shows the count of duplicate images it contains
- The tree should be collapsible (folders with subfolders can expand/collapse)
- Width: ~220px, resizable is nice-to-have but not required

### Zone B — Top bar
- Fixed at top, full width
- Contains: folder path input, Browse button, Scan button, Cancel button (visible during scan)
- Right side: Settings toggle button (opens/closes zone C)
- Progress bar appears here during scan (below the input row)
- Clean, compact — single row when not scanning

### Zone C — Right settings pane
- Hidden by default, slides in when Settings is clicked
- Contains all scan settings: algorithm selector, threshold, extensions filter, min width, max height
- Width: ~260px
- Can be closed via the same Settings toggle or a close (X) button
- Settings are read when Scan is clicked — no live-apply needed

### Zone D — Central main area
- Takes all remaining space (flexible)
- Displays duplicate groups as cards
- **Each group is collapsible/expandable**:
  - **Collapsed state**: single row showing: match type badge, confidence %, number of images, total wasted space, expand button (▶)
  - **Expanded state**: full card with side-by-side image thumbnails, metadata grids, quality bars, delete buttons, collapse button (▼)
- Default: first 3 groups expanded, rest collapsed (to avoid overwhelming the user with large scan results)
- Sorting: groups sorted by confidence descending (exact first, then perceptual by confidence)
- Stats bar at the bottom or top of this zone: total files, groups, savings, duration

### CSS layout approach
Use CSS Grid for the 4-zone layout:
```css
.app-layout {
    display: grid;
    grid-template-areas:
        "topbar  topbar   topbar"
        "sidebar main     settings"
        "status  status   status";
    grid-template-columns: 220px 1fr 0px;  /* settings column = 0 when hidden */
    grid-template-rows: auto 1fr auto;
    height: 100vh;
}

.app-layout.settings-open {
    grid-template-columns: 220px 1fr 260px;
}
```

### Design tokens (keep current dark theme)
```css
:root {
    --bg:       #0f1419;
    --bg-card:  #1a2332;
    --bg-hover: #253345;
    --border:   #2a3a4e;
    --text:     #e2e8f0;
    --muted:    #5a6a7a;
    --accent:   #2dd4bf;    /* teal */
    --danger:   #ef4444;
    --success:  #22c55e;
    --warning:  #f59e0b;
    --exact:    #8b5cf6;    /* purple badge */
    --perceptual: #06b6d4;  /* cyan badge */
    --font:     'SF Mono', 'Fira Code', 'JetBrains Mono', 'Consolas', monospace;
}
```

---

## TASK 3: Execution order

Execute in this order to minimize risk:

### Step 1 — Create the file structure
Create all directories and empty files:
```
mkdir -p static/css static/js
```

### Step 2 — Extract helpers.js
Move `formatBytes`, `formatDate`, `formatDuration`, `formatGPS`, `escapeHtml`, `qualityColor` from app.js into `js/helpers.js` with proper exports.

### Step 3 — Extract state.js
Create the shared state object:
```js
export const state = {
    scanResult: null,
    pollTimer: null,
    selectedFolder: null,    // for sidebar filtering
    settingsOpen: false,
    expandedGroups: new Set() // track which groups are expanded
};
```

### Step 4 — Extract api.js
Move all `fetch()` calls into api.js with named exports: `apiScan()`, `apiResults()`, `apiDelete()`, `apiBrowse()`, `apiCancel()`, `apiMismatchReport()`.

### Step 5 — Extract components.js
Move `showToast()` and `showConfirm()`.

### Step 6 — Extract scan.js
Move `startScan()`, `pollResults()`, `cancelScan()`, `updateProgress()`.

### Step 7 — Extract browse.js
Move `openBrowse()` and `showBrowseDialog()`.

### Step 8 — Extract actions.js
Move `deleteFile()`, `reportMismatch()`, batch operations.

### Step 9 — Create sidebar.js (new)
Build the folder tree from scan results. Logic:
1. After scan completes, iterate all groups → all images → extract directory paths
2. Build a tree structure from those paths
3. Render as a collapsible folder tree in zone A
4. On click, update `state.selectedFolder` and re-render the main area filtered

### Step 10 — Create settings.js (new)
Extract settings panel logic. Read values from DOM inputs and return a settings object when scan starts.

### Step 11 — Refactor render.js
The most complex file. Must handle:
- `renderResults()` — main orchestrator, respects `state.selectedFolder` filter
- `buildGroup()` — builds a group card with expand/collapse
- `buildImageCard()` — builds individual image card with metadata
- Expand/collapse: toggle group ID in `state.expandedGroups`, re-render that group only (not the whole page)

### Step 12 — Rewrite app.js as entry point
```js
import { initScan } from './scan.js';
import { initSidebar } from './sidebar.js';
import { initSettings } from './settings.js';
import { initBrowse } from './browse.js';
import { initActions } from './actions.js';

document.addEventListener('DOMContentLoaded', () => {
    initScan();
    initSidebar();
    initSettings();
    initBrowse();
    initActions();
});
```

### Step 13 — Split CSS
Split current `style.css` into `css/base.css`, `css/layout.css`, `css/cards.css`, `css/components.css`.

### Step 14 — Rewrite index.html
New HTML skeleton with the 4-zone grid layout, loading split CSS and JS module.

### Step 15 — Test
Run `go run .` and verify all features work in the browser.

---

## Backend reference (DO NOT MODIFY unless necessary)

### Go files
| File         | Lines | Purpose                                          |
|------------- |-------|--------------------------------------------------|
| main.go      | 110   | CLI args, server startup                         |
| scanner.go   | 192   | Recursive filesystem walk                        |
| hasher.go    | 471   | xxHash (exact) + dHash/pHash (perceptual)        |
| metadata.go  | 439   | EXIF extraction + quality scoring                |
| grouper.go   | 583   | BK-Tree indexing + duplicate grouping             |
| server.go    | 1081  | HTTP API + static serving via go:embed            |

### API endpoints (existing, do not change)
| Method | Endpoint             | Purpose                              |
|--------|----------------------|--------------------------------------|
| GET    | /                    | Serves index.html                    |
| GET    | /static/*            | Serves embedded static files         |
| POST   | /api/scan            | Start scan with settings             |
| GET    | /api/results         | Poll scan progress/results           |
| POST   | /api/cancel          | Cancel running scan                  |
| POST   | /api/delete          | Soft-delete a file (.deleted)        |
| GET    | /api/thumbnail?path= | 400px JPEG thumbnail                 |
| POST   | /api/browse          | Directory browser                    |
| POST   | /api/report-mismatch | Diagnostic report for false matches  |

### API response shape (from /api/results)
```json
{
    "status": "complete",
    "progress": {"current": 100, "total": 100, "phase": "Done"},
    "stats": {
        "total_files": 1234,
        "duplicate_groups": 15,
        "wasted_bytes": 245366784,
        "duration_ms": 4521
    },
    "groups": [
        {
            "id": "uuid",
            "match_type": "exact",
            "confidence": 100.0,
            "images": [
                {
                    "path": "/photos/2024/img001.jpg",
                    "filename": "img001.jpg",
                    "size": 4521984,
                    "width": 4032,
                    "height": 3024,
                    "date_taken": "2024-06-15T14:30:00Z",
                    "gps_lat": 49.749,
                    "gps_lon": 6.637,
                    "camera": "iPhone 15 Pro",
                    "lens": "iPhone 15 Pro back camera",
                    "iso": 64,
                    "quality_score": 78,
                    "is_best": true
                }
            ]
        }
    ]
}
```

---

## Key constraints
- **No frameworks** — vanilla HTML/CSS/JS only (no React, Vue, Svelte)
- **ES modules** — use `import`/`export`, loaded via `<script type="module">`
- **go:embed compatibility** — all static files under `static/` directory
- **Dark theme** — navy/teal palette as specified in design tokens
- **Monospace font** — consistent with current design
- **No file exceeds 150 lines** — this is a hard constraint for token efficiency
- **Every function is documented** with a short comment explaining what it does
