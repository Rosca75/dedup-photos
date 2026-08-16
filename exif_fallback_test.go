// exif_fallback_test.go — regression guard for the plan 05 optimisation.
//
// The plan's "Watch for" clause: files whose EXIF is not in the header window
// must still get correct metadata via the fallback — "verify explicitly by
// forcing the fallback path on a sample and diffing the result against the fast
// path".
//
// Forcing it is easy: ExtractMetadataFast takes the fast path only when
// ScanExif.OK is true, so passing a zero ScanExif reproduces exactly what a
// file with EXIF outside the window does at runtime. The two results must be
// identical, INCLUDING QualityScore — that is the field which decides which
// duplicate is marked "best", i.e. which file the user is offered to delete.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFallbackMatchesPrefilledPath(t *testing.T) {
	matches, _ := filepath.Glob("samples/*")
	nested, _ := filepath.Glob("samples/*/*")
	matches = append(matches, nested...)

	checked := 0
	for _, path := range matches {
		ext := strings.ToLower(filepath.Ext(path))
		if _, ok := imageFormatForExt(ext); !ok {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}

		// Reproduce what the hash phase computes and hands forward.
		dh, ph, w, h, exif, _ := computeDHashFromHeader(path)
		_, _ = dh, ph
		if !exif.OK {
			t.Logf("%s: no EXIF from the header window — this file only ever "+
				"takes the fallback, nothing to compare", filepath.Base(path))
			continue
		}

		fast := ExtractMetadataFast(path, w, h, info.Size(), exif)
		slow := ExtractMetadataFast(path, w, h, info.Size(), ScanExif{})

		checked++
		if fast != slow {
			t.Errorf("%s: fast path and fallback disagree\n  fast: %+v\n  slow: %+v",
				filepath.Base(path), fast, slow)
		}
		if fast.QualityScore != slow.QualityScore {
			t.Errorf("%s: QualityScore differs (%d vs %d) — this moves the "+
				"\"best image\" marker", filepath.Base(path), fast.QualityScore, slow.QualityScore)
		}
	}

	if checked == 0 {
		t.Skip("no sample files produced header-window EXIF")
	}
	t.Logf("fast path == fallback on %d sample files", checked)
}

// TestPrefilledNeedsDimensions pins the guard in ExtractMetadataFast: a
// non-HEIC file with unknown dimensions must NOT take the prefilled shortcut,
// because the fallback's own 128 KB read can still recover dimensions that the
// hash phase's (possibly 64 KB) buffer did not — and losing them would zero the
// resolution part of QualityScore.
func TestPrefilledNeedsDimensions(t *testing.T) {
	matches, _ := filepath.Glob("samples/false_duplicates/*.JPG")
	if len(matches) == 0 {
		t.Skip("no non-HEIC samples")
	}
	path := matches[0]
	info, err := os.Stat(path)
	if err != nil {
		t.Skipf("cannot stat %s: %v", path, err)
	}

	// Pass EXIF as captured but claim dimensions are unknown, exactly as a
	// 64 KB buffer that failed DecodeConfig would.
	_, _, _, _, exif, _ := computeDHashFromHeader(path)
	if !exif.OK {
		t.Skip("sample yielded no header-window EXIF")
	}
	got := ExtractMetadataFast(path, 0, 0, info.Size(), exif)

	if got.Width == 0 || got.Height == 0 {
		t.Errorf("%s: dimensions not recovered (%dx%d) — the prefilled shortcut "+
			"skipped the fallback's DecodeConfig", filepath.Base(path), got.Width, got.Height)
	}
	if got.QualityScore == 0 {
		t.Errorf("%s: QualityScore is 0, so the resolution component was lost", filepath.Base(path))
	}
}
