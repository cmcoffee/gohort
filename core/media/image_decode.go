package media

import (
	"bytes"
	"fmt"
	"image"
)

// MaxImagePixels bounds the pixel count of any image decoded from bytes this
// process did not make. 50 megapixels clears every real photo and screenshot
// (a 48 MP phone sensor, an 8K frame at 33 MP) with room to spare.
//
// The bound exists because a decoder allocates for the dimensions the HEADER
// claims, not for the bytes that arrived: a few kilobytes of PNG can declare
// 100,000 x 100,000 pixels and ask for 40 GB before a single row is read.
const MaxImagePixels = 50_000_000

// CheckImageDimensions reports whether an image of the given size may be
// decoded, and says why not when it may not.
func CheckImageDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("the image declares no usable dimensions (%dx%d)", width, height)
	}
	if int64(width)*int64(height) > MaxImagePixels {
		return fmt.Errorf("the image declares %dx%d pixels, over the %d megapixel limit", width, height, MaxImagePixels/1_000_000)
	}
	return nil
}

// DecodeImage is image.Decode with the size read first. DecodeConfig parses
// only the header, so an image that would not fit is refused before anything
// is allocated for it. Use it for every image from outside the process: an
// upload, a download, a backend's render, a page capture.
func DecodeImage(data []byte) (image.Image, string, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	if err := CheckImageDimensions(cfg.Width, cfg.Height); err != nil {
		return nil, "", err
	}
	return image.Decode(bytes.NewReader(data))
}
