// =============================================================================
// quicktime_box.go — Minimal ISOBMFF / QuickTime box reader
// =============================================================================
//
// QuickTime and MP4 files are trees of boxes (atoms). Each box is a 4-byte big-
// endian size, a 4-byte type, then its payload — which for container boxes is
// more boxes. Two size values are special: 1 means the real 64-bit size follows
// the header, and 0 means the box runs to the end of the file.
//
// Only what livephoto_quicktime.go needs is implemented here: find a box at the
// top level of a file, and find a child box inside a container already in
// memory. Nothing else about the format is interpreted.
// =============================================================================

package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

// readTopLevelBox walks a file's top-level box chain and returns the payload
// (everything after the header) of the first box of the requested type.
//
// It reads only the 8-byte headers while searching, so finding a box near the
// end of a large file costs a handful of small reads rather than the whole file.
func readTopLevelBox(f *os.File, fileSize int64, want string) ([]byte, error) {
	header := make([]byte, 8)
	for off := int64(0); off+8 <= fileSize; {
		if _, err := f.ReadAt(header, off); err != nil {
			return nil, err
		}
		size := int64(binary.BigEndian.Uint32(header[0:4]))
		typ := string(header[4:8])
		payloadOff := off + 8

		switch {
		case size == 1:
			// Extended size: the real 64-bit length follows the header.
			ext := make([]byte, 8)
			if _, err := f.ReadAt(ext, off+8); err != nil {
				return nil, err
			}
			size = int64(binary.BigEndian.Uint64(ext))
			payloadOff = off + 16
		case size == 0:
			// Box runs to the end of the file.
			size = fileSize - off
		case size < 8:
			return nil, fmt.Errorf("invalid box size %d at offset %d", size, off)
		}

		if typ == want {
			length := off + size - payloadOff
			if length <= 0 || payloadOff+length > fileSize {
				return nil, fmt.Errorf("box %q has an invalid payload range", want)
			}
			payload := make([]byte, length)
			_, err := f.ReadAt(payload, payloadOff)
			return payload, err
		}
		off += size
	}
	return nil, fmt.Errorf("box %q not found", want)
}

// findChildBox scans an in-memory box container for a child of the given type
// and returns that child's payload.
func findChildBox(container []byte, want string) ([]byte, error) {
	for off := 0; off+8 <= len(container); {
		size := int(binary.BigEndian.Uint32(container[off : off+4]))
		typ := string(container[off+4 : off+8])
		if size < 8 || off+size > len(container) {
			return nil, fmt.Errorf("box %q not found (bad child size at %d)", want, off)
		}
		if typ == want {
			return container[off+8 : off+size], nil
		}
		off += size
	}
	return nil, fmt.Errorf("box %q not found", want)
}

// skipMetaVersionFlags normalises the two forms of the `meta` box.
//
// A QuickTime `meta` is a plain container whose children start immediately; the
// ISO/MP4 variant prefixes 4 bytes of version and flags. Both appear in the
// wild, so detect which by looking for the mandatory `hdlr` child at both
// offsets.
func skipMetaVersionFlags(meta []byte) []byte {
	if len(meta) > 12 && string(meta[4:8]) != "hdlr" && string(meta[8:12]) == "hdlr" {
		return meta[4:]
	}
	return meta
}
