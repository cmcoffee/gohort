package scribe

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func solidPNG(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var (
	red  = color.RGBA{255, 0, 0, 255}
	blue = color.RGBA{0, 0, 255, 255}
)

// The logo goes on the finished picture as it was uploaded: in the bottom
// right, a fifth of the height, and nowhere else.
func TestTheLogoIsPlacedAsItIs(t *testing.T) {
	out, err := placeLogo(solidPNG(t, 300, 100, red), solidPNG(t, 50, 50, blue))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil || img.Bounds().Dx() != 300 || img.Bounds().Dy() != 100 {
		t.Fatalf("the header changed size: %v %v", img.Bounds(), err)
	}
	at := func(x, y int) color.RGBA {
		r, g, b, a := img.At(x, y).RGBA()
		return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
	}
	// 20px square, 4px in from the bottom right corner.
	if got := at(286, 86); got != blue {
		t.Errorf("the logo is not in the corner: %v", got)
	}
	for _, p := range [][2]int{{10, 10}, {150, 50}, {298, 98}, {270, 86}} {
		if got := at(p[0], p[1]); got != red {
			t.Errorf("the picture changed at %v: %v", p, got)
		}
	}
	if _, err := placeLogo(solidPNG(t, 300, 100, red), []byte("not a picture")); err == nil {
		t.Error("a logo that is not a picture was placed")
	}
}

// The title never reaches the image model in quotes, and the prompt ends on
// the no-writing rule.
func TestTheHeaderPromptCarriesNoQuotedText(t *testing.T) {
	g := Guide{ID: "g1", Title: `Deploying "Atlas" safely`}
	scene := (&Scribe{}).headerScene(context.Background(), g)
	if strings.ContainsAny(scene, "\"“”") {
		t.Errorf("the scene carries a quote: %q", scene)
	}
	p := headerJob{guide: g, scene: scene}.prompt()
	if !strings.HasSuffix(p, headerPictureRule) || strings.ContainsAny(p, "\"“”") {
		t.Errorf("prompt: %q", p)
	}
	if got := cleanHeaderScene("A desk at dawn with a “Welcome” mug.\n\nSecond paragraph."); got != "A desk at dawn with a Welcome mug." {
		t.Errorf("clean: %q", got)
	}
	for _, s := range []string{headerSceneSystem, headerPictureRule} {
		if strings.Contains(s, "—") {
			t.Error("prompts take no em-dashes")
		}
	}
}

// Generate end to end, on a backend that records what it was asked and takes
// no picture: a guide with a logo gets a header with the logo placed on it,
// and the prompt it was made from never names the title in quotes.
func TestAHeaderIsGeneratedWithTheGuidesLogo(t *testing.T) {
	var mu sync.Mutex
	var asked string
	dir := t.TempDir()
	RegisterImageBackend("test-header", func(ctx context.Context, prompt string, landscape bool) (*ImageGenResult, error) {
		mu.Lock()
		asked = prompt
		mu.Unlock()
		p := filepath.Join(dir, "out.png")
		return &ImageGenResult{URL: p}, os.WriteFile(p, solidPNG(t, 300, 100, red), 0o644)
	})
	t.Cleanup(func() { UnregisterImageBackend("test-header") })
	prev := ImageProviderFunc
	ImageProviderFunc = func() string { return "test-header" }
	t.Cleanup(func() { ImageProviderFunc = prev })
	// A generator that takes no picture: the logo is placed as uploaded.
	withEditors(t, nil)

	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	T := &Scribe{AppCore: AppCore{DB: root}}
	udb.Set(guideImagesTable, guideImageKey("g1", "logo1"), guideImage{Mime: "image/png", Data: solidPNG(t, 50, 50, blue)})
	udb.Set(guideImagesTable, guideImageKey("g2", "other"), guideImage{Mime: "image/png", Data: solidPNG(t, 50, 50, blue)})
	saveGuideRev(udb, Guide{ID: "g1", Owner: "u", Title: `Deploying "Atlas" safely`}, "new")

	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		T.handleImage(w, httptest.NewRequest(http.MethodPost, "/scribe/image?id=g1", strings.NewReader(body)), udb, "u")
		return w
	}
	// Only a picture uploaded to this guide can go with its header.
	if w := post(`{"logo": "` + T.guideImagePath("g2", "other") + `"}`); w.Code != http.StatusBadRequest {
		t.Errorf("another guide's picture was taken as the logo: %d", w.Code)
	}
	if w := post(`{"logo": "` + T.guideImagePath("g1", "logo1") + `"}`); w.Code != http.StatusOK {
		t.Fatalf("setting the logo: %d %s", w.Code, w.Body.String())
	}
	w := post(`{"generate": true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("generate: %d %s", w.Code, w.Body.String())
	}
	g, _ := loadGuide(udb, "g1")
	const pre = "data:image/png;base64,"
	if !strings.HasPrefix(g.ImageURL, pre) {
		t.Fatalf("header: %.60s", g.ImageURL)
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(g.ImageURL, pre))
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if r, _, b, _ := img.At(286, 86).RGBA(); r != 0 || b == 0 {
		t.Error("the logo is not on the header")
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(asked, `"Atlas"`) || !strings.HasSuffix(asked, headerPictureRule) {
		t.Errorf("the image model was asked: %q", asked)
	}

	// Cleared, the next header goes without it.
	if w := post(`{"logo": ""}`); w.Code != http.StatusOK {
		t.Fatalf("clearing the logo: %d", w.Code)
	}
	if g, _ := loadGuide(udb, "g1"); g.HeaderLogo != "" {
		t.Error("the logo stayed")
	}
}

func withEditors(t *testing.T, editors []ImageBackendChoice) {
	t.Helper()
	prev := headerEditors
	headerEditors = func() []ImageBackendChoice { return editors }
	t.Cleanup(func() { headerEditors = prev })
}

// editCall is what the stand-in editing backend was handed.
type editCall struct {
	prompt string
	images [][]byte
}

// withFakeEdit stands in for the editing backend: it records the pictures it
// was handed, in order, and answers with a green picture of its own.
func withFakeEdit(t *testing.T) *[]editCall {
	t.Helper()
	var calls []editCall
	dir := t.TempDir()
	prev := editHeaderImage
	editHeaderImage = func(sess *ToolSession, req EditImageRequest) (*ImageGenResult, error) {
		c := editCall{prompt: req.Prompt}
		for _, ref := range req.Images {
			data, _, err := sess.ResolveImageRef(ref)
			if err != nil {
				return nil, err
			}
			c.images = append(c.images, data)
		}
		calls = append(calls, c)
		p := filepath.Join(dir, "edit.png")
		return &ImageGenResult{URL: p}, os.WriteFile(p, solidPNG(t, 300, 100, color.RGBA{0, 255, 0, 255}), 0o644)
	}
	t.Cleanup(func() { editHeaderImage = prev })
	return &calls
}

// With a generator that takes pictures, the logo is HANDED to it: the header
// is generated, then given to the editor with the logo, and what comes back
// is the header, with nothing pasted over it. A starting picture goes in
// first, the logo beside it.
func TestTheLogoIsHandedToTheGenerator(t *testing.T) {
	dir := t.TempDir()
	banner := solidPNG(t, 300, 100, red)
	RegisterImageBackend("test-header", func(ctx context.Context, prompt string, landscape bool) (*ImageGenResult, error) {
		p := filepath.Join(dir, "out.png")
		return &ImageGenResult{URL: p}, os.WriteFile(p, banner, 0o644)
	})
	t.Cleanup(func() { UnregisterImageBackend("test-header") })
	prev := ImageProviderFunc
	ImageProviderFunc = func() string { return "test-header" }
	t.Cleanup(func() { ImageProviderFunc = prev })
	withEditors(t, []ImageBackendChoice{{Name: "editor", Edits: true, MaxImages: 2, Default: true}})
	calls := withFakeEdit(t)

	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	T := &Scribe{AppCore: AppCore{DB: root}}
	logo, photo := solidPNG(t, 50, 50, blue), solidPNG(t, 80, 60, color.RGBA{255, 255, 0, 255})
	udb.Set(guideImagesTable, guideImageKey("g1", "logo1"), guideImage{Mime: "image/png", Data: logo})
	udb.Set(guideImagesTable, guideImageKey("g1", "photo1"), guideImage{Mime: "image/png", Data: photo})
	g := Guide{ID: "g1", Owner: "u", Title: "Field guide", HeaderLogo: T.guideImagePath("g1", "logo1")}

	out, err := T.generateHeader(context.Background(), g, udb)
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("the editor was called %d time(s)", len(*calls))
	}
	c := (*calls)[0]
	if len(c.images) != 2 || !bytes.Equal(c.images[0], banner) || !bytes.Equal(c.images[1], logo) || c.prompt != headerLogoBlend {
		t.Errorf("the editor should get the header then the logo, told to work it in: %d image(s), %q", len(c.images), c.prompt)
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(out, "data:image/png;base64,"))
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if r, gr, b, _ := img.At(286, 86).RGBA(); r != 0 || gr == 0 || b != 0 {
		t.Error("the logo was pasted over the generator's own result")
	}

	// From a picture: the picture first, the logo second, in one render.
	g.HeaderFrom = T.guideImagePath("g1", "photo1")
	if _, err := T.generateHeader(context.Background(), g, udb); err != nil {
		t.Fatal(err)
	}
	c = (*calls)[1]
	if len(c.images) != 2 || !bytes.Equal(c.images[0], photo) || !bytes.Equal(c.images[1], logo) || !strings.Contains(c.prompt, headerLogoBlend) {
		t.Errorf("from a picture: %d image(s), %q", len(c.images), c.prompt)
	}

	// An editor that takes one picture at a time cannot be handed both, so
	// the logo is placed as uploaded.
	withEditors(t, []ImageBackendChoice{{Name: "editor", Edits: true, MaxImages: 1, Default: true}})
	g.HeaderFrom = ""
	if _, err := T.generateHeader(context.Background(), g, udb); err != nil || len(*calls) != 2 {
		t.Errorf("a one-picture editor should not be handed the logo: %v, %d call(s)", err, len(*calls))
	}
}

// The header goes to the editing backend that fits its pictures, not to the
// default whatever it takes: a header and a logo to a two-input backend, the
// three-input one only when nothing smaller takes them.
func TestTheHeaderPicksTheBackendThatFits(t *testing.T) {
	withEditors(t, []ImageBackendChoice{
		{Name: "three", Edits: true, MaxImages: 3, Default: true},
		{Name: "two", Edits: true, MaxImages: 2},
		{Name: "text", MaxImages: 0},
	})
	if got := headerEditor(2).Name; got != "two" {
		t.Errorf("two pictures went to %q, want the two-input backend", got)
	}
	if got := headerEditor(3).Name; got != "three" {
		t.Errorf("three pictures went to %q", got)
	}
	if got := headerEditor(1).Name; got != "two" {
		t.Errorf("one picture went to %q, want the smallest that takes it", got)
	}
	if got := headerEditor(4).Name; got != "" {
		t.Errorf("four pictures went to %q, but nothing takes four in one render", got)
	}
	withEditors(t, []ImageBackendChoice{
		{Name: "a", Edits: true, MaxImages: 2},
		{Name: "b", Edits: true, MaxImages: 2, Default: true},
	})
	if got := headerEditor(2).Name; got != "b" {
		t.Errorf("a tie went to %q, want the default", got)
	}
}
