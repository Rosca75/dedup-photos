//go:build darwin

package main

import "github.com/ebitengine/purego"

// heicDynamicVersionAtLeast returns true if the dynamically loaded libheif is
// at least the given major.minor version.
func heicDynamicVersionAtLeast(major, minor int) bool {
	var handle uintptr
	var err error
	for _, lib := range []string{"libheif.dylib", "/opt/homebrew/lib/libheif.dylib"} {
		handle, err = purego.Dlopen(lib, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err == nil {
			break
		}
	}
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
