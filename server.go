// =============================================================================
// server.go — HTTP API server for the DedupPhotos web interface
// =============================================================================
//
// This file sets up the HTTP server that powers the web UI. When you open
// http://localhost:8080 in your browser, this code handles all the requests:
// serving the HTML page, starting scans, returning results, serving thumbnails,
// and deleting files.
//
// HOW WEB SERVERS WORK IN GO:
//   Go's standard library includes a production-quality HTTP server. You don't
//   need a framework like Express (Node.js) or Flask (Python). You just:
//   1. Register "handler" functions for URL patterns (routes).
//   2. Call http.ListenAndServe to start listening for connections.
//   Each incoming request runs in its own goroutine automatically, so the
//   server can handle many requests concurrently.
//
// ARCHITECTURE:
//   The frontend (index.html) communicates with this server via JSON APIs:
//   - POST /api/scan     → Start scanning a directory for duplicates.
//   - GET  /api/results  → Poll for scan progress and results.
//   - POST /api/delete   → Delete (rename) a duplicate file.
//   - GET  /api/thumbnail→ Get a resized thumbnail of an image.
//
// THREAD SAFETY:
//   Because requests run concurrently, we protect shared state (scan results,
//   thumbnail cache) with sync.Mutex and sync.Map. Without these, two
//   goroutines could read/write the same data simultaneously, causing bugs.
//
// Key Go concepts:
//   - go:embed:    Embeds files into the binary at compile time.
//   - http.Handler: Interface for handling HTTP requests.
//   - sync.Mutex:  Mutual exclusion lock for protecting shared data.
//   - sync.Map:    Concurrent-safe map (no mutex needed).
//   - goroutine:   Lightweight thread (used for background scanning).
// =============================================================================

package main

import (
	"bytes"    // For working with byte buffers (thumbnail encoding).
	"context"  // For cancellation support.
	_ "embed"  // Required for the //go:embed directive below.
	"encoding/json" // For encoding/decoding JSON (API communication).
	"fmt"           // Formatted I/O.
	"image"         // Standard image interface.
	"image/jpeg"    // JPEG encoder (for thumbnails).
	"log"           // Logging with timestamps.
	"net/http"      // HTTP server and client.
	"os"            // File operations.
	"path/filepath" // For directory browsing.
	"runtime"       // Runtime info (number of CPUs).
	"sort"          // For sorting directory entries.
	"strings"       // For string operations.
	"sync"          // Synchronization primitives (Mutex, Map).
	"time"          // Time measurement.

	// Image format decoders (registered via blank imports).
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
	_ "image/gif"
	_ "image/png"
)

// =============================================================================
// Embedded static files
// =============================================================================

// go:embed is a Go compiler directive that reads a file at compile time and
// stores its contents in a variable. This means the compiled binary contains
// the HTML file — you don't need to ship it separately. Very convenient for
// single-binary deployment.
//
// The comment MUST be exactly "//go:embed <path>" (no space after //).
// The path is relative to the Go source file.
//
//go:embed static/index.html
var indexHTML []byte // The raw bytes of the index.html file.

// =============================================================================
// Global state — protected by a mutex
// =============================================================================

// scanMutex protects the scanResult variable from concurrent access.
// When the background scan goroutine updates scanResult, and the API handler
// reads it simultaneously, the mutex prevents data races.
//
// HOW A MUTEX WORKS:
//   - Lock():   "I'm about to read/write shared data. Wait if someone else
//     has the lock."
//   - Unlock(): "I'm done. Someone else can have the lock now."
//     You MUST always Unlock after Locking, ideally with defer.
var scanMutex sync.Mutex

// scanResult holds the current state of the scanning process. It's read by
// the /api/results endpoint and written by the background scan goroutine.
var scanResult ScanResult

// thumbnailCache stores generated thumbnails to avoid re-computing them.
var thumbnailCache sync.Map

// scanCancel holds the cancel function for the current scan.
// Calling it aborts the scan in progress.
var scanCancel context.CancelFunc

// appSettings holds the current application settings.
var appSettings AppSettings

// AppSettings holds user-configurable settings for the application.
type AppSettings struct {
	Algorithm       string            `json:"algorithm"`        // "dhash", "phash", or "ahash"
	Threshold       int               `json:"threshold"`        // Hamming distance threshold
	MinWidth        int               `json:"min_width"`        // Minimum image width (0 = no limit)
	MaxHeight       int               `json:"max_height"`       // Maximum image height (0 = no limit)
	Extensions      map[string]bool   `json:"extensions"`       // Extension -> enabled
}

func init() {
	// Initialize default settings.
	appSettings = AppSettings{
		Algorithm: "dhash",
		Threshold: 10,
		MinWidth:  0,
		MaxHeight: 0,
		Extensions: map[string]bool{
			".jpg": true, ".jpeg": true, ".png": true,
			".tiff": true, ".tif": true, ".bmp": true,
			".webp": true, ".gif": true, ".heic": true, ".heif": true,
		},
	}
}

// =============================================================================
// Types — JSON request/response structures
// =============================================================================

// ScanRequest is the JSON body sent by the frontend when starting a scan.
// The `json:"path"` tags tell Go's JSON decoder which JSON field maps to
// which struct field.
type ScanRequest struct {
	Path      string `json:"path"`      // Directory path to scan for duplicates.
	Threshold int    `json:"threshold"` // Hamming distance threshold for perceptual matching.
}

// DeleteRequest is the JSON body sent when the user wants to delete a file.
type DeleteRequest struct {
	Path string `json:"path"` // Path to the file to delete (rename).
}

// ScanProgress reports how far along the current scan is. The frontend uses
// this to update a progress bar.
type ScanProgress struct {
	Current int    `json:"current"` // Number of items processed so far.
	Total   int    `json:"total"`   // Total number of items to process.
	Phase   string `json:"phase"`   // Human-readable description of the current phase.
}

// ScanStats contains summary statistics about a completed scan.
type ScanStats struct {
	TotalFiles      int   `json:"total_files"`      // Total image files found.
	DuplicateGroups int   `json:"duplicate_groups"` // Number of duplicate groups detected.
	WastedBytes     int64 `json:"wasted_bytes"`     // Total bytes that could be freed.
	DurationMs      int64 `json:"duration_ms"`      // How long the scan took in milliseconds.
}

// ScanResult is the complete response from /api/results. It combines status,
// progress, statistics, and the actual duplicate groups.
type ScanResult struct {
	Status   string           `json:"status"`   // "idle", "scanning", or "complete".
	Progress ScanProgress     `json:"progress"` // Current progress (only meaningful during scanning).
	Stats    ScanStats        `json:"stats"`    // Summary statistics (only meaningful when complete).
	Groups   []DuplicateGroup `json:"groups"`   // The duplicate groups found.
}

// =============================================================================
// StartServer — Initialize and start the HTTP server
// =============================================================================

// StartServer registers all HTTP routes and starts listening for connections.
// This function blocks forever (until the process is killed), because the
// HTTP server runs in an infinite loop accepting connections.
//
// Parameters:
//   - port: The port number to listen on (e.g., 8080).
func StartServer(port int) {
	// -------------------------------------------------------------------------
	// Register HTTP routes (URL patterns → handler functions)
	// -------------------------------------------------------------------------
	//
	// http.HandleFunc maps a URL pattern to a handler function. When a
	// request matches the pattern, Go calls the handler function with:
	//   - w: http.ResponseWriter — used to send the response.
	//   - r: *http.Request — contains the request details (method, URL, body).

	// GET / → Serve the embedded HTML page.
	// This is the main entry point: when you visit http://localhost:8080/,
	// you get the DedupPhotos web interface.
	http.HandleFunc("/", handleIndex)

	// POST /api/scan → Start scanning a directory for duplicates.
	http.HandleFunc("/api/scan", handleScan)

	// GET /api/results → Get the current scan status, progress, and results.
	http.HandleFunc("/api/results", handleResults)

	// POST /api/delete → Delete (rename) a file.
	http.HandleFunc("/api/delete", handleDelete)

	// GET /api/thumbnail?path=<filepath> → Get a resized thumbnail of an image.
	http.HandleFunc("/api/thumbnail", handleThumbnail)

	// GET /api/browse?path=<dirpath> → List directories for folder browser.
	http.HandleFunc("/api/browse", handleBrowse)

	// POST /api/cancel → Cancel the current scan.
	http.HandleFunc("/api/cancel", handleCancel)

	// POST /api/report-mismatch → Generate a mismatch report for two images.
	http.HandleFunc("/api/report-mismatch", handleReportMismatch)

	// GET/POST /api/settings → Get or update application settings.
	http.HandleFunc("/api/settings", handleSettings)

	// -------------------------------------------------------------------------
	// Start the HTTP server
	// -------------------------------------------------------------------------

	// Build the address string. fmt.Sprintf formats a string (like printf).
	// ":%d" means "listen on all interfaces, port <d>".
	addr := fmt.Sprintf(":%d", port)

	// Log the startup message. log.Printf adds a timestamp automatically.
	log.Printf("HTTP server listening on %s", addr)

	// http.ListenAndServe starts the server. It blocks forever, processing
	// incoming requests. If it returns, something went wrong (e.g., the
	// port is already in use).
	err := http.ListenAndServe(addr, nil)
	if err != nil {
		// log.Fatalf prints an error message and exits the program.
		log.Fatalf("Server failed to start: %v", err)
	}
}

// =============================================================================
// setCORSHeaders — Add CORS headers to every response
// =============================================================================

// setCORSHeaders adds Cross-Origin Resource Sharing (CORS) headers to the
// response. CORS is a browser security feature that blocks requests from
// one origin (e.g., localhost:3000) to another (e.g., localhost:8080).
//
// By setting "Access-Control-Allow-Origin: *", we allow requests from any
// origin. This is fine for a local development tool but would be a security
// risk for a production web app.
//
// Parameters:
//   - w: The response writer to add headers to.
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

// =============================================================================
// handleIndex — Serve the main HTML page
// =============================================================================

// handleIndex serves the embedded index.html file. This is what you see when
// you open http://localhost:8080/ in your browser.
func handleIndex(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers.
	setCORSHeaders(w)

	// Only serve the index page for the exact "/" path. Other paths (like
	// "/favicon.ico") should get a 404.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Set the Content-Type header so the browser knows to render it as HTML.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Write the embedded HTML bytes to the response.
	// w.Write sends data to the client (browser).
	w.Write(indexHTML)

	log.Printf("GET / — served index.html (%d bytes)", len(indexHTML))
}

// =============================================================================
// handleScan — Start a duplicate-detection scan
// =============================================================================

// handleScan handles POST requests to /api/scan. It reads the scan parameters
// from the JSON body, validates them, and launches the scan in a background
// goroutine so the request can return immediately.
//
// The frontend then polls /api/results to track progress and get the final
// results.
func handleScan(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)

	// Handle CORS preflight requests. Browsers send an OPTIONS request
	// before POST requests to check if CORS is allowed.
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Only accept POST requests.
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	log.Printf("POST /api/scan — received scan request")

	// -------------------------------------------------------------------------
	// Parse the JSON request body
	// -------------------------------------------------------------------------

	// json.NewDecoder creates a JSON decoder that reads from r.Body (the
	// request body). Decode() parses the JSON into our ScanRequest struct.
	var req ScanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	// Validate the path. It must be a non-empty string pointing to a directory.
	if req.Path == "" {
		http.Error(w, `{"error":"path is required"}`, http.StatusBadRequest)
		return
	}

	// Check that the path exists and is a directory.
	info, err := os.Stat(req.Path)
	if err != nil {
		msg := fmt.Sprintf(`{"error":"path does not exist: %s"}`, req.Path)
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if !info.IsDir() {
		http.Error(w, `{"error":"path is not a directory"}`, http.StatusBadRequest)
		return
	}

	// Default the threshold to 10 if not provided (or 0).
	// A threshold of 10 means we consider two images as perceptual duplicates
	// if their dHash values differ in 10 or fewer bits (out of 64).
	if req.Threshold <= 0 {
		req.Threshold = 10
	}

	// -------------------------------------------------------------------------
	// Initialize scan state
	// -------------------------------------------------------------------------

	// Lock the mutex before modifying shared state. defer ensures Unlock
	// happens when this block exits.
	scanMutex.Lock()
	scanResult = ScanResult{
		Status: "scanning",
		Progress: ScanProgress{
			Phase: "Starting scan...",
		},
	}
	scanMutex.Unlock()

	// Clear the thumbnail cache for the new scan.
	thumbnailCache = sync.Map{}

	// Cancel any previous scan.
	if scanCancel != nil {
		scanCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	scanCancel = cancel

	// Use settings threshold if request doesn't specify one, or use the request value.
	threshold := req.Threshold
	if threshold <= 0 {
		threshold = appSettings.Threshold
		if threshold <= 0 {
			threshold = 10
		}
	}

	go func() {
		startTime := time.Now()

		log.Printf("[scan] Starting scan of: %s (threshold: %d)", req.Path, threshold)

		scanMutex.Lock()
		scanResult.Progress.Phase = "Scanning directory for images..."
		scanMutex.Unlock()

		paths, err := ScanDirectory(req.Path)
		if err != nil {
			log.Printf("[scan] Error scanning directory: %v", err)
			scanMutex.Lock()
			scanResult.Status = "complete"
			scanResult.Progress.Phase = fmt.Sprintf("Error: %v", err)
			scanMutex.Unlock()
			return
		}

		// Filter paths based on settings (extensions, min width, max height).
		paths = filterPathsBySettings(paths)

		totalFiles := len(paths)
		log.Printf("[scan] Found %d image files (after filtering)", totalFiles)

		// Check for cancellation.
		if ctx.Err() != nil {
			setScanCancelled()
			return
		}

		scanMutex.Lock()
		scanResult.Progress.Total = totalFiles
		scanResult.Progress.Phase = "Hashing images..."
		scanMutex.Unlock()

		if totalFiles == 0 {
			scanMutex.Lock()
			scanResult.Status = "complete"
			scanResult.Progress.Phase = "No images found"
			scanResult.Stats = ScanStats{
				TotalFiles: 0,
				DurationMs: time.Since(startTime).Milliseconds(),
			}
			scanMutex.Unlock()
			return
		}

		numWorkers := runtime.NumCPU()
		hashes := HashAllImagesWithContext(ctx, paths, numWorkers)

		// Check for cancellation.
		if ctx.Err() != nil {
			setScanCancelled()
			return
		}

		scanMutex.Lock()
		scanResult.Progress.Current = totalFiles
		scanResult.Progress.Phase = "Grouping duplicates..."
		scanMutex.Unlock()

		groups := GroupDuplicates(hashes, threshold)

		// Check for cancellation.
		if ctx.Err() != nil {
			setScanCancelled()
			return
		}

		var wastedBytes int64
		for _, group := range groups {
			for i, img := range group.Images {
				if i > 0 {
					wastedBytes += img.Size
				}
			}
		}

		duration := time.Since(startTime)

		scanMutex.Lock()
		scanResult = ScanResult{
			Status: "complete",
			Progress: ScanProgress{
				Current: totalFiles,
				Total:   totalFiles,
				Phase:   "Complete",
			},
			Stats: ScanStats{
				TotalFiles:      totalFiles,
				DuplicateGroups: len(groups),
				WastedBytes:     wastedBytes,
				DurationMs:      duration.Milliseconds(),
			},
			Groups: groups,
		}
		scanMutex.Unlock()

		log.Printf("[scan] Scan complete! %d files, %d groups, %d wasted bytes, took %v",
			totalFiles, len(groups), wastedBytes, duration)
	}()

	// -------------------------------------------------------------------------
	// Return immediate response (scan is running in background)
	// -------------------------------------------------------------------------

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "scanning",
		"message": "Scan started",
	})
}

// =============================================================================
// handleResults — Return current scan status and results
// =============================================================================

// handleResults handles GET requests to /api/results. It returns the current
// scan state as JSON, including progress (if scanning) or final results
// (if complete).
func handleResults(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)

	// Handle CORS preflight.
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Lock the mutex while reading shared state. Even reads need locking
	// because another goroutine (the scan) might be writing simultaneously.
	scanMutex.Lock()
	// Make a copy of the result so we can release the lock quickly.
	// This is important for performance — we don't want to hold the lock
	// while encoding JSON (which involves I/O).
	result := scanResult
	scanMutex.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// =============================================================================
// handleDelete — Delete (rename) a file
// =============================================================================

// handleDelete handles POST requests to /api/delete. Instead of permanently
// deleting the file (which is scary and irreversible!), we rename it by
// appending ".deleted" to the filename. This way, the user can manually
// recover the file if they change their mind.
//
// For example: /photos/sunset.jpg → /photos/sunset.jpg.deleted
func handleDelete(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)

	// Handle CORS preflight.
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// Parse the JSON request body.
	var req DeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	if req.Path == "" {
		http.Error(w, `{"error":"path is required"}`, http.StatusBadRequest)
		return
	}

	log.Printf("POST /api/delete — path: %s", req.Path)

	// -------------------------------------------------------------------------
	// Safety check: Make sure the path exists and is a regular file
	// -------------------------------------------------------------------------
	info, err := os.Stat(req.Path)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("file not found: %s", req.Path),
		})
		return
	}

	// IsDir() returns true if the path is a directory. We only delete files,
	// not directories, for safety.
	if info.IsDir() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "cannot delete a directory",
		})
		return
	}

	// -------------------------------------------------------------------------
	// SAFETY: Rename the file instead of deleting it
	// -------------------------------------------------------------------------
	//
	// os.Rename moves/renames a file. We append ".deleted" to the filename
	// so the user can find and recover it later. This is much safer than
	// os.Remove which would permanently delete the file.
	//
	// Example: /photos/sunset.jpg → /photos/sunset.jpg.deleted
	deletedPath := req.Path + ".deleted"
	err = os.Rename(req.Path, deletedPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to rename file: %v", err),
		})
		return
	}

	// Remove the thumbnail from cache since the file is gone.
	thumbnailCache.Delete(req.Path)

	log.Printf("POST /api/delete — renamed %s to %s", req.Path, deletedPath)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("File renamed to %s", deletedPath),
	})
}

// =============================================================================
// setScanCancelled — Helper to mark scan as cancelled
// =============================================================================

func setScanCancelled() {
	scanMutex.Lock()
	scanResult.Status = "cancelled"
	scanResult.Progress.Phase = "Scan cancelled by user"
	scanMutex.Unlock()
	log.Printf("[scan] Scan cancelled by user")
}

// =============================================================================
// filterPathsBySettings — Filter image paths based on current app settings
// =============================================================================

func filterPathsBySettings(paths []string) []string {
	var filtered []string
	for _, p := range paths {
		ext := strings.ToLower(filepath.Ext(p))
		if enabled, ok := appSettings.Extensions[ext]; ok && !enabled {
			continue // Extension is disabled in settings.
		}
		filtered = append(filtered, p)
	}

	// If min width or max height is set, we need to check image dimensions.
	if appSettings.MinWidth > 0 || appSettings.MaxHeight > 0 {
		var dimFiltered []string
		for _, p := range filtered {
			f, err := os.Open(p)
			if err != nil {
				dimFiltered = append(dimFiltered, p) // Keep on error.
				continue
			}
			config, _, err := image.DecodeConfig(f)
			f.Close()
			if err != nil {
				dimFiltered = append(dimFiltered, p) // Keep on error.
				continue
			}
			if appSettings.MinWidth > 0 && config.Width < appSettings.MinWidth {
				continue
			}
			if appSettings.MaxHeight > 0 && config.Height > appSettings.MaxHeight {
				continue
			}
			dimFiltered = append(dimFiltered, p)
		}
		return dimFiltered
	}

	return filtered
}

// =============================================================================
// handleBrowse — List directories for folder browser
// =============================================================================

func handleBrowse(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	requestedPath := r.URL.Query().Get("path")
	if requestedPath == "" {
		// Default to user's home directory.
		home, err := os.UserHomeDir()
		if err != nil {
			requestedPath = "/"
		} else {
			requestedPath = home
		}
	}

	// Clean and resolve the path.
	requestedPath = filepath.Clean(requestedPath)

	info, err := os.Stat(requestedPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Path not found: " + requestedPath})
		return
	}
	if !info.IsDir() {
		requestedPath = filepath.Dir(requestedPath)
	}

	entries, err := os.ReadDir(requestedPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Cannot read directory: " + err.Error()})
		return
	}

	type DirEntry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}

	var dirs []DirEntry
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue // Skip hidden directories.
		}
		dirs = append(dirs, DirEntry{
			Name: entry.Name(),
			Path: filepath.Join(requestedPath, entry.Name()),
		})
	}

	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name)
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"current": requestedPath,
		"parent":  filepath.Dir(requestedPath),
		"dirs":    dirs,
	})
}

// =============================================================================
// handleCancel — Cancel the current scan
// =============================================================================

func handleCancel(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	if scanCancel != nil {
		scanCancel()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Scan cancellation requested",
	})
}

// =============================================================================
// handleReportMismatch — Generate a mismatch report
// =============================================================================

func handleReportMismatch(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		GroupID string   `json:"group_id"`
		Paths   []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	type ImageReport struct {
		Path       string `json:"path"`
		Filename   string `json:"filename"`
		Size       int64  `json:"size"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		XXHash     string `json:"xxhash"`
		DHash      string `json:"dhash"`
		DateTaken  string `json:"date_taken"`
		Camera     string `json:"camera"`
	}

	var images []ImageReport
	for _, p := range req.Paths {
		meta := ExtractMetadata(p)
		xxh, _ := ComputeXXHash(p)
		dh, _ := ComputeDHash(p)
		images = append(images, ImageReport{
			Path:      meta.Path,
			Filename:  meta.Filename,
			Size:      meta.Size,
			Width:     meta.Width,
			Height:    meta.Height,
			XXHash:    fmt.Sprintf("%016x", xxh),
			DHash:     fmt.Sprintf("%016x", dh),
			DateTaken: meta.DateTaken,
			Camera:    meta.Camera,
		})
	}

	// Compute hamming distance between the pair if exactly 2 images.
	hammingDist := -1
	if len(req.Paths) == 2 {
		dh1, err1 := ComputeDHash(req.Paths[0])
		dh2, err2 := ComputeDHash(req.Paths[1])
		if err1 == nil && err2 == nil {
			hammingDist = HammingDistance(dh1, dh2)
		}
	}

	report := map[string]interface{}{
		"report_type":      "mismatch",
		"group_id":         req.GroupID,
		"images":           images,
		"hamming_distance":  hammingDist,
		"threshold_used":   appSettings.Threshold,
		"algorithm":        appSettings.Algorithm,
		"generated_at":     time.Now().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=mismatch_report.json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(report)
}

// =============================================================================
// handleSettings — Get or update application settings
// =============================================================================

func handleSettings(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method == "GET" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(appSettings)
		return
	}

	if r.Method == "POST" {
		var newSettings AppSettings
		if err := json.NewDecoder(r.Body).Decode(&newSettings); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}
		// Validate.
		if newSettings.Algorithm == "" {
			newSettings.Algorithm = "dhash"
		}
		if newSettings.Threshold <= 0 {
			newSettings.Threshold = 10
		}
		if newSettings.Extensions == nil {
			newSettings.Extensions = appSettings.Extensions
		}
		appSettings = newSettings
		log.Printf("[settings] Updated: algorithm=%s threshold=%d minWidth=%d maxHeight=%d",
			appSettings.Algorithm, appSettings.Threshold, appSettings.MinWidth, appSettings.MaxHeight)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":  true,
			"settings": appSettings,
		})
		return
	}

	http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
}

// =============================================================================
// handleThumbnail — Generate and serve image thumbnails
// =============================================================================

// handleThumbnail handles GET requests to /api/thumbnail?path=<filepath>.
// It opens the image, resizes it to a maximum of 400px on the longest side
// (maintaining aspect ratio), encodes it as JPEG, and serves it to the browser.
//
// Thumbnails are cached in memory (thumbnailCache) so repeated requests for
// the same image don't require re-processing.
func handleThumbnail(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)

	// Read the "path" query parameter from the URL.
	// For example, /api/thumbnail?path=/photos/sunset.jpg
	// r.URL.Query().Get("path") extracts the value of "path".
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path parameter is required", http.StatusBadRequest)
		return
	}

	// -------------------------------------------------------------------------
	// Check the cache first
	// -------------------------------------------------------------------------
	//
	// sync.Map.Load returns (value, true) if the key exists, or (nil, false)
	// if it doesn't. If we have a cached thumbnail, serve it immediately.
	if cached, ok := thumbnailCache.Load(path); ok {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=3600") // Cache for 1 hour.
		w.Write(cached.([]byte))                                // Type assertion: convert interface{} to []byte.
		return
	}

	// -------------------------------------------------------------------------
	// Open and decode the original image
	// -------------------------------------------------------------------------
	file, err := os.Open(path)
	if err != nil {
		http.Error(w, fmt.Sprintf("cannot open file: %v", err), http.StatusNotFound)
		return
	}
	defer file.Close()

	// image.Decode automatically detects the format and decodes the image.
	img, _, err := image.Decode(file)
	if err != nil {
		http.Error(w, fmt.Sprintf("cannot decode image: %v", err), http.StatusInternalServerError)
		return
	}

	// -------------------------------------------------------------------------
	// Resize the image to max 400px on the longest side
	// -------------------------------------------------------------------------
	//
	// We maintain the aspect ratio so the image doesn't look stretched.
	// If the image is already smaller than 400px, we don't upscale it.

	bounds := img.Bounds()
	srcWidth := bounds.Max.X - bounds.Min.X
	srcHeight := bounds.Max.Y - bounds.Min.Y

	const maxDim = 400 // Maximum dimension (width or height) in pixels.

	// Calculate the new dimensions while maintaining aspect ratio.
	newWidth := srcWidth
	newHeight := srcHeight

	if srcWidth > maxDim || srcHeight > maxDim {
		if srcWidth >= srcHeight {
			// Landscape or square: constrain by width.
			newWidth = maxDim
			// Calculate proportional height. Integer division is fine here.
			newHeight = srcHeight * maxDim / srcWidth
		} else {
			// Portrait: constrain by height.
			newHeight = maxDim
			newWidth = srcWidth * maxDim / srcHeight
		}
	}

	// Ensure minimum dimensions of 1×1 (avoid division by zero).
	if newWidth < 1 {
		newWidth = 1
	}
	if newHeight < 1 {
		newHeight = 1
	}

	// Create a new RGBA image for the thumbnail.
	// image.NewRGBA allocates a new image with the given dimensions.
	thumb := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))

	// Nearest-neighbor resize: for each pixel in the thumbnail, sample the
	// corresponding pixel from the original image.
	for y := 0; y < newHeight; y++ {
		for x := 0; x < newWidth; x++ {
			// Map thumbnail coordinates back to original image coordinates.
			srcX := bounds.Min.X + (x * srcWidth / newWidth)
			srcY := bounds.Min.Y + (y * srcHeight / newHeight)

			// Copy the color from the source to the thumbnail.
			thumb.Set(x, y, img.At(srcX, srcY))
		}
	}

	// -------------------------------------------------------------------------
	// Encode as JPEG and cache the result
	// -------------------------------------------------------------------------

	// bytes.Buffer is an in-memory byte buffer that implements io.Writer.
	// We encode the JPEG into this buffer, then serve it from the buffer.
	var buf bytes.Buffer

	// jpeg.Encode writes the image as JPEG to the buffer.
	// Quality 85 is a good balance between file size and visual quality.
	err = jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 85})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to encode thumbnail: %v", err), http.StatusInternalServerError)
		return
	}

	// Get the encoded bytes.
	thumbnailBytes := buf.Bytes()

	// Store in cache for future requests. sync.Map.Store is thread-safe.
	thumbnailCache.Store(path, thumbnailBytes)

	// -------------------------------------------------------------------------
	// Serve the thumbnail
	// -------------------------------------------------------------------------
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(thumbnailBytes)
}
