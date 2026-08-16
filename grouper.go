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
//   Pass 2 (Perceptual): A BK-Tree finds images with similar fingerprints,
//                        then refinePerceptualGroups splits the transitive
//                        Union-Find chains into clusters that hold together.
//   Pass 3 (Series):     Relabels high-confidence perceptual groups whose
//                        filenames are sequential (burst / rafale mode).
//
// OPTIMISATIONS IMPLEMENTED HERE
// ─────────────────────────────────
// #2  parallelExtractMetadata: all ExtractMetadata calls run concurrently
//     via runParallel (from parallel.go).  Single-threaded was ~8 ms/file.
// #4  aspectBucket + per-bucket BK-Trees: REVERTED — measured at 99.1% of a
//     real library in one bucket, so it pruned nothing while making crops
//     unmatchable. See aspectBucket.
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
// Threshold units
// =============================================================================
//
// The UI slider is a PERCENTAGE of the hash width; the BK-Tree and the
// confidence figure work in BITS. Keeping the two apart matters: the slider
// value used to be handed to BKTree.Search unconverted, so "25%" requested a
// radius of 25 bits — 39% of the hash, where two unrelated photos (32 bits
// apart on average) routinely link. Measured on a 6,596-image library that
// setting put 99.4% of every photo into a single group.

// perceptualHashBits is the width of the dHash produced by
// computeDHashFromImage. Confidence and threshold conversion both derive from
// it rather than repeating the literal 64.
const perceptualHashBits = 64

// defaultThresholdPercent is used when the request carries no threshold.
// At 10% (6 bits) a real-library measurement found 97.8% of duplicates while
// the largest group stayed at 0.6% of the library.
const defaultThresholdPercent = 10

// maxThresholdPercent caps the slider below the point where Union-Find starts
// fusing the whole library. On the measured library the giant component appears
// at 14 bits (~22%); 18% is 11 bits, comfortably short of it.
const maxThresholdPercent = 18

// hammingThresholdBits converts a slider percentage into a Hamming distance.
// Call this exactly once, at the boundary between request and grouper.
func hammingThresholdBits(percent int) int {
	if percent < 0 {
		percent = 0
	}
	if percent > maxThresholdPercent {
		percent = maxThresholdPercent
	}
	return percent * perceptualHashBits / 100
}

// =============================================================================
// Algorithm selection — applied at MATCH time, not at hash time
// =============================================================================
//
// Every file carries both a dHash and a pHash, computed from the same decode.
// The setting therefore only decides how two files are COMPARED, which is what
// makes it meaningful at last:
//
//   - "dhash" — difference hash only, the historical behaviour.
//   - "phash" — DCT perceptual hash only. Previously this selected an average
//     hash, and only for formats almost nobody in this corpus uses; on HEIC and
//     thumbnail-bearing JPEG it was silently ignored, so all three settings
//     produced byte-identical hashes.
//   - "both"  — the two fingerprints must AGREE. This was never implemented at
//     all: "both" fell through a switch's default case straight to dHash.
//
// "both" is the only setting that is genuinely stronger than the others. The
// two hashes measure different things — neighbouring-pixel gradients versus
// low-frequency DCT energy — so requiring both to fall inside the threshold
// suppresses the chance pairings that let Union-Find chain unrelated photos
// together, at the cost of a little recall.
type matchMode int

const (
	matchDHash matchMode = iota
	matchPHash
	matchBoth
)

// parseMatchMode maps the request's algorithm string onto a match mode.
// Unknown values fall back to dHash, which is the historical default.
func parseMatchMode(algorithm string) matchMode {
	switch strings.ToLower(strings.TrimSpace(algorithm)) {
	case "phash":
		return matchPHash
	case "both":
		return matchBoth
	default:
		return matchDHash
	}
}

// indexHash returns the fingerprint used to BUILD the BK-Tree — the structure
// that generates candidates. "both" indexes on dHash and then verifies pHash,
// so the tree only ever needs one hash.
func (m matchMode) indexHash(h ImageHash) uint64 {
	if m == matchPHash {
		return h.PHash
	}
	return h.DHash
}

// distance returns the effective distance between two images under this mode.
// For "both" it is the WORSE of the two fingerprints, so a pair only counts as
// close when neither hash disagrees.
func (m matchMode) distance(a, b ImageHash) int {
	switch m {
	case matchPHash:
		return HammingDistance(a.PHash, b.PHash)
	case matchBoth:
		d := HammingDistance(a.DHash, b.DHash)
		if p := HammingDistance(a.PHash, b.PHash); p > d {
			return p
		}
		return d
	default:
		return HammingDistance(a.DHash, b.DHash)
	}
}

// usable reports whether an image has the fingerprints this mode needs.
func (m matchMode) usable(h ImageHash) bool {
	switch m {
	case matchPHash:
		return h.PHash != 0
	case matchBoth:
		return h.DHash != 0 && h.PHash != 0
	default:
		return h.DHash != 0
	}
}

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

// aspectBucket returns a string key grouping images with similar aspect ratios
// (within 5% tolerance), in landscape form and rounded to the nearest 0.05.
//
// NO LONGER USED FOR BK-TREE PARTITIONING. It was introduced as optimisation #4
// on the claim that per-bucket trees cut search scope by ~90%. Measured on the
// libraries this tool is actually pointed at, that claim does not hold: 99.1% of
// a 1,746-image phone library landed in a single bucket (ratio 1.35), so the
// partition pruned nothing at all while still costing a hard false negative —
// any crop that shifts the aspect ratio by more than 5% could never be compared
// against its own original, no matter how the threshold was set.
//
// A BK-Tree already prunes by the triangle inequality, so the partition was
// redundant on the workload that matters and harmful on the one case (crops)
// where perceptual matching earns its keep. It is kept here because
// ReportMismatch still reports the bucket as a diagnostic.
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
//
// This produces CANDIDATES only. Union-Find is transitive, so the sets it
// returns can be long chains in which neighbouring links are close but the ends
// are unrelated. refinePerceptualGroups breaks those apart afterwards.
func searchBKBucket(bucketHashes []ImageHash, threshold int, mode matchMode, uf *UnionFind, byPath map[string]ImageHash) {
	tree := NewBKTree()
	for _, h := range bucketHashes {
		tree.Insert(mode.indexHash(h), h.Path)
	}
	for _, h := range bucketHashes {
		for _, result := range tree.Search(mode.indexHash(h), threshold) {
			if result.Path == h.Path {
				continue
			}
			// Under "both", the tree only proves the indexed hash is close.
			// The second fingerprint has to agree before the pair is linked.
			if mode == matchBoth && mode.distance(h, byPath[result.Path]) > threshold {
				continue
			}
			uf.Union(h.Path, result.Path)
		}
	}
}

// =============================================================================
// Group refinement — break transitive chains into coherent clusters
// =============================================================================

// perceptualGroup is one refined cluster: a set of paths plus the distance
// between its two FURTHEST-APART members.
//
// MaxDist, not the minimum, is what confidence is derived from. The old code
// recorded the closest pair anywhere in the set, which meant a group reported
// perfect confidence the moment it swallowed one identical pair — the
// 1,730-image group produced by a 25-bit threshold reported 100% while 93.7%
// of its own internal pairs were further apart than the threshold that built it.
type perceptualGroup struct {
	Paths   []string
	MaxDist int
}

// refinePerceptualGroups turns raw Union-Find candidate sets into clusters
// where every member is within `threshold` of a shared leader.
//
// Union-Find alone cannot do this. Similarity is transitive under it, so A~B
// and B~C group A with C even when A and C are nothing alike; once the
// threshold is loose enough that chance links appear, a single giant component
// swallows the library in one step. Bounding each cluster to a radius around a
// leader removes the chaining without needing the threshold to be perfect.
func refinePerceptualGroups(raw map[string][]string, hashMap map[string]ImageHash, threshold int, mode matchMode) []perceptualGroup {
	var out []perceptualGroup
	for _, paths := range raw {
		if len(paths) < 2 {
			continue
		}
		out = append(out, splitChainedGroup(paths, hashMap, threshold, mode)...)
	}
	// Sort for deterministic output regardless of Go's map iteration order.
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Paths) != len(out[j].Paths) {
			return len(out[i].Paths) > len(out[j].Paths)
		}
		return out[i].Paths[0] < out[j].Paths[0]
	})
	return out
}

// leaderScanLimit caps the O(n²) highest-degree leader search. Above it the
// first remaining path is used instead, which keeps a pathologically large
// candidate set (the symptom this whole change exists to fix) from turning the
// refinement into an O(n³) stall.
const leaderScanLimit = 256

// splitChainedGroup repeatedly carves the densest cluster out of a candidate
// set until nothing with 2+ members remains.
//
// Each pass picks a leader, claims every remaining path within `threshold` of
// it, and emits that as one group. Choosing the highest-degree path as leader
// makes the first cluster form around the densest core rather than around an
// arbitrary outlier.
func splitChainedGroup(paths []string, hashMap map[string]ImageHash, threshold int, mode matchMode) []perceptualGroup {
	remaining := append([]string(nil), paths...)
	sort.Strings(remaining) // deterministic leader choice on ties

	var out []perceptualGroup
	for len(remaining) >= 2 {
		leader := pickLeader(remaining, hashMap, threshold, mode)
		leaderHash := hashMap[leader]

		var members, rest []string
		for _, p := range remaining {
			if p == leader || mode.distance(leaderHash, hashMap[p]) <= threshold {
				members = append(members, p)
			} else {
				rest = append(rest, p)
			}
		}

		if len(members) >= 2 {
			out = append(out, perceptualGroup{
				Paths:   members,
				MaxDist: maxPairDistance(members, hashMap, mode),
			})
		}

		// The leader always joins members, so rest always shrinks. This guard
		// is belt-and-braces against a future edit reintroducing a stall.
		if len(rest) >= len(remaining) {
			break
		}
		remaining = rest
	}
	return out
}

// pickLeader returns the path with the most neighbours within threshold, or the
// first path when the set is too large to scan affordably.
func pickLeader(paths []string, hashMap map[string]ImageHash, threshold int, mode matchMode) string {
	if len(paths) > leaderScanLimit {
		return paths[0]
	}
	best, bestDegree := paths[0], -1
	for _, p := range paths {
		ph := hashMap[p]
		degree := 0
		for _, q := range paths {
			if p == q {
				continue
			}
			if mode.distance(ph, hashMap[q]) <= threshold {
				degree++
			}
		}
		if degree > bestDegree {
			best, bestDegree = p, degree
		}
	}
	return best
}

// maxPairDistance returns the distance between the two furthest-apart members
// of a group — the group's diameter.
func maxPairDistance(paths []string, hashMap map[string]ImageHash, mode matchMode) int {
	worst := 0
	for i := 0; i < len(paths); i++ {
		hi := hashMap[paths[i]]
		for j := i + 1; j < len(paths); j++ {
			if d := mode.distance(hi, hashMap[paths[j]]); d > worst {
				worst = d
			}
		}
	}
	return worst
}

// =============================================================================
// findPerceptualPaths — Pass 2: aspect-ratio-bucketed BK-Tree search (#4)
// =============================================================================

// findPerceptualPaths detects perceptual duplicate CANDIDATES with a BK-Tree
// over every eligible image.
//
// Uses Union-Find to collect transitive similarity chains (A~B, B~C → {A,B,C}).
// Those chains are candidates, not results: refinePerceptualGroups splits them
// into clusters whose members are all mutually close before anything is
// reported to the user.
//
// Returns groups: map from Union-Find root → list of candidate paths.
func findPerceptualPaths(hashes []ImageHash, exactGrouped map[string]bool, threshold int, mode matchMode) (
	groups map[string][]string,
) {
	// Collect valid hashes: no errors, not already exact-grouped, non-zero dHash.
	var valid []ImageHash
	byPath := make(map[string]ImageHash, len(hashes))
	for _, h := range hashes {
		if h.Error != nil || exactGrouped[h.Path] || !mode.usable(h) {
			continue
		}
		valid = append(valid, h)
		byPath[h.Path] = h
	}

	uf := NewUnionFind()

	// One BK-Tree over every candidate. See aspectBucket for why the per-ratio
	// partition was removed: it pruned nothing on real libraries and silently
	// excluded crops from ever matching their originals.
	searchBKBucket(valid, threshold, mode, uf, byPath)

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
// using all available CPU cores. It accepts a hashMap carrying what the hash
// phase already worked out for each file — dimensions (Optimization A: no
// re-open for DecodeConfig) and, since plan 05, the parsed EXIF as well.
//
// With both present this phase does no file I/O at all for the common case,
// which is what took it from ~99% of grouping time down to noise. Files whose
// EXIF was not captured fall back to reading inside ExtractMetadataFast.
//
// Workers write into an index-aligned slice; the final map is assembled
// sequentially, avoiding a mutex on the hot path.
func parallelExtractMetadata(ctx context.Context, paths []string, hashMap map[string]ImageHash) map[string]ImageMetadata {
	n := len(paths)
	metaSlice := make([]ImageMetadata, n)
	runParallelIndexed(ctx, n, runtime.NumCPU(), func(i int) {
		path := paths[i]
		h := hashMap[path]
		metaSlice[i] = ExtractMetadataFast(path, h.Width, h.Height, h.Size, h.Exif)
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
			meta.PHash = h.PHash
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
// threshold is a HAMMING DISTANCE IN BITS, not the slider's percentage. Convert
// with hammingThresholdBits before calling — passing a percentage straight
// through is the bug that put 99.4% of a 6,596-image library into one group.
//
// Flow:
//  1. findExactPaths — O(n) grouping by xxHash.
//  2. findPerceptualPaths — aspect-ratio bucketed BK-Trees (#4), producing
//     transitive Union-Find candidate sets.
//  3. refinePerceptualGroups — split those chains into clusters whose members
//     are all within threshold of a shared leader, and record each cluster's
//     diameter for the confidence figure.
//  4. collectUniquePaths + parallelExtractMetadata — all file opens run
//     concurrently instead of single-threaded (#2).
//  5. buildGroup — look up pre-computed metadata from the map.
//  6. detectSeriesGroups — relabel burst sequences.
func GroupDuplicates(hashes []ImageHash, threshold int, algorithm string, includeSeries bool) []DuplicateGroup {
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

	// Pass 2: Perceptual duplicate CANDIDATES using aspect-ratio-bucketed
	// BK-Trees, then refinement into clusters that actually hold together.
	fmt.Println("[grouper] Pass 2: Perceptual matching (aspect-ratio BK-Trees)...")
	mode := parseMatchMode(algorithm)
	tSearch := time.Now()
	candidates := findPerceptualPaths(hashes, exactGrouped, threshold, mode)
	searchMs := time.Since(tSearch).Milliseconds()
	candidateCount, largestCandidate := 0, 0
	for _, paths := range candidates {
		if len(paths) >= 2 {
			candidateCount++
		}
		if len(paths) > largestCandidate {
			largestCandidate = len(paths)
		}
	}

	tRefine := time.Now()
	percGroups := refinePerceptualGroups(candidates, hashMap, threshold, mode)
	refineMs := time.Since(tRefine).Milliseconds()
	largestRefined := 0
	for _, g := range percGroups {
		if len(g.Paths) > largestRefined {
			largestRefined = len(g.Paths)
		}
	}
	fmt.Printf("[grouper] Pass 2: %d candidate sets (largest %d) -> %d refined groups (largest %d).\n",
		candidateCount, largestCandidate, len(percGroups), largestRefined)
	fmt.Printf("[perf]    BK-tree search: %dms, cluster refinement: %dms\n", searchMs, refineMs)

	// Optimization B: Lightweight pre-filter to remove filename-sequential
	// groups BEFORE metadata extraction when the user doesn't want series.
	// This avoids opening thousands of burst files on slow drives.
	//
	// The cutoff is half the scan threshold: burst frames sit very close
	// together, so a group that is both tight and sequentially named is almost
	// always a burst. It now tests the group's DIAMETER rather than its closest
	// pair, so a loose group can no longer sneak through on one tight pair.
	if !includeSeries {
		preFilterThreshold := threshold / 2
		kept := percGroups[:0]
		preFilterCount := 0
		for _, g := range percGroups {
			if g.MaxDist <= preFilterThreshold && isFilenameSeriesFromPaths(g.Paths) {
				preFilterCount++
				continue
			}
			kept = append(kept, g)
		}
		percGroups = kept
		fmt.Printf("[grouper] Pre-filtered %d series groups (diameter <= %d). Remaining perceptual: %d\n",
			preFilterCount, preFilterThreshold, len(percGroups))
	}

	// Note on singletons: refinePerceptualGroups only emits clusters of 2+, so
	// the one-element Union-Find roots that used to reach the metadata phase are
	// already gone by here. That filtering matters — measured on a 1,134-file
	// iPhone corpus over SMB, 981 of 1,005 perceptual entries (97.6%) were
	// singletons, and dropping them before metadata extraction cut that phase
	// from 17.9s to 0.6s.

	// Parallel metadata extraction for all duplicate files (#2).
	// Uses ExtractMetadataFast with pre-computed dimensions (Optimization A).
	percPaths := make(map[string][]string, len(percGroups))
	for i, g := range percGroups {
		percPaths[strconv.Itoa(i)] = g.Paths
	}
	allPaths := collectUniquePaths(exactGroups, percPaths)
	fmt.Printf("[grouper] Extracting metadata for %d files (parallel)...\n", len(allPaths))
	tMeta := time.Now()
	metaMap := parallelExtractMetadata(context.Background(), allPaths, hashMap)
	fmt.Printf("[perf]    Metadata extraction: %dms for %d files\n", time.Since(tMeta).Milliseconds(), len(allPaths))
	// Perf tracing (Trace 3): report the read-vs-EXIF split for the metadata phase.
	printAndResetMetadataSplit()

	// Build DuplicateGroup structs from pre-computed metadata.
	var groups []DuplicateGroup
	for _, paths := range exactGroups {
		groups = append(groups, buildGroup("exact", 100.0, paths, metaMap, hashMap))
	}
	for _, g := range percGroups {
		// Confidence comes from the group's WORST pair, so the number describes
		// the whole group rather than its luckiest coincidence.
		confidence := (1.0 - float64(g.MaxDist)/float64(perceptualHashBits)) * 100.0
		groups = append(groups, buildGroup("perceptual", confidence, g.Paths, metaMap, hashMap))
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
