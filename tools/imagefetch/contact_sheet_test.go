package imagefetch

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// pngOf is a solid w×h picture.
func pngOf(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// near says whether a JPEG-decoded pixel is within tol of c on every channel.
func near(got color.Color, c color.RGBA, tol int) bool {
	r, g, b, _ := got.RGBA()
	d := func(a uint32, want uint8) bool {
		v := int(a>>8) - int(want)
		return v >= -tol && v <= tol
	}
	return d(r, c.R) && d(g, c.G) && d(b, c.B)
}

var (
	red   = color.RGBA{0xd0, 0x20, 0x20, 0xff}
	green = color.RGBA{0x20, 0xb0, 0x30, 0xff}
	blue  = color.RGBA{0x20, 0x40, 0xd0, 0xff}
	gold  = color.RGBA{0xe0, 0xc0, 0x20, 0xff}
)

// Every picture is on the sheet, in order, each scaled to the tile height or,
// for a very wide one, to the width cap, and the result's band is coloured.
func TestASheetHoldsEveryPictureInOrder(t *testing.T) {
	sheet, err := contactSheet([][]byte{
		pngOf(t, 640, 480, red),
		pngOf(t, 1024, 1024, green),
		pngOf(t, 2000, 500, blue), // 0.96 would make it 1920 wide: capped to 720×180
	}, []string{"1", "2", "RESULT"})
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(sheet))
	if err != nil {
		t.Fatalf("the sheet is not a JPEG: %v", err)
	}
	wantW := sheetGap + 640 + sheetGap + 480 + sheetGap + 720 + sheetGap
	wantH := sheetGap + sheetLabelHeight + sheetTileHeight + sheetGap
	if b := img.Bounds(); b.Dx() != wantW || b.Dy() != wantH {
		t.Fatalf("sheet is %dx%d, want %dx%d", b.Dx(), b.Dy(), wantW, wantH)
	}
	mid := sheetGap + sheetLabelHeight + sheetTileHeight/2
	for _, c := range []struct {
		x    int
		want color.RGBA
	}{
		{sheetGap + 320, red},
		{sheetGap + 640 + sheetGap + 240, green},
		{sheetGap + 640 + sheetGap + 480 + sheetGap + 360, blue},
	} {
		if got := img.At(c.x, mid); !near(got, c.want, 24) {
			t.Errorf("pixel at x=%d is %v, want about %v: pictures out of order or not drawn", c.x, got, c.want)
		}
	}
	// The result's band is the blue one; a source's is grey.
	bandY := sheetGap + 4
	if got := img.At(wantW-sheetGap-8, bandY); !near(got, sheetResultBand, 24) {
		t.Errorf("RESULT band is %v, want %v", got, sheetResultBand)
	}
	if got := img.At(sheetGap+600, bandY); !near(got, sheetLabelBand, 24) {
		t.Errorf("a source's band is %v, want %v", got, sheetLabelBand)
	}
}

// The label is drawn, not just the band: there is white text in it.
func TestASheetLabelsItsTiles(t *testing.T) {
	sheet, err := contactSheet([][]byte{pngOf(t, 480, 480, red), pngOf(t, 480, 480, green)}, []string{"1", "RESULT"})
	if err != nil {
		t.Fatal(err)
	}
	img, _ := jpeg.Decode(bytes.NewReader(sheet))
	light := 0
	for y := sheetGap; y < sheetGap+sheetLabelHeight; y++ {
		for x := sheetGap + 480 + sheetGap; x < sheetGap+480+sheetGap+200; x++ {
			if r, g, b, _ := img.At(x, y).RGBA(); r>>8 > 200 && g>>8 > 200 && b>>8 > 200 {
				light++
			}
		}
	}
	if light < 50 {
		t.Errorf("found %d white pixels in the RESULT label band: the label was not drawn", light)
	}
}

// More than a row's worth wraps onto a second row.
func TestASheetWrapsAfterThree(t *testing.T) {
	var pics [][]byte
	for _, c := range []color.RGBA{red, green, blue, gold} {
		pics = append(pics, pngOf(t, 480, 480, c))
	}
	sheet, err := contactSheet(pics, []string{"1", "2", "3", "RESULT"})
	if err != nil {
		t.Fatal(err)
	}
	img, _ := jpeg.Decode(bytes.NewReader(sheet))
	row := sheetLabelHeight + sheetTileHeight + sheetGap
	if b := img.Bounds(); b.Dy() != sheetGap+2*row || b.Dx() != sheetGap+3*(480+sheetGap) {
		t.Errorf("sheet is %dx%d, want two rows three wide", b.Dx(), b.Dy())
	}
	if got := img.At(sheetGap+240, sheetGap+row+sheetLabelHeight+240); !near(got, gold, 24) {
		t.Errorf("the fourth picture is not first on the second row: %v", got)
	}
}

// A picture that does not decode fails the sheet, naming which one.
func TestASheetRefusesWhatDoesNotDecode(t *testing.T) {
	_, err := contactSheet([][]byte{pngOf(t, 10, 10, red), []byte("not a picture")}, []string{"1", "RESULT"})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("picture 2 (RESULT)")) {
		t.Errorf("err = %v, want it to name picture 2", err)
	}
	if _, err := contactSheet(nil, nil); err == nil {
		t.Error("an empty sheet should be refused")
	}
}
