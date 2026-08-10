// Pass B of a two-pass edit: spend real steps on the face, and only on the face.
//
// The problem this solves is arithmetic, not prompting. An edit model works in
// latent space at roughly 1MP with an 8x downsample, so a face occupying 120px
// of a 1024px frame is about fifteen latent pixels — there is not enough signal
// there to reconstruct a specific person, and no amount of instruction adds
// any. Turning the step count up on the whole frame does not fix it either: it
// spends the entire budget re-rendering a beach to recover a nose.
//
// So: let pass A redraw the scene cheaply and approximately (that is what a
// "put them somewhere they have never been" request actually wants), then find
// the face IN THE RESULT, crop it, blow it up to the model's native working
// size, and re-render just that at a high step count with the original photo
// alongside it as the identity reference. A few hundred pixels of face instead
// of fifteen, at a fraction of the cost of a high-step full frame.
//
// Why crop-and-stitch rather than the mask input the connector already has
// (ComfyNodeMap.MaskNodes): a mask needs a graph wired for inpainting and an
// admin who mapped the node, and it still renders the face at whatever size it
// occupies in the frame. Cropping is arithmetic — it works on every editing
// backend, including the plain REST ones that have no graph at all, and it is
// the part that actually buys the resolution.
//
// This is best-effort by construction. Every failure path returns pass A's
// render untouched, because a scene that is right with a soft face beats an
// error where a picture should be. Each bail leaves a Debug breadcrumb: a
// silent no-op here looks exactly like a feature that was never switched on.
package core

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"

	xdraw "golang.org/x/image/draw"
)

const (
	// faceCropMargin grows the detector's square before cropping. The cascade
	// boxes the face proper — brow to chin — and re-rendering exactly that
	// leaves the model no jaw, hair or neck to blend against, which reads as a
	// mask edge no feather can hide.
	faceCropMargin = 1.7

	// faceWorkPixels is the long edge the crop is scaled to before it goes back
	// to the model: the working size these models are trained at. Below it the
	// exercise is pointless; far above it the model starts inventing detail it
	// was never trained to place.
	faceWorkPixels = 1024

	// faceFeatherFraction sets the blend band as a fraction of the crop's edge.
	faceFeatherFraction = 12

	// maxFaceRefines caps how many faces get their own render. Each one is a
	// full backend round trip, and a group shot would otherwise turn a single
	// edit into a dozen renders against a shared GPU. Faces are ordered
	// largest-first, so the cap keeps the subject and drops the crowd.
	maxFaceRefines = 2

	// faceRefinePrompt instructs the identity transfer.
	//
	// It names what must NOT change first. Given only "make this look like the
	// reference", the model re-poses the head to match the reference photo and
	// the refined crop no longer lines up with the body pass A drew — a
	// straight-on face stitched onto a three-quarter shoulder. The scene owns
	// the geometry and the light; the reference owns the identity, and nothing
	// else.
	faceRefinePrompt = "Keep the head angle, gaze direction, expression, lighting, shadows, skin tone and image grain of the first image exactly as they are. " +
		"Change only the identity: make the person in the first image be the same person as in the other image(s), matching their facial structure, features and likeness. " +
		"Do not change the pose, the framing, the background, the clothing or the crop."
)

func init() {
	RegisterTunable(TunableSpec{
		Key: "tune_image_face_steps", Category: "Images",
		Label: "Face refinement steps",
		Help:  "Sampling steps for the second, face-only pass of an edit. Deliberately higher than the backend's default: the first pass redraws the whole scene and is tuned for speed, this one runs on a small crop where identity is decided, so the steps are cheap and the detail is the entire point.",
		Kind:  KindInt, Default: 20, Min: 1, Max: 150,
	})
}

// faceRefine carries one refinement pass. A struct rather than a parameter list
// because every stage below needs the backend, the session and the identity
// references, and threading five arguments through four functions is how they
// drift apart.
type faceRefine struct {
	spec    RestImageSpec
	sess    *ToolSession
	backend string
	refs    []inputImage // the caller's ORIGINAL source photos: the identity
	seed    int
}

// refineFaces runs pass B over an edit's output when the request asked for it.
//
// It never returns an error: the caller has a finished picture in hand, and the
// worst outcome available here is "the face is no better than it already was".
func (s RestImageSpec) refineFaces(sess *ToolSession, req EditImageRequest, images []inputImage, out restImageOutcome, seed int) restImageOutcome {
	if !req.RefineFaces {
		return out
	}
	return faceRefine{spec: s, sess: sess, backend: req.Backend, refs: images, seed: seed}.run(out)
}

// run is the whole pass: decode, detect, and refine each face in turn.
func (f faceRefine) run(out restImageOutcome) restImageOutcome {
	// A URL-only backend hands back a link rather than pixels. Fetching it to
	// read those pixels is the same SSRF outcomeAsInputImage refuses by design,
	// and a face pass is not the place to make that exception.
	if out.b64 == "" {
		Debug("[face_refine] %q returns an image URL rather than image data — nothing to read, skipping", f.backend)
		return out
	}
	if len(f.refs) == 0 {
		Debug("[face_refine] no source photo on this edit — nothing to take an identity from, skipping")
		return out
	}
	// The crop occupies a slot, so a single-input backend has nowhere to put
	// the reference. Refining without one would sharpen whichever face pass A
	// invented, which is a better picture of the wrong person.
	if slots := f.spec.MaxImages(); slots < 2 {
		Debug("[face_refine] %q takes one image at a time — no slot for the identity reference alongside the crop, skipping", f.backend)
		return out
	}
	raw, err := base64.StdEncoding.DecodeString(out.b64)
	if err != nil {
		Debug("[face_refine] undecodable render: %v", err)
		return out
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		Debug("[face_refine] unreadable render: %v", err)
		return out
	}
	faces := detectFaces(src)
	if len(faces) == 0 {
		Debug("[face_refine] no face found in the render — leaving it alone")
		return out
	}
	if len(faces) > maxFaceRefines {
		Log("[face_refine] %d faces found, refining the %d largest", len(faces), maxFaceRefines)
		faces = faces[:maxFaceRefines]
	}

	// One mutable canvas for the whole pass: with two faces the second
	// composite has to land on top of the first, not on a stale copy.
	canvas := image.NewRGBA(src.Bounds())
	draw.Draw(canvas, src.Bounds(), src, src.Bounds().Min, draw.Src)

	done := 0
	for i, face := range faces {
		box := expandFaceBox(face.box, src.Bounds(), faceCropMargin)
		if box.Empty() {
			continue
		}
		refined, err := f.render(canvas, box)
		if err != nil {
			// Stop rather than continue: a failing backend fails the next one
			// too, and each attempt costs a full render deadline.
			Log("[face_refine] face %d of %d failed, keeping the render as-is: %v", i+1, len(faces), err)
			break
		}
		blendInto(canvas, refined, box, box.Dx()/faceFeatherFraction)
		done++
	}
	if done == 0 {
		return out
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		Debug("[face_refine] re-encode failed: %v", err)
		return out
	}
	Log("[face_refine] %q: refined %d face(s) at %d steps", f.backend, done, f.steps())
	return restImageOutcome{b64: base64.StdEncoding.EncodeToString(buf.Bytes())}
}

// render crops box out of the canvas, scales it up to the model's working size,
// and sends it back through the backend with the identity references.
func (f faceRefine) render(canvas image.Image, box image.Rectangle) (image.Image, error) {
	crop := scaleImageTo(subImage(canvas, box), faceWorkPixels, faceWorkPixels)
	var buf bytes.Buffer
	if err := png.Encode(&buf, crop); err != nil {
		return nil, fmt.Errorf("encoding the face crop: %w", err)
	}
	face, err := verifyInputImage("face-crop.png", buf.Bytes())
	if err != nil {
		return nil, err
	}
	out, err := timeImageRender(f.backend, func() (restImageOutcome, error) {
		return f.spec.generate(f.sess, restImageParams{
			prompt:   faceRefinePrompt,
			negative: f.spec.DefaultNegative,
			steps:    f.steps(),
			seed:     f.seed,
			images:   f.slots(face),
		})
	})
	if err != nil {
		return nil, err
	}
	if out.b64 == "" {
		return nil, fmt.Errorf("the refinement render returned a URL rather than image data")
	}
	data, err := base64.StdEncoding.DecodeString(out.b64)
	if err != nil {
		return nil, fmt.Errorf("the refinement render returned invalid base64: %w", err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("the refinement render is unreadable: %w", err)
	}
	return scaleImageTo(img, box.Dx(), box.Dy()), nil
}

// slots builds the image list for the refinement render: the crop first as the
// subject being edited, then the identity references, in the caller's order.
//
// Filled to EXACTLY the backend's slot count. A compose graph errors on a
// partial fill (the same rule cascadeSteps enforces for a normal edit), so when
// there are fewer references than slots the last one repeats. Repeating a
// reference is a no-op for identity — it is the same face twice — where an
// unfilled slot is a hard failure.
func (f faceRefine) slots(face inputImage) []inputImage {
	want := f.spec.MaxImages()
	out := make([]inputImage, 0, want)
	out = append(out, face)
	for i := 0; len(out) < want; i++ {
		if i < len(f.refs) {
			out = append(out, f.refs[i])
			continue
		}
		out = append(out, f.refs[len(f.refs)-1])
	}
	return out
}

// steps is the refinement step count: the request's own value never applies
// here. That number was chosen for a full-frame redraw and is usually low on
// purpose — carrying it into this pass would reproduce the exact problem the
// pass exists to fix.
func (f faceRefine) steps() int {
	return TuneInt("tune_image_face_steps")
}

// --- geometry ----------------------------------------------------------------

// expandFaceBox grows a detector square around its centre by margin and clamps
// it inside bounds, staying square.
//
// Square matters: the crop is scaled to a square working size, and feeding a
// stretched face to a model that has only ever seen unstretched ones is its own
// source of drift. When the grown square will not fit — a face near an edge, or
// a big face in a small frame — it shrinks to fit rather than being cropped to
// a rectangle, then slides back inside the frame.
func expandFaceBox(box, bounds image.Rectangle, margin float64) image.Rectangle {
	if box.Empty() || bounds.Empty() || margin <= 0 {
		return image.Rectangle{}
	}
	side := box.Dx()
	if box.Dy() > side {
		side = box.Dy()
	}
	side = int(float64(side) * margin)
	if side > bounds.Dx() {
		side = bounds.Dx()
	}
	if side > bounds.Dy() {
		side = bounds.Dy()
	}
	if side <= 0 {
		return image.Rectangle{}
	}
	// Centre on the face, then slide whole so the square stays intact.
	x := box.Min.X + box.Dx()/2 - side/2
	y := box.Min.Y + box.Dy()/2 - side/2
	if x < bounds.Min.X {
		x = bounds.Min.X
	}
	if y < bounds.Min.Y {
		y = bounds.Min.Y
	}
	if x+side > bounds.Max.X {
		x = bounds.Max.X - side
	}
	if y+side > bounds.Max.Y {
		y = bounds.Max.Y - side
	}
	return image.Rect(x, y, x+side, y+side)
}

// subImage returns the r-sized view of src, copied when src cannot slice.
func subImage(src image.Image, r image.Rectangle) image.Image {
	type subImager interface {
		SubImage(image.Rectangle) image.Image
	}
	if s, ok := src.(subImager); ok {
		return s.SubImage(r)
	}
	out := image.NewRGBA(r)
	draw.Draw(out, r, src, r.Min, draw.Src)
	return out
}

// resizeImage scales src to w x h. CatmullRom rather than a cheaper kernel
// because this runs twice on the region the whole pass is about: up to the
// working size and back down again, where a soft resampler would give back the
// detail the render just bought.
func scaleImageTo(src image.Image, w, h int) *image.RGBA {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(out, out.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return out
}

// blendInto composites src into dst over the region r, fading it out over a
// feather-wide band at the edges.
//
// The band is what makes the stitch invisible. Even a good refinement differs
// slightly from its surroundings in white balance and noise, and a hard edge
// turns that difference into a visible rectangle around someone's head —
// exactly the artefact that makes an edit look edited.
func blendInto(dst *image.RGBA, src image.Image, r image.Rectangle, feather int) {
	r = r.Intersect(dst.Bounds())
	if r.Empty() {
		return
	}
	if feather < 1 {
		feather = 1
	}
	// Cap against the deepest pixel the region actually HAS, which is
	// (side-1)/2 and not side/2: on an even-sided region the centre sits one
	// short of half, so a band of side/2 never reaches full strength and the
	// refined face lands permanently semi-transparent. A region too small to
	// hold any band at all blends hard rather than not at all.
	if deepest := (minInt(r.Dx(), r.Dy()) - 1) / 2; feather > deepest {
		feather = deepest
	}
	sb := src.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			a := edgeAlpha(x-r.Min.X, y-r.Min.Y, r.Dx(), r.Dy(), feather)
			if a <= 0 {
				continue
			}
			sr, sg, sb8, _ := src.At(sb.Min.X+x-r.Min.X, sb.Min.Y+y-r.Min.Y).RGBA()
			dr, dg, db, da := dst.At(x, y).RGBA()
			dst.SetRGBA(x, y, color.RGBA{
				R: mix8(sr, dr, a), G: mix8(sg, dg, a), B: mix8(sb8, db, a), A: uint8(da >> 8),
			})
		}
	}
}

// edgeAlpha is the blend weight at (x,y) inside a w x h region: 0 on the border,
// rising linearly to 1 once feather pixels in. A feather of 0 or less means the
// region is too small to fade across, so it copies straight.
func edgeAlpha(x, y, w, h, feather int) float64 {
	if feather < 1 {
		return 1
	}
	d := minInt(minInt(x, w-1-x), minInt(y, h-1-y))
	if d >= feather {
		return 1
	}
	if d <= 0 {
		return 0
	}
	return float64(d) / float64(feather)
}

// mix8 blends two 16-bit channel values by weight a and returns an 8-bit one.
func mix8(src, dst uint32, a float64) uint8 {
	v := float64(src)*a + float64(dst)*(1-a)
	if v < 0 {
		v = 0
	}
	if v > 65535 {
		v = 65535
	}
	return uint8(int(v) >> 8)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
