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
	"bytes"   // For working with byte buffers (thumbnail encoding).
	_ "embed" // Required for the //go:embed directive below. The blank import
	// ("_") tells Go "I need this package's side effects but won't call
	// any of its functions directly."
	"encoding/json" // For encoding/decoding JSON (API communication).
	"fmt"           // Formatted I/O.
	"image"         // Standard image interface.
	"image/jpeg"    // JPEG encoder (for thumbnails).
	"log"           // Logging with timestamps.
	"net/http"      // HTTP server and client.
	"os"            // File operations.
	"runtime"       // Runtime info (number of CPUs).
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
// sync.Map is a concurrent-safe map that doesn't need a mutex.
// Keys are file paths (string), values are JPEG byte slices ([]byte).
var thumbnailCache sync.Map

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

	// -------------------------------------------------------------------------
	// Launch the scan in a background goroutine
	// -------------------------------------------------------------------------
	//
	// We use "go func() { ... }()" to run the scan in a separate goroutine.
	// This means handleScan returns immediately (the HTTP response is sent
	// to the client), while the actual scanning happens in the background.
	// The frontend polls /api/results to track progress.
	go func() {
		startTime := time.Now()

		log.Printf("[scan] Starting scan of: %s (threshold: %d)", req.Path, req.Threshold)

		// -----------------------------------------------------------------
		// Phase 1: Scan the directory for image files
		// -----------------------------------------------------------------
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

		totalFiles := len(paths)
		log.Printf("[scan] Found %d image files", totalFiles)

		scanMutex.Lock()
		scanResult.Progress.Total = totalFiles
		scanResult.Progress.Phase = "Hashing images..."
		scanMutex.Unlock()

		if totalFiles == 0 {
			// No images found — complete immediately with empty results.
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

		// -----------------------------------------------------------------
		// Phase 2: Hash all images using the worker pool
		// -----------------------------------------------------------------
		//
		// We use runtime.NumCPU() workers to parallelize hashing across
		// all CPU cores. This is the most time-consuming phase.
		numWorkers := runtime.NumCPU()
		hashes := HashAllImages(paths, numWorkers)

		scanMutex.Lock()
		scanResult.Progress.Current = totalFiles
		scanResult.Progress.Phase = "Grouping duplicates..."
		scanMutex.Unlock()

		// -----------------------------------------------------------------
		// Phase 3: Group duplicates
		// -----------------------------------------------------------------
		groups := GroupDuplicates(hashes, req.Threshold)

		// -----------------------------------------------------------------
		// Phase 4: Compute statistics
		// -----------------------------------------------------------------
		//
		// Calculate "wasted bytes" — the total size of all duplicate files
		// (excluding the best one in each group, which we'd keep).
		var wastedBytes int64
		for _, group := range groups {
			for i, img := range group.Images {
				if i > 0 { // Skip the first image (the "best" one to keep).
					wastedBytes += img.Size
				}
			}
		}

		duration := time.Since(startTime)

		// -----------------------------------------------------------------
		// Store final results
		// -----------------------------------------------------------------
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
