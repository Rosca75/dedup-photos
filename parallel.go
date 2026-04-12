// =============================================================================
// parallel.go — Shared worker-pool utility used by hasher.go and grouper.go
// =============================================================================
//
// runParallel was previously inlined inside hasher.go. Moving it to its own
// file lets grouper.go (parallel ExtractMetadata) and scanner.go (concurrent
// directory walk) reuse it without importing from each other.
//
// Pattern: pre-filled buffered channel + N goroutines + sync.WaitGroup.
// =============================================================================

package main

import (
	"context"
	"sync"
)

// runParallel executes fn for each item in paths using numWorkers goroutines.
// It respects context cancellation — workers exit early when ctx is cancelled.
//
// HOW IT WORKS (worker-pool pattern):
//  1. A buffered channel is pre-filled with all work items (no separate producer
//     goroutine needed — the channel capacity equals len(paths)).
//  2. N goroutines are started; each pulls items from the channel until it's empty.
//  3. sync.WaitGroup blocks until every worker has finished.
//  4. Each worker checks ctx.Err() before processing an item, so cancellation
//     (e.g., user clicked "Cancel") stops work quickly without leaking goroutines.
//
// Parameters:
//   - ctx:        Cancellation context (pass context.Background() if no cancel needed).
//   - paths:      Slice of work items; fn is called exactly once per item.
//   - numWorkers: Degree of parallelism. Use runtime.NumCPU() for CPU-bound work.
//   - fn:         The work function. MUST be safe to call concurrently.
func runParallel(ctx context.Context, paths []string, numWorkers int, fn func(string)) {
	if len(paths) == 0 {
		return
	}

	// Pre-fill a buffered channel so every job is available immediately.
	// Capacity = len(paths) means no goroutine ever blocks on send.
	jobs := make(chan string, len(paths))
	var wg sync.WaitGroup

	// Launch numWorkers goroutines. Each drains the channel independently.
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				// Stop processing if the scan was cancelled.
				if ctx.Err() != nil {
					return
				}
				fn(path)
			}
		}()
	}

	// Enqueue all jobs. Stop early if cancelled so the sender doesn't block.
	for _, path := range paths {
		if ctx.Err() != nil {
			break
		}
		jobs <- path
	}
	close(jobs) // Signal workers: no more items; exit when channel is empty.

	wg.Wait() // Block until all workers have finished their current item.
}
