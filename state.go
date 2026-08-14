// state.go — Global scan state and undo/redo stacks. All access is mutex-guarded.
package main

import (
	"context" // context.CancelFunc type for scan cancellation.
	"sync"    // sync.Mutex and sync.Map for thread-safe state access.
)

// scanMutex protects scanResult and scanCancel from concurrent access.
// The background scan goroutine writes these; GetResults() reads them.
var scanMutex sync.Mutex

// scanResult holds the latest scan state (idle / scanning / complete).
// Written by the scan goroutine; read by GetResults().
var scanResult ScanResult

// thumbnailCache stores computed JPEG thumbnails keyed by file path.
// sync.Map is safe for concurrent reads and writes without a mutex.
var thumbnailCache sync.Map

// scanCancel is the cancel function for the currently running scan.
// Calling it stops the background scan goroutine via context cancellation.
var scanCancel context.CancelFunc

// actionMutex protects the undo/redo stacks below.
var actionMutex sync.Mutex

// undoStack records reversible delete operations (max 20 entries).
var undoStack []Action

// redoStack records undone actions so they can be re-applied.
var redoStack []Action

// maxUndoActions is the maximum number of actions kept in the undo stack.
const maxUndoActions = 20
