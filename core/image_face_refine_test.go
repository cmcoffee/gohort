package core

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// solidPNG is a flat single-colour image, base64'd the way a backend returns one.
func solidPNG(t *testing.T, w, h int, c color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding fixture: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestExpandFaceBoxGrowsAndStaysSquare(t *testing.T) {
	bounds := image.Rect(0, 0, 1000, 1000)
	// A 100px face at the centre, grown 1.7x, is a 170px square still centred.
	got := expandFaceBox(image.Rect(450, 450, 550, 550), bounds, 1.7)
	if got.Dx() != got.Dy() {
		t.Errorf("box %v is not square (%dx%d)", got, got.Dx(), got.Dy())
	}
	if got.Dx() != 170 {
		t.Errorf("side = %d, want 170", got.Dx())
	}
	// Centre preserved: the face must not drift inside its own crop.
	if cx, cy := got.Min.X+got.Dx()/2, got.Min.Y+got.Dy()/2; cx != 500 || cy != 500 {
		t.Errorf("centre = (%d,%d), want (500,500)", cx, cy)
	}
}

func TestExpandFaceBoxClampsAtEdgeWithoutDistorting(t *testing.T) {
	bounds := image.Rect(0, 0, 1000, 1000)
	// A face jammed into the top-left: the grown square cannot stay centred, so
	// it slides inside the frame rather than being cropped to a rectangle.
	got := expandFaceBox(image.Rect(0, 0, 100, 100), bounds, 1.7)
	if got.Dx() != got.Dy() {
		t.Errorf("box %v is not square (%dx%d)", got, got.Dx(), got.Dy())
	}
	if !got.In(bounds) {
		t.Errorf("box %v escapes bounds %v", got, bounds)
	}
	if got.Min.X < 0 || got.Min.Y < 0 {
		t.Errorf("box %v has negative origin", got)
	}
}

func TestExpandFaceBoxShrinksToFitSmallFrame(t *testing.T) {
	// A big face in a small frame: the grown square is larger than the image,
	// so it must shrink to the frame rather than overflow it.
	bounds := image.Rect(0, 0, 120, 200)
	got := expandFaceBox(image.Rect(10, 10, 110, 110), bounds, 1.7)
	if got.Dx() != got.Dy() {
		t.Errorf("box %v is not square", got)
	}
	if got.Dx() > 120 {
		t.Errorf("side = %d, wider than the 120px frame", got.Dx())
	}
	if !got.In(bounds) {
		t.Errorf("box %v escapes bounds %v", got, bounds)
	}
}

func TestExpandFaceBoxRejectsEmpty(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 100)
	if got := expandFaceBox(image.Rectangle{}, bounds, 1.7); !got.Empty() {
		t.Errorf("empty face gave %v, want an empty box", got)
	}
	if got := expandFaceBox(image.Rect(0, 0, 10, 10), image.Rectangle{}, 1.7); !got.Empty() {
		t.Errorf("empty bounds gave %v, want an empty box", got)
	}
}

func TestEdgeAlphaRampsFromBorderToFull(t *testing.T) {
	const w, h, feather = 100, 100, 10
	if a := edgeAlpha(0, 50, w, h, feather); a != 0 {
		t.Errorf("alpha on the border = %v, want 0 — a hard edge is the seam this exists to avoid", a)
	}
	if a := edgeAlpha(50, 50, w, h, feather); a != 1 {
		t.Errorf("alpha at the centre = %v, want 1 — the refined face must land at full strength", a)
	}
	if a := edgeAlpha(5, 50, w, h, feather); a <= 0 || a >= 1 {
		t.Errorf("alpha mid-band = %v, want strictly between 0 and 1", a)
	}
	// Monotonic inward.
	if edgeAlpha(3, 50, w, h, feather) >= edgeAlpha(7, 50, w, h, feather) {
		t.Error("alpha does not increase toward the centre")
	}
}

func TestBlendIntoReplacesCentreAndKeepsBorder(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			dst.SetRGBA(x, y, color.RGBA{R: 0, G: 0, B: 0, A: 255})
		}
	}
	src := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			src.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	region := image.Rect(30, 30, 70, 70)
	blendInto(dst, src, region, 5)

	if got := dst.RGBAAt(50, 50); got.R != 255 {
		t.Errorf("centre R = %d, want 255 — the refinement did not land", got.R)
	}
	if got := dst.RGBAAt(30, 50); got.R != 0 {
		t.Errorf("region border R = %d, want 0 — the blend must reach zero at the edge", got.R)
	}
	if got := dst.RGBAAt(10, 10); got.R != 0 {
		t.Errorf("pixel outside the region R = %d, want 0 — blendInto wrote out of bounds", got.R)
	}
	// Mid-band is a genuine mix, not one or the other.
	if got := dst.RGBAAt(32, 50); got.R == 0 || got.R == 255 {
		t.Errorf("mid-band R = %d, want a blend", got.R)
	}
}

func TestBlendIntoCapsFeatherOnSmallRegions(t *testing.T) {
	// A feather wider than half the region would never reach full strength,
	// leaving the refined face permanently semi-transparent.
	dst := image.NewRGBA(image.Rect(0, 0, 20, 20))
	src := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			src.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	region := image.Rect(5, 5, 15, 15)
	blendInto(dst, src, region, 500)
	if got := dst.RGBAAt(10, 10); got.R != 255 {
		t.Errorf("centre R = %d, want 255 — an oversized feather starved the blend", got.R)
	}
}

func TestScaleImageToProducesRequestedSize(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 64, 32))
	got := scaleImageTo(src, 1024, 1024)
	if got.Bounds().Dx() != 1024 || got.Bounds().Dy() != 1024 {
		t.Errorf("size = %v, want 1024x1024", got.Bounds())
	}
	// Degenerate requests must not panic or produce a zero-area image.
	if got := scaleImageTo(src, 0, -5); got.Bounds().Dx() < 1 || got.Bounds().Dy() < 1 {
		t.Errorf("degenerate size gave %v", got.Bounds())
	}
}

func TestDetectFacesFindsNothingInFlatImage(t *testing.T) {
	// The false-positive guard. A cascade that fires on flat colour would send
	// every render off to repaint a face onto empty sky.
	img := image.NewRGBA(image.Rect(0, 0, 512, 512))
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 120, G: 140, B: 160, A: 255})
		}
	}
	if got := detectFaces(img); len(got) != 0 {
		t.Errorf("detected %d face(s) in flat colour, want 0", len(got))
	}
}

func TestDetectFacesHandlesTinyImage(t *testing.T) {
	// Smaller than the minimum face size: must return nothing rather than
	// asking the cascade for a window bigger than the image.
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	if got := detectFaces(img); len(got) != 0 {
		t.Errorf("detected %d face(s) in an 8x8 image, want 0", len(got))
	}
}

// --- pass guards -------------------------------------------------------------
//
// Each of these must return the FIRST pass's render byte-for-byte. The pass is
// best-effort: the caller already has a finished picture, and every bail here
// has to leave it exactly as it was.

func TestRefineFacesOffByDefaultOnTheRequest(t *testing.T) {
	spec := RestImageSpec{MaxInputImages: 2}
	in := restImageOutcome{b64: solidPNG(t, 64, 64, color.RGBA{A: 255})}
	got := spec.refineFaces(nil, EditImageRequest{Backend: "x"}, []inputImage{{name: "a.png"}}, in, 1)
	if got.b64 != in.b64 {
		t.Error("the pass ran without RefineFaces set — a native caller must not pay for a second render")
	}
}

func TestFaceRefineSkipsURLOnlyBackend(t *testing.T) {
	in := restImageOutcome{url: "https://example.invalid/out.png"}
	f := faceRefine{spec: RestImageSpec{MaxInputImages: 2}, backend: "x", refs: []inputImage{{name: "a.png"}}}
	if got := f.run(in); got.url != in.url || got.b64 != "" {
		t.Errorf("got %+v, want the URL outcome untouched", got)
	}
}

func TestFaceRefineSkipsWithoutIdentityReference(t *testing.T) {
	in := restImageOutcome{b64: solidPNG(t, 64, 64, color.RGBA{A: 255})}
	f := faceRefine{spec: RestImageSpec{MaxInputImages: 2}, backend: "x"}
	if got := f.run(in); got.b64 != in.b64 {
		t.Error("the pass ran with no source photo — there is no identity to transfer")
	}
}

func TestFaceRefineSkipsSingleSlotBackend(t *testing.T) {
	in := restImageOutcome{b64: solidPNG(t, 64, 64, color.RGBA{A: 255})}
	f := faceRefine{spec: RestImageSpec{MaxInputImages: 1}, backend: "x", refs: []inputImage{{name: "a.png"}}}
	if got := f.run(in); got.b64 != in.b64 {
		t.Error("the pass ran on a one-slot backend — the crop leaves no room for the reference")
	}
}

func TestFaceRefineSkipsUndecodableRender(t *testing.T) {
	in := restImageOutcome{b64: "not base64 at all!!"}
	f := faceRefine{spec: RestImageSpec{MaxInputImages: 2}, backend: "x", refs: []inputImage{{name: "a.png"}}}
	if got := f.run(in); got.b64 != in.b64 {
		t.Error("an undecodable render must be passed straight through, not replaced")
	}
}

func TestFaceRefineSkipsWhenNoFaceFound(t *testing.T) {
	// The common no-op: an edit with no person in it. No backend is wired here,
	// so reaching a render at all would fail the test by panicking on nil.
	in := restImageOutcome{b64: solidPNG(t, 512, 512, color.RGBA{R: 120, G: 140, B: 160, A: 255})}
	f := faceRefine{spec: RestImageSpec{MaxInputImages: 2}, backend: "x", refs: []inputImage{{name: "a.png"}}}
	if got := f.run(in); got.b64 != in.b64 {
		t.Error("a faceless render must come back unchanged")
	}
}

// --- slot filling ------------------------------------------------------------

func TestSlotsPutsCropFirstAndFillsExactly(t *testing.T) {
	crop := inputImage{name: "face-crop.png"}
	f := faceRefine{
		spec: RestImageSpec{MaxInputImages: 3},
		refs: []inputImage{{name: "one.png"}, {name: "two.png"}},
	}
	got := f.slots(crop)
	if len(got) != 3 {
		t.Fatalf("filled %d slot(s), want exactly 3 — a compose graph errors on a partial fill", len(got))
	}
	want := []string{"face-crop.png", "one.png", "two.png"}
	for i, w := range want {
		if got[i].name != w {
			t.Errorf("slot %d = %q, want %q", i, got[i].name, w)
		}
	}
}

func TestSlotsRepeatsLastReferenceToFill(t *testing.T) {
	// Fewer references than slots: repeating the reference is a no-op for
	// identity, where an unfilled slot is a hard backend failure.
	f := faceRefine{
		spec: RestImageSpec{MaxInputImages: 4},
		refs: []inputImage{{name: "one.png"}},
	}
	got := f.slots(inputImage{name: "face-crop.png"})
	if len(got) != 4 {
		t.Fatalf("filled %d slot(s), want 4", len(got))
	}
	if got[0].name != "face-crop.png" {
		t.Errorf("slot 0 = %q, want the crop first", got[0].name)
	}
	for i := 1; i < 4; i++ {
		if got[i].name != "one.png" {
			t.Errorf("slot %d = %q, want the reference repeated", i, got[i].name)
		}
	}
}

func TestSlotsTrimsSurplusReferences(t *testing.T) {
	f := faceRefine{
		spec: RestImageSpec{MaxInputImages: 2},
		refs: []inputImage{{name: "one.png"}, {name: "two.png"}, {name: "three.png"}},
	}
	got := f.slots(inputImage{name: "face-crop.png"})
	if len(got) != 2 {
		t.Fatalf("filled %d slot(s), want 2 — more images than slots is the failure this avoids", len(got))
	}
	if got[1].name != "one.png" {
		t.Errorf("slot 1 = %q, want the FIRST reference — it carries the subject", got[1].name)
	}
}
