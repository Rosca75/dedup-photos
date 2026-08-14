//go:build linux

package main

import "github.com/ebitengine/purego"

// heicDynamicVersionAtLeast returns true if the dynamically loaded libheif is
// at least the given major.minor version. If the library cannot be opened,
// returns true (safe default: don't force WASM mode unnecessarily).
func heicDynamicVersionAtLeast(major, minor int) bool {
	handle, err := purego.Dlopen("libheif.so", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return true
	}
	defer purego.Dlclose(handle)

	var getMajor func() uint32
	var getMinor func() uint32
	purego.RegisterLibFunc(&getMajor, handle, "heif_get_version_number_major")
	purego.RegisterLibFunc(&getMinor, handle, "heif_get_version_number_minor")

	maj := int(getMajor())
	min := int(getMinor())
	if maj != major {
		return maj > major
	}
	return min >= minor
}
