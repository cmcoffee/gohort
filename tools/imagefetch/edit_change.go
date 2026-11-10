package imagefetch

// Whether an edit changed anything. The description check compares what two
// looks wrote about the pictures, and a picture handed back nearly as it was
// reads the same as the one edited, so it passed: "that's not me, that's the
// same photo", after the agent had announced the swap done. This is measured
// on the pixels instead, and needs no model.
//
// Both pictures are laid on one grid, each cell its mean brightness, and the
// largest change in any cell is the measure. A swapped face moves the cells
// it covers a long way; a picture rendered again unchanged moves none of them
// much. So it can only report that nothing changed, never judge a change.

import (
	"fmt"
	"image"

	"github.com/cmcoffee/gohort/core/media"
)

const (
	// changeGrid is the cells across and down: fine enough that one face in
	// a group photo covers whole cells.
	changeGrid = 48
	// unchangedBelow is the largest cell change, as a share of full
	// brightness, under which an edit is taken to have changed nothing.
	// Measured on live Gemini face swaps of a four-person stage photo: the
	// result the person called "the same photo" moved no cell more than
	// 3.3%; one whose hair and beard changed, 7.4%; one whose face and hair
	// were redrawn, 19%.
	unchangedBelow = 0.05
)

// largestChange is the largest change in mean brightness of any grid cell
// between before and after, 0 to 1; ok false when either cannot be read.
// Sizes may differ: each cell is the same share of each picture.
func largestChange(before, after []byte) (float64, bool) {
	a, _, err := media.DecodeImage(before)
	if err != nil {
		return 0, false
	}
	b, _, err := media.DecodeImage(after)
	if err != nil {
		return 0, false
	}
	ga, gb := cellMeans(a), cellMeans(b)
	most := 0.0
	for i := range ga {
		d := ga[i] - gb[i]
		if d < 0 {
			d = -d
		}
		if d > most {
			most = d
		}
	}
	return most, true
}

// cellMeans is img's mean brightness per grid cell, 0 to 1, row by row.
func cellMeans(img image.Image) []float64 {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	out := make([]float64, changeGrid*changeGrid)
	if w <= 0 || h <= 0 {
		return out
	}
	// Sampled rather than every pixel: a few points per cell settle its mean.
	const per = 6
	for cy := 0; cy < changeGrid; cy++ {
		for cx := 0; cx < changeGrid; cx++ {
			sum, n := 0.0, 0
			for sy := 0; sy < per; sy++ {
				for sx := 0; sx < per; sx++ {
					x := bounds.Min.X + (cx*per+sx)*w/(changeGrid*per)
					y := bounds.Min.Y + (cy*per+sy)*h/(changeGrid*per)
					r, g, b, _ := img.At(x, y).RGBA()
					sum += (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 65535
					n++
				}
			}
			out[cy*changeGrid+cx] = sum / float64(n)
		}
	}
	return out
}

// unchangedNote is what an edit says when its result is the picture it was
// given: no cell moved by unchangedBelow or more.
func unchangedNote(most float64) string {
	return fmt.Sprintf(" AUTOMATIC CHECK, measured on the pixels, which outranks every look above: the result is almost the same picture as the one edited, no part of it changed by more than %.0f%%. "+
		"The edit did NOTHING visible: do NOT present it as done or describe a change in it. Tell the person plainly that it came back unchanged.", most*100)
}
