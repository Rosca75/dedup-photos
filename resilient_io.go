// =============================================================================
// resilient_io.go — Concurrency-adaptive file reading for flaky filesystems
// =============================================================================
//
// WHY THIS EXISTS
// ───────────────
// The exact-duplicate lane reads whole files to compute a full xxHash. On a
// local disk that is fine at any concurrency. On a GVFS/FUSE SMB mount — how
// this application's owner actually reaches their library — it is not: whole
// file reads fail with EINVAL ("invalid argument") as soon as more than one is
// in flight, and the failures were silently swallowed by a bare `return`.
//
// Measured on that mount, 200 files, reading every byte of each:
//
//	 concurrency | whole-file reads succeeding
//	-------------|---------------------------
//	  8 workers  |  25 / 200
//	  4 workers  |  24 /  96
//	  2 workers  |  48 /  96
//	  1 worker   | 200 / 200
//
// Successes land almost exactly at files/workers, i.e. the mount admits one
// whole-file read at a time and rejects the rest outright. The consequence in
// production was total: of 70 exact-duplicate candidates in one folder, 65 were
// dropped, so byte-identical files were never reported as duplicates at all.
//
// WHAT DOESN'T WORK (all measured, all rejected)
// ──────────────────────────────────────────────
//   - Reading in 64 KB / 256 KB / 1 MB chunks through one handle: 12/96, i.e.
//     no better than os.ReadFile. The limit is not the size of an individual
//     read call.
//   - Streaming the hash with io.CopyBuffer: 12/96. Same reason.
//   - ReadAt windows through one handle: 14/120.
//   - Reopening the file per 512 KB window: 65/120. Per 1 MB window: 88/120.
//     Better, but still loses a quarter of the files.
//   - Blind retries at the same concurrency: 29/96, at 2.3x the wall time.
//
// Short bounded reads are unaffected — 64 KB, 128 KB and even 512 KB prefixes
// all complete 200/200 at 8 workers — which is why the partial-hash and
// perceptual phases were never visibly broken. Only reading a file to its end
// trips the limit.
//
// WHAT THIS DOES
// ──────────────
// Full concurrency first, serial only if the filesystem proves it cannot cope:
//
//  1. Pass 1 runs at the normal worker count. Failures are counted, not ignored.
//  2. If the early sample shows most reads failing, pass 1 is cancelled on the
//     spot rather than grinding through thousands of doomed reads.
//  3. Pass 2 re-runs everything unfinished at concurrency 1, where the mount is
//     reliable, with a second attempt for anything still failing.
//
// On a healthy filesystem pass 1 succeeds outright and pass 2 never runs, so
// this costs nothing. On the SMB mount it gives up on the parallel pass after
// roughly a dozen files and recovers the rest serially.
// =============================================================================

package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// adaptiveProbeSize is how many results pass 1 collects before it is allowed to
// judge the filesystem. Small enough to bail out quickly, large enough that a
// couple of unrelated failures (a deleted file, a permissions problem) do not
// trigger a needless serial pass.
const adaptiveProbeSize = 12

// adaptiveFailureRatio is the share of early results that must fail before
// pass 1 is abandoned. At >50% the filesystem is clearly rejecting concurrent
// work rather than hitting isolated bad files.
const adaptiveFailureRatio = 0.5

// serialRetryAttempts is how many times pass 2 tries a single file before
// giving up and reporting it as unreadable.
const serialRetryAttempts = 2

// runAdaptiveIO applies work to every path, degrading to serial execution if
// concurrency turns out to be the problem.
//
// Returns the paths that could not be processed even serially — those are real
// failures and callers must surface them rather than pretend the files did not
// exist.
func runAdaptiveIO(ctx context.Context, paths []string, numWorkers int, label string, work func(path string) error) (unreadable []string) {
	n := len(paths)
	if n == 0 {
		return nil
	}

	attempted := make([]bool, n) // written by exactly one goroutine per index
	failed := make([]bool, n)

	var done, failures atomic.Int64
	var aborted atomic.Bool

	// ---- Pass 1: full concurrency, with an early bail-out ------------------
	if numWorkers > 1 {
		passCtx, cancel := context.WithCancel(ctx)
		runParallelIndexed(passCtx, n, numWorkers, func(i int) {
			err := work(paths[i])
			attempted[i] = true
			if err != nil {
				failed[i] = true
				f := failures.Add(1)
				d := done.Add(1)
				// Judge the filesystem once enough results are in.
				if d >= adaptiveProbeSize && float64(f) > adaptiveFailureRatio*float64(d) {
					if aborted.CompareAndSwap(false, true) {
						cancel()
					}
				}
				return
			}
			done.Add(1)
		})
		cancel()
	}

	// ---- Collect whatever pass 1 did not finish ---------------------------
	var pending []int
	for i := 0; i < n; i++ {
		if !attempted[i] || failed[i] {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	if numWorkers > 1 {
		reason := "retrying serially"
		if aborted.Load() {
			reason = "concurrent reads rejected by this filesystem, switching to serial"
		}
		fmt.Printf("[io] %s: %d of %d files unfinished after the parallel pass — %s\n",
			label, len(pending), n, reason)
	}

	// ---- Pass 2: strictly serial, with retries ----------------------------
	for _, i := range pending {
		if ctx.Err() != nil {
			break
		}
		var err error
		for attempt := 0; attempt < serialRetryAttempts; attempt++ {
			if err = work(paths[i]); err == nil {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
		}
		if err != nil {
			unreadable = append(unreadable, paths[i])
		}
	}

	if len(unreadable) > 0 {
		fmt.Printf("[io] %s: %d files could not be read at all (reported, not silently skipped)\n",
			label, len(unreadable))
	}
	return unreadable
}
