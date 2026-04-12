// =============================================================================
// grouper.go — BK-Tree and duplicate grouping logic
// =============================================================================
//
// This file contains the core duplicate-detection logic. It takes the hashes
// computed by hasher.go and groups images that are duplicates of each other.
//
// There are TWO types of duplicates we detect:
//
// 1. EXACT DUPLICATES: Files that are byte-for-byte identical. These have
//    the same xxHash value. This is the easy case — if two files hash to
//    the same value, they're identical (with astronomically high probability).
//
// 2. PERCEPTUAL DUPLICATES: Images that look similar but aren't byte-identical.
//    For example, a JPEG saved at 90% quality vs. 80% quality, or a photo
//    that was resized. These have similar (but not identical) dHash values.
//    We use a BK-Tree data structure to efficiently find similar hashes.
//
// DATA STRUCTURES USED:
//
// - BK-Tree (Burkhard-Keller Tree): A tree data structure designed for
//   searching in metric spaces. It lets us efficiently find all hashes within
//   a given Hamming distance of a query hash. Without a BK-Tree, we'd have
//   to compare every hash to every other hash (O(n²)). The BK-Tree prunes
//   large portions of the search space using the triangle inequality.
//
// - Union-Find (Disjoint Set Union): A data structure for grouping elements
//   into sets. When we find that image A is similar to B, and B is similar
//   to C, Union-Find lets us merge all three into one group efficiently.
//   It uses "path compression" to keep operations nearly O(1).
//
// Key Go concepts:
//   - map:       Hash table (dictionary). Used extensively for grouping.
//   - slice:     Dynamic array. Go's primary collection type.
//   - sort:      The sort package lets us sort slices with custom comparisons.
//   - UUID:      Universally Unique Identifier — a random 128-bit ID.
// =============================================================================

package main

import (
	"context"       // For context.Background() used in parallel metadata extraction.
	"fmt"           // Formatted I/O.
	"path/filepath" // For extracting filenames from paths.
	"regexp"        // For extracting numeric suffixes from filenames.
	"runtime"       // For runtime.NumCPU() — number of parallel workers.
	"sort"          // Sorting algorithms.
	"strconv"       // For converting strings to numbers.
	"strings"       // For string manipulation.
	"sync"          // For sync.Mutex used to protect metaMap during parallel writes.

	// uuid generates random UUIDs (Universally Unique Identifiers).
	// A UUID looks like "550e8400-e29b-41d4-a716-446655440000".
	// We use them as unique IDs for duplicate groups.
	"github.com/google/uuid"
)

// =============================================================================
// BK-Tree Types and Implementation
// =============================================================================

// BKNode represents a single node in the BK-Tree. Each node stores a hash
// value and the file path it came from, plus a map of children.
//
// The key insight of a BK-Tree: children are indexed by their Hamming distance
// from the parent. So if the parent has hash H, and a child has distance 3
// from H, it's stored at Children[3]. This structure enables efficient pruning
// during search.
type BKNode struct {
	Hash     uint64          // The dHash value stored at this node.
	Path     string          // The file path associated with this hash.
	Children map[int]*BKNode // Child nodes, indexed by Hamming distance from this node.
}

// BKTree is a Burkhard-Keller tree for efficient nearest-neighbor search
// in Hamming space. It allows us to find all hashes within a given distance
// of a query hash without comparing against every single hash.
type BKTree struct {
	Root *BKNode // The root node of the tree. nil if the tree is empty.
}

// SearchResult holds one result from a BK-Tree search: a matching hash,
// the file path, and how "far away" it is from the query (in Hamming distance).
type SearchResult struct {
	Hash     uint64 // The dHash value that matched.
	Path     string // The file path associated with this hash.
	Distance int    // Hamming distance from the query hash (0 = identical).
}

// DuplicateGroup represents a group of images that are duplicates of each
// other (either exact or perceptual). The frontend displays these groups
// and lets the user decide which to keep.
type DuplicateGroup struct {
	ID         string          `json:"id"`         // Unique identifier (UUID) for this group.
	MatchType  string          `json:"match_type"` // "exact" or "perceptual".
	Confidence float64         `json:"confidence"` // How confident we are (0-100%). 100% for exact matches.
	Images     []ImageMetadata `json:"images"`     // The images in this group, sorted by quality score (best first).
}

// =============================================================================
// NewBKTree — Create an empty BK-Tree
// =============================================================================

// NewBKTree creates and returns a new, empty BK-Tree. You add nodes to it
// with Insert() and search it with Search().
func NewBKTree() *BKTree {
	return &BKTree{Root: nil}
}

// =============================================================================
// Insert — Add a hash to the BK-Tree
// =============================================================================

// Insert adds a new hash/path pair to the BK-Tree.
//
// HOW INSERTION WORKS:
//  1. If the tree is empty, the new node becomes the root.
//  2. Otherwise, compute the Hamming distance from the new hash to the
//     current node's hash.
//  3. If no child exists at that distance, add the new node there.
//  4. If a child already exists at that distance, recurse into that child
//     (repeat from step 2 with the child as the current node).
//
// This builds a tree where each node's children are organized by their
// distance from the parent, which is what makes search efficient.
//
// Parameters:
//   - hash: The dHash value to insert.
//   - path: The file path associated with this hash.
func (t *BKTree) Insert(hash uint64, path string) {
	// Create the new node with an empty children map.
	newNode := &BKNode{
		Hash:     hash,
		Path:     path,
		Children: make(map[int]*BKNode),
	}

	// If the tree is empty, this node becomes the root.
	if t.Root == nil {
		t.Root = newNode
		return
	}

	// Walk down the tree to find the right place for this node.
	current := t.Root
	for {
		// Compute the Hamming distance between the new hash and the
		// current node's hash. This tells us "how different" they are.
		distance := HammingDistance(hash, current.Hash)

		// Check if there's already a child at this distance.
		child, exists := current.Children[distance]

		if !exists {
			// No child at this distance — insert the new node here.
			// This is the base case that ends the loop.
			current.Children[distance] = newNode
			return
		}

		// A child already exists at this distance. Move down to that child
		// and continue the loop (effectively recursing without recursion).
		current = child
	}
}

// =============================================================================
// Search — Find all hashes within a given distance
// =============================================================================

// Search finds all hashes in the BK-Tree that are within `threshold` Hamming
// distance of the given query hash. This is the key operation that makes
// duplicate detection efficient.
//
// HOW BK-TREE SEARCH WORKS (with pruning):
//
// The BK-Tree exploits the "triangle inequality" property of Hamming distance:
//
//	|d(a,c) - d(b,c)| ≤ d(a,b) ≤ d(a,c) + d(b,c)
//
// In plain English: if we know the distance from A to B, and we know the
// distance from B to C, we can bound the distance from A to C.
//
// During search:
//  1. Compute d = distance from query to current node.
//  2. If d ≤ threshold, this node is a match! Add it to results.
//  3. For children: only visit children at distances in [d-threshold, d+threshold].
//     Children outside this range CANNOT be within threshold of the query
//     (guaranteed by the triangle inequality). This is the pruning step.
//
// This pruning typically eliminates most of the tree, making search much
// faster than a brute-force comparison against all hashes.
//
// Parameters:
//   - hash:      The query hash to search for.
//   - threshold: Maximum Hamming distance to consider as a match (e.g., 10).
//
// Returns:
//   - []SearchResult: All matching hashes within the threshold distance.
func (t *BKTree) Search(hash uint64, threshold int) []SearchResult {
	// If the tree is empty, there are no results.
	if t.Root == nil {
		return nil
	}

	// We'll collect matching results in this slice.
	var results []SearchResult

	// We use a stack (slice used as LIFO) for iterative depth-first search.
	// This avoids recursion, which can overflow the stack for very deep trees.
	// "stack" holds nodes we still need to examine.
	stack := []*BKNode{t.Root}

	// Process nodes until the stack is empty.
	for len(stack) > 0 {
		// Pop the last element from the stack (LIFO = depth-first search).
		// In Go, we do this by taking the last element and shrinking the slice.
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		// Compute the Hamming distance from the query hash to this node.
		distance := HammingDistance(hash, current.Hash)

		// If the distance is within the threshold, this is a match!
		if distance <= threshold {
			results = append(results, SearchResult{
				Hash:     current.Hash,
				Path:     current.Path,
				Distance: distance,
			})
		}

		// =====================================================================
		// PRUNING STEP — This is what makes BK-Trees efficient!
		// =====================================================================
		//
		// We only need to visit children at distances in the range:
		//   [distance - threshold, distance + threshold]
		//
		// Why? Because of the triangle inequality:
		//   If a child is at distance `childDist` from the current node, then
		//   the distance from the query to that child's subtree is bounded by:
		//     |distance - childDist| ≤ d(query, child) ≤ distance + childDist
		//
		//   For the child to be within `threshold` of the query, we need:
		//     childDist ≥ distance - threshold  (lower bound)
		//     childDist ≤ distance + threshold  (upper bound)
		//
		// Children outside this range are guaranteed to be too far away.
		// This pruning typically skips the majority of the tree.

		// Calculate the range of child distances to explore.
		minDist := distance - threshold // Lower bound (inclusive).
		maxDist := distance + threshold // Upper bound (inclusive).

		// Iterate over all children of the current node.
		for childDist, childNode := range current.Children {
			// Only visit this child if its distance falls within our range.
			if childDist >= minDist && childDist <= maxDist {
				stack = append(stack, childNode)
			}
			// Children outside [minDist, maxDist] are PRUNED — we skip them
			// entirely, along with their entire subtrees. This is the
			// performance win.
		}
	}

	return results
}

// =============================================================================
// Union-Find (Disjoint Set Union) — For merging overlapping groups
// =============================================================================

// UnionFind is a data structure for efficiently grouping elements into
// disjoint (non-overlapping) sets. It supports two operations:
//
//   - Find(x):    Returns the "representative" (root) of the set containing x.
//   - Union(x,y): Merges the sets containing x and y into one set.
//
// We use this when building perceptual duplicate groups. If image A is similar
// to B, and B is similar to C, we want all three in the same group. Union-Find
// handles this transitivity efficiently.
//
// EXAMPLE:
//
//	Union("a", "b")  → {a, b} are in the same set.
//	Union("b", "c")  → {a, b, c} are all in the same set.
//	Find("a") == Find("c")  → true (they share a representative).
//
// The "path compression" optimization in Find() keeps the tree flat, making
// both operations nearly O(1) on average (technically O(α(n)), where α is
// the inverse Ackermann function — essentially constant).
type UnionFind struct {
	parent map[string]string // Maps each element to its parent. If parent[x] == x, x is a root.
}

// NewUnionFind creates a new, empty Union-Find data structure.
func NewUnionFind() *UnionFind {
	return &UnionFind{
		parent: make(map[string]string),
	}
}

// Find returns the representative (root) of the set containing x.
// If x hasn't been seen before, it becomes its own set (parent = itself).
//
// PATH COMPRESSION: After finding the root, we update x's parent to point
// directly to the root. This flattens the tree, making future Find() calls
// for x (and any elements along the path) nearly instant.
//
// Parameters:
//   - x: The element to find the root of.
//
// Returns:
//   - string: The representative (root) element of x's set.
func (uf *UnionFind) Find(x string) string {
	// If x hasn't been seen before, initialize it as its own root.
	// In Go, accessing a missing map key returns the zero value ("" for strings).
	if _, exists := uf.parent[x]; !exists {
		uf.parent[x] = x
		return x
	}

	// Walk up the parent chain until we find the root (an element whose
	// parent is itself).
	if uf.parent[x] != x {
		// PATH COMPRESSION: Recursively find the root, then set x's parent
		// directly to the root. This flattens the tree structure.
		uf.parent[x] = uf.Find(uf.parent[x])
	}

	return uf.parent[x]
}

// Union merges the sets containing x and y. After this call, Find(x) and
// Find(y) will return the same representative.
//
// Parameters:
//   - x, y: Two elements whose sets should be merged.
func (uf *UnionFind) Union(x, y string) {
	// Find the roots of both elements.
	rootX := uf.Find(x)
	rootY := uf.Find(y)

	// If they already have the same root, they're already in the same set.
	// Nothing to do.
	if rootX == rootY {
		return
	}

	// Make rootX the parent of rootY (arbitrary choice — a more optimized
	// version would use "union by rank" to keep the tree balanced, but for
	// our use case this is sufficient).
	uf.parent[rootY] = rootX
}

// =============================================================================
// GroupDuplicates — Main duplicate grouping logic
// =============================================================================

// GroupDuplicates takes a list of image hashes and groups them into duplicate
// sets. It performs two passes:
//
//	Pass 1 (Exact):      Groups images with identical xxHash values. These are
//	                      byte-for-byte identical files. Confidence = 100%.
//
//	Pass 2 (Perceptual): Uses a BK-Tree to find images with similar dHash
//	                      values (within the Hamming distance threshold).
//	                      Confidence = (1 - distance/64) × 100%.
//
// Parameters:
//   - hashes:    Slice of ImageHash structs (one per image, with xxHash and dHash).
//   - threshold: Maximum Hamming distance for perceptual matching (e.g., 10).
//     Lower = stricter matching. 0 = exact dHash matches only.
//
// Returns:
//   - []DuplicateGroup: A list of duplicate groups, sorted by the number of
//     images (largest groups first).
func GroupDuplicates(hashes []ImageHash, threshold int) []DuplicateGroup {
	var groups []DuplicateGroup

	// Keep track of which paths have already been assigned to an exact-match
	// group. This prevents the same pair from appearing in both exact and
	// perceptual groups.
	exactGrouped := make(map[string]bool)

	// =========================================================================
	// PASS 1: Exact duplicates (same xxHash = byte-identical files)
	// =========================================================================
	//
	// Strategy: Build a map from xxHash → list of paths. Any xxHash with
	// more than one path means those files are exact duplicates.
	//
	// Time complexity: O(n) — we just iterate through all hashes once.

	fmt.Println("[grouper] Pass 1a: Bucketing exact duplicates (same xxHash)...")

	// xxHashMap maps each xxHash value to a slice of ImageHash structs that
	// share that hash. If two files are byte-identical, their xxHash will
	// be the same, so they'll end up in the same slice.
	xxHashMap := make(map[uint64][]ImageHash)

	for _, h := range hashes {
		// Skip images that had errors during hashing.
		if h.Error != nil {
			continue
		}
		// Append this image to the slice for its xxHash value.
		xxHashMap[h.XXHash] = append(xxHashMap[h.XXHash], h)
	}

	// Mark all paths that are in exact-duplicate buckets so Pass 2 can skip
	// them. We do this NOW (before Pass 2 builds the BK-Tree) because Pass 2
	// reads exactGrouped to exclude already-grouped images.
	//
	// We do NOT build the DuplicateGroup structs yet — that happens after
	// parallel metadata extraction below.
	exactGroupCount := 0
	for _, bucket := range xxHashMap {
		if len(bucket) < 2 {
			continue
		}
		for _, h := range bucket {
			exactGrouped[h.Path] = true
		}
		exactGroupCount++
	}

	fmt.Printf("[grouper] Pass 1a complete: found %d exact duplicate buckets.\n", exactGroupCount)

	// =========================================================================
	// PASS 2: Perceptual duplicates (similar dHash via BK-Tree)
	// =========================================================================
	//
	// Strategy:
	//   1. Build a BK-Tree from all dHash values.
	//   2. For each hash, search the BK-Tree for similar hashes.
	//   3. Use Union-Find to merge overlapping pairs into groups.
	//   4. Extract the final groups.
	//
	// Why Union-Find? Because similarity is transitive in practice:
	//   If A is similar to B, and B is similar to C, then A, B, C should
	//   all be in the same group (even if A and C aren't directly similar).

	fmt.Println("[grouper] Pass 2: Finding perceptual duplicates (similar dHash)...")

	// Step 2a: Build the BK-Tree from all valid dHash values.
	tree := NewBKTree()

	// We also keep a list of valid hashes (no errors, not zero, not already
	// in an exact group) for the search phase.
	var validHashes []ImageHash

	for _, h := range hashes {
		// Skip images with errors.
		if h.Error != nil {
			continue
		}
		// Skip images already in an exact duplicate group.
		if exactGrouped[h.Path] {
			continue
		}
		// Skip all-zero dHashes. A dHash of 0 typically means the image is
		// a solid color or couldn't be properly processed. Including these
		// would create a huge false-positive group of unrelated images.
		if h.DHash == 0 {
			continue
		}

		// Insert into the BK-Tree and add to our valid list.
		tree.Insert(h.DHash, h.Path)
		validHashes = append(validHashes, h)
	}

	// Step 2b: Search the BK-Tree for each hash and union similar images.
	uf := NewUnionFind()

	// For computing confidence, we need to remember the minimum distance
	// within each merged group. We'll track the minimum distance between
	// any two images that caused them to be unioned.
	minDistances := make(map[string]int) // root path → minimum distance in group

	for _, h := range validHashes {
		// Search the BK-Tree for all hashes within the threshold distance.
		results := tree.Search(h.DHash, threshold)

		for _, result := range results {
			// Don't match an image with itself.
			if result.Path == h.Path {
				continue
			}

			// Union the two images into the same group.
			uf.Union(h.Path, result.Path)

			// Track the minimum Hamming distance for confidence calculation.
			root := uf.Find(h.Path)
			if existing, ok := minDistances[root]; !ok || result.Distance < existing {
				minDistances[root] = result.Distance
			}
		}
	}

	// Step 2c: Collect the groups from Union-Find.
	// We iterate over all valid hashes, find their root, and group them.
	perceptualGroups := make(map[string][]string) // root → list of paths

	for _, h := range validHashes {
		root := uf.Find(h.Path)
		perceptualGroups[root] = append(perceptualGroups[root], h.Path)
	}

	// =========================================================================
	// Parallel metadata extraction
	// =========================================================================
	//
	// At this point we know exactly which paths will appear in duplicate groups
	// (exact buckets with 2+ files, perceptual groups with 2+ files). We collect
	// them all and extract their metadata concurrently across all CPU cores.
	//
	// WHY HERE: Both exact and perceptual grouping are now complete, so we know
	// the full set of paths that need metadata. Running all extractions in
	// parallel (instead of serially inside each group-building loop) is the
	// key performance win of this optimization.

	// Collect all unique paths that need metadata.
	// We use a "seen" map to avoid duplicates (a path can only be in one group,
	// but we build this defensively in case logic ever changes).
	var allDupPaths []string
	seen := make(map[string]bool)

	// Exact-duplicate paths.
	for _, bucket := range xxHashMap {
		if len(bucket) < 2 {
			continue
		}
		for _, h := range bucket {
			if !seen[h.Path] {
				allDupPaths = append(allDupPaths, h.Path)
				seen[h.Path] = true
			}
		}
	}

	// Perceptual-duplicate paths.
	for _, paths := range perceptualGroups {
		if len(paths) < 2 {
			continue
		}
		for _, path := range paths {
			if !seen[path] {
				allDupPaths = append(allDupPaths, path)
				seen[path] = true
			}
		}
	}

	// Run ExtractMetadata in parallel. Each worker writes into metaMap under
	// a mutex. Reading is done only after runParallel returns (no races).
	metaMap := make(map[string]ImageMetadata, len(allDupPaths))
	var muMeta sync.Mutex

	fmt.Printf("[grouper] Extracting metadata for %d duplicate images in parallel (%d workers)...\n",
		len(allDupPaths), runtime.NumCPU())

	runParallel(context.Background(), allDupPaths, runtime.NumCPU(), func(path string) {
		meta := ExtractMetadata(path)
		muMeta.Lock()
		metaMap[path] = meta
		muMeta.Unlock()
	})

	// =========================================================================
	// Pass 1b: Build exact DuplicateGroup structs using pre-computed metadata
	// =========================================================================

	for _, bucket := range xxHashMap {
		if len(bucket) < 2 {
			continue
		}

		group := DuplicateGroup{
			ID:         uuid.New().String(), // Generate a random UUID.
			MatchType:  "exact",
			Confidence: 100.0, // Byte-identical = 100% confident.
		}

		// Look up pre-computed metadata from metaMap (no file I/O here).
		for _, h := range bucket {
			group.Images = append(group.Images, metaMap[h.Path])
		}

		// Sort images within the group by quality score (highest first).
		sort.Slice(group.Images, func(i, j int) bool {
			return group.Images[i].QualityScore > group.Images[j].QualityScore
		})

		// Mark the first image (highest quality) as the "best" — the one
		// we recommend keeping.
		if len(group.Images) > 0 {
			group.Images[0].IsBest = true
		}

		groups = append(groups, group)
	}

	fmt.Printf("[grouper] Pass 1b complete: built %d exact duplicate groups.\n", exactGroupCount)

	// =========================================================================
	// Pass 2b: Build perceptual DuplicateGroup structs using pre-computed metadata
	// =========================================================================

	// Step 2d: Build DuplicateGroup structs for groups with 2+ images.
	perceptualCount := 0
	for root, paths := range perceptualGroups {
		if len(paths) < 2 {
			// Not a duplicate group — only one image.
			continue
		}

		// Calculate confidence based on the minimum Hamming distance in the group.
		// Confidence = (1 - minDistance / 64) × 100
		// A distance of 0 = 100% confident. A distance of 64 = 0% confident.
		confidence := 100.0
		if dist, ok := minDistances[root]; ok {
			confidence = (1.0 - float64(dist)/64.0) * 100.0
		}

		group := DuplicateGroup{
			ID:         uuid.New().String(),
			MatchType:  "perceptual",
			Confidence: confidence,
		}

		// Look up pre-computed metadata from metaMap (no file I/O here).
		for _, path := range paths {
			group.Images = append(group.Images, metaMap[path])
		}

		// Sort by quality score descending (best first).
		sort.Slice(group.Images, func(i, j int) bool {
			return group.Images[i].QualityScore > group.Images[j].QualityScore
		})

		// Mark the best image.
		if len(group.Images) > 0 {
			group.Images[0].IsBest = true
		}

		groups = append(groups, group)
		perceptualCount++
	}

	fmt.Printf("[grouper] Pass 2b complete: built %d perceptual duplicate groups.\n", perceptualCount)

	// =========================================================================
	// PASS 3: Detect "series" (burst/rafale) groups among perceptual matches
	// =========================================================================
	//
	// A "series" is a set of photos taken in rapid succession (burst mode).
	// They look very similar (high confidence) but have sequential filenames
	// like IMG_2413.JPG, IMG_2414.JPG, IMG_2415.JPG. These are NOT true
	// duplicates — the user intentionally took multiple shots.
	//
	// Detection criteria:
	//   1. The group is perceptual (not exact — exact means byte-identical).
	//   2. Confidence >= 95% (images are very similar visually).
	//   3. Filenames share a common prefix with sequential numeric suffixes.
	//      e.g., IMG_2413, IMG_2414, IMG_2415 → prefix "IMG_", numbers 2413-2415.
	//
	// If all criteria are met, we relabel the group as "series" instead of
	// "perceptual". The frontend can then display a blue "SERIES" badge.

	fmt.Println("[grouper] Pass 3: Detecting burst/series groups...")
	seriesCount := 0
	for i := range groups {
		if groups[i].MatchType == "perceptual" && groups[i].Confidence >= 95.0 {
			if isSeriesGroup(groups[i].Images) {
				groups[i].MatchType = "series"
				seriesCount++
			}
		}
	}
	fmt.Printf("[grouper] Pass 3 complete: found %d series (burst) groups.\n", seriesCount)

	// =========================================================================
	// Final sorting: largest groups first (most duplicates = most wasted space)
	// =========================================================================
	sort.Slice(groups, func(i, j int) bool {
		return len(groups[i].Images) > len(groups[j].Images)
	})

	fmt.Printf("[grouper] Total: %d duplicate groups found.\n", len(groups))

	return groups
}

// =============================================================================
// isSeriesGroup — Check if images form a sequential burst/series
// =============================================================================

// numericSuffixRegex matches a trailing number at the end of a filename stem.
// For example, "IMG_2413" matches with suffix "2413" and prefix "IMG_".
var numericSuffixRegex = regexp.MustCompile(`^(.*?)(\d+)$`)

// isSeriesGroup checks whether a set of images have sequential filenames,
// indicating they were taken in burst/rafale mode (e.g., IMG_2413, IMG_2414).
//
// The algorithm:
//  1. Extract the filename stem (without extension) for each image.
//  2. Split each stem into a text prefix and a numeric suffix.
//  3. Check that all images share the same prefix.
//  4. Check that the numeric suffixes form a consecutive sequence
//     (e.g., 2413, 2414, 2415 — no gaps larger than 1).
//
// Returns true if the images form a series, false otherwise.
func isSeriesGroup(images []ImageMetadata) bool {
	if len(images) < 2 {
		return false
	}

	// Extract prefix and numeric suffix for each image.
	type parsed struct {
		prefix string
		number int
	}
	var items []parsed

	for _, img := range images {
		// Get just the filename without the directory path.
		base := filepath.Base(img.Path)
		// Remove the file extension (e.g., ".JPG") to get the stem.
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)

		// Try to match a trailing number in the stem.
		matches := numericSuffixRegex.FindStringSubmatch(stem)
		if matches == nil {
			// No trailing number — can't be a series.
			return false
		}

		prefix := matches[1]
		num, err := strconv.Atoi(matches[2])
		if err != nil {
			return false
		}
		items = append(items, parsed{prefix: strings.ToLower(prefix), number: num})
	}

	// Check that all images share the same prefix.
	for i := 1; i < len(items); i++ {
		if items[i].prefix != items[0].prefix {
			return false
		}
	}

	// Sort by numeric suffix and check for consecutive numbers.
	sort.Slice(items, func(i, j int) bool {
		return items[i].number < items[j].number
	})

	for i := 1; i < len(items); i++ {
		gap := items[i].number - items[i-1].number
		// Allow a gap of at most 1 (consecutive). A gap of 0 means same number
		// (different extensions?), gap of 1 means sequential.
		if gap < 0 || gap > 1 {
			return false
		}
	}

	return true
}
