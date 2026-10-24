package scribe

// A guide's header image, generated.
//
// Image models write text badly, so a header carries none, and it still came
// out with lettering on it often enough to be the thing people noticed. The
// prompt said "Do NOT render any text", and then quoted the guide's title,
// and a quoted phrase is the strongest cue an image model has to draw those
// words. Now the title never reaches the image model: the worker describes a
// scene for the guide in plain visual terms, and the prompt is built from
// that.
//
// Two pictures of the owner's own can go with it, each HANDED to the image
// generator as an input picture, so it works them into the image rather than
// having them stuck on top. The header is generated wide from the scene first,
// then each picture is its own pass on an editor that takes two, the header so
// far and the one picture to add:
//   - a picture to include, whose main subject is placed into the scene.
//   - a logo, worked into the scene and copied as it is. Where no editor
//     takes two pictures, the logo is placed in the corner as uploaded
//     instead, which is the only fallback that keeps it intact.
//
// One pass used to be handed the picture and the logo together, with the
// scene written out beside them, and it kept whichever picture fitted the
// words and dropped the other. An edit prompt points at the pictures and asks
// for one change; the scene in words goes only to the render with no picture
// to compete with.

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// headerSceneSystem asks the worker for what the header shows, never what it
// says.
const headerSceneSystem = "You describe one picture for the banner at the top of a document. Reply with one or two sentences naming only what is seen: the objects, the setting, any people, the light and the colours. The picture carries no writing of any kind, so never mention words, letters, numbers, signs, labels, screens showing text, book covers, logos or the document's title, and never quote anything. Reply with the description only."

// headerPictureRule closes every header prompt.
const headerPictureRule = " Purely pictorial: plain surfaces, with no writing, lettering, numbers, signage or logos anywhere in the image."

// headerOneResult closes every edit prompt: a renderer handed two pictures
// will otherwise sometimes show the second one inside the first.
const headerOneResult = " Produce one finished picture: the second picture is never shown inside it as an inset, thumbnail, corner overlay, side-by-side panel or collage."

// headerPlacePicture asks for the owner's picture in the second picture to be
// placed into the header in the first. It points at the pictures rather than
// describing them.
const headerPlacePicture = "Put the main subject of the second picture into the first picture, at a natural size and place in the scene, lit and shaded like the rest of it. Keep the rest of the first picture as it is, and add no writing anywhere." + headerOneResult

// headerLogoBlend asks for the logo in the second picture to be worked into
// the first. It points at the pictures rather than describing them, and asks
// for the logo copied, since a generator that redraws one misspells it.
const headerLogoBlend = "Put the logo from the second picture into the first picture so it belongs to the scene: on a surface, object or sign that fits, at a modest size, lit and shaded like the rest of the scene. Copy the logo exactly as it appears in the second picture, its shapes, colours and any lettering unchanged. Add no other writing anywhere." + headerOneResult

// headerEditors and editHeaderImage are the editing backends and the edit
// call, seams so a test can stand in for a connector.
var (
	headerEditors   = func() []ImageBackendChoice { return ReachableImageBackends(nil) }
	editHeaderImage = EditImageWithBackend
)

// maxHeaderPixels bounds a picture decoded for the logo step, so a small file
// that claims to be enormous is refused before it is decoded.
const maxHeaderPixels = 40_000_000

// headerJob is one header render: the guide it is for, and the owner's own
// pictures that go with it.
type headerJob struct {
	guide Guide
	logo  []byte // worked into the header, or placed on it as given
	from  []byte // a picture whose subject is placed into the header
	scene string
}

// generateHeader renders g's header and returns what the browser shows: the
// generator's remote URL as it is, or a data URL.
func (T *Scribe) generateHeader(ctx context.Context, g Guide, udb Database) (string, error) {
	job := headerJob{guide: g, logo: guidePicture(udb, g, g.HeaderLogo), from: guidePicture(udb, g, g.HeaderFrom)}
	if g.HeaderLogo != "" && job.logo == nil {
		return "", fmt.Errorf("the logo is no longer stored with this guide: upload it again")
	}
	if g.HeaderFrom != "" && job.from == nil {
		return "", fmt.Errorf("the picture to include is no longer stored with this guide: upload it again")
	}
	job.scene = T.headerScene(ctx, g)
	// Every pass after the first hands the editor two pictures: the header so
	// far and the one to add (headerEditor picks the backend that fits).
	two := headerEditor(2)

	var res *ImageGenResult
	var err error
	if job.from != nil && two.Name == "" {
		// No editor here takes two pictures, so the owner's picture cannot be
		// placed into a generated scene: it is made into the header instead.
		one := headerEditor(1)
		if one.Name == "" {
			return "", fmt.Errorf("this deployment has no image backend that works from a picture: remove the picture, or ask an administrator to set one up")
		}
		res, err = job.edit(ctx, "picture as the header", one.Name, job.editPrompt(), [][]byte{job.from})
	} else {
		Log("[scribe.header] guide %s: generating the header from the scene", g.ID)
		res, err = GenerateImageLandscape(ctx, "", job.prompt())
		if err == nil && job.from != nil {
			res, err = job.add(ctx, res, "picture", two.Name, headerPlacePicture, job.from)
		}
	}
	blend := job.logo != nil && two.Name != ""
	if err == nil && blend {
		res, err = job.add(ctx, res, "logo", two.Name, headerLogoBlend, job.logo)
	}
	if err != nil {
		return "", err
	}
	pasteLogo := job.logo != nil && !blend
	if isRemote(res.URL) && !pasteLogo {
		return res.URL, nil
	}
	data, mime, err := headerBytes(ctx, res.URL, isRemote(res.URL))
	if err != nil {
		return "", err
	}
	if pasteLogo {
		if data, err = placeLogo(data, job.logo); err != nil {
			return "", err
		}
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// add is one pass: the header so far as the first picture and pic as the
// second, the order prompt points at.
func (j headerJob) add(ctx context.Context, so *ImageGenResult, step, backend, prompt string, pic []byte) (*ImageGenResult, error) {
	base, _, err := headerBytes(ctx, so.URL, isRemote(so.URL))
	if err != nil {
		return nil, err
	}
	return j.edit(ctx, step, backend, prompt, [][]byte{base, pic})
}

// edit hands pictures to the editing backend, in order, as the first, second
// and so on that its prompt points at. Each pass is logged, so a header that
// came out without one of its pictures can be traced to the pass.
func (j headerJob) edit(ctx context.Context, step, backend, prompt string, images [][]byte) (*ImageGenResult, error) {
	Log("[scribe.header] guide %s: %s pass via %q with %d picture(s)", j.guide.ID, step, backend, len(images))
	sess := &ToolSession{Ctx: ctx, Username: j.guide.Owner}
	var refs []string
	for _, img := range images {
		refs = append(refs, sess.RegisterInboundMedia("image", img, ""))
	}
	return editHeaderImage(sess, EditImageRequest{Backend: backend, Prompt: prompt, Images: refs})
}

func isRemote(ref string) bool {
	return strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://")
}

// headerScene is what the header shows, in the worker's words. With no
// worker, or nothing usable back, the guide's subject stands in, unquoted.
func (T *Scribe) headerScene(ctx context.Context, g Guide) string {
	var b strings.Builder
	b.WriteString("The document: " + strings.TrimSpace(g.Title))
	if s := strings.TrimSpace(g.Subtitle); s != "" {
		b.WriteString("\n" + s)
	}
	if secs := g.sorted(); len(secs) > 0 {
		var names []string
		for i, s := range secs {
			if i == 12 {
				break
			}
			names = append(names, s.Title)
		}
		b.WriteString("\nIts sections: " + strings.Join(names, "; "))
	}
	if T != nil && T.LLM != nil {
		resp, err := T.WorkerChat(ctx, []Message{{Role: "user", Content: b.String()}},
			WithSystemPrompt(headerSceneSystem), WithCaller("scribe.header_scene"), WithThink(false), WithMaxTokens(160))
		if err == nil && resp != nil {
			if scene := cleanHeaderScene(resp.Content); scene != "" {
				return scene
			}
		}
	}
	return "An editorial scene that suggests the subject of a document about " + cleanHeaderScene(g.Title) + "."
}

// cleanHeaderScene keeps the first paragraph, without quote marks: a quoted
// phrase in an image prompt is drawn as text.
func cleanHeaderScene(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.NewReplacer("\"", "", "“", "", "”", "", "‘", "", "’", "'", "`", "", "\n", " ").Replace(s)
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}

func (j headerJob) prompt() string {
	return "A wide banner image, about 3:1, clean editorial quality. " + j.scene + headerPictureRule
}

// editPrompt is for a header made from the owner's picture itself, where no
// editor takes a second picture to place it into a generated scene. One
// picture, so the scene in words has nothing to compete with.
func (j headerJob) editPrompt() string {
	return "Make this picture into a wide editorial banner, keeping what it shows, set in this scene: " + j.scene + headerPictureRule
}

// headerEditor is the editing backend that fits n pictures in one render:
// one whose inputs number exactly n first, then the one with the fewest that
// take n, the default winning a tie. The default alone was taken whatever it
// took, so a deployment with a two-input and a three-input backend sent a
// header and a logo to the three-input one. Name is "" when none fits.
func headerEditor(n int) headerBackend {
	var pick headerBackend
	better := func(b ImageBackendChoice) bool {
		if pick.Name == "" {
			return true
		}
		if (b.MaxImages == n) != (pick.MaxImages == n) {
			return b.MaxImages == n
		}
		if b.MaxImages != pick.MaxImages {
			return b.MaxImages < pick.MaxImages
		}
		return b.Default && !pick.Default
	}
	for _, b := range headerEditors() {
		if b.Edits && b.MaxImages >= n && better(b) {
			pick = headerBackend{b}
		}
	}
	return pick
}

// headerBackend is an editing backend as the header reads it.
type headerBackend struct{ ImageBackendChoice }

// guidePicture is a picture stored with g, named by the URL its upload
// returned, or nil. Only g's own pictures: a URL naming another guide's is
// not read.
func guidePicture(udb Database, g Guide, url string) []byte {
	m := guideImageRefRE.FindStringSubmatch(url)
	if m == nil || m[1] != g.ID {
		return nil
	}
	var img guideImage
	if !udb.Get(guideImagesTable, guideImageKey(m[1], m[2]), &img) || len(img.Data) == 0 {
		return nil
	}
	return img.Data
}

// headerBytes reads a generator's result: a local file it wrote (removed once
// read), or the remote one it named.
func headerBytes(ctx context.Context, ref string, isURL bool) ([]byte, string, error) {
	var data []byte
	var err error
	if isURL {
		data, err = fetchHeader(ctx, ref)
	} else {
		data, err = os.ReadFile(ref)
		os.Remove(ref)
	}
	if err != nil {
		return nil, "", fmt.Errorf("could not read the generated image: %w", err)
	}
	mime := http.DetectContentType(data)
	if !guideImageTypes[mime] {
		return nil, "", fmt.Errorf("the generator returned %s, not a picture", mime)
	}
	return data, mime, nil
}

// fetchHeader downloads the picture an image backend handed back by URL.
func fetchHeader(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4*maxGuideImageBytes))
}

// placeLogo draws logo onto the bottom right of banner, scaled to a fifth of
// its height and no more than a third of its width, and returns a PNG.
func placeLogo(banner, logo []byte) ([]byte, error) {
	bg, err := decodeBounded(banner)
	if err != nil {
		return nil, fmt.Errorf("could not read the generated image: %w", err)
	}
	lg, err := decodeBounded(logo)
	if err != nil {
		return nil, fmt.Errorf("could not read the logo: %w", err)
	}
	b, lb := bg.Bounds(), lg.Bounds()
	if lb.Dx() == 0 || lb.Dy() == 0 {
		return nil, fmt.Errorf("the logo is empty")
	}
	h := b.Dy() / 5
	w := h * lb.Dx() / lb.Dy()
	if maxW := b.Dx() / 3; w > maxW {
		w, h = maxW, maxW*lb.Dy()/lb.Dx()
	}
	if w < 1 || h < 1 {
		return nil, fmt.Errorf("the generated image is too small to carry a logo")
	}
	margin := b.Dy() / 25
	out := image.NewRGBA(b)
	draw.Draw(out, b, bg, b.Min, draw.Src)
	at := image.Rect(b.Max.X-margin-w, b.Max.Y-margin-h, b.Max.X-margin, b.Max.Y-margin)
	xdraw.CatmullRom.Scale(out, at, lg, lb, draw.Over, nil)
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeBounded decodes a picture whose header claims a size it can hold.
func decodeBounded(data []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxHeaderPixels {
		return nil, fmt.Errorf("%dx%d is not a size a header can use", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}
