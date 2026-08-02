package core

// The INLINE image-input shape: the source photo rides in the request body as
// base64 instead of being uploaded first. It was written for A1111 img2img and
// then had no declaration using it, so nothing exercised the token half of the
// design. a1111_img2img is that declaration.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// b64OnePixel is a real 1x1 PNG: the result path decode-verifies what a backend
// returns, so a placeholder string would fail for the wrong reason.
const b64OnePixel = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAACklEQVR4nGNgAAACAAEA//8DAAAGAAV9XBsAAAAASUVORK5CYII="

// newCapturingImageServer records the submitted body and replies with reply.
func newCapturingImageServer(t *testing.T, into *string, reply string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*into = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
}

func TestInlineBackendIsAnEditorWithoutAnUploadEndpoint(t *testing.T) {
	spec, err := ApplyRestImagePreset("a1111_img2img", RestImageSpec{Credential: "no_auth"}, map[string]string{"base_url": "http://localhost:7860"})
	if err != nil {
		t.Fatalf("ApplyRestImagePreset: %v", err)
	}
	if !spec.SupportsImageInput() {
		t.Error("an {images} token in the body IS image input — no upload endpoint needed")
	}
	if spec.UploadURL != "" {
		t.Error("the inline shape must not declare an upload endpoint")
	}
	if spec.MaxImages() != 1 {
		t.Errorf("MaxImages = %d, want 1", spec.MaxImages())
	}
	c := Connector{Name: "a1111_edit", Kind: RestImageConnectorKind}
	c.Spec, _ = json.Marshal(spec)
	if err := (restImageHandler{}).Validate(c); err != nil {
		t.Fatalf("must validate: %v", err)
	}
}

func TestInlineBodyCarriesTheImageAsBase64(t *testing.T) {
	secureAPITestStore(t)
	var got string
	srv := newCapturingImageServer(t, &got, `{"images":["`+b64OnePixel+`"]}`)
	defer srv.Close()

	spec, err := ApplyRestImagePreset("a1111_img2img", RestImageSpec{Credential: "no_auth"}, map[string]string{"base_url": srv.URL})
	if err != nil {
		t.Fatalf("ApplyRestImagePreset: %v", err)
	}
	png := smallPNG(t)
	if _, err := spec.generate(&ToolSession{}, restImageParams{
		prompt: "make it snowy",
		seed:   1,
		images: []inputImage{{name: "photo.png", data: png}},
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	// init_images must be a JSON ARRAY of base64 — that is what {images}
	// expands to, and the single-value {image} token would produce a bare
	// string that A1111 rejects.
	var body struct {
		InitImages []string `json:"init_images"`
		Prompt     string   `json:"prompt"`
		Denoising  float64  `json:"denoising_strength"`
	}
	if err := json.Unmarshal([]byte(got), &body); err != nil {
		t.Fatalf("submitted body is not JSON: %v\n%s", err, got)
	}
	if len(body.InitImages) != 1 {
		t.Fatalf("init_images = %v, want one entry", body.InitImages)
	}
	if decoded, err := decodeBase64Image(body.InitImages[0]); err != nil || len(decoded) != len(png) {
		t.Error("init_images[0] is not the source photo")
	}
	if body.Prompt != "make it snowy" {
		t.Errorf("prompt = %q", body.Prompt)
	}
	// The strength stays where the operator set it, not on the tool surface.
	if body.Denoising <= 0 || body.Denoising >= 1 {
		t.Errorf("denoising_strength = %v, want the preset's fixed value", body.Denoising)
	}
}

func TestInlineBackendUploadsNothing(t *testing.T) {
	// uploadInputImages must no-op for the inline shape. Reaching for an upload
	// endpoint that does not exist would fail every edit on this backend.
	spec, err := ApplyRestImagePreset("a1111_img2img", RestImageSpec{Credential: "no_auth"}, map[string]string{"base_url": "http://localhost:7860"})
	if err != nil {
		t.Fatalf("ApplyRestImagePreset: %v", err)
	}
	up, mask, err := spec.uploadInputImages(&ToolSession{}, restImageParams{
		images: []inputImage{{name: "photo.png", data: smallPNG(t)}},
	})
	if err != nil {
		t.Fatalf("uploadInputImages: %v", err)
	}
	if up != nil || mask != nil {
		t.Errorf("inline shape must upload nothing, got %v / %v", up, mask)
	}
}

func TestImg2ImgTemplateIsPureData(t *testing.T) {
	// The whole point of the declaration split: an editing backend is one
	// template value naming an existing strategy, with no code of its own.
	tpl, ok := GetConnectorTemplate("a1111_img2img")
	if !ok {
		t.Fatal("a1111_img2img template is not registered")
	}
	if tpl.Strategy != "rest_image_preset" {
		t.Errorf("strategy = %q, want the shared preset strategy", tpl.Strategy)
	}
	if tpl.Params["preset"] != "a1111_img2img" {
		t.Errorf("params = %v, want the img2img preset", tpl.Params)
	}
	plain, _ := GetConnectorTemplate("a1111")
	if tpl.Strategy != plain.Strategy {
		t.Error("both a1111 declarations must ride the same strategy — that is what makes them data")
	}
	if !strings.Contains(strings.ToLower(tpl.Description), "photo") {
		t.Errorf("description should say it edits a photo: %q", tpl.Description)
	}
}
