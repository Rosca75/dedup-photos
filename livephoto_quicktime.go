// =============================================================================
// livephoto_quicktime.go — Read ContentIdentifier out of a Live Photo's .MOV
// =============================================================================
//
// The video half of a Live Photo carries the same UUID as its still, under the
// QuickTime metadata key "com.apple.quicktime.content.identifier".
// livephoto_apple.go handles the still side; quicktime_box.go does the box
// walking.
//
// The metadata lives at:
//
//	moov
//	  └─ meta
//	       ├─ keys   the key names, numbered from 1
//	       └─ ilst   the values; each child's BOX TYPE is the key's number
//
// Note the indirection in `ilst`: a child box is not named after its key, it is
// named after the key's *index* — box type 0x00000003 means "the third entry in
// keys". That is why both boxes have to be read together.
//
// Cost note: `moov` sits at a median 99.8% of the file in iPhone recordings, so
// this is a seek to the tail of a multi-megabyte file — ~25 ms over SMB. Cheap
// once, but far too expensive to run across a whole library, which is why
// pairing happens at delete time rather than during a scan (see livephoto.go).
// =============================================================================

package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

// quickTimeContentIDKey is the metadata key Apple stores the Live Photo UUID
// under in the video half.
const quickTimeContentIDKey = "com.apple.quicktime.content.identifier"

// movContentID returns the QuickTime ContentIdentifier for a video file, or ""
// when it carries none (i.e. it is not half of a Live Photo).
func movContentID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}

	moov, err := readTopLevelBox(f, info.Size(), "moov")
	if err != nil {
		return "", fmt.Errorf("no moov box: %w", err)
	}
	meta, err := findChildBox(moov, "meta")
	if err != nil {
		return "", fmt.Errorf("no moov/meta box: %w", err)
	}
	meta = skipMetaVersionFlags(meta)

	keysBox, err := findChildBox(meta, "keys")
	if err != nil {
		return "", fmt.Errorf("no keys box: %w", err)
	}
	ilstBox, err := findChildBox(meta, "ilst")
	if err != nil {
		return "", fmt.Errorf("no ilst box: %w", err)
	}

	return lookupIlstValue(ilstBox, parseQuickTimeKeys(keysBox), quickTimeContentIDKey), nil
}

// parseQuickTimeKeys reads the `keys` box into an ordered list of key names.
// Layout: 4 bytes version/flags, 4 bytes entry count, then entries of
// [4-byte size][4-byte namespace][key name].
func parseQuickTimeKeys(box []byte) []string {
	if len(box) < 8 {
		return nil
	}
	count := int(binary.BigEndian.Uint32(box[4:8]))
	keys := make([]string, 0, count)
	off := 8
	for i := 0; i < count && off+8 <= len(box); i++ {
		size := int(binary.BigEndian.Uint32(box[off : off+4]))
		if size < 8 || off+size > len(box) {
			break
		}
		keys = append(keys, string(box[off+8:off+size]))
		off += size
	}
	return keys
}

// lookupIlstValue finds the value stored under wantKey.
//
// Each `ilst` child's box type is a big-endian 1-based index into keys, and the
// value sits in a nested `data` box after 4 bytes of type indicator and 4 bytes
// of locale.
func lookupIlstValue(box []byte, keys []string, wantKey string) string {
	const dataHeaderLen = 8
	for off := 0; off+8 <= len(box); {
		size := int(binary.BigEndian.Uint32(box[off : off+4]))
		if size < 8 || off+size > len(box) {
			return ""
		}
		index := int(binary.BigEndian.Uint32(box[off+4 : off+8]))
		if index >= 1 && index <= len(keys) && keys[index-1] == wantKey {
			data, err := findChildBox(box[off+8:off+size], "data")
			if err != nil || len(data) <= dataHeaderLen {
				return ""
			}
			return string(data[dataHeaderLen:])
		}
		off += size
	}
	return ""
}
