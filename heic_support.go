package main

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rosca75/heic"
	"github.com/bep/imagemeta"
)

// initHEIC configures the heic package at startup.
// If dynamic libheif is in use but its version is < 1.18, force WASM mode so
// that HDR / tmap-brand HEIC files (common on iPhone) decode correctly.
func initHEIC() {
	if heic.Dynamic() != nil {
		// Dynamic libheif not available; WASM will be used automatically.
		return
	}
	if !heicDynamicVersionAtLeast(1, 18) {
		heic.ForceWasmMode = true
		log.Println("[heic] Dynamic libheif < 1.18; using WASM decoder for full compatibility")
	}
}

// isHEIC reports whether path has a .heic or .heif extension.
func isHEIC(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".heic" || ext == ".heif"
}

// heicThumbnailJPEG returns a JPEG-encoded, max-400px thumbnail for a HEIC
// file. It tries the embedded thumbnail first (fast path); falls back to a
// full decode if none is present.
func heicThumbnailJPEG(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return nil, err
	}

	img, err := heic.DecodeThumbnail(bytes.NewReader(data))
	if err != nil {
		img, err = heic.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
	}

	result := resizeImageToJPEG(img, 400, 85)
	if result == nil {
		return nil, fmt.Errorf("jpeg encode failed")
	}
	return result, nil
}

// resizeImageToJPEG resizes img to fit within maxDim×maxDim (preserving aspect
// ratio) and JPEG-encodes it at the given quality level. Returns nil on error.
func resizeImageToJPEG(img image.Image, maxDim, quality int) []byte {
	b := img.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	newW, newH := srcW, srcH
	if srcW > maxDim || srcH > maxDim {
		if srcW >= srcH {
			newW = maxDim
			newH = srcH * maxDim / srcW
		} else {
			newH = maxDim
			newW = srcW * maxDim / srcH
		}
	}
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	thumb := image.NewRGBA(image.Rect(0, 0, newW, newH))
	for y := 0; y < newH; y++ {
		for x := 0; x < newW; x++ {
			thumb.Set(x, y, img.At(b.Min.X+x*srcW/newW, b.Min.Y+y*srcH/newH))
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: quality}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// computeDHashHEIC computes a dHash and image dimensions for a HEIC file.
// It reads the entire file (HEIC containers require full access), then tries
// the embedded thumbnail for a fast dHash before falling back to full decode.
func computeDHashHEIC(path, algorithm string) (dHash uint64, width, height int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, ErrNoThumbnail
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return 0, 0, 0, ErrNoThumbnail
	}

	// Get full-image dimensions from the ISOBMFF container metadata — no WASM.
	if res, metaErr := imagemeta.Decode(imagemeta.Options{
		R:           bytes.NewReader(data),
		ImageFormat: imagemeta.HEIF,
		Sources:     imagemeta.CONFIG,
	}); metaErr == nil && res.ImageConfig.Width > 0 {
		width, height = res.ImageConfig.Width, res.ImageConfig.Height
	}

	img, thumbErr := heic.DecodeThumbnail(bytes.NewReader(data))
	if thumbErr != nil {
		img, err = heic.Decode(bytes.NewReader(data))
		if err != nil {
			return 0, width, height, ErrNoThumbnail
		}
	}

	return computeDHashFromImage(img), width, height, nil
}

// extractHEICExif populates meta with EXIF data from a HEIC file using
// bep/imagemeta, which natively handles the HEIC ISOBMFF EXIF container.
func extractHEICExif(path string, meta *ImageMetadata) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	var make_, model string
	var latRef, lonRef string
	var lat, lon float64
	var hasLat, hasLon bool

	_, _ = imagemeta.Decode(imagemeta.Options{
		R:           f,
		ImageFormat: imagemeta.HEIF,
		HandleTag: func(ti imagemeta.TagInfo) error {
			switch ti.Tag {
			case "DateTimeOriginal":
				if s, ok := ti.Value.(string); ok && s != "" && meta.DateTaken == "" {
					if t, parseErr := time.Parse("2006:01:02 15:04:05", s); parseErr == nil {
						meta.DateTaken = t.Format("2006-01-02T15:04:05")
					}
				}
			case "Make":
				if s, ok := ti.Value.(string); ok {
					make_ = strings.TrimSpace(s)
				}
			case "Model":
				if s, ok := ti.Value.(string); ok {
					model = strings.TrimSpace(s)
				}
			case "LensModel":
				if s, ok := ti.Value.(string); ok && meta.Lens == "" {
					meta.Lens = strings.TrimSpace(s)
				}
			case "ISO":
				if meta.ISO == 0 {
					switch v := ti.Value.(type) {
					case uint16:
						meta.ISO = int(v)
					case uint32:
						meta.ISO = int(v)
					}
				}
			case "FocalLength":
				if meta.FocalLength == "" {
					if r, ok := ti.Value.(imagemeta.Rat[uint32]); ok {
						meta.FocalLength = fmt.Sprintf("%.1fmm", r.Float64())
					}
				}
			case "GPSLatitudeRef":
				if s, ok := ti.Value.(string); ok {
					latRef = s
				}
			case "GPSLongitudeRef":
				if s, ok := ti.Value.(string); ok {
					lonRef = s
				}
			case "GPSLatitude":
				if fv, ok := ti.Value.(float64); ok {
					lat = fv
					hasLat = true
				}
			case "GPSLongitude":
				if fv, ok := ti.Value.(float64); ok {
					lon = fv
					hasLon = true
				}
			case "ImageDescription":
				if s, ok := ti.Value.(string); ok && meta.Description == "" {
					meta.Description = strings.TrimSpace(s)
				}
			}
			return nil
		},
	})

	if meta.Camera == "" && (make_ != "" || model != "") {
		meta.Camera = strings.TrimSpace(make_ + " " + model)
	}
	if hasLat && meta.GPSLat == 0 {
		meta.GPSLat = lat
		if strings.HasPrefix(strings.ToUpper(latRef), "S") {
			meta.GPSLat = -lat
		}
	}
	if hasLon && meta.GPSLon == 0 {
		meta.GPSLon = lon
		if strings.HasPrefix(strings.ToUpper(lonRef), "W") {
			meta.GPSLon = -lon
		}
	}
}
