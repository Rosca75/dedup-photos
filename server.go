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
//   - POST /api/set-folder → Set the folder path for scanning.
//   - POST /api/cancel-scan → Cancel a running scan.
//   - POST /api/report-mismatch → Report a mismatch between two images.
//   - GET  /api/get-settings → Get the current settings.
//   - POST /api/set-settings → Update the settings.
//   - POST /api/apply-action → Apply bulk actions to groups of duplicates.
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
	_ "embed" // Required for the //go:embed directive below.
	"encoding/json" // For encoding/decoding JSON (API communication).
	"fmt"           // Formatted I/O.
	"log"           // Logging with timestamps.
	"net/http"      // HTTP server and client.
	"os"            // File operations.
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
// ===========================================================================

//go:embed static/index.html
var indexHTML []byte

// ===========================================================================
// Global state — protected by a mutex
// ===========================================================================

var scanMutex sync.Mutex
var scanResult ScanResult
var thumbnailCache sync.Map
var scanCancel chan struct{} // Add this for canceling scans

// Global settings for the application
type Settings struct {
	Algorithm   string   `json:"algorithm"`
	Threshold   int      `json:"threshold"`
	MinWidth    int      `json:"min_width"`
	MaxHeight   int      `json:"max_height"`
	Extensions  []string `json:"extensions"`
	ScanPath    string   `json:"scan_path"`
}

var globalSettings = Settings{
	Algorithm:  "phash",
	Threshold:  10,
	MinWidth:   0,
	MaxHeight:  0,
	Extensions: []string{"jpg", "jpeg", "png", "gif"},
	ScanPath:   "",
}

// ===========================================================================
// Types — JSON request/response structures
// ===========================================================================

type ScanRequest struct {
	Path      string `json:"path"`
	Threshold int    `json:"threshold"`
}

type DeleteRequest struct {
	Path string `json:"path"`
}

type SetFolderRequest struct {
	Path string `json:"path"`
}

type ScanProgress struct {
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Phase   string `json:"phase"`
}

type ScanStats struct {
	TotalFiles      int   `json:"total_files"`
	DuplicateGroups int   `json:"duplicate_groups"`
	WastedBytes     int64 `json:"wasted_bytes"`
	DurationMs      int64 `json:"duration_ms"`
}

type DuplicateGroup struct {
	MatchType   string `json:"match_type"`
	Confidence  float64 `json:"confidence"`
	Images      []struct {
		Path         string `json:"path"`
		Filename     string `json:"filename"`
		Size         int64  `json:"size"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		DateTaken    string `json:"date_taken"`
		GPSLat       string `json:"gps_lat"`
		GPSLon       string `json:"gps_lon"`
		Camera       string `json:"camera"`
		Lens         string `json:"lens"`
		ISO          int    `json:"iso"`
		QualityScore int    `json:"quality_score"`
		IsBest       bool   `json:"is_best"`
	} `json:"images"`
}

type ScanResult struct {
	Status   string           `json:"status"`
	Progress ScanProgress     `json:"progress"`
	Stats    ScanStats        `json:"stats"`
	Groups   []DuplicateGroup `json:"groups"`
}

// =============================================================================
// StartServer — Initialize and start the HTTP server
// ===========================================================================

func StartServer(port int) {
	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/api/scan", handleScan)
	http.HandleFunc("/api/results", handleResults)
	http.HandleFunc("/api/delete", handleDelete)
	http.HandleFunc("/api/thumbnail", handleThumbnail)
	http.HandleFunc("/api/set-folder", handleSetFolder)
	http.HandleFunc("/api/cancel-scan", handleCancelScan)
	http.HandleFunc("/api/report-mismatch", handleReportMismatch)
	http.HandleFunc("/api/get-settings", handleGetSettings)
	http.HandleFunc("/api/set-settings", handleSetSettings)
	http.HandleFunc("/api/apply-action", handleApplyAction)

	addr := fmt.Sprintf(":%d", port)
	log.Printf("HTTP server listening on %s", addr)
	err := http.ListenAndServe(addr, nil)
	if err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}

// ===========================================================================
// setCORSHeaders — Add CORS headers to every response
// ===========================================================================

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

// ===========================================================================
// New Handlers
// ===========================================================================

func handleSetFolder(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req SetFolderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	globalSettings.ScanPath = req.Path
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func handleCancelScan(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if scanCancel != nil {
		close(scanCancel)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func handleReportMismatch(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Path1 string `json:"path1"`
		Path2 string `json:"path2"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	log.Printf("[mismatch] Reported: %s vs %s", req.Path1, req.Path2)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func handleGetSettings(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(globalSettings)
}

func handleSetSettings(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req Settings
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	globalSettings = req
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func handleApplyAction(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Action string   `json:"action"`
		Groups []string `json:"groups"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	// Placeholder: Implement bulk action logic here
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// ===========================================================================
// Existing Handlers (handleIndex, handleScan, handleResults, handleDelete, handleThumbnail)
// ... (keep all existing handler code as is from your original file)
// ===========================================================================