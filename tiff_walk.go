// =============================================================================
// tiff_walk.go — Minimal TIFF/EXIF IFD reader
// =============================================================================
//
// bep/imagemeta handles all the EXIF this project normally needs (see
// exif_extract.go). This file exists for the one thing it cannot do: hand back
// the raw bytes of a maker note. It returns tag 0x927C as a Go string and
// truncates it, which loses Apple's ContentIdentifier — so livephoto_apple.go
// walks the TIFF structure itself, using the helpers here.
//
// A TIFF block is a header naming a byte order and the offset of IFD0, followed
// by IFDs: a 2-byte entry count, then 12-byte entries of
// [tag][type][value count][value or offset-to-value]. Every offset is measured
// from the start of the TIFF header, which is why callers pass the block as a
// slice beginning exactly there.
// =============================================================================

package main

import "encoding/binary"

// findExifTIFF locates the TIFF block holding a file's EXIF and returns it as a
// slice starting at the TIFF header, the origin all TIFF offsets are relative to.
//
// It scans for the TIFF signature rather than resolving the ISOBMFF `iloc` box
// properly. That is a shortcut, but a well-behaved one: a candidate is accepted
// only when a plausible IFD0 follows, and it was correct on every file of a
// 1,038-image HEIC corpus. It also works unchanged on JPEG, whose APP1 payload
// begins with the same TIFF header — untested, as that corpus had no JPEG Live
// Photos.
func findExifTIFF(buf []byte) ([]byte, bool) {
	for i := 0; i+8 < len(buf); i++ {
		bigEndian := buf[i] == 'M' && buf[i+1] == 'M' && buf[i+2] == 0x00 && buf[i+3] == 0x2A
		littleEndian := buf[i] == 'I' && buf[i+1] == 'I' && buf[i+2] == 0x2A && buf[i+3] == 0x00
		if !bigEndian && !littleEndian {
			continue
		}
		candidate := buf[i:]
		if _, _, ok := tiffByteOrder(candidate); ok {
			return candidate, true
		}
	}
	return nil, false
}

// tiffByteOrder reads a TIFF header and returns its byte order and the offset of
// IFD0. The offset is validated against the buffer, so a false positive from the
// signature scan in findExifTIFF is rejected here rather than causing an
// out-of-range read later.
func tiffByteOrder(tiff []byte) (binary.ByteOrder, uint32, bool) {
	if len(tiff) < 8 {
		return nil, 0, false
	}
	var bo binary.ByteOrder
	switch {
	case tiff[0] == 'M' && tiff[1] == 'M':
		bo = binary.BigEndian
	case tiff[0] == 'I' && tiff[1] == 'I':
		bo = binary.LittleEndian
	default:
		return nil, 0, false
	}
	ifd0 := bo.Uint32(tiff[4:8])
	// An IFD needs at least its 2-byte entry count to sit within the buffer.
	if ifd0 < 8 || int(ifd0)+2 > len(tiff) {
		return nil, 0, false
	}
	return bo, ifd0, true
}

// walkTIFFIFD calls fn once per entry of the IFD at offset off within buf.
//
// raw holds the entry's value bytes when they could be resolved, and is nil
// otherwise. Values of 4 bytes or fewer are stored inline in the entry's offset
// field; longer ones live at the offset it holds.
//
// buf is whatever the offsets are relative to. For a normal IFD that is the TIFF
// block; for a maker note it is the maker note itself, which is the detail that
// makes a truncated maker note unusable.
func walkTIFFIFD(buf []byte, bo binary.ByteOrder, off uint32, fn func(tag, typ uint16, count, valOff uint32, raw []byte)) {
	if int(off)+2 > len(buf) {
		return
	}
	entryCount := int(bo.Uint16(buf[off : off+2]))
	for i := 0; i < entryCount; i++ {
		entry := int(off) + 2 + i*12
		if entry+12 > len(buf) {
			return
		}
		tag := bo.Uint16(buf[entry : entry+2])
		typ := bo.Uint16(buf[entry+2 : entry+4])
		count := bo.Uint32(buf[entry+4 : entry+8])
		valOff := bo.Uint32(buf[entry+8 : entry+12])

		size := count * tiffTypeSize(typ)
		var raw []byte
		if size <= 4 {
			raw = buf[entry+8 : entry+8+int(size)]
		} else if int(valOff)+int(size) <= len(buf) {
			raw = buf[valOff : valOff+size]
		}
		fn(tag, typ, count, valOff, raw)
	}
}

// tiffTypeSize returns the byte width of one element of a TIFF field type.
// Unknown types are treated as 1 byte, which keeps the walk in bounds.
func tiffTypeSize(typ uint16) uint32 {
	switch typ {
	case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
		return 1
	case 3, 8: // SHORT, SSHORT
		return 2
	case 4, 9, 11: // LONG, SLONG, FLOAT
		return 4
	case 5, 10, 12: // RATIONAL, SRATIONAL, DOUBLE
		return 8
	}
	return 1
}
