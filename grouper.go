// =============================================================================
// grouper.go — BK-Tree, Union-Find, and duplicate grouping logic
// =============================================================================
//
// This file groups the hashes computed by hasher_pipeline.go into sets of
// duplicate images. Two passes are performed:
//
//   Pass 1 (Exact):      Files sharing the same xxHash are byte-identical.
//                        XXHash = 0 (singletons) are skipped — they cannot
//                        have exact duplicates.
//   Pass 2 (Perceptual): A BK-Tree finds images with similar dHash values.
//                        NEW (#4): one BK-Tree per aspect-ratio bucket, which
//                        reduces search scope by ~90% (10 buckets instead of
//                        one tree with 5 700 nodes).
//   Pass 3 (Series):     Relabels high-confidence perceptual groups whose
//                        filenames are sequential (burst / rafale mode).
//
// OPTIMISATIONS IMPLEMENTED HERE
// ─────────────────────────────────
// #2  parallelExtractMetadata: all ExtractMetadata calls run concurrently
//     via runParallel (from parallel.go).  Single-threaded was ~8 ms/file.
// #4  aspectBucket + per-bucket BK-Trees: groups images by quantised aspect
//     ratio before inserting into BK-Trees, so each tree is ~90% smaller.
//
// DATA STRUCTURES
// ───────────────
//   BKTree / BKNode: Burkhard-Keller tree for O(n^α) nearest-neighbour search
//     in Hamming space (α < 1 due to pruning via the triangle inequality).
//   UnionFind: Disjoint Set Union for grouping transitive similarity pairs
//     (A similar to B, B similar to C → all three in the same group).
// =============================================================================

package main

import (
	"context"       // For context.Background() used in parallel metadata extraction.
	"fmt"           // Formatted I/O.
	"math"          // math.Round for aspect-ratio quantisation.
	"path/filepath" // For extracting filenames from paths.
	"regexp"        // For detecting sequential numeric suffixes.
	"runtime"       // runtime.NumCPU — worker count for metadata extraction.
	"sort"          // For sorting groups and images.
	"strconv"       // String → int conversion for series detection.
	"strings"       // String manipulation.
	"time"          // time.Parse / time.Second for burst time-window detection.

	"github.com/google/uuid" // UUID for unique group IDs.
)

// =============================================================================
// BK-Tree types
// =============================================================================

// BKNode is one node in a Burkhard-Keller tree. Children are keyed by the
// Hamming distance between the child's hash and this node's hash.
type BKNode struct {
	Hash     uint64
	Path     string
	Children map[int]*BKNode
}

// BKTree is the root of a Burkhard-Keller tree for Hamming-space search.
type BKTree struct {
	Root *BKNode
}

// SearchResult holds one matching result from a BK-Tree query.
type SearchResult struct {
	Hash     uint64
	Path     string
	Distance int // Hamming distance from the query hash.
}

// DuplicateGroup represents a set of images that are duplicates of each other.
type DuplicateGroup struct {
	ID         string          `json:"id"`
	MatchType  string          `json:"match_type"` // "exact", "perceptual", or "series"
	Confidence float64         `json:"confidence"`
	Images     []ImageMetadata `json:"images"` // Best image (IsBest=true) is first.
}

// =============================================================================
// NewBKTree / Insert / Search
// =============================================================================

// NewBKTree returns an empty BK-Tree.
func NewBKTree() *BKTree { return &BKTree{} }

// Insert adds a hash/path pair to the BK-Tree.
// Children are indexed by Hamming distance from the parent, which is what
// allows the tree to prune large portions of the search space.
func (t *BKTree) Insert(hash uint64, path string) {
	node := &BKNode{Hash: hash, Path: path, Children: make(map[int]*BKNode)}
	if t.Root == nil {
		t.Root = node
		return
	}
	cur := t.Root
	for {
		d := HammingDistance(hash, cur.Hash)
		if child, ok := cur.Children[d]; !ok {
			cur.Children[d] = node
			return
		} else {
			cur = child
		}
	}
}

// Search finds all hashes within `threshold` Hamming distance of `hash`.
// The triangle inequality guarantees that children outside [d-t, d+t] cannot
// match, so large subtrees are pruned without examination.
func (t *BKTree) Search(hash uint64, threshold int) []SearchResult {
	if t.Root == nil {
		return nil
	}
	var results []SearchResult
	stack := []*BKNode{t.Root}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		d := HammingDistance(hash, cur.Hash)
		if d <= threshold {
			results = append(results, SearchResult{Hash: cur.Hash, Path: cur.Path, Distance: d})
		}
		lo, hi := d-threshold, d+threshold
		for cd, child := range cur.Children {
			if cd >= lo && cd <= hi {
				stack = append(stack, child)
			}
		}
	}
	return results
}

// =============================================================================
// Union-Find (Disjoint Set Union)
// =============================================================================

// UnionFind groups elements into sets, supporting Find (with path compression)
// and Union operations in near-O(1) amortised time.
type UnionFind struct {
	parent map[string]string
}

// NewUnionFind returns an empty Union-Find structure.
func NewUnionFind() *UnionFind { return &UnionFind{parent: make(map[string]string)} }

// Find returns the root representative of x's set, applying path compression.
func (uf *UnionFind) Find(x string) string {
	if _, ok := uf.parent[x]; !ok {
		uf.parent[x] = x
		return x
	}
	if uf.parent[x] != x {
		uf.parent[x] = uf.Find(uf.parent[x])
	}
	return uf.parent[x]
}

// Union merges the sets containing x and y.
func (uf *UnionFind) Union(x, y string) {
	rx, ry := uf.Find(x), uf.Find(y)
	if rx != ry {
		uf.parent[ry] = rx
	}
}

// =============================================================================
// aspectBucket — Aspect-ratio quantisation for BK-Tree bucketing (#4)
// =============================================================================

// aspectBucket returns a string key that groups images with similar aspect
// ratios together (within 5% tolerance). Building one BK-Tree per bucket
// instead of one global tree reduces each tree's size by ~90%, making the
// O(n^α) search significantly cheaper.
//
// The ratio is always expressed in landscape form (≥ 1.0), then rounded to
// the nearest 0.05. For example, 4:3 (1.333) and 16:9 (1.778) end up in
// different buckets; near-identical crops of the same photo end up together.
//
// Width = 0 or Height = 0 means unknown dimensions → "unknown" bucket, which
// compares all unknown-dimension images against each other (safe: no false
// negatives).
func aspectBucket(width, height int) string {
	if width == 0 || height == 0 {
		return "unknown"
	}
	ratio := float64(width) / float64(height)
	if ratio < 1.0 {
		ratio = 1.0 / ratio // Always use the landscape (≥1) form.
	}
	// Round to nearest 0.05 (±2.5% tolerance per side = ±5% range).
	quantized := math.Round(ratio*20) / 20
	return fmt.Sprintf("%.2f", quantized)
}

// =============================================================================
// findExactPaths — Pass 1: group files by xxHash
// =============================================================================

// findExactPaths builds exact-duplicate groups from the xxHash values.
// Files with XXHash = 0 are skipped (they are singletons — no other file has
// the same byte count, so exact duplication is impossible).
//
// Returns:
//   - groups:       slice of path-lists; each list has 2+ identical files.
//   - exactGrouped: set of paths already assigned to an exact group (excluded
//     from Pass 2 to avoid double-reporting).
func findExactPaths(hashes []ImageHash) (groups [][]string, exactGrouped map[string]bool) {
	xxMap := make(map[uint64][]string)
	for _, h := range hashes {
		if h.Error != nil || h.XXHash == 0 {
			// XXHash = 0 → singleton (unique file size); skip exact matching.
			continue
		}
		xxMap[h.XXHash] = append(xxMap[h.XXHash], h.Path)
	}
	exactGrouped = make(map[string]bool)
	for _, paths := range xxMap {
		if len(paths) >= 2 {
			groups = append(groups, paths)
			for _, p := range paths {
				exactGrouped[p] = true
			}
		}
	}
	return
}

// =============================================================================
// searchBKBucket — BK-Tree search + Union-Find for one aspect bucket
// =============================================================================

// searchBKBucket builds a BK-Tree from bucketHashes, searches it for every
// hash within the threshold, and merges matches using the shared UnionFind.
// Called once per aspect bucket in findPerceptualPaths.
func searchBKBucket(bucketHashes []ImageHash, threshold int, uf *UnionFind, minDist map[string]int) {
	tree := NewBKTree()
	for _, h := range bucketHashes {
		tree.Insert(h.DHash, h.Path)
	}
	for _, h := range bucketHashes {
		for _, result := range tree.Search(h.DHash, threshold) {
			if result.Path == h.Path {
				continue
			}
			uf.Union(h.Path, result.Path)
			root := uf.Find(h.Path)
			if existing, ok := minDist[root]; !ok || result.Distance < existing {
				minDist[root] = result.Distance
			}
		}
	}
}

// =============================================================================
// findPerceptualPaths — Pass 2: aspect-ratio-bucketed BK-Tree search (#4)
// =============================================================================

// findPerceptualPaths detects perceptual duplicates using one BK-Tree per
// aspect-ratio bucket (#4). Images in different buckets are never compared,
// reducing each BK-Tree to ~1/10 the size of a single global tree.
//
// Uses Union-Find to collect transitive similarity chains (A~B, B~C → {A,B,C}).
//
// Returns:
//   - groups:  map from Union-Find root → list of duplicate paths.
//   - minDist: map from root → minimum Hamming distance in that group
//     (used to compute confidence: (1 - dist/64) × 100%).
func findPerceptualPaths(hashes []ImageHash, exactGrouped map[string]bool, threshold int) (
	groups map[string][]string,
	minDist map[string]int,
) {
	// Collect valid hashes: no errors, not already exact-grouped, non-zero dHash.
	var valid []ImageHash
	for _, h := range hashes {
		if h.Error != nil || exactGrouped[h.Path] || h.DHash == 0 {
			continue
		}
		valid = append(valid, h)
	}

	// Group valid hashes into aspect-ratio buckets.
	// Images with different ratios are extremely unlikely to be perceptual dups.
	aspectBuckets := make(map[string][]ImageHash)
	for _, h := range valid {
		b := aspectBucket(h.Width, h.Height)
		aspectBuckets[b] = append(aspectBuckets[b], h)
	}

	uf := NewUnionFind()
	minDist = make(map[string]int)

	// Build and search one BK-Tree per aspect bucket.
	for _, bucketHashes := range aspectBuckets {
		searchBKBucket(bucketHashes, threshold, uf, minDist)
	}

	// Collect Union-Find groups.
	groups = make(map[string][]string)
	for _, h := range valid {
		root := uf.Find(h.Path)
		groups[root] = append(groups[root], h.Path)
	}
	return
}

// =============================================================================
// parallelExtractMetadata — Concurrent metadata extraction (#2)
// =============================================================================

// parallelExtractMetadata runs ExtractMetadataFast for every path concurrently
// using all available CPU cores. It accepts a hashMap of pre-computed dimensions
// from the hashing phase so that ExtractMetadataFast can skip re-opening files
// for DecodeConfig (Optimization A — single file open).
//
// Workers write into an index-aligned slice; the final map is assembled
// sequentially, avoiding a mutex on the hot path.
func parallelExtractMetadata(ctx context.Context, paths []string, hashMap map[string]ImageHash) map[string]ImageMetadata {
	n := len(paths)
	metaSlice := make([]ImageMetadata, n)
	runParallelIndexed(ctx, n, runtime.NumCPU(), func(i int) {
		path := paths[i]
		h := hashMap[path]
		metaSlice[i] = ExtractMetadataFast(path, h.Width, h.Height, h.Size)
	})
	metaMap := make(map[string]ImageMetadata, n)
	for i, path := range paths {
		metaMap[path] = metaSlice[i]
	}
	return metaMap
}

// collectUniquePaths deduplicates paths from exact and perceptual groups.
// The result is the minimal set of files that need metadata extraction.
func collectUniquePaths(exactGroups [][]string, percGroups map[string][]string) []string {
	seen := make(map[string]bool)
	var out []string
	addPath := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, group := range exactGroups {
		for _, p := range group {
			addPath(p)
		}
	}
	for _, paths := range percGroups {
		for _, p := range paths {
			addPath(p)
		}
	}
	return out
}

// =============================================================================
// buildGroup — Construct a DuplicateGroup from pre-computed metadata
// =============================================================================

// buildGroup creates a DuplicateGroup for the given paths and matchType.
// It looks up each path in metaMap (parallel-extracted) rather than calling
// ExtractMetadata inline. hashMap provides the pre-computed XXHash and DHash
// values so ReportMismatch can use them without re-reading files.
func buildGroup(matchType string, confidence float64, paths []string, metaMap map[string]ImageMetadata, hashMap map[string]ImageHash) DuplicateGroup {
	group := DuplicateGroup{
		ID:         uuid.New().String(),
		MatchType:  matchType,
		Confidence: confidence,
	}
	for _, path := range paths {
		meta, ok := metaMap[path]
		if !ok {
			meta = ExtractMetadata(path) // Fallback — should not occur in practice.
		}
		// Copy pre-computed hash values so ReportMismatch doesn't re-read files.
		if h, ok := hashMap[path]; ok {
			meta.XXHash = h.XXHash
			meta.DHash = h.DHash
		}
		group.Images = append(group.Images, meta)
	}
	sort.Slice(group.Images, func(i, j int) bool {
		return group.Images[i].QualityScore > group.Images[j].QualityScore
	})
	if len(group.Images) > 0 {
		group.Images[0].IsBest = true
	}
	return group
}

// detectSeriesGroups relabels high-confidence perceptual groups whose
// filenames form a sequential burst (IMG_2413, IMG_2414 …) as "series".
// Burst photos are visually identical but intentionally distinct shots.
func detectSeriesGroups(groups []DuplicateGroup) {
	for i := range groups {
		if groups[i].MatchType == "perceptual" && groups[i].Confidence >= 95.0 {
			if isSeriesGroup(groups[i].Images) {
				groups[i].MatchType = "series"
			}
		}
	}
}

// =============================================================================
// GroupDuplicates — Main entry point
// =============================================================================

// GroupDuplicates takes the slice of ImageHash values produced by the hash
// pipeline and returns a sorted list of duplicate groups.
//
// The restructured flow vs. the original:
//  1. findExactPaths    — O(n) grouping by xxHash.
//  2. findPerceptualPaths — aspect-ratio bucketed BK-Trees (#4).
//  3. collectUniquePaths + parallelExtractMetadata — all file opens run
//     concurrently instead of single-threaded (#2).
//  4. buildGroup — look up pre-computed metadata from the map.
//  5. detectSeriesGroups — relabel burst sequences.
func GroupDuplicates(hashes []ImageHash, threshold int, includeSeries bool) []DuplicateGroup {
	// Build a quick lookup from path → ImageHash for pre-computed dimensions.
	// This lets parallelExtractMetadata skip re-opening files for dimensions
	// (Optimization A — single file open).
	hashMap := make(map[string]ImageHash, len(hashes))
	for _, h := range hashes {
		hashMap[h.Path] = h
	}

	// Pass 1: Exact duplicates.
	fmt.Println("[grouper] Pass 1: Finding exact duplicates (xxHash)...")
	exactGroups, exactGrouped := findExactPaths(hashes)
	fmt.Printf("[grouper] Pass 1: %d exact duplicate groups.\n", len(exactGroups))

	// Pass 2: Perceptual duplicates using aspect-ratio-bucketed BK-Trees.
	fmt.Println("[grouper] Pass 2: Perceptual matching (aspect-ratio BK-Trees)...")
	percGroups, percMinDist := findPerceptualPaths(hashes, exactGrouped, threshold)
	percCount := 0
	for _, paths := range percGroups {
		if len(paths) >= 2 {
			percCount++
		}
	}
	fmt.Printf("[grouper] Pass 2: %d perceptual duplicate groups.\n", percCount)

	// Optimization B: Lightweight pre-filter to remove filename-sequential
	// groups BEFORE metadata extraction when the user doesn't want series.
	// This avoids opening thousands of burst files on slow drives.
	//
	// We use threshold/2 as the distance cutoff (instead of the old hard-coded 3)
	// to catch more burst groups. For the default threshold of 10, this means
	// distance ≤ 5 → confidence ≥ 92%. Most burst shots have distance 2-5 and
	// sequential filenames, so this catches the vast majority.
	if !includeSeries {
		preFilterCount := 0
		preFilterThreshold := threshold / 2
		for root, paths := range percGroups {
			if len(paths) < 2 {
				continue
			}
			// Check groups with distance ≤ half the scan threshold
			// (confidence ≥ ~85-92% depending on the threshold value).
			if dist, ok := percMinDist[root]; ok && dist <= preFilterThreshold {
				if isFilenameSeriesFromPaths(paths) {
					delete(percGroups, root)
					preFilterCount++
				}
			}
		}
		fmt.Printf("[grouper] Pre-filtered %d series groups (dist ≤ %d). Remaining perceptual: %d\n", preFilterCount, preFilterThreshold, len(percGroups))
	}

	// Parallel metadata extraction for all duplicate files (#2).
	// Uses ExtractMetadataFast with pre-computed dimensions (Optimization A).
	allPaths := collectUniquePaths(exactGroups, percGroups)
	fmt.Printf("[grouper] Extracting metadata for %d files (parallel)...\n", len(allPaths))
	metaMap := parallelExtractMetadata(context.Background(), allPaths, hashMap)

	// Build DuplicateGroup structs from pre-computed metadata.
	var groups []DuplicateGroup
	for _, paths := range exactGroups {
		groups = append(groups, buildGroup("exact", 100.0, paths, metaMap, hashMap))
	}
	for root, paths := range percGroups {
		if len(paths) < 2 {
			continue
		}
		dist := percMinDist[root]
		confidence := (1.0 - float64(dist)/64.0) * 100.0
		groups = append(groups, buildGroup("perceptual", confidence, paths, metaMap, hashMap))
	}

	// Pass 3: Detect burst/series groups among perceptual matches.
	fmt.Println("[grouper] Pass 3: Detecting burst/series groups...")
	detectSeriesGroups(groups)

	// If the user opted out of series groups, remove them from results.
	// This saves the frontend from rendering large burst groups and keeps
	// the results focused on actual duplicates.
	if !includeSeries {
		filtered := groups[:0] // Reuse the underlying slice.
		for _, g := range groups {
			if g.MatchType != "series" {
				filtered = append(filtered, g)
			}
		}
		groups = filtered
		fmt.Printf("[grouper] Filtered out series groups. Remaining: %d groups.\n", len(groups))
	}

	// Sort largest groups first (most duplicates = most wasted space).
	sort.Slice(groups, func(i, j int) bool {
		return len(groups[i].Images) > len(groups[j].Images)
	})
	fmt.Printf("[grouper] Total: %d duplicate groups.\n", len(groups))
	return groups
}

// =============================================================================
// isSeriesGroup — Detect sequential burst-mode filenames
// =============================================================================

// numericSuffixRegex matches a trailing number in a filename stem.
var numericSuffixRegex = regexp.MustCompile(`^(.*?)(\d+)$`)

// isSeriesGroup returns true when images form a burst/series sequence.
//
// Two independent criteria (either one is sufficient):
//
// Criterion A — Filename sequential (relaxed):
//
//	All filenames share the same prefix with numeric suffixes.
//	Max gap between sorted consecutive suffixes ≤ 5.
//	(Catches IMG_4446, IMG_4448, IMG_4450, IMG_4451)
//
// Criterion B — Time + Camera proximity:
//
//	All files have the same camera make+model (non-empty).
//	All EXIF DateTimeOriginal values fall within a 60-second window.
//	(Catches bursts with any naming scheme)
func isSeriesGroup(images []ImageMetadata) bool {
	if len(images) < 2 {
		return false
	}

	// --- Criterion B: Time + Camera proximity (check first, cheaper) ---
	if isTimeCameraSeries(images) {
		return true
	}

	// --- Criterion A: Filename sequential (relaxed gap) ---
	return isFilenameSeries(images)
}

// isTimeCameraSeries checks if all images share the same camera and were
// taken within 60 seconds of each other (EXIF DateTimeOriginal).
// This catches burst shots where filenames don't follow a sequential pattern.
func isTimeCameraSeries(images []ImageMetadata) bool {
	// Every image must have both Camera and DateTaken populated.
	if images[0].Camera == "" || images[0].DateTaken == "" {
		return false
	}

	refCamera := images[0].Camera
	var times []time.Time

	for _, img := range images {
		// Different cameras → definitely not a burst from the same device.
		if img.Camera != refCamera {
			return false
		}
		if img.DateTaken == "" {
			return false
		}
		// Parse the ISO 8601 date string into a Go time.Time value.
		t, err := time.Parse("2006-01-02T15:04:05", img.DateTaken)
		if err != nil {
			return false
		}
		times = append(times, t)
	}

	// Find the earliest and latest timestamps in the group.
	minT, maxT := times[0], times[0]
	for _, t := range times[1:] {
		if t.Before(minT) {
			minT = t
		}
		if t.After(maxT) {
			maxT = t
		}
	}

	// All photos within 60 seconds = likely a burst sequence.
	return maxT.Sub(minT) <= 60*time.Second
}

// isFilenameSeries checks if filenames share a common prefix with numeric
// suffixes that are near-consecutive (max gap ≤ 5). This relaxes the old
// gap ≤ 1 rule to catch iPhone naming gaps like IMG_4446, IMG_4448, IMG_4450.
func isFilenameSeries(images []ImageMetadata) bool {
	type parsed struct {
		prefix string
		number int
	}
	var items []parsed

	for _, img := range images {
		base := filepath.Base(img.Path)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		m := numericSuffixRegex.FindStringSubmatch(stem)
		if m == nil {
			return false
		}
		num, err := strconv.Atoi(m[2])
		if err != nil {
			return false
		}
		items = append(items, parsed{prefix: strings.ToLower(m[1]), number: num})
	}

	// All must share the same prefix (case-insensitive).
	for i := 1; i < len(items); i++ {
		if items[i].prefix != items[0].prefix {
			return false
		}
	}

	// Sorted suffixes must have max gap ≤ 5 (relaxed from the old gap ≤ 1).
	sort.Slice(items, func(i, j int) bool { return items[i].number < items[j].number })
	for i := 1; i < len(items); i++ {
		gap := items[i].number - items[i-1].number
		if gap < 0 || gap > 5 {
			return false
		}
	}
	return true
}

// isFilenameSeriesFromPaths checks if a list of file paths have sequential
// filenames (relaxed gap ≤ 5). This is a lightweight check that requires no
// file I/O — it only looks at the path strings. Used for early series
// pre-filtering before expensive metadata extraction (Optimization B).
func isFilenameSeriesFromPaths(paths []string) bool {
	if len(paths) < 2 {
		return false
	}
	type parsed struct {
		prefix string
		number int
	}
	var items []parsed
	for _, p := range paths {
		base := filepath.Base(p)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		m := numericSuffixRegex.FindStringSubmatch(stem)
		if m == nil {
			return false
		}
		num, err := strconv.Atoi(m[2])
		if err != nil {
			return false
		}
		items = append(items, parsed{prefix: strings.ToLower(m[1]), number: num})
	}
	// All same prefix.
	for i := 1; i < len(items); i++ {
		if items[i].prefix != items[0].prefix {
			return false
		}
	}
	// Sorted suffixes: max gap ≤ 5.
	sort.Slice(items, func(i, j int) bool { return items[i].number < items[j].number })
	for i := 1; i < len(items); i++ {
		if gap := items[i].number - items[i-1].number; gap < 0 || gap > 5 {
			return false
		}
	}
	return true
}
