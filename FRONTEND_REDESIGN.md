## Frontend Redesign — Based on Mockup & Charte Graphique

This document specifies a complete frontend redesign of DedupPhotos.
It replaces the current 4-zone card-based layout with a professional,
minimalist, table-centric layout following the provided mockup and charte graphique.

**Reference files:**
- Mockup: `dedup_photos_mockup.png` (provided by the owner)
- Charte graphique: `charte_graphique_dedup_photos.txt` (provided by the owner)

---

## OVERALL LAYOUT CHANGE

The current layout uses card-based duplicate groups with expand/collapse.
The new layout is a **classic desktop application** style with:

```
┌──────────────────────────────────────────────────────────────────────────┐
│  TOP BAR — scan settings + action buttons + stats                        │
│  [Logo] [Source folder] [Browse] [Threshold slider] [Algorithm]          │
│  [Extensions checkboxes] [Max width] [Max height] [Normalised size]      │
│  [SCAN button] [Undo] [Redo] [Refresh]              [Stats: 4 values]   │
├──────────┬───────────────────────────────────────────────────────────────┤
│          │                                                               │
│  LEFT    │  CENTER — Data table                                          │
│  PANEL   │  ┌────────────────────────────────────────────────────────┐   │
│          │  │ ☐ │ % diff │ Type │ Filename │ Path │ Dimensions │ ...│   │
│ Preview  │  │───┼────────┼──────┼──────────┼──────┼────────────┼────│   │
│ (image)  │  │   │ 2.6%   │Percep│ [group header: 2 images, 2MB]    │   │
│          │  │   │        │Orig. │ IMG_2413 │ \pho │ 4032x3024  │...│   │
│ Metadata │  │   │        │Dupl. │ IMG_2413 │ \pho │ 2032x1024  │...│   │
│ (read    │  └────────────────────────────────────────────────────────┘   │
│  only)   │                                                               │
│          ├───────────────────────────────────────────────────────────────┤
│ Location │                                                               │
│ with     │                                                               │
│ duplicates│                                                              │
│ (tree)   │                                                               │
│          │                                                               │
│ Filters  │                                                               │
│ (search, │                                                               │
│  % diff, │                                                               │
│  ext.)   │                                                               │
└──────────┴───────────────────────────────────────────────────────────────┘
```

---

## CHARTE GRAPHIQUE — Design Tokens

Replace ALL current CSS variables with these. The theme is LIGHT, professional, minimalist.

```css
:root {
    /* Primary */
    --primary:          #1A3A5C;   /* Deep blue — buttons, links, interactive */
    --primary-light:    #4A90E2;   /* Sky blue — accents, active states */
    
    /* Secondary */
    --success:          #50C878;   /* Mint green — success, validation, positive */
    
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
    --font-size-h1:     2.5rem;
    --font-size-h2:     2rem;
    --font-size-h3:     1.75rem;
    --font-size-body:   1rem;
    --font-size-small:  0.875rem;
    --font-weight-semi: 600;
    --font-weight-regular: 400;
    
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

**Typography**: Import Inter from Google Fonts:
```html
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600&display=swap" rel="stylesheet">
```

**Icons**: Use Feather Icons (via CDN or inline SVGs).
```html
<script src="https://unpkg.com/feather-icons"></script>
```
Call `feather.replace()` in app.js init to render `<i data-feather="icon-name"></i>` tags.

---

## TOP BAR — Zone B (detailed spec)

The top bar spans the full width. It contains TWO rows:

### Row 1: Scan configuration
Left to right:
1. **Logo** — "Dedup Photos" icon + text (Inter Semi-bold, --primary color)
2. **Source folder** — text input + "Browse..." button
3. **Threshold difference** — range slider 0%–100% with value label
4. **Algorithm** — dropdown: dHash (default), pHash, Both
5. **Extensions to include** — checkbox grid (3 columns):
   - JPG ☑, TIFF ☐, GIF ☐
   - PNG ☐, EMF ☐, DNG ☐
   - BMP ☐, PSD ☐, ARW ☐
6. **Max width (px)** — number input
7. **Max height (px)** — number input
8. **Normalised image size** — dropdown: 16x16, 32x32 (default), 64x64, 128x128

### Row 2 (right-aligned): Action buttons + Stats
Left side:
- **Scan button** — large, prominent, --primary background, white icon (magnifying glass).
  Launches scan directly (no confirmation). Shows a spinner during scan.
  
- **Undo button** — circular, icon only (↩), --primary-light.
  Undoes the last delete action. Max 20 actions in undo stack.
  
- **Redo button** — circular, icon only (↪), --primary-light.
  Redoes the last undone action.
  
- **Refresh button** — circular, icon only (↻), --primary-light.
  Re-fetches results from the server without re-scanning.

Right side (stats, always visible):
- **Files scanned**: `2694`
- **Duplicate groups**: `1`
- **Space savings**: `134 MB`
- **Duration**: `2.2s`

### Progress bar
When scan is running, a thin progress bar appears below the top bar (full width),
with phase text and counts (e.g., "Computing fingerprints... 1200/2694").

---

## LEFT PANEL — Zone A (detailed spec)

Width: ~220px. Scrollable vertically. Contains 4 sections stacked:

### Section 1: Preview
- **Image thumbnail** — square area (~180x180px), shows the image when user clicks
  a row in the center table. Placeholder with crossed-out icon when nothing selected.
- Below the image: **metadata list** (read-only, small text):
  - Filename
  - Size
  - Resolution
  - Date
  - Camera
  - GPS
  - Quality score

### Section 2: Location with duplicates
- **Collapsible folder tree** built from scan results
- Shows only folders that contain duplicates
- Each folder shows duplicate count
- Clicking a folder filters the center table to that folder
- "All" at the top to clear filter

### Section 3: Filters
These filter the center table dynamically (no re-scan needed):

- **Filename** — text search input with magnifying glass icon
- **% diff with the original** — range slider (0%–100%) to filter groups
  by their difference percentage
- **Extension** — checkbox grid to show/hide by file type:
  JPG ☐, TIFF ☐, GIF ☐
  PNG ☐, EMF ☐, DNG ☐
  BMP ☐, PSD ☐, ARW ☐

---

## CENTER — Zone D: Data Table (detailed spec)

This is the BIGGEST change from the current design. Instead of card-based groups,
we use a **flat data table** (like a spreadsheet) with group headers.

### Table structure

| ☐ | % of difference | ⇕ Type | ⇕ Filename | ⇕ File path | ⇕ Dimensions | ⇕ Extension | ⇕ File size | ⇕ Blockiness | ⇕ Blurring | Action |
|---|-----------------|--------|-----------|-------------|--------------|-------------|-------------|-------------|-----------|--------|

### Row types

**Group header row** (grey background, slightly darker):
- Checkbox (select entire group)
- % of difference value (e.g., "2.6%")
- Type badge: "Perceptual" (orange) or "Exact" (blue)
- Summary: "2 images" + "2 MB wasted"
- Rest of columns empty
- Expandable/collapsible (click to show/hide member rows)

**Image row** (white background, indented under group):
- No checkbox (or individual checkbox for multi-select)
- Empty % column
- Type badge: "Original" (green) or "Duplicate" (red)
  - "Original" = the image with `is_best: true`
  - "Duplicate" = all other images in the group
- Filename: e.g., "IMG_2413.JPG"
- File path: relative path from scan root
- Dimensions: e.g., "4032x3024"
- Extension: e.g., "JPG"
- File size: e.g., "2.0 MB"
- Blockiness: computed value (float, 2 decimals)
- Blurring: computed value (float, 2 decimals)
- Action column: delete button (red trash icon 🗑)

### Table behaviors

- **Sortable columns**: click column header to sort (single column, toggle asc/desc).
  Sort indicator arrows (⇕ → ↑ or ↓).
- **Row click**: clicking an image row shows its preview in the left panel
- **Row hover**: subtle highlight (--bg-subtle)
- **Selected row**: slightly stronger highlight (--primary-light at 10% opacity)
- **Group expand/collapse**: click the group header row to toggle visibility of its image rows.
  Default: all groups expanded.
- **Scrollable**: table body scrolls vertically, header stays fixed (sticky header)
- **Batch actions** (accessible via toolbar or right-click):
  - Select multiple groups via checkboxes
  - "Keep the best" — for selected groups, delete all non-best images
  - "Delete all duplicates" — for selected groups, delete all duplicate-tagged images

---

## NEW BACKEND FEATURES REQUIRED

### 1. Blockiness & Blurring computation

Add two new fields to `ImageMetadata`:
```go
Blockiness  float64 `json:"blockiness"`  // JPEG blockiness score (0 = smooth, higher = more blocky)
Blurring    float64 `json:"blurring"`    // Blur detection score (0 = sharp, higher = more blurry)
```

**Blockiness** (JPEG artifact detection):
- Compute the average absolute difference between adjacent pixel rows/columns
  at 8-pixel intervals (JPEG block boundaries)
- Compare with overall average gradient
- Higher ratio = more visible JPEG blocking artifacts

**Blurring** (sharpness estimation):
- Apply a Laplacian operator (second derivative) on the grayscale image
- Compute the variance of the result
- Lower variance = more blurry

Both should be computed during the hashing/metadata phase and stored in cache.

### 2. Normalised image size parameter

Add to `ScanRequest`:
```go
NormalisedSize int `json:"normalised_size"` // Reduced image size for comparison: 16, 32, 64, 128
```

This controls the dHash computation size. Currently hardcoded to 9x8.
The normalised size determines the precision of perceptual matching:
- 16x16 = fast, less precise
- 32x32 = default, good balance
- 64x64 = slower, more precise
- 128x128 = slowest, highest precision

### 3. RAW format support

Add to `supportedExtensions` in `scanner.go`:
```go
".dng":  true,  // Adobe Digital Negative
".arw":  true,  // Sony RAW
".cr2":  true,  // Canon RAW v2
".cr3":  true,  // Canon RAW v3
".nef":  true,  // Nikon RAW
".orf":  true,  // Olympus RAW
".rw2":  true,  // Panasonic RAW
".raf":  true,  // Fujifilm RAW
".psd":  true,  // Adobe Photoshop
".emf":  true,  // Enhanced Metafile
```

Note: Go's `image.Decode` does NOT natively decode RAW formats. For RAW files:
- Extract the embedded JPEG preview (most RAW files contain one in EXIF)
- Use that preview for hashing and thumbnails
- If no embedded preview, skip perceptual hashing (xxHash only)

### 4. Undo/Redo system

Add new API endpoints:
```
POST /api/undo    → Undo last delete (restore from .deleted)
POST /api/redo    → Redo last undone action (delete again)
GET  /api/history → Returns undo/redo stack state
```

Backend maintains a stack of up to 20 actions:
```go
type Action struct {
    Type     string `json:"type"`      // "delete"
    Path     string `json:"path"`      // original file path
    TrashPath string `json:"trash_path"` // .deleted path
    Timestamp int64  `json:"timestamp"`
}
```

Undo = rename `.deleted` back to original path.
Redo = rename original path back to `.deleted`.

---

## FRONTEND FILE STRUCTURE (updated)

The current modular structure is kept but files are reorganized:

```
static/
├── index.html              Rewrite — new layout structure
├── css/
│   ├── base.css            Rewrite — new design tokens (Inter, light theme)
│   ├── layout.css          Rewrite — new grid (topbar + left panel + center table)
│   ├── table.css           NEW — data table styles (replaces cards.css)
│   └── components.css      Update — new button styles, badges, filters, dialogs
├── js/
│   ├── app.js              Update — add feather icons init
│   ├── state.js            Update — add undo/redo stacks, selected row, sort state
│   ├── api.js              Update — add undo/redo/history endpoints
│   ├── helpers.js           Keep mostly as-is
│   ├── components.js        Update — new badge/button styles
│   ├── scan.js             Update — add normalised_size parameter
│   ├── browse.js            Keep as-is
│   ├── sidebar.js          Rewrite — becomes left panel (preview + tree + filters)
│   ├── settings.js         Remove — settings are now in the top bar directly
│   ├── table.js            NEW — replaces render.js: table rendering, sorting, row selection
│   ├── filters.js          NEW — dynamic filtering logic (filename, % diff, extension)
│   ├── preview.js          NEW — left panel preview + metadata display
│   ├── actions.js          Update — add undo/redo logic
│   └── history.js          NEW — undo/redo stack management (max 20 actions)
```

### Files to DELETE
- `static/css/cards.css` — replaced by `table.css`
- `static/js/render.js` — replaced by `table.js`
- `static/js/settings.js` — settings moved into top bar
- `static/app.js` — old monolithic file (if still present)
- `static/style.css` — old monolithic file (if still present)

---

## CONSTRAINTS

- **No frameworks** — vanilla HTML/CSS/JS only, ES modules
- **No JS file > 150 lines** — split if exceeded
- **Font**: Inter loaded from Google Fonts CDN
- **Icons**: Feather Icons loaded from CDN
- **Light theme** — this is a LIGHT theme design, not dark
- **go:embed** still works — all files under `static/`
- **All existing functionality preserved**: scan, cancel, delete, browse,
  mismatch report, batch operations, cache, progress polling
- **Responsive**: the left panel can be collapsed on small screens (< 1024px)

---

## IMPLEMENTATION ORDER

1. Update `base.css` with new design tokens + Inter font
2. Rewrite `index.html` with new layout structure  
3. Rewrite `layout.css` for new grid
4. Create `table.css` for data table
5. Update `components.css` for new buttons/badges
6. Create `table.js` (replaces render.js)
7. Create `preview.js` for left panel image preview
8. Create `filters.js` for dynamic filtering
9. Create `history.js` for undo/redo stack
10. Update `state.js` with new state fields
11. Update `api.js` with new endpoints
12. Rewrite `sidebar.js` for new left panel structure
13. Update `scan.js` for new parameters
14. Update `actions.js` for undo/redo
15. Update `app.js` for new initializations
16. Backend: Add blockiness/blurring to metadata.go + ImageMetadata
17. Backend: Add normalised_size to ScanRequest + hasher.go
18. Backend: Add RAW extensions to scanner.go
19. Backend: Add undo/redo endpoints to server.go
20. Test everything with `go run .`

---

## IMPORTANT: Update CLAUDE.md after implementation

After completing this redesign, CLAUDE.md must be updated to reflect:
- New file structure (table.js, filters.js, preview.js, history.js)
- Removed files (cards.css, render.js, settings.js)
- New API endpoints (undo, redo, history)
- New design tokens (light theme, Inter font)
- New ImageMetadata fields (blockiness, blurring)
- New ScanRequest fields (normalised_size)
