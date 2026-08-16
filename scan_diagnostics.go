// =============================================================================
// scan_diagnostics.go — Count what the scan could NOT do, and say so
// =============================================================================
//
// A duplicate finder that silently skips files is worse than one that finds
// fewer duplicates, because the user cannot tell the difference between "no
// duplicates here" and "this file was never compared". Two real cases from a
// measured 8,159-image library:
//
//   - 1,563 files (19.2%) produced no perceptual hash, because the fast path
//     could not reach an embedded thumbnail. They are still exact-matched, but
//     they never take part in perceptual matching and nothing said so.
//   - On a GVFS/SMB mount, 65 of 70 exact-duplicate candidates failed to read
//     and were dropped by a bare `return`, so byte-identical files were never
//     reported at all.
//
// These counters are gathered during a scan and reported in ScanStats so the
// UI can show them. They are package-level because the phases that produce them
// are spread across the pipeline and run concurrently.
// =============================================================================

package main

import "sync"

// scanDiagnostics accumulates per-scan counts of work that could not be done.
type scanDiagnostics struct {
	mu sync.Mutex

	// unreadable holds files that could not be read even after the serial
	// retry in runAdaptiveIO. These are hard I/O failures.
	unreadable []string

	// noPerceptualHash counts files that hashed fine for exact matching but
	// produced dHash = 0, so they were excluded from perceptual matching.
	noPerceptualHash int
}

var scanDiag scanDiagnostics

// resetScanDiagnostics clears the counters at the start of a scan.
func resetScanDiagnostics() {
	scanDiag.mu.Lock()
	defer scanDiag.mu.Unlock()
	scanDiag.unreadable = nil
	scanDiag.noPerceptualHash = 0
}

// recordUnreadable notes files that could not be read at all.
func recordUnreadable(paths []string) {
	scanDiag.mu.Lock()
	defer scanDiag.mu.Unlock()
	scanDiag.unreadable = append(scanDiag.unreadable, paths...)
}

// recordNoPerceptualHash notes how many files ended up with dHash = 0.
func recordNoPerceptualHash(count int) {
	scanDiag.mu.Lock()
	defer scanDiag.mu.Unlock()
	scanDiag.noPerceptualHash += count
}

// snapshotScanDiagnostics returns the current counts plus a sample of the
// unreadable paths, for reporting.
func snapshotScanDiagnostics() (unreadableCount, noPerceptual int, sample []string) {
	scanDiag.mu.Lock()
	defer scanDiag.mu.Unlock()
	unreadableCount = len(scanDiag.unreadable)
	noPerceptual = scanDiag.noPerceptualHash
	limit := 10
	if unreadableCount < limit {
		limit = unreadableCount
	}
	sample = append(sample, scanDiag.unreadable[:limit]...)
	return
}
