package imagefetch

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"testing"
)

// scene is a busy w by h picture (gradients and stripes, so a re-encode
// moves pixels everywhere a little), with a w/10-wide square patch drawn in
// patch when patch is not nil, the size of one face in a group photo.
func scene(w, h int, patch color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), uint8((x/7 + y/5) % 2 * 200), 255})
		}
	}
	if patch != nil {
		s := w / 10
		draw.Draw(img, image.Rect(w/8, h/3, w/8+s, h/3+s), &image.Uniform{patch}, image.Point{}, draw.Src)
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A result that is the picture it was given, re-encoded or at another size,
// reads as unchanged; one with a single face-sized patch changed does not.
func TestAnEditThatChangedNothingIsMeasured(t *testing.T) {
	before := encodePNG(t, scene(800, 450, nil))
	for name, after := range map[string][]byte{
		"re-encoded":   encodeJPEG(t, scene(800, 450, nil)),
		"another size": encodePNG(t, scene(1600, 900, nil)),
	} {
		if most, ok := largestChange(before, after); !ok || most >= unchangedBelow {
			t.Errorf("%s: largest change %.3f (read %v), want under %.2f", name, most, ok, unchangedBelow)
		}
	}
	changed := encodePNG(t, scene(800, 450, color.RGBA{250, 220, 200, 255}))
	if most, ok := largestChange(before, changed); !ok || most < unchangedBelow {
		t.Errorf("a face-sized patch changed: largest change %.3f, want at least %.2f", most, unchangedBelow)
	}
	if _, ok := largestChange(before, []byte("not a picture")); ok {
		t.Error("an unreadable result was measured")
	}
}
