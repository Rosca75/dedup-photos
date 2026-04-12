// =============================================================================
// parallel.go — Shared concurrent worker-pool utility
// =============================================================================
//
// This file provides the runParallel helper, which is used by both the hash
// pipeline (hasher.go) and the metadata extraction phase (grouper.go).
// Keeping it here avoids duplicating the worker-pool pattern across files.
// =============================================================================

package main

import (
	"context" // For cancellation support.
	"sync"    // For WaitGroup.
)

// runParallel executes fn for each item in paths using numWorkers goroutines.
// It respects context cancellation — workers stop early if ctx is cancelled.
//
// HOW IT WORKS:
//  1. A buffered channel "jobs" is filled with every path.
//  2. numWorkers goroutines each pull paths from the channel and call fn.
//  3. When the channel is drained (or ctx is cancelled), workers exit.
//  4. WaitGroup blocks until all workers finish.
//
// Parameters:
//   - ctx:        Context for cancellation (pass context.Background() if not needed).
//   - paths:      The list of file paths to process.
//   - numWorkers: How many goroutines to use (typically runtime.NumCPU()).
//   - fn:         The function to run on each path.
func runParallel(ctx context.Context, paths []string, numWorkers int, fn func(string)) {
	if len(paths) == 0 {
		return
	}

	jobs := make(chan string, len(paths))
	var wg sync.WaitGroup

	// Start worker goroutines.
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				if ctx.Err() != nil {
					return
				}
				fn(path)
			}
		}()
	}

	// Send jobs, checking for cancellation.
	for _, path := range paths {
		if ctx.Err() != nil {
			break
		}
		jobs <- path
	}
	close(jobs)

	wg.Wait()
}
