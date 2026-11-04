package imagefetch

// A contact sheet: several pictures as ONE, so a model can compare them.
//
// A blend's check needs every source and the result in view at once. Queued as
// separate pictures they often are not: a model with a per-request picture
// limit (vLLM's, two on the deployment this was written for) gets the newest
// ones as pixels and every older one as a paragraph some other model wrote,
// and "is source 1's subject in the result" cannot be answered against a
// paragraph. One picture fits under any limit, and side by side is how the
// comparison is made anyway.

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp"
)

// The sheet's geometry. A tile is scaled to sheetTileHeight, or narrower to
// fit sheetTileMaxWidth for a very wide picture; three to a row keeps the
// sheet near the size a vision model reads at without shrinking it again.
const (
	sheetTileHeight   = 480
	sheetTileMaxWidth = 720
	sheetPerRow       = 3
	sheetGap          = 16
	sheetLabelHeight  = 52
	sheetLabelScale   = 3 // basicfont is 7x13; three times reads at a glance
)

var (
	sheetBackground = color.RGBA{0x20, 0x20, 0x20, 0xff}
	sheetLabelBand  = color.RGBA{0x30, 0x30, 0x30, 0xff}
	sheetResultBand = color.RGBA{0x2b, 0x4a, 0x7a, 0xff} // the RESULT tile stands out
)

// contactSheet tiles pictures into one JPEG, left to right and row by row, each
// under its label. The last tile is taken to be the result and its band is
// coloured, so it is told apart by more than the word.
func contactSheet(pictures [][]byte, labels []string) ([]byte, error) {
	if len(pictures) == 0 || len(pictures) != len(labels) {
		return nil, fmt.Errorf("contact sheet: %d pictures for %d labels", len(pictures), len(labels))
	}
	tiles := make([]image.Image, len(pictures))
	for i, data := range pictures {
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("contact sheet: picture %d (%s) does not decode: %w", i+1, labels[i], err)
		}
		tiles[i] = scaleTile(img)
	}

	// Rows first, so the sheet is as wide as its widest row.
	type placed struct {
		x, y int
		img  image.Image
	}
	var layout []placed
	width, y := 0, sheetGap
	for start := 0; start < len(tiles); start += sheetPerRow {
		x := sheetGap
		for _, t := range tiles[start:min(start+sheetPerRow, len(tiles))] {
			layout = append(layout, placed{x: x, y: y, img: t})
			x += t.Bounds().Dx() + sheetGap
		}
		width = max(width, x)
		y += sheetLabelHeight + sheetTileHeight + sheetGap
	}

	sheet := image.NewRGBA(image.Rect(0, 0, width, y))
	draw.Draw(sheet, sheet.Bounds(), &image.Uniform{sheetBackground}, image.Point{}, draw.Src)
	for i, p := range layout {
		tw := p.img.Bounds().Dx()
		band := sheetLabelBand
		if i == len(layout)-1 {
			band = sheetResultBand
		}
		bandRect := image.Rect(p.x, p.y, p.x+tw, p.y+sheetLabelHeight)
		draw.Draw(sheet, bandRect, &image.Uniform{band}, image.Point{}, draw.Src)
		drawLabel(sheet, bandRect, labels[i])
		// Centred vertically in the tile area, for a picture made narrower
		// (and so shorter) to fit the width cap.
		th := p.img.Bounds().Dy()
		top := p.y + sheetLabelHeight + (sheetTileHeight-th)/2
		draw.Draw(sheet, image.Rect(p.x, top, p.x+tw, top+th), p.img, p.img.Bounds().Min, draw.Src)
	}

	var out bytes.Buffer
	if err := jpeg.Encode(&out, sheet, &jpeg.Options{Quality: 90}); err != nil {
		return nil, fmt.Errorf("contact sheet: encode: %w", err)
	}
	return out.Bytes(), nil
}

// scaleTile scales a picture to the tile height, or to the width cap when that
// is the tighter of the two, keeping its shape.
func scaleTile(img image.Image) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	scale := float64(sheetTileHeight) / float64(h)
	if float64(w)*scale > sheetTileMaxWidth {
		scale = float64(sheetTileMaxWidth) / float64(w)
	}
	nw, nh := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// drawLabel writes text into the band, scaled up from the 7x13 bitmap font and
// inset from the band's left edge. A label wider than its band is cut off at
// the band's edge rather than spilling into the next tile.
func drawLabel(dst *image.RGBA, band image.Rectangle, text string) {
	face := basicfont.Face7x13
	small := image.NewRGBA(image.Rect(0, 0, 7*len(text)+2, 15))
	d := font.Drawer{Dst: small, Src: image.White, Face: face, Dot: fixed.P(1, 11)}
	d.DrawString(text)
	w, h := small.Bounds().Dx()*sheetLabelScale, small.Bounds().Dy()*sheetLabelScale
	x, y := band.Min.X+12, band.Min.Y+(band.Dy()-h)/2
	// Drawn onto the band's own sub-image, which is what clips it there.
	clip := dst.SubImage(band).(*image.RGBA)
	draw.NearestNeighbor.Scale(clip, image.Rect(x, y, x+w, y+h), small, small.Bounds(), draw.Over, nil)
}
