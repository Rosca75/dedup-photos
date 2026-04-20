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

	"golang.org/x/image/draw"

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
	initHEIC()
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

	// Phase 1: Walk the primary path via walkScanRoot, which routes to the
	// concurrent or filtered walker depending on which filters are active.
	t0 := time.Now()
	setPhase("Scanning directory...")

	paths, err := walkScanRoot(req.Path, allowedExts, req)
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
// Called by JS once per scan, when it sees status transition to "complete".
// While scanning, JS uses the much cheaper GetProgress() instead so the
// 500 ms poller doesn't serialise thousands of duplicate groups on every tick.
func (a *App) GetResults() ScanResult {
	scanMutex.Lock()
	defer scanMutex.Unlock()
	return scanResult
}

// ScanProgressUpdate is the lightweight payload returned by GetProgress.
// It carries only the fields the UI needs during polling — no duplicate groups.
type ScanProgressUpdate struct {
	Status   string       `json:"status"`
	Progress ScanProgress `json:"progress"`
}

// GetProgress returns only the scan status and progress — no duplicate groups.
// The frontend polls this every 500 ms during an active scan, avoiding the
// multi-MB serialisation cost of GetResults() until the scan actually finishes.
func (a *App) GetProgress() ScanProgressUpdate {
	scanMutex.Lock()
	defer scanMutex.Unlock()
	return ScanProgressUpdate{
		Status:   scanResult.Status,
		Progress: scanResult.Progress,
	}
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

	// HEIC/HEIF fast path: use embedded thumbnail when available.
	if isHEIC(path) {
		jpegBytes, err := heicThumbnailJPEG(path)
		if err != nil {
			return ""
		}
		thumbnailCache.Store(path, jpegBytes)
		return base64.StdEncoding.EncodeToString(jpegBytes)
	}

	// Open and decode the image file.
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		// Fallback for RAW formats (DNG, ARW, CR2): scan for embedded JPEG preview.
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

	// Bilinear resize via golang.org/x/image/draw — uses pixel-format-specific
	// fast paths and avoids the color.Color interface entirely, cutting per-
	// thumbnail CPU from tens of ms to single digits.
	dstRect := image.Rect(0, 0, newW, newH)
	thumb := image.NewRGBA(dstRect)
	draw.ApproxBiLinear.Scale(thumb, dstRect, img, b, draw.Src, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 85}); err != nil {
		return ""
	}
	jpegBytes := buf.Bytes()
	thumbnailCache.Store(path, jpegBytes)
	return base64.StdEncoding.EncodeToString(jpegBytes)
}

// =============================================================================
// GetImageQualityMetrics
// =============================================================================

// GetImageQualityMetrics computes blockiness and blurring scores for a single
// image. Called lazily by the frontend when the user expands a duplicate group,
// so the expensive full-image decode only happens for images the user views.
//
// This was moved out of the scan pipeline because ComputeImageQualityMetrics
// does a full JPEG decode (~150 ms/file on USB), which was causing the grouping
// phase to take 8+ minutes for large scans on external drives.
//
// Returns a map with "blockiness" and "blurring" float64 values (both 0.0 if
// the image cannot be decoded).
func (a *App) GetImageQualityMetrics(path string) map[string]interface{} {
	blockiness, blurring := ComputeImageQualityMetrics(path)
	return map[string]interface{}{
		"blockiness": blockiness,
		"blurring":   blurring,
	}
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
		// Use cached hash values from the scan — no file re-reads needed.
		if img.XXHash != 0 {
			ir.XXHash = fmt.Sprintf("%016x", img.XXHash)
		}
		if img.DHash != 0 {
			ir.DHash = fmt.Sprintf("%016x", img.DHash)
		}
		rpt.Images = append(rpt.Images, ir)
	}

	b, err := json.Marshal(rpt)
	if err != nil {
		return `{"error":"serialisation failed"}`
	}
	return string(b)
}
