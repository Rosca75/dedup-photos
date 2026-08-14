// types.go — Shared request/response types serialised between Go backend and JS frontend via Wails.
package main

// Action represents a reversible delete (soft-delete via rename to .deleted).
type Action struct {
	Type      string `json:"type"`       // "delete"
	Path      string `json:"path"`       // Original file path.
	TrashPath string `json:"trash_path"` // Renamed path (.deleted suffix).
	Timestamp int64  `json:"timestamp"`  // Unix milliseconds.
}

// ScanRequest is passed by the JS frontend when starting a scan.
// Wails automatically deserialises the JS object into this struct.
type ScanRequest struct {
	Path              string   `json:"path"`               // Primary directory to scan.
	Threshold         int      `json:"threshold"`          // Hamming distance threshold (0–100).
	Algorithm         string   `json:"algorithm"`          // "dhash", "phash", or "both".
	MinWidth          int      `json:"min_width"`          // Minimum image width in px (0=no limit).
	MaxHeight         int      `json:"max_height"`         // Maximum image height in px (0=no limit).
	Extensions        []string `json:"extensions"`         // e.g. ["jpg","png"] (empty=all).
	NormalisedSize    int      `json:"normalised_size"`    // Hash grid size: 16, 32, 64, or 128.
	IncludeSubfolders bool     `json:"include_subfolders"` // Recurse into subdirectories.
	MinFileSize       int64    `json:"min_file_size"`      // Minimum file size in bytes.
	MaxFileSize       int64    `json:"max_file_size"`      // Maximum file size in bytes.
	ExtraPaths        []string `json:"extra_paths"`        // Additional directories to scan.
	IncludeSeries     bool     `json:"include_series"`     // Include burst/series groups in results.
}

// ReportMismatchRequest is the argument to ReportMismatch().
type ReportMismatchRequest struct {
	GroupID string `json:"group_id"` // UUID of the duplicate group.
}

// DeleteRequest is the argument to DeleteFile().
type DeleteRequest struct {
	Path string `json:"path"` // Absolute path of the file to delete.
}

// ScanProgress reports how far along the current scan is.
type ScanProgress struct {
	Current int    `json:"current"` // Items processed so far.
	Total   int    `json:"total"`   // Total items to process.
	Phase   string `json:"phase"`   // Human-readable current activity.
}

// ScanStats contains summary statistics for a completed scan.
type ScanStats struct {
	TotalFiles      int   `json:"total_files"`      // Image files found.
	DuplicateGroups int   `json:"duplicate_groups"` // Duplicate groups detected.
	WastedBytes     int64 `json:"wasted_bytes"`     // Bytes that could be freed.
	DurationMs      int64 `json:"duration_ms"`      // Scan duration in milliseconds.
}

// ScanResult is the full response returned by GetResults().
// Status is "idle", "scanning", "complete", or "cancelled".
type ScanResult struct {
	Status   string           `json:"status"`
	Progress ScanProgress     `json:"progress"`
	Stats    ScanStats        `json:"stats"`
	Groups   []DuplicateGroup `json:"groups"`
}
