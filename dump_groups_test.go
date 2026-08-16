// dump_groups_test.go — opt-in headless harness for before/after comparison of
// grouping output.
//
// Written for Validation item 3 of docs/05-GROUPING-METADATA-REUSE.md, which
// requires proving that grouping output is unchanged by an optimisation: same
// match type, same sorted member filenames, same chosen "best image" per group.
// It is kept because that check applies to any future change to the hasher,
// grouper or metadata phases, and there is otherwise no headless entry point —
// the app is a Wails GUI. A test file can call into `package main` without a
// build tag and without colliding with main().
//
// It skips unless DUMP_PATH is set, so it costs nothing in a normal `go test`
// run. Point it at a SMALL directory: this is a scan, and scanning a full photo
// library over a network share takes tens of minutes.
//
// The corpus location is taken from the environment and must stay that way —
// never hardcode a path here, this repository is public.
//
// It replicates the three phases runScan performs, skipping only the Wails
// global-state bookkeeping, and prints a deterministic dump that `diff` can
// compare across builds.
//
// Usage:
//
//	DUMP_PATH="/path/to/library" DUMP_OUT=/tmp/before.txt \
//	  go test -run TestDumpGroups -timeout 60m -v .
//
// Env:
//
//	DUMP_PATH       directory to scan (required; the test skips without it)
//	DUMP_OUT        file to write the dump to (default: stdout only)
//	DUMP_THRESHOLD  slider percentage, same units as the UI (default 10)
//	DUMP_ALG        "dhash" | "phash" | "both" (default "dhash")
package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDumpGroups(t *testing.T) {
	path := os.Getenv("DUMP_PATH")
	if path == "" {
		t.Skip("DUMP_PATH not set — this is an opt-in measurement harness")
	}

	threshold := 10
	if v := os.Getenv("DUMP_THRESHOLD"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("DUMP_THRESHOLD=%q is not a number: %v", v, err)
		}
		threshold = n
	}
	alg := os.Getenv("DUMP_ALG")
	if alg == "" {
		alg = "dhash"
	}

	// The backend choice is normally made in App.startup; without it every HEIC
	// would take a different decode path than a real scan.
	initHEIC()
	resetScanDiagnostics()

	req := ScanRequest{Path: path, Threshold: threshold, Algorithm: alg, IncludeSubfolders: true}
	ctx := context.Background()

	start := time.Now()
	paths, err := walkScanRoot(path, map[string]bool{}, req)
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
	t.Logf("[dump] walk: %d files in %.2fs", len(paths), time.Since(start).Seconds())

	start = time.Now()
	hashes := HashAllImagesWithProgress(ctx, paths, runtime.NumCPU(), nil, []string{path})
	t.Logf("[dump] hash phase: %.2fs", time.Since(start).Seconds())

	start = time.Now()
	groups := GroupDuplicates(hashes, hammingThresholdBits(threshold), alg, req.IncludeSeries)
	t.Logf("[dump] grouping: %.2fs (%d groups)", time.Since(start).Seconds(), len(groups))

	unreadable, skippedPerceptual, _ := snapshotScanDiagnostics()
	out := renderGroupDump(groups, len(paths), skippedPerceptual, unreadable, threshold, alg)

	if dest := os.Getenv("DUMP_OUT"); dest != "" {
		if err := os.WriteFile(dest, []byte(out), 0644); err != nil {
			t.Fatalf("write %s: %v", dest, err)
		}
		t.Logf("[dump] wrote %s (%d bytes)", dest, len(out))
	} else {
		fmt.Print(out)
	}
}

// renderGroupDump formats the parts of the result that must not change.
//
// Everything here is sorted: group IDs are fresh UUIDs on every run and the
// group slice order depends on map iteration, so an unsorted dump would differ
// between two runs of the SAME build and prove nothing. Members are sorted by
// path, groups by their first member.
//
// QualityScore is printed alongside the best-image marker because a change in
// EXIF availability shows up there first — a group whose scores all shifted
// equally still picks the same best image, and that is worth being able to see.
func renderGroupDump(groups []DuplicateGroup, totalFiles, skippedPerceptual, unreadable, threshold int, alg string) string {
	type row struct {
		key  string
		text string
	}
	rows := make([]row, 0, len(groups))

	for _, g := range groups {
		members := make([]string, 0, len(g.Images))
		best := "(none)"
		for _, img := range g.Images {
			members = append(members, fmt.Sprintf("%s q=%d", img.Path, img.QualityScore))
			if img.IsBest {
				best = img.Path
			}
		}
		sort.Strings(members)

		var b strings.Builder
		fmt.Fprintf(&b, "group type=%s confidence=%.2f n=%d\n", g.MatchType, g.Confidence, len(g.Images))
		fmt.Fprintf(&b, "  best=%s\n", best)
		for _, m := range members {
			fmt.Fprintf(&b, "  member=%s\n", m)
		}
		key := ""
		if len(members) > 0 {
			key = members[0]
		}
		rows = append(rows, row{key: key + "|" + g.MatchType, text: b.String()})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })

	var out strings.Builder
	fmt.Fprintf(&out, "threshold=%d%% algorithm=%s\n", threshold, alg)
	fmt.Fprintf(&out, "totalFiles=%d groups=%d skippedPerceptual=%d unreadable=%d\n\n",
		totalFiles, len(groups), skippedPerceptual, unreadable)
	for _, r := range rows {
		out.WriteString(r.text)
	}
	return out.String()
}
