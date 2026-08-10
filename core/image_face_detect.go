// Finding a face in a rendered picture.
//
// This exists for pass B of a two-pass edit, and the thing that makes it worth
// a detector at all is WHICH image gets searched: the OUTPUT, never the input.
// A "put this person on a beach" edit is a full redraw — the subject is
// re-posed, re-lit and re-scaled into a scene that did not exist before — so
// the source photo's face coordinates describe a face that is no longer there.
// Only the finished frame knows where the person ended up.
//
// pigo is a pure-Go PICO cascade: no cgo, no model server, no GPU, a few
// milliseconds on a 1MP frame. That matters because this runs inline in an
// image render that is already slow, on the same box as a resident LLM.
//
// It detects, it does not recognize. This says "a face is here", never "this is
// Rory". Identity stays where it already lives (image_subject.go, keyed on a
// transport handle) and nothing in this file is allowed to become an opinion
// about who someone is — it only decides where the second render is spent.
package core

import (
	_ "embed"
	"image"
	_ "image/jpeg" // decoders for whatever the backend hands back
	_ "image/png"
	"sort"
	"sync"

	pigo "github.com/esimov/pigo/core"
	_ "golang.org/x/image/webp"
)

//go:embed cascade/facefinder
var faceCascade []byte

const (
	// faceQualityFloor is pigo's confidence cut. Below ~5 the facefinder
	// cascade starts returning texture — a tree canopy, a gravel path — and a
	// false positive here is not a missed refinement, it is a render that
	// paints somebody's face onto a shrub.
	faceQualityFloor = 5.0

	// faceMinFraction is the smallest face worth a dedicated render, as a
	// fraction of the frame's short edge. Below this it is a bystander in the
	// background, or it is so small that the refined crop would be mostly
	// invention. Either way the second render costs more than it returns.
	faceMinFraction = 12

	// faceMinPixels floors the above for small frames.
	faceMinPixels = 48
)

// faceRegion is one detected face in output-image pixel coordinates.
type faceRegion struct {
	box image.Rectangle
	q   float32 // cascade confidence; kept for the log, not for ranking
}

var (
	faceOnce   sync.Once
	faceFinder *pigo.Pigo
	faceErr    error
)

// faceClassifier unpacks the embedded cascade once. The error is cached too: a
// corrupt embed is a build problem, not something to retry per render.
func faceClassifier() (*pigo.Pigo, error) {
	faceOnce.Do(func() {
		faceFinder, faceErr = pigo.NewPigo().Unpack(faceCascade)
	})
	return faceFinder, faceErr
}

// detectFaces returns the faces in img, largest first.
//
// Largest first rather than highest-confidence first because the ordering is
// used to decide what gets refined when the budget only covers one or two: in a
// "person in a scenario" render the subject is the big face and the crowd
// behind them is not, and a background extra can easily out-score the subject
// on cascade confidence while being worth nothing to refine.
func detectFaces(img image.Image) []faceRegion {
	cls, err := faceClassifier()
	if err != nil {
		Debug("[face_refine] cascade unavailable: %v", err)
		return nil
	}
	b := img.Bounds()
	cols, rows := b.Dx(), b.Dy()
	if cols <= 0 || rows <= 0 {
		return nil
	}
	short := cols
	if rows < short {
		short = rows
	}
	minSize := short / faceMinFraction
	if minSize < faceMinPixels {
		minSize = faceMinPixels
	}
	if minSize > short {
		// A frame too small to hold a face at the minimum we care about.
		return nil
	}
	dets := cls.RunCascade(pigo.CascadeParams{
		MinSize:     minSize,
		MaxSize:     short,
		ShiftFactor: 0.1,
		ScaleFactor: 1.1,
		ImageParams: pigo.ImageParams{
			Pixels: pigo.RgbToGrayscale(img),
			Rows:   rows,
			Cols:   cols,
			Dim:    cols,
		},
	}, 0.0)
	dets = cls.ClusterDetections(dets, 0.2)

	out := make([]faceRegion, 0, len(dets))
	for _, d := range dets {
		if d.Q < faceQualityFloor || d.Scale <= 0 {
			continue
		}
		half := d.Scale / 2
		// pigo reports row/col as the CENTRE of a square window, and in
		// image-space row is y. Offset by the bounds origin: a sub-image
		// (which a crop is) does not start at 0,0.
		r := image.Rect(
			b.Min.X+d.Col-half, b.Min.Y+d.Row-half,
			b.Min.X+d.Col+half, b.Min.Y+d.Row+half,
		).Intersect(b)
		if r.Empty() {
			continue
		}
		out = append(out, faceRegion{box: r, q: d.Q})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return area(out[i].box) > area(out[j].box)
	})
	return out
}

// area is the pixel count of r, as an int64 so a large frame cannot overflow
// the comparison on a 32-bit build.
func area(r image.Rectangle) int64 {
	return int64(r.Dx()) * int64(r.Dy())
}
