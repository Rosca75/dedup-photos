// =============================================================================
// app.go — Wails App struct with all methods exposed to the JS frontend.
// =============================================================================
//
// Every public method on *App is callable from JavaScript via:
//   await window.go.main.App.MethodName(args...)
//
// Wails automatically:
//   - Converts JS objects → Go structs (using json: struct tags)
//   - Returns Go structs → JS objects (same serialisation)
//   - Runs each call in its own goroutine on the Go side
//
// The scan pipeline uses a background goroutine; JS polls GetResults()
// every 500ms to check progress and retrieve final results.
// =============================================================================

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"os"
	"runtime"
	"strings"
	"time"

	// Wails runtime — aliased to avoid collision with stdlib "runtime" above.
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the Wails application struct. All exported methods on *App are
// exposed to the frontend JavaScript as window.go.main.App.MethodName().
type App struct {
	ctx context.Context // Wails context; available after startup().
}

// NewApp creates and returns a new App instance. Called from main.go.
func NewApp() *App {
	return &App{}
}

// startup is invoked by Wails after the window is created.
// The ctx allows calling Wails runtime APIs (e.g. file dialogs, events).
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// =============================================================================
// StartScan — Launch a duplicate-detection scan in the background.
// =============================================================================

// StartScan starts scanning the directory described by req.
// It returns immediately with {"status":"scanning"}; the actual work runs in
// a goroutine. The JS side polls GetResults() every 500ms for progress.
func (a *App) StartScan(req ScanRequest) map[string]string {
	if req.Threshold <= 0 {
		req.Threshold = 10
	}
	if req.Algorithm == "" {
		req.Algorithm = "dhash"
	}

	// Cancel any running scan, then reset shared state.
	scanMutex.Lock()
	if scanCancel != nil {
		scanCancel()
	}
	scanResult = ScanResult{
		Status:   "scanning",
		Progress: ScanProgress{Phase: "Starting scan..."},
	}
	scanMutex.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	scanMutex.Lock()
	scanCancel = cancel
	scanMutex.Unlock()

	go runScan(ctx, req) // Background goroutine — does the real work.
	return map[string]string{"status": "scanning", "message": "Scan started"}
}

// runScan executes all scan phases and updates the global scanResult.
// Runs as a goroutine launched by StartScan.
//
// Per-phase timing is printed to the console using the [perf] prefix so that
// the performance improvement from each optimisation is easy to observe.
func runScan(ctx context.Context, req ScanRequest) {
	scanStart := time.Now()
	log.Printf("[scan] Start: %s threshold=%d alg=%s", req.Path, req.Threshold, req.Algorithm)

	// Build the extension filter map.
	allowedExts := make(map[string]bool)
	for _, ext := range req.Extensions {
		e := strings.ToLower(strings.TrimSpace(ext))
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		allowedExts[e] = true
	}

	setPhase := func(phase string) {
		scanMutex.Lock()
		scanResult.Progress.Phase = phase
		scanMutex.Unlock()
	}

	// Phase 1: Walk the primary path.
	// Use ConcurrentScanDirectory when no dimension or file-size filters are
	// active (those require opening each file, so the concurrent walker hands
	// off to ScanDirectoryFiltered which handles that correctly).
	t0 := time.Now()
	setPhase("Scanning directory...")

	var paths []string
	var err error
	noExtraFilters := req.MinWidth == 0 && req.MaxHeight == 0 && req.MinFileSize == 0 && req.MaxFileSize == 0
	if len(allowedExts) == 0 && noExtraFilters && req.IncludeSubfolders {
		// Fast path: concurrent walker (#8) — best for NAS / network shares.
		paths, err = ConcurrentScanDirectory(req.Path)
	} else {
		// Filtered path: sequential walker with per-file dimension / size checks.
		paths, err = ScanDirectoryFiltered(req.Path, allowedExts, req.MinWidth, req.MaxHeight, req.IncludeSubfolders, req.MinFileSize, req.MaxFileSize)
	}
	if err != nil {
		scanMutex.Lock()
		scanResult.Status = "complete"
		scanResult.Progress.Phase = fmt.Sprintf("Error: %v", err)
		scanMutex.Unlock()
		return
	}
	log.Printf("[perf] Directory walk:   %.2fs  (%d files found)", time.Since(t0).Seconds(), len(paths))

	// Merge extra paths (multi-folder scan).
	for _, ep := range req.ExtraPaths {
		ep = strings.TrimSpace(ep)
		if ep == "" {
			continue
		}
		if info, err2 := os.Stat(ep); err2 != nil || !info.IsDir() {
			continue
		}
		extra, err2 := ScanDirectoryFiltered(ep, allowedExts, req.MinWidth, req.MaxHeight, req.IncludeSubfolders, req.MinFileSize, req.MaxFileSize)
		if err2 == nil {
			paths = append(paths, extra...)
		}
	}

	if ctx.Err() != nil {
		setScanCancelled()
		return
	}

	totalFiles := len(paths)
	scanMutex.Lock()
	scanResult.Progress.Total = totalFiles
	scanMutex.Unlock()

	if totalFiles == 0 {
		scanMutex.Lock()
		scanResult.Status = "complete"
		scanResult.Progress.Phase = "No images found"
		scanResult.Stats = ScanStats{DurationMs: time.Since(scanStart).Milliseconds()}
		scanMutex.Unlock()
		return
	}

	// Phase 2: Hash all images (the split pipeline in hasher_pipeline.go).
	// The pipeline prints its own [perf] lines for each sub-phase.
	t0 = time.Now()
	allPaths := append([]string{req.Path}, req.ExtraPaths...)
	hashes := HashAllImagesWithProgress(ctx, paths, runtime.NumCPU(), req.Algorithm,
		func(phase string, cur, total int) {
			scanMutex.Lock()
			scanResult.Progress.Phase = phase
			scanResult.Progress.Current = cur
			scanResult.Progress.Total = total
			scanMutex.Unlock()
		}, allPaths)

	if ctx.Err() != nil {
		setScanCancelled()
		return
	}
	log.Printf("[perf] Hash phase total: %.2fs", time.Since(t0).Seconds())

	// Phase 3: Group duplicates (parallel metadata + aspect-ratio BK-Trees).
	t0 = time.Now()
	setPhase("Grouping duplicates...")
	groups := GroupDuplicates(hashes, req.Threshold, req.IncludeSeries)
	log.Printf("[perf] Grouping:         %.2fs  (%d groups)", time.Since(t0).Seconds(), len(groups))

	// Phase 4: Compute wasted bytes (all images except the first in each group).
	var wastedBytes int64
	for _, g := range groups {
		for i, img := range g.Images {
			if i > 0 {
				wastedBytes += img.Size
			}
		}
	}

	dur := time.Since(scanStart)
	scanMutex.Lock()
	scanResult = ScanResult{
		Status:   "complete",
		Progress: ScanProgress{Current: totalFiles, Total: totalFiles, Phase: "Complete"},
		Stats: ScanStats{
			TotalFiles:      totalFiles,
			DuplicateGroups: len(groups),
			WastedBytes:     wastedBytes,
			DurationMs:      dur.Milliseconds(),
		},
		Groups: groups,
	}
	scanMutex.Unlock()

	// Print the full summary matching the format from PERF-OPTIMISATION.
	log.Printf("[perf] TOTAL:            %.2fs  (%d files, %d groups)", dur.Seconds(), totalFiles, len(groups))
	log.Printf("[scan] Done: %d files, %d groups, took %v", totalFiles, len(groups), dur)
}

// setScanCancelled marks the scan result as cancelled.
func setScanCancelled() {
	scanMutex.Lock()
	scanResult.Status = "cancelled"
	scanResult.Progress.Phase = "Scan cancelled"
	scanMutex.Unlock()
}

// =============================================================================
// GetResults / CancelScan
// =============================================================================

// GetResults returns the current scan state (progress or final results).
// Called by JS every 500ms while scanning.
func (a *App) GetResults() ScanResult {
	scanMutex.Lock()
	defer scanMutex.Unlock()
	return scanResult
}

// CancelScan signals the running scan goroutine to stop early.
func (a *App) CancelScan() map[string]string {
	scanMutex.Lock()
	if scanCancel != nil {
		scanCancel()
	}
	scanMutex.Unlock()
	return map[string]string{"status": "cancelled"}
}

// =============================================================================
// DeleteFile
// =============================================================================

// DeleteFile permanently removes a file from disk and evicts its thumbnail.
// Returns {"success": true/false, "message"/"error": "..."}.
func (a *App) DeleteFile(path string) map[string]interface{} {
	if path == "" {
		return map[string]interface{}{"success": false, "error": "path is required"}
	}
	info, err := os.Stat(path)
	if err != nil {
		return map[string]interface{}{"success": false, "error": fmt.Sprintf("file not found: %s", path)}
	}
	if info.IsDir() {
		return map[string]interface{}{"success": false, "error": "cannot delete a directory"}
	}
	if err := os.Remove(path); err != nil {
		return map[string]interface{}{"success": false, "error": fmt.Sprintf("failed to delete: %v", err)}
	}
	thumbnailCache.Delete(path) // Remove from thumbnail cache.
	log.Printf("[delete] Permanently deleted: %s", path)
	return map[string]interface{}{"success": true, "message": "Deleted: " + path}
}

// =============================================================================
// GetThumbnail
// =============================================================================

// GetThumbnail returns a base64-encoded JPEG thumbnail (max 400px).
// JS usage: const b64 = await App.GetThumbnail(path);
//
//	if (b64) img.src = 'data:image/jpeg;base64,' + b64;
func (a *App) GetThumbnail(path string) string {
	if path == "" {
		return ""
	}

	// Serve from in-memory cache if available.
	if cached, ok := thumbnailCache.Load(path); ok {
		return base64.StdEncoding.EncodeToString(cached.([]byte))
	}

	// Open and decode the image file.
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		// Fallback for RAW formats (DNG, ARW, CR2): byte-scan for
		// embedded JPEG SOI/EOI markers. Slower but works for many camera formats.
		if embedded := extractEmbeddedJPEG(path); embedded != nil {
			thumbnailCache.Store(path, embedded)
			return base64.StdEncoding.EncodeToString(embedded)
		}
		return ""
	}

	// Resize to max 400px on the longest side, preserving aspect ratio.
	b := img.Bounds()
	srcW, srcH := b.Max.X-b.Min.X, b.Max.Y-b.Min.Y
	newW, newH := srcW, srcH
	const maxDim = 400
	if srcW > maxDim || srcH > maxDim {
		if srcW >= srcH {
			newW = maxDim
			newH = srcH * maxDim / srcW
		} else {
			newH = maxDim
			newW = srcW * maxDim / srcH
		}
	}
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	// Nearest-neighbour resize (fast, sufficient for thumbnails).
	thumb := image.NewRGBA(image.Rect(0, 0, newW, newH))
	for y := 0; y < newH; y++ {
		for x := 0; x < newW; x++ {
			thumb.Set(x, y, img.At(b.Min.X+x*srcW/newW, b.Min.Y+y*srcH/newH))
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 85}); err != nil {
		return ""
	}
	jpegBytes := buf.Bytes()
	thumbnailCache.Store(path, jpegBytes)
	return base64.StdEncoding.EncodeToString(jpegBytes)
}

// =============================================================================
// OpenFolderDialog
// =============================================================================

// OpenFolderDialog opens the native OS folder picker dialog.
// On Windows this is the standard Explorer folder browser; on macOS it's Finder.
//
// Returns the selected folder path, or an empty string if the user cancelled.
// The frontend calls this when the user clicks the "Browse..." button.
func (a *App) OpenFolderDialog() (string, error) {
	return wailsRuntime.OpenDirectoryDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Select folder to scan",
	})
}

// =============================================================================
// ReportMismatch
// =============================================================================

// ReportMismatch generates a JSON diagnostic report for a duplicate group.
// Returns the report as a JSON string. JS converts it to a Blob for download:
//
//	const blob = new Blob([jsonStr], { type: "application/json" });
func (a *App) ReportMismatch(groupID string) string {
	scanMutex.Lock()
	var target *DuplicateGroup
	for i := range scanResult.Groups {
		if scanResult.Groups[i].ID == groupID {
			g := scanResult.Groups[i]
			target = &g
			break
		}
	}
	scanMutex.Unlock()

	if target == nil {
		return `{"error":"group not found"}`
	}

	type ImageRpt struct {
		Path         string  `json:"path"`
		Filename     string  `json:"filename"`
		Size         int64   `json:"size"`
		Width        int     `json:"width"`
		Height       int     `json:"height"`
		DateTaken    string  `json:"date_taken"`
		Camera       string  `json:"camera"`
		QualityScore int     `json:"quality_score"`
		XXHash       string  `json:"xxhash"`
		DHash        string  `json:"dhash"`
		GPSLat       float64 `json:"gps_lat"`
		GPSLon       float64 `json:"gps_lon"`
	}
	type Report struct {
		ReportType string     `json:"report_type"`
		GroupID    string     `json:"group_id"`
		MatchType  string     `json:"match_type"`
		Confidence float64    `json:"confidence"`
		Timestamp  string     `json:"timestamp"`
		Images     []ImageRpt `json:"images"`
	}

	rpt := Report{
		ReportType: "mismatch_report",
		GroupID:    target.ID,
		MatchType:  target.MatchType,
		Confidence: target.Confidence,
		Timestamp:  time.Now().Format(time.RFC3339),
	}
	for _, img := range target.Images {
		ir := ImageRpt{
			Path: img.Path, Filename: img.Filename, Size: img.Size,
			Width: img.Width, Height: img.Height, DateTaken: img.DateTaken,
			Camera: img.Camera, QualityScore: img.QualityScore,
			GPSLat: img.GPSLat, GPSLon: img.GPSLon,
		}
		if h, err := ComputeXXHash(img.Path); err == nil {
			ir.XXHash = fmt.Sprintf("%016x", h)
		}
		if h, err := ComputeDHash(img.Path); err == nil {
			ir.DHash = fmt.Sprintf("%016x", h)
		}
		rpt.Images = append(rpt.Images, ir)
	}

	b, err := json.Marshal(rpt)
	if err != nil {
		return `{"error":"serialisation failed"}`
	}
	return string(b)
}
