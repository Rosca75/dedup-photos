//go:build !linux && !darwin

package main

// heicDynamicVersionAtLeast returns true on platforms where we don't perform
// a dynamic library version probe (Windows, BSD, etc.).
// Dynamic libheif is not supported on these platforms anyway, so initHEIC
// will have already returned early after checking heic.Dynamic().
func heicDynamicVersionAtLeast(major, minor int) bool {
	return true
}
