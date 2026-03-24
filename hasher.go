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
// ComputePHash — Average-based perceptual hash (alternative algorithm)
// =============================================================================

// ComputePHash computes a simple average-based perceptual hash. Unlike dHash
// which compares adjacent pixels, pHash compares each pixel to the overall
// average brightness. This can be more robust for certain types of edits.
func ComputePHash(path string) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("failed to open image %s: %w", path, err)
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return 0, fmt.Errorf("failed to decode image %s: %w", path, err)
	}

	bounds := img.Bounds()
	srcWidth := bounds.Max.X - bounds.Min.X
	srcHeight := bounds.Max.Y - bounds.Min.Y

	const size = 8
	gray := make([][]uint32, size)
	for y := 0; y < size; y++ {
		gray[y] = make([]uint32, size)
	}

	var total uint64
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			srcX := bounds.Min.X + (x * srcWidth / size)
			srcY := bounds.Min.Y + (y * srcHeight / size)
			r, g, b, _ := img.At(srcX, srcY).RGBA()
			luminance := (299*r + 587*g + 114*b) / 1000
			gray[y][x] = luminance
			total += uint64(luminance)
		}
	}

	avg := total / (size * size)

	var hash uint64
	bitPosition := 0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if uint64(gray[y][x]) > avg {
				hash |= 1 << uint(bitPosition)
			}
			bitPosition++
		}
	}

	return hash, nil
}

// =============================================================================
// HashAllImagesWithContext — Parallel hashing with cancellation support
// =============================================================================

// HashAllImagesWithContext is like HashAllImages but supports cancellation via
// a context. It also supports choosing the perceptual hash algorithm.
func HashAllImagesWithContext(ctx context.Context, paths []string, numWorkers int, algorithm string) []ImageHash {
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}

	totalImages := len(paths)
	if totalImages == 0 {
		return []ImageHash{}
	}

	fmt.Printf("[hasher] Starting %d workers to hash %d images (algorithm: %s)...\n", numWorkers, totalImages, algorithm)

	jobs := make(chan string, totalImages)
	results := make(chan ImageHash, totalImages)

	var wg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				// Check for cancellation.
				if ctx.Err() != nil {
					return
				}

				result := ImageHash{Path: path}

				xxh, err := ComputeXXHash(path)
				if err != nil {
					result.Error = err
					results <- result
					continue
				}
				result.XXHash = xxh

				// Choose perceptual hash algorithm.
				switch algorithm {
				case "phash":
					ph, err := ComputePHash(path)
					if err != nil {
						result.Error = err
						results <- result
						continue
					}
					result.DHash = ph // Store in same field for compatibility.
				case "both":
					// Use dHash for grouping. XOR of dHash^pHash would destroy
					// the metric space property needed by the BK-Tree.
					dh, err := ComputeDHash(path)
					if err != nil {
						result.Error = err
						results <- result
						continue
					}
					result.DHash = dh
				default: // "dhash"
					dh, err := ComputeDHash(path)
					if err != nil {
						result.Error = err
						results <- result
						continue
					}
					result.DHash = dh
				}

				results <- result
			}
		}()
	}

	// Send jobs, checking for cancellation.
	go func() {
		for _, path := range paths {
			if ctx.Err() != nil {
				break
			}
			jobs <- path
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
		if ctx.Err() != nil {
			// Drain remaining results.
			for range results {
			}
			break
		}
		allResults = append(allResults, result)
		processed++

		if processed%100 == 0 || processed == totalImages {
			fmt.Printf("[hasher] Progress: %d / %d images hashed\n", processed, totalImages)
		}
	}

	fmt.Printf("[hasher] Done! Hashed %d images.\n", len(allResults))
	return allResults
}

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
