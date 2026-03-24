// =============================================================================
// hasher.go — Hashing engine with parallel processing
// =============================================================================
//
// This file computes two types of hashes for each image:
//
// 1. xxHash (exact match): A very fast non-cryptographic hash of the raw file
//    bytes. If two files have the same xxHash, they are byte-for-byte identical.
//    Think of it like a fingerprint — identical files produce identical hashes.
//
// 2. dHash (perceptual match): A "difference hash" that captures the visual
//    structure of an image. Even if two photos are slightly different (resized,
//    recompressed, color-adjusted), their dHashes will be very similar. This
//    lets us find "visually similar" duplicates, not just exact copies.
//
// The file also implements a parallel processing pipeline using goroutines
// (Go's lightweight threads) and channels (Go's way of communicating between
// goroutines). This lets us hash many images simultaneously, taking advantage
// of multi-core CPUs.
//
// Key Go concepts used here:
//   - goroutine:    A lightweight thread. Started with the "go" keyword.
//   - channel:      A typed pipe for sending data between goroutines.
//   - sync.WaitGroup: A counter that lets you wait for multiple goroutines.
//   - math/bits:    Standard library for bit manipulation (popcount, etc.).
// =============================================================================

package main

import (
	"context"   // For cancellation support.
	"fmt"       // Formatted I/O (printing).
	"image"     // Go's standard image interface and decoding registry.
	"math/bits" // Bit counting functions (like popcount).
	"os"        // File operations.
	"runtime"   // Access to Go runtime info like number of CPUs.
	"sync"      // Synchronization primitives (WaitGroup, Mutex).

	// Standard image format decoders. Importing these with "_" (blank import)
	// registers them with the image package so image.Decode can recognize them.
	// You don't call any functions from these packages directly — the import
	// side effect is what matters.
	_ "image/gif"  // Registers GIF decoder.
	_ "image/jpeg" // Registers JPEG decoder.
	_ "image/png"  // Registers PNG decoder.

	// Extended image format decoders from the Go "x" (experimental) packages.
	// These aren't in the standard library but are maintained by the Go team.
	_ "golang.org/x/image/bmp"  // Registers BMP decoder.
	_ "golang.org/x/image/tiff" // Registers TIFF decoder.
	_ "golang.org/x/image/webp" // Registers WebP decoder.

	// xxhash is an extremely fast non-cryptographic hash function.
	// "v2" means we're using version 2 of the package.
	"github.com/cespare/xxhash/v2"
)

// =============================================================================
// Types
// =============================================================================

// ImageHash holds the results of hashing a single image file.
// Each image gets two hashes computed: an xxHash for exact matching and a
// dHash for perceptual (visual similarity) matching.
type ImageHash struct {
	Path   string // The absolute filesystem path to the image file.
	XXHash uint64 // xxHash64 of the raw file bytes — for exact duplicate detection.
	DHash  uint64 // Perceptual difference hash — for visual similarity detection.
	Error  error  // If something went wrong hashing this file, the error is stored here.
}

// =============================================================================
// ComputeXXHash — Fast file-level hash for exact duplicate detection
// =============================================================================

// ComputeXXHash reads the entire file at the given path and computes its
// xxHash64 digest. xxHash is one of the fastest non-cryptographic hash
// functions available — it can process several GB/s on modern hardware.
//
// Two files with the same xxHash are (with overwhelming probability) byte-for-
// byte identical. This is our first-pass duplicate check: fast and definitive.
//
// Parameters:
//   - path: Absolute path to the file to hash.
//
// Returns:
//   - uint64: The 64-bit xxHash digest.
//   - error:  Non-nil if the file couldn't be read.
func ComputeXXHash(path string) (uint64, error) {
	// os.ReadFile reads the entire file into memory as a byte slice ([]byte).
	// For very large files this could use a lot of RAM, but photos are
	// typically 1-30 MB, which is fine.
	data, err := os.ReadFile(path)
	if err != nil {
		// If we can't read the file (permissions, missing, etc.), return the
		// error. The caller will handle it gracefully.
		return 0, fmt.Errorf("failed to read file %s: %w", path, err)
	}

	// xxhash.Sum64 computes the xxHash64 digest of the byte slice in a single
	// call. This is the simplest API — there's also a streaming API if you
	// don't want to load the whole file into memory.
	hash := xxhash.Sum64(data)

	return hash, nil
}

// =============================================================================
// ComputeDHash — Perceptual "difference hash" for visual similarity
// =============================================================================

// ComputeDHash computes a "difference hash" (dHash) for the image at the
// given path. This is a perceptual hashing algorithm that captures the
// visual structure of an image in a 64-bit integer.
//
// HOW THE dHash ALGORITHM WORKS (step by step):
//
//  1. LOAD the image and decode it into pixels.
//
//  2. RESIZE to 9 columns × 8 rows. Why 9×8? Because we'll compare each
//     pixel to its right neighbor, producing 8 comparisons per row.
//     8 rows × 8 comparisons = 64 bits = one uint64.
//
//  3. CONVERT TO GRAYSCALE. We only care about brightness differences, not
//     color. Each pixel becomes a single brightness value (0-255).
//
//  4. COMPUTE DIFFERENCES. For each pixel, compare it to the pixel to its
//     right. If the left pixel is brighter, set the bit to 1; otherwise 0.
//
//  5. PACK INTO 64 BITS. The 64 comparison results become a 64-bit integer.
//
// WHY THIS WORKS:
//   - The resize step removes fine detail, keeping only the overall structure.
//   - Comparing neighbors instead of using absolute values makes the hash
//     invariant to brightness/contrast changes.
//   - Two visually similar images will have very similar dHash values.
//   - The "Hamming distance" (number of differing bits) between two dHashes
//     tells you how visually different the images are. 0 = identical structure,
//     64 = completely different.
//
// Parameters:
//   - path: Absolute path to an image file.
//
// Returns:
//   - uint64: The 64-bit difference hash.
//   - error:  Non-nil if the image couldn't be decoded.
func ComputeDHash(path string) (uint64, error) {
	// -------------------------------------------------------------------------
	// Step 1: Open and decode the image
	// -------------------------------------------------------------------------

	// Open the file for reading. os.Open returns a *os.File (file handle)
	// and an error. In Go, you always check errors immediately.
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("failed to open image %s: %w", path, err)
	}
	// "defer" schedules a function call to run when the enclosing function
	// returns. This ensures we always close the file, even if an error occurs
	// later. It's like a "finally" block in Python/Java.
	defer file.Close()

	// image.Decode reads the image data and returns an image.Image interface.
	// It automatically detects the format (JPEG, PNG, etc.) by looking at the
	// file's magic bytes, using the decoders we registered via blank imports.
	// The second return value is the format name (e.g., "jpeg"), which we
	// ignore with "_".
	img, _, err := image.Decode(file)
	if err != nil {
		return 0, fmt.Errorf("failed to decode image %s: %w", path, err)
	}

	// -------------------------------------------------------------------------
	// Step 2: Resize to 9×8 using nearest-neighbor sampling
	// -------------------------------------------------------------------------
	//
	// We need a tiny 9×8 version of the image. Instead of using a fancy
	// resizing library, we use "nearest-neighbor" sampling: for each pixel
	// in the output, we pick the closest corresponding pixel in the input.
	//
	// This is the simplest possible resize algorithm. It's not pretty for
	// display purposes, but for hashing it's perfectly adequate.

	// Get the dimensions of the original image. Bounds() returns a Rectangle
	// with Min and Max points. We need the width (Max.X - Min.X) and height.
	bounds := img.Bounds()
	srcWidth := bounds.Max.X - bounds.Min.X  // Original width in pixels.
	srcHeight := bounds.Max.Y - bounds.Min.Y // Original height in pixels.

	// Target dimensions for the dHash algorithm.
	const dstWidth = 9  // 9 columns (so we get 8 horizontal comparisons).
	const dstHeight = 8 // 8 rows.

	// Create a 2D grid to store grayscale values. In Go, we represent this
	// as a slice of slices. Each element will hold a brightness value (0-65535).
	// We use uint32 because Go's color model uses 16-bit channels internally.
	gray := make([][]uint32, dstHeight)
	for y := 0; y < dstHeight; y++ {
		gray[y] = make([]uint32, dstWidth)
	}

	// Fill in the grayscale grid by sampling from the original image.
	for y := 0; y < dstHeight; y++ {
		for x := 0; x < dstWidth; x++ {
			// Map the (x, y) position in our 9×8 grid back to a position
			// in the original image. This is the "nearest-neighbor" part:
			// we simply pick the pixel at the proportional position.
			//
			// For example, if the original is 1000px wide and we want
			// column 3 of 9, we sample at pixel 1000 * 3 / 9 = 333.
			srcX := bounds.Min.X + (x * srcWidth / dstWidth)
			srcY := bounds.Min.Y + (y * srcHeight / dstHeight)

			// Get the color of the pixel at (srcX, srcY). The At() method
			// returns a color.Color interface. RGBA() returns the red, green,
			// blue, and alpha channels as uint32 values in the range [0, 65535].
			r, g, b, _ := img.At(srcX, srcY).RGBA()

			// Convert RGB to grayscale using the luminance formula.
			// This formula weights the channels according to human perception:
			// green contributes most to perceived brightness, blue the least.
			// The formula is: Y = 0.299*R + 0.587*G + 0.114*B
			// We multiply by 1000 and divide by 1000 to avoid floating point.
			luminance := (299*r + 587*g + 114*b) / 1000

			gray[y][x] = luminance
		}
	}

	// -------------------------------------------------------------------------
	// Step 3: Compute the difference hash
	// -------------------------------------------------------------------------
	//
	// For each pixel, compare it to its right neighbor. If the left pixel
	// is brighter (higher luminance), set the corresponding bit to 1.
	// This gives us 8 comparisons per row × 8 rows = 64 bits.

	var hash uint64  // The 64-bit hash we're building. Starts at 0 (all bits off).
	bitPosition := 0 // Tracks which bit we're currently setting (0 to 63).

	for y := 0; y < dstHeight; y++ {
		for x := 0; x < dstWidth-1; x++ { // dstWidth-1 because we compare x to x+1.
			// Compare current pixel to its right neighbor.
			if gray[y][x] > gray[y][x+1] {
				// Set the bit at 'bitPosition' to 1.
				// The "|=" operator is bitwise OR-assignment.
				// "1 << bitPosition" creates a number with only bit 'bitPosition' set.
				// OR-ing it into hash sets that bit without affecting others.
				hash |= 1 << uint(bitPosition)
			}
			// If left <= right, the bit stays 0 (its default).

			bitPosition++ // Move to the next bit.
		}
	}

	return hash, nil
}

// =============================================================================
// HashAllImages — Parallel hashing using a worker pool
// =============================================================================

// HashAllImages computes both xxHash and dHash for every image path in the
// input slice, using multiple goroutines (lightweight threads) for speed.
//
// HOW THE WORKER POOL WORKS:
//
//	This is a very common Go concurrency pattern called "fan-out/fan-in":
//
//	1. PRODUCER: The main goroutine sends file paths into a "jobs" channel.
//	2. WORKERS:  N goroutines (one per CPU core) read from the jobs channel.
//	             Each worker hashes one image at a time and sends the result
//	             to a "results" channel.
//	3. COLLECTOR: A goroutine reads all results from the results channel
//	              and puts them into a slice.
//
//	Think of it like a restaurant: there's one person taking orders (producer),
//	multiple cooks working in parallel (workers), and one person collecting
//	the finished plates (collector).
//
//	"channel" in Go is like a thread-safe queue. You send values in with
//	"channel <- value" and receive them with "value := <-channel".
//
// Parameters:
//   - paths:      Slice of absolute file paths to hash.
//   - numWorkers: How many goroutines to run in parallel. Typically
//     runtime.NumCPU() (one per CPU core).
//
// Returns:
//   - []ImageHash: One ImageHash struct per input path, containing both hashes
//     (or an error if hashing failed).
func HashAllImages(paths []string, numWorkers int) []ImageHash {
	// If numWorkers is 0 or negative, default to the number of CPU cores.
	// runtime.NumCPU() returns the number of logical CPUs (including
	// hyper-threading). This is usually a good default for I/O + CPU work.
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}

	// Total number of images to process. len() returns the length of a slice.
	totalImages := len(paths)

	// If there are no images, return an empty slice immediately.
	if totalImages == 0 {
		return []ImageHash{}
	}

	fmt.Printf("[hasher] Starting %d workers to hash %d images...\n", numWorkers, totalImages)

	// -------------------------------------------------------------------------
	// Create channels
	// -------------------------------------------------------------------------

	// "jobs" is a buffered channel of strings (file paths). Workers will read
	// paths from this channel. The buffer size of totalImages means we can
	// send all paths without blocking (pre-loading the queue).
	jobs := make(chan string, totalImages)

	// "results" is a buffered channel of ImageHash structs. Workers send
	// their results here. We buffer it to totalImages so workers never block
	// waiting for the collector to read.
	results := make(chan ImageHash, totalImages)

	// -------------------------------------------------------------------------
	// Start worker goroutines
	// -------------------------------------------------------------------------

	// sync.WaitGroup is a counter that lets you wait for a group of
	// goroutines to finish. You Add(n) before starting goroutines, each
	// goroutine calls Done() when finished, and Wait() blocks until the
	// counter reaches zero.
	var wg sync.WaitGroup

	// Launch numWorkers goroutines. Each one runs the same anonymous function.
	for w := 0; w < numWorkers; w++ {
		wg.Add(1) // Increment the WaitGroup counter by 1.

		// "go func() { ... }()" launches an anonymous function as a new
		// goroutine. The goroutine runs concurrently with everything else.
		go func() {
			// defer wg.Done() decrements the WaitGroup counter when this
			// goroutine finishes (when the function returns).
			defer wg.Done()

			// "range jobs" reads from the jobs channel until it's closed.
			// Each iteration, "path" receives the next value from the channel.
			// When the channel is closed AND empty, the loop ends.
			for path := range jobs {
				// Create an ImageHash struct to hold results for this file.
				result := ImageHash{Path: path}

				// Compute the xxHash (exact file fingerprint).
				xxh, err := ComputeXXHash(path)
				if err != nil {
					// If xxHash fails, store the error and send the result.
					// We don't stop — other files might be fine.
					result.Error = err
					results <- result
					continue
				}
				result.XXHash = xxh

				// Compute the dHash (perceptual/visual fingerprint).
				dh, err := ComputeDHash(path)
				if err != nil {
					// dHash can fail if the image format isn't supported
					// (like HEIC). We still have the xxHash, which is useful
					// for exact matching, so we keep the result.
					result.Error = err
					// Keep the xxHash we already computed.
					results <- result
					continue
				}
				result.DHash = dh

				// Send the completed result to the results channel.
				results <- result
			}
		}()
	}

	// -------------------------------------------------------------------------
	// Send all file paths to the jobs channel (producer)
	// -------------------------------------------------------------------------

	// We send all paths into the jobs channel. Because the channel is
	// buffered to totalImages, this won't block.
	for _, path := range paths {
		jobs <- path
	}

	// Close the channel to signal workers that no more jobs are coming.
	// Workers' "for path := range jobs" loops will exit after processing
	// all remaining items.
	close(jobs)

	// -------------------------------------------------------------------------
	// Wait for all workers to finish, then close results channel
	// -------------------------------------------------------------------------

	// We launch another goroutine to wait for all workers and then close the
	// results channel. We can't do this in the main goroutine because we
	// also need to read from results (which would deadlock).
	go func() {
		wg.Wait()      // Block until all workers have called wg.Done().
		close(results) // Signal the collector that no more results are coming.
	}()

	// -------------------------------------------------------------------------
	// Collect all results (collector)
	// -------------------------------------------------------------------------

	// Preallocate the output slice with the exact capacity we need.
	// make([]ImageHash, 0, totalImages) creates a slice with length 0 but
	// capacity totalImages, avoiding reallocations as we append.
	allResults := make([]ImageHash, 0, totalImages)
	processed := 0

	// Read from the results channel until it's closed.
	for result := range results {
		allResults = append(allResults, result)
		processed++

		// Print progress every 100 images or on the last one.
		// The "%" operator is modulo (remainder after division).
		if processed%100 == 0 || processed == totalImages {
			fmt.Printf("[hasher] Progress: %d / %d images hashed\n", processed, totalImages)
		}
	}

	fmt.Printf("[hasher] Done! Hashed %d images.\n", len(allResults))

	return allResults
}

// =============================================================================
// HashAllImagesWithContext — Like HashAllImages but supports cancellation
// =============================================================================

func HashAllImagesWithContext(ctx context.Context, paths []string, numWorkers int) []ImageHash {
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}

	totalImages := len(paths)
	if totalImages == 0 {
		return []ImageHash{}
	}

	fmt.Printf("[hasher] Starting %d workers to hash %d images (with cancellation support)...\n", numWorkers, totalImages)

	jobs := make(chan string, totalImages)
	results := make(chan ImageHash, totalImages)

	var wg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				// Check for cancellation before processing each image.
				select {
				case <-ctx.Done():
					return
				default:
				}

				result := ImageHash{Path: path}

				xxh, err := ComputeXXHash(path)
				if err != nil {
					result.Error = err
					results <- result
					continue
				}
				result.XXHash = xxh

				dh, err := ComputeDHash(path)
				if err != nil {
					result.Error = err
					results <- result
					continue
				}
				result.DHash = dh

				results <- result
			}
		}()
	}

	// Send jobs, but stop early if cancelled.
	go func() {
		for _, path := range paths {
			select {
			case <-ctx.Done():
				break
			case jobs <- path:
			}
		}
		close(jobs)
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	allResults := make([]ImageHash, 0, totalImages)
	processed := 0

	for result := range results {
		allResults = append(allResults, result)
		processed++
		if processed%100 == 0 || processed == totalImages {
			fmt.Printf("[hasher] Progress: %d / %d images hashed\n", processed, totalImages)
		}
	}

	fmt.Printf("[hasher] Done! Hashed %d images.\n", len(allResults))
	return allResults
}

// =============================================================================
// HammingDistance — Count the number of differing bits between two hashes
// =============================================================================

// HammingDistance computes the Hamming distance between two 64-bit integers.
// The Hamming distance is the number of bit positions where the two values
// differ. For dHash values:
//
//   - Distance 0:  The images have identical visual structure.
//   - Distance 1-5: Very similar (probably the same photo, resized/recompressed).
//   - Distance 6-10: Somewhat similar (maybe same scene, different angle).
//   - Distance >10: Probably different images.
//
// HOW IT WORKS:
//  1. XOR the two values. XOR produces a 1 bit wherever the inputs differ
//     and a 0 bit wherever they match. So the XOR result has a 1 for every
//     position where a and b are different.
//  2. Count the number of 1 bits (called "popcount" or "population count").
//     This gives us the total number of differing positions.
//
// Example:
//
//	a = 0b1010  (binary)
//	b = 0b1100
//	a ^ b = 0b0110  (two bits differ)
//	popcount(0b0110) = 2
//	So the Hamming distance is 2.
//
// Parameters:
//   - a, b: Two 64-bit hash values to compare.
//
// Returns:
//   - int: The number of differing bits (0 to 64).
func HammingDistance(a, b uint64) int {
	// "^" is the XOR (exclusive or) operator. It produces a 1 bit for every
	// position where a and b differ.
	xor := a ^ b

	// bits.OnesCount64 counts the number of 1 bits in a uint64. This is also
	// known as "popcount" (population count). Modern CPUs have a dedicated
	// hardware instruction for this (POPCNT), so it's extremely fast.
	return bits.OnesCount64(xor)
}
