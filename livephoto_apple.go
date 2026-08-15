// =============================================================================
// livephoto_apple.go — Read Apple's ContentIdentifier out of a HEIC still
// =============================================================================
//
// An iPhone Live Photo is stored as two files: a .HEIC still and a .MOV clip.
// They are the same photo, and the authoritative link between them is a UUID
// Apple writes into both. On the still it lives in the Apple maker note, at tag
// 0x0011; livephoto_quicktime.go handles the video side.
//
// Reaching it means walking four nested containers:
//
//	ISOBMFF (the HEIC file)
//	  └─ Exif item payload
//	       └─ TIFF header + IFD0
//	            └─ tag 0x8769  Exif IFD
//	                 └─ tag 0x927C  MakerNote  ("Apple iOS\0\0\x01" + IFD)
//	                      └─ tag 0x0011  ContentIdentifier
//
// bep/imagemeta cannot be used for this even though it surfaces the tag as
// "MakerNoteApple": it hands the value back as a Go string and the bytes come
// back truncated (824 of 1731 on a test file). Since Apple's value offsets are
// relative to the start of the maker note, the UUID at offset 1312 then falls
// outside the blob entirely. The TIFF walk in tiff_walk.go is used instead.
//
// Measured on 1,038 iPhone HEICs: 699 carry the tag (the other 339 are plain
// stills), and all 699 had it inside the first 128 KB — the same window the hash
// phase already reads.
// =============================================================================

package main

import (
	"bytes"
	"encoding/binary"
	"strings"
)

// appleMakerNoteHeaderLen is the length of the Apple maker-note preamble:
// "Apple iOS\0\0\x01" followed by a 2-byte byte-order marker ("MM" or "II").
// The maker note's own IFD starts immediately after it.
const appleMakerNoteHeaderLen = 14

// appleContentIDTag is Apple's maker-note tag holding the Live Photo UUID.
const appleContentIDTag = 0x0011

// heicContentID returns the Apple ContentIdentifier UUID for a HEIC still, or
// "" when the file carries none (i.e. it is not half of a Live Photo).
//
// header must hold the front of the file; heicHeaderReadSize is enough.
func heicContentID(header []byte) string {
	tiff, ok := findExifTIFF(header)
	if !ok {
		return ""
	}
	bo, ifd0, ok := tiffByteOrder(tiff)
	if !ok {
		return ""
	}

	// IFD0 → the Exif IFD's offset.
	var exifIFD uint32
	walkTIFFIFD(tiff, bo, ifd0, func(tag uint16, _ uint16, _ uint32, valOff uint32, _ []byte) {
		if tag == 0x8769 {
			exifIFD = valOff
		}
	})
	if exifIFD == 0 {
		return ""
	}

	// Exif IFD → the raw maker-note bytes.
	var maker []byte
	walkTIFFIFD(tiff, bo, exifIFD, func(tag uint16, _ uint16, _ uint32, _ uint32, raw []byte) {
		if tag == 0x927C {
			maker = raw
		}
	})
	return appleMakerNoteID(maker)
}

// appleMakerNoteID parses an Apple maker note and returns tag 0x0011.
//
// The maker note is a self-contained TIFF-style IFD with its OWN byte order, and
// crucially its value offsets are relative to the start of the maker note rather
// than to the TIFF header — hence walking it with the maker note itself as the
// buffer. That is also why a truncated maker note is useless: the UUID sits
// roughly 1,300 bytes in.
func appleMakerNoteID(blob []byte) string {
	if !bytes.HasPrefix(blob, []byte("Apple iOS")) || len(blob) < appleMakerNoteHeaderLen+2 {
		return ""
	}
	var bo binary.ByteOrder = binary.BigEndian
	if blob[12] == 'I' && blob[13] == 'I' {
		bo = binary.LittleEndian
	}

	var id string
	walkTIFFIFD(blob, bo, appleMakerNoteHeaderLen, func(tag, typ uint16, _ uint32, _ uint32, raw []byte) {
		// The UUID is stored as an ASCII string (TIFF type 2).
		if tag == appleContentIDTag && typ == 2 && raw != nil {
			id = strings.TrimRight(string(raw), "\x00")
		}
	})
	return id
}
