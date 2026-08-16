// exif_parity_test.go — regression guard for the plan 05 optimisation.
//
// The hash phase hands extractExifInto a bytes.Reader over the header buffer it
// already holds, instead of an *os.File. This test asserts the two readers
// produce IDENTICAL ImageMetadata — the property that makes the swap safe.
//
// Keep this. If it ever fails, the failure mode is not a crash: EXIF absence is
// not an error, so a drift here surfaces as a shifted QualityScore, which moves
// the "best image" marker onto a different file — i.e. it changes which photo
// the user is offered to delete. That is exactly the class of silent breakage
// CLAUDE.md §9 exists to prevent.
//
// Runs on samples/ only: fast, no network share, no external fixtures.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExifBufferMatchesFileHandle(t *testing.T) {
	matches, _ := filepath.Glob("samples/*")
	nested, _ := filepath.Glob("samples/*/*")
	matches = append(matches, nested...)

	checked := 0
	for _, path := range matches {
		ext := strings.ToLower(filepath.Ext(path))
		format, ok := imageFormatForExt(ext)
		if !ok {
			continue
		}

		// Ground truth: the file handle, exactly as production does today.
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		var fromFile ImageMetadata
		extractExifInto(f, format, &fromFile)
		f.Close()

		// Candidate: the 128 KB window the hash phase already holds.
		buf, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if len(buf) > heicHeaderReadSize {
			buf = buf[:heicHeaderReadSize]
		}
		var fromBuf ImageMetadata
		extractExifInto(bytes.NewReader(buf), format, &fromBuf)

		checked++
		if fromBuf != fromFile {
			t.Errorf("%s: buffer and file-handle EXIF differ\n  buf : %+v\n  file: %+v",
				filepath.Base(path), fromBuf, fromFile)
			continue
		}
		if fromFile.DateTaken == "" {
			t.Logf("%s: no EXIF from either reader (nothing to compare)", filepath.Base(path))
		}
	}

	if checked == 0 {
		t.Skip("no sample images with a known image format")
	}
	t.Logf("compared %d sample files", checked)
}
