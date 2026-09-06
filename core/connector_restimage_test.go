package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/cmcoffee/snugforge/kvlite"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tinyPNG is a 1x1 transparent PNG, base64-encoded — a valid decodable payload.
const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

func decodeSpec(t *testing.T, s RestImageSpec) Connector {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return Connector{Name: "img", Kind: RestImageConnectorKind, Spec: raw}
}

func TestRestImageValidate(t *testing.T) {
	h := restImageHandler{}

	// Missing submit_url.
	if err := h.Validate(decodeSpec(t, RestImageSpec{ImageB64Path: "images.0"})); err == nil {
		t.Error("expected error for missing submit_url")
	}
	// Synchronous backend with no result path.
	if err := h.Validate(decodeSpec(t, RestImageSpec{SubmitURL: "http://x/y"})); err == nil {
		t.Error("expected error for missing result path")
	}
	// Valid synchronous (no_auth avoids the credential-store lookup).
	if err := h.Validate(decodeSpec(t, RestImageSpec{SubmitURL: "http://x/y", Credential: "no_auth", ImageB64Path: "images.0"})); err != nil {
		t.Errorf("valid sync spec rejected: %v", err)
	}
	// Poll backend missing poll_ready_path.
	if err := h.Validate(decodeSpec(t, RestImageSpec{SubmitURL: "http://x/y", Credential: "no_auth", SubmitIDPath: "prompt_id", PollURL: "http://x/h/{id}"})); err == nil {
		t.Error("expected error for poll backend missing poll_ready_path")
	}
	// Valid poll backend.
	if err := h.Validate(decodeSpec(t, RestImageSpec{
		SubmitURL: "http://x/y", Credential: "no_auth", SubmitIDPath: "prompt_id",
		PollURL: "http://x/h/{id}", PollReadyPath: "{id}.done", PollB64Path: "{id}.img",
	})); err != nil {
		t.Errorf("valid poll spec rejected: %v", err)
	}
}

func TestApplyRestImagePresetA1111(t *testing.T) {
	spec, err := ApplyRestImagePreset("a1111", RestImageSpec{Credential: "no_auth"},
		map[string]string{"base_url": "http://localhost:7860"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.SubmitURL != "http://localhost:7860/sdapi/v1/txt2img" {
		t.Errorf("base_url not substituted: %q", spec.SubmitURL)
	}
	if spec.ImageB64Path != "images.0" {
		t.Errorf("preset image path lost: %q", spec.ImageB64Path)
	}
	// Runtime tokens must survive preset application (only vars are substituted).
	if !strings.Contains(spec.SubmitBody, "{prompt}") {
		t.Errorf("runtime {prompt} token was consumed: %q", spec.SubmitBody)
	}
	if spec.DefaultWidth != 512 {
		t.Errorf("preset default width lost: %d", spec.DefaultWidth)
	}
}

func TestApplyRestImagePresetComfyVars(t *testing.T) {
	spec, err := ApplyRestImagePreset("comfyui", RestImageSpec{Credential: "no_auth"},
		map[string]string{"base_url": "http://localhost:8188"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.PollURL != "http://localhost:8188/history/{id}" {
		t.Errorf("poll_url base_url not substituted / {id} lost: %q", spec.PollURL)
	}
	if !strings.HasPrefix(spec.PollURLTemplate, "http://localhost:8188/view?") {
		t.Errorf("poll_url_template base_url not substituted: %q", spec.PollURLTemplate)
	}
	// poll_fields keep their runtime {id} tokens.
	if !strings.Contains(spec.PollFields["filename"], "{id}") {
		t.Errorf("poll_fields {id} consumed: %v", spec.PollFields)
	}
}

func TestRestJSONString(t *testing.T) {
	var node any
	json.Unmarshal([]byte(`{"images":["AAA","BBB"],"data":{"n":3,"ok":true},"job-1":{"out":{"9":{"images":[{"filename":"g.png"}]}}}}`), &node)
	cases := map[string]string{
		"images.0":                      "AAA",
		"images.1":                      "BBB",
		"data.n":                        "3",
		"data.ok":                       "true",
		"job-1.out.9.images.0.filename": "g.png", // id key with a dash, numeric node id, array index
		"missing.path":                  "",
	}
	for path, want := range cases {
		if got := restJSONString(node, path); got != want {
			t.Errorf("restJSONString(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestExtractBase64(t *testing.T) {
	var node any
	json.Unmarshal([]byte(`{"images":["`+tinyPNG+`"]}`), &node)
	out, err := extractOutcome(node, "images.0", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.b64 != tinyPNG || out.url != "" {
		t.Errorf("unexpected outcome: b64=%q url=%q", out.b64, out.url)
	}
}

func TestExtractBase64Invalid(t *testing.T) {
	var node any
	json.Unmarshal([]byte(`{"images":["not_valid_base64_!!!"]}`), &node)
	if _, err := extractOutcome(node, "images.0", "", "", nil); err == nil {
		t.Error("expected error decoding invalid base64")
	}
}

func TestExtractURL(t *testing.T) {
	var node any
	json.Unmarshal([]byte(`{"data":[{"url":"https://cdn.example/img.png"}]}`), &node)
	out, err := extractOutcome(node, "", "data.0.url", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.url != "https://cdn.example/img.png" || out.b64 != "" {
		t.Errorf("unexpected outcome: b64=%q url=%q", out.b64, out.url)
	}
}

func TestExtractURLTemplateFetchesBytes(t *testing.T) {
	imgBytes, _ := base64.StdEncoding.DecodeString(tinyPNG)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filename") != "g.png" || r.URL.Query().Get("subfolder") != "" || r.URL.Query().Get("type") != "output" {
			http.Error(w, "bad params", http.StatusBadRequest)
			return
		}
		w.Write(imgBytes)
	}))
	defer srv.Close()

	tmpl := srv.URL + "/view?filename={filename}&subfolder={subfolder}&type={type}"
	vars := map[string]string{"filename": "g.png", "subfolder": "", "type": "output"}
	out, err := extractOutcome(nil, "", "", tmpl, vars)
	if err != nil {
		t.Fatal(err)
	}
	if out.b64 != tinyPNG {
		t.Errorf("fetched image bytes mismatch: %q", out.b64)
	}
}

func TestExtractURLTemplateUnresolvedToken(t *testing.T) {
	// A field resolved empty leaves an unfilled {type} token → clear error, no fetch.
	_, err := extractOutcome(nil, "", "",
		"http://x/view?f={filename}&t={type}", map[string]string{"filename": "g.png"})
	if err == nil || !strings.Contains(err.Error(), "template") {
		t.Errorf("expected template-token error, got %v", err)
	}
}

func TestResolveImageDims(t *testing.T) {
	// Unset defaults → 512 square.
	if w, h := resolveImageDims(false, 0, 0); w != 512 || h != 512 {
		t.Errorf("unset dims = %dx%d, want 512x512", w, h)
	}
	// Non-landscape keeps the spec defaults as-is.
	if w, h := resolveImageDims(false, 768, 512); w != 768 || h != 512 {
		t.Errorf("square-request dims = %dx%d, want 768x512", w, h)
	}
	// Landscape orients wide: a portrait default gets swapped.
	if w, h := resolveImageDims(true, 512, 768); w != 768 || h != 512 {
		t.Errorf("landscape dims = %dx%d, want 768x512 (swapped)", w, h)
	}
	// Landscape with an already-wide default is untouched.
	if w, h := resolveImageDims(true, 1024, 576); w != 1024 || h != 576 {
		t.Errorf("landscape wide dims = %dx%d, want 1024x576", w, h)
	}
}

func TestImageBackendRegistryRouting(t *testing.T) {
	// A backend registered by name is reachable through generateWithProvider's
	// default case — the seam that lets a connector serve the native pipeline.
	// The backend returns an error so the success/usage-record path is skipped.
	RegisterImageBackend("unit_test_backend", func(_ context.Context, prompt string, landscape bool) (*ImageGenResult, error) {
		return nil, fmt.Errorf("routed:%s:%v", prompt, landscape)
	})
	if !ImageBackendRegistered("unit_test_backend") {
		t.Fatal("backend not registered")
	}
	_, err := generateWithProvider(context.Background(), "unit_test_backend", "", "a cat", true)
	if err == nil || err.Error() != "routed:a cat:true" {
		t.Errorf("routing failed, got %v", err)
	}
	// An unknown provider still errors clearly.
	if _, err := generateWithProvider(context.Background(), "nope_no_backend", "", "x", false); err == nil ||
		!strings.Contains(err.Error(), "unknown image provider") {
		t.Errorf("expected unknown-provider error, got %v", err)
	}
}

func TestResolvePollFields(t *testing.T) {
	var node any
	json.Unmarshal([]byte(`{"job-1":{"outputs":{"9":{"images":[{"filename":"g.png","subfolder":"sub","type":"output"}]}}}}`), &node)
	fields := map[string]string{
		"filename":  "{id}.outputs.9.images.0.filename",
		"subfolder": "{id}.outputs.9.images.0.subfolder",
		"type":      "{id}.outputs.9.images.0.type",
	}
	got := resolvePollFields(fields, node, map[string]string{"id": "job-1"})
	if got["filename"] != "g.png" || got["subfolder"] != "sub" || got["type"] != "output" {
		t.Errorf("poll fields resolved wrong: %v", got)
	}
}

func TestParseHTTPDispatchResult(t *testing.T) {
	status, body := parseHTTPDispatchResult("HTTP 200 OK\n{\"images\":[\"x\"]}\n... [TRUNCATED 1MB]")
	if status != 200 {
		t.Errorf("status = %d", status)
	}
	if body != `{"images":["x"]}` {
		t.Errorf("body = %q", body)
	}
}

func TestJSONInner(t *testing.T) {
	// A prompt with a quote and newline must be escaped so it can't break the body JSON.
	got := jsonInner("a \"b\"\nc")
	body := `{"prompt":"` + got + `"}`
	var m map[string]string
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("escaped prompt broke JSON: %v (%s)", err, body)
	}
	if m["prompt"] != "a \"b\"\nc" {
		t.Errorf("round-trip mismatch: %q", m["prompt"])
	}
}

func TestStripDataURIPrefix(t *testing.T) {
	if got := stripDataURIPrefix("data:image/png;base64," + tinyPNG); got != tinyPNG {
		t.Errorf("prefix not stripped: %q", got)
	}
	if got := stripDataURIPrefix(tinyPNG); got != tinyPNG {
		t.Errorf("bare base64 altered: %q", got)
	}
}

func TestResolveAspect(t *testing.T) {
	// Unknown aspect → not ok.
	if _, _, ok := resolveAspect("nope", 512, 512); ok {
		t.Error("unknown aspect should return ok=false")
	}
	// Square preserves the default.
	if w, h, ok := resolveAspect("square", 512, 512); !ok || w != 512 || h != 512 {
		t.Errorf("square = %dx%d ok=%v", w, h, ok)
	}
	// Wide is landscape-oriented and preserves area (~512² for an SD1.5 backend).
	w, h, ok := resolveAspect("wide", 512, 512)
	if !ok || w <= h {
		t.Errorf("wide should be landscape: %dx%d", w, h)
	}
	if area := w * h; area < 512*512*8/10 || area > 512*512*12/10 {
		t.Errorf("wide area %d drifted too far from 512² (%d)", area, 512*512)
	}
	// Same aspect scales up with an SDXL-sized default (1024²).
	xw, xh, _ := resolveAspect("wide", 1024, 1024)
	if xw <= w {
		t.Errorf("wide on 1024² (%d) should exceed wide on 512² (%d)", xw, w)
	}
	if xw <= xh {
		t.Errorf("wide on 1024² not landscape: %dx%d", xw, xh)
	}
	// tall is the portrait inverse of wide.
	tw, th, _ := resolveAspect("tall", 512, 512)
	if tw >= th {
		t.Errorf("tall should be portrait: %dx%d", tw, th)
	}
}

func TestImageHostPattern(t *testing.T) {
	cases := map[string]string{
		"http://alpaca.snuglab.local:8188/prompt":  "http://alpaca.snuglab.local:8188/**",
		"https://api.example.com/sdapi/v1/txt2img": "https://api.example.com/**",
		"http://localhost:7860/foo":                "http://localhost:7860/**",
	}
	for in, want := range cases {
		if got := imageHostPattern(in); got != want {
			t.Errorf("imageHostPattern(%q) = %q, want %q", in, got, want)
		}
	}
	// The derived pattern must actually admit the connector's own http host but
	// reject a different host — the whole point of scoping instead of http*://**.
	p := imageHostPattern("http://alpaca.snuglab.local:8188/prompt")
	if !urlAllowedByCredential(SecureCredential{AllowedURLPattern: p}, "http://alpaca.snuglab.local:8188/history/abc") {
		t.Error("scoped pattern should allow the same host's poll URL")
	}
	if urlAllowedByCredential(SecureCredential{AllowedURLPattern: p}, "http://169.254.169.254/latest/meta-data") {
		t.Error("scoped pattern must NOT allow a different host (SSRF)")
	}
}

func TestRestImageToolName(t *testing.T) {
	if got := restImageToolName("my-comfy"); got != "generate_image_my_comfy" {
		t.Errorf("hyphen not normalized: %q", got)
	}
}

// Approving an image connector must leave its backend registered.
//
// Materialize ends in a full sweep of the registry against the store, and on
// the approve path it runs BEFORE ApproveConnector writes Approved=true — so
// the sweep read the not-yet-approved row and unregistered the backend
// Materialize had just registered. The connector then sat approved-but-absent:
// missing from the picker, missing from ReachableImageBackends, and answering
// "no image backend named X is available" to anything that named it, until a
// restart re-materialized it. Every image connector looked like it needed a
// restart to take effect.
func TestApprovingAnImageConnectorLeavesItRegistered(t *testing.T) {
	peerImageDB(t)
	c := decodeSpec(t, RestImageSpec{
		SubmitURL: "https://gpu.example/render", SubmitMethod: "POST",
		SubmitBody: `{"prompt":"{prompt}"}`, ImageB64Path: "images.0",
	})
	if err := SaveConnector(RootDB, c); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := ApproveConnector(RootDB, c.Name); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if !ImageBackendRegistered(c.Name) {
		t.Error("the backend is not registered right after approval — it will not appear until a restart")
	}
	if !ImageBackendReachable(nil, c.Name) {
		t.Error("an approved connector is not reachable, so nothing can name it as a backend")
	}

	// The sweep must still do its job: unapproving drops the backend.
	if err := UnapproveConnector(RootDB, c.Name); err != nil {
		t.Fatalf("unapprove: %v", err)
	}
	if ImageBackendRegistered(c.Name) {
		t.Error("an unapproved connector kept a live backend")
	}
}

// "Any save should reload the entire image generation."
//
// It didn't. Materialize registered the row that changed and nothing else,
// and Teardown was a no-op on the registry — so an unapproved, deleted or
// renamed connector kept a live backend closure for the rest of the process.
// ImageBackendRegistered went on saying yes, which is what the admin's
// image-provider picker keys off, and only a restart cleared it.
func reloadTestDB(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	t.Cleanup(func() {
		RootDB = prev
		restImageMu.Lock()
		for n := range ownedImageBackends {
			delete(ownedImageBackends, n)
			UnregisterImageBackend(n)
		}
		restImageMu.Unlock()
	})
	return db
}

func comfyConnector(t *testing.T, name, baseURL string) Connector {
	t.Helper()
	tpl, ok := GetConnectorTemplate("comfyui")
	if !ok {
		t.Fatal("comfyui template not registered")
	}
	raw, _, err := comfyBuildSpec(tpl, map[string]any{
		"base_url": baseURL, "workflow_type": ComfyTypeGenerate, "credential": "no_auth",
	})
	if err != nil {
		t.Fatalf("build spec: %v", err)
	}
	return Connector{Name: name, Kind: RestImageConnectorKind, Spec: json.RawMessage(raw), Approved: true}
}

// Unapproving a connector must take its backend out of the registry, not leave
// it on offer until the process restarts.
func TestTearingDownAConnectorDropsItsBackend(t *testing.T) {
	db := reloadTestDB(t)
	c := comfyConnector(t, "comfyui", "http://box:8188")
	db.Set(connectorsTable, c.Name, c)

	if err := (restImageHandler{}).Materialize(c); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if !ImageBackendRegistered("comfyui") {
		t.Fatal("an approved connector must have a live backend")
	}

	c.Approved = false
	db.Set(connectorsTable, c.Name, c)
	if err := (restImageHandler{}).Teardown(c); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if ImageBackendRegistered("comfyui") {
		t.Error("an unapproved connector must not stay registered as a backend")
	}
}

// The sweep is the "reload everything" part: saving ANY connector reconciles
// the whole registry, so a name that went away behind the app's back — a
// rename, a delete, an unapprove that skipped Teardown — is cleaned up by the
// next save rather than surviving to the next restart.
func TestASaveReconcilesEveryBackendNotJustItsOwn(t *testing.T) {
	db := reloadTestDB(t)
	gone := comfyConnector(t, "old_name", "http://box:8188")
	db.Set(connectorsTable, gone.Name, gone)
	if err := (restImageHandler{}).Materialize(gone); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if !ImageBackendRegistered("old_name") {
		t.Fatal("setup: backend should be live")
	}

	// The row disappears without Teardown running — what a rename does.
	db.Unset(connectorsTable, "old_name")

	// Saving an UNRELATED connector must still clean it up.
	other := comfyConnector(t, "new_name", "http://box:8188")
	db.Set(connectorsTable, other.Name, other)
	if err := (restImageHandler{}).Materialize(other); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	if ImageBackendRegistered("old_name") {
		t.Error("the orphaned backend should have been swept on the next save")
	}
	if !ImageBackendRegistered("new_name") {
		t.Error("the saved connector must be registered")
	}
}

// The sweep only removes names rest_image put there — it must not clobber a
// backend registered by anything else.
func TestTheSweepLeavesForeignBackendsAlone(t *testing.T) {
	db := reloadTestDB(t)
	RegisterImageBackend("not_a_connector", func(_ context.Context, _ string, _ bool) (*ImageGenResult, error) {
		return nil, nil
	})
	t.Cleanup(func() { UnregisterImageBackend("not_a_connector") })

	c := comfyConnector(t, "comfyui", "http://box:8188")
	db.Set(connectorsTable, c.Name, c)
	if err := (restImageHandler{}).Materialize(c); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if !ImageBackendRegistered("not_a_connector") {
		t.Error("a backend this handler did not register must survive the sweep")
	}
}

// Re-saving with a new server keeps exactly one backend under the same name,
// pointed at the edit — the whole point of reloading on save.
func TestReSavingRefreshesRatherThanDuplicates(t *testing.T) {
	db := reloadTestDB(t)
	c := comfyConnector(t, "comfyui", "http://oldbox:8188")
	db.Set(connectorsTable, c.Name, c)
	if err := (restImageHandler{}).Materialize(c); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	moved := comfyConnector(t, "comfyui", "http://newbox:9000")
	db.Set(connectorsTable, moved.Name, moved)
	if err := (restImageHandler{}).Materialize(moved); err != nil {
		t.Fatalf("re-materialize: %v", err)
	}

	// One entry under this name, not a second registration alongside the old
	// one. Asserted against the handler's own set rather than the whole
	// registry, which other tests in this package also write to.
	restImageMu.Lock()
	owned := len(ownedImageBackends)
	mine := ownedImageBackends["comfyui"]
	restImageMu.Unlock()
	if !mine || owned != 1 {
		t.Errorf("handler-owned backends = %d (comfyui present: %v), want exactly comfyui", owned, mine)
	}
	stored, ok := GetConnector(db, "comfyui")
	if !ok {
		t.Fatal("connector missing")
	}
	s, err := (restImageHandler{}).parse(stored)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.SubmitURL == "" || !strings.Contains(s.SubmitURL, "newbox") {
		t.Errorf("the live spec still points at the old server: %q", s.SubmitURL)
	}
}

// The INLINE image-input shape: the source photo rides in the request body as
// base64 instead of being uploaded first. It was written for A1111 img2img and
// then had no declaration using it, so nothing exercised the token half of the
// design. a1111_img2img is that declaration.

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

// A SYNCHRONOUS image backend answers the submit with the finished picture, so
// the submit IS the render. It was governed by tune_secure_api_request_timeout
// — 30 seconds, a cap sized for ordinary API calls — while the image deadline
// the operator actually set (900s for an edit) governed only the poll loop a
// synchronous backend never reaches.
//
// A peer render is exactly this shape: the far side runs the whole job under
// its own ten-minute budget and replies with pixels. Every peer edit that took
// longer than half a minute came back as "den.snuglab.com did not respond
// within 30s", which reads as a network fault rather than a deadline nobody
// could see.

func TestSynchronousImageSubmitGetsTheRenderDeadline(t *testing.T) {
	sync := RestImageSpec{
		SubmitURL: "http://gpu.example/render", SubmitMethod: "POST",
		SubmitBody: `{"prompt":"{prompt}","init_images":{images}}`, ImageB64Path: "images.0",
	}
	// An editing spec, so it takes the (longer) edit deadline.
	if !sync.SupportsImageInput() {
		t.Fatal("the fixture is meant to be an editing backend")
	}
	got := sync.submitTimeoutSecs()
	want := int(sync.pollDeadline() / time.Second)
	if got != want {
		t.Errorf("submit timeout = %ds, want the render deadline %ds", got, want)
	}
	if got <= int(secureAPIRequestTimeout()/time.Second) {
		t.Errorf("submit timeout %ds is no better than the general API cap — the bug is unfixed", got)
	}

	// An explicit per-connector deadline still wins.
	sync.PollMaxSecs = 1234
	if got := sync.submitTimeoutSecs(); got != 1234 {
		t.Errorf("an explicit PollMaxSecs must govern the submit too, got %ds", got)
	}
}

func TestAsyncImageSubmitKeepsTheGeneralCap(t *testing.T) {
	// With a poll URL the submit should return an id immediately and the poll
	// loop does the waiting. Letting the submit hang for fifteen minutes there
	// would hide a genuinely wedged backend.
	async := RestImageSpec{
		SubmitURL: "http://gpu.example/prompt", SubmitMethod: "POST",
		PollURL: "http://gpu.example/history/{id}", ImageB64Path: "images.0",
	}
	if got := async.submitTimeoutSecs(); got != 0 {
		t.Errorf("submit timeout = %ds, want 0 (leave the general cap alone) for a polling backend", got)
	}
}

func TestDispatchReadsTheTimeoutOverrideWhateverItsNumericType(t *testing.T) {
	// The args map is hand-built in one place and JSON-decoded in another, so
	// the same value arrives as int here and float64 there. An override that is
	// silently ignored is the exact failure this change removes.
	for _, v := range []any{900, int64(900), float64(900)} {
		if got := secureTimeoutSeconds(map[string]any{secureTimeoutArg: v}); got != 900 {
			t.Errorf("%T override read as %d, want 900", v, got)
		}
	}
	if got := secureTimeoutSeconds(map[string]any{}); got != 0 {
		t.Errorf("absent override read as %d, want 0", got)
	}
}

// End to end through the real dispatch: a backend slower than the general API
// cap must still be waited for. Uses a tiny override rather than a real 30s
// wait — the point is that the SPEC's number governs, not the general one.
func TestASlowSynchronousRenderIsNotCutOffByTheAPICap(t *testing.T) {
	peerImageDB(t)
	prev := ImageDir()
	SetImageDir(t.TempDir())
	t.Cleanup(func() { SetImageDir(prev) })

	// General API cap: 1s. Render deadline: 30s. The backend takes ~1.4s, so it
	// finishes only if the render deadline is the one being enforced.
	tdb := &DBase{Store: kvlite.MemStore()}
	tdb.Set(WebTable, "tune_secure_api_request_timeout", float64(1))
	SetTunablesDB(tdb)
	t.Cleanup(func() { SetTunablesDB(nil) })

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1400 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"images":[%q]}`, tinyPNG)
	}))
	t.Cleanup(slow.Close)

	spec := RestImageSpec{
		SubmitURL: slow.URL + "/sdapi/v1/img2img", SubmitMethod: "POST",
		SubmitBody:   `{"prompt":"{prompt}","init_images":{images}}`,
		ImageB64Path: "images.0", Credential: "no_auth",
		MaxInputImages: 1, PollMaxSecs: 30,
	}
	raw, _ := json.Marshal(spec)
	if err := SaveConnector(RootDB, Connector{Name: "slowedit", Kind: RestImageConnectorKind, Spec: raw}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := ApproveConnector(RootDB, "slowedit"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	src := filepath.Join(ImageDir(), "src.png")
	png, _ := base64.StdEncoding.DecodeString(tinyPNG)
	if err := os.WriteFile(src, png, 0644); err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{WorkspaceDir: ImageDir()}
	res, err := EditImageWithBackend(sess, EditImageRequest{
		Backend: "slowedit", Prompt: "one legible number on the left pillar",
		Images: []string{"src.png"},
	})
	if err != nil {
		t.Fatalf("a render slower than the general API cap was cut off: %v", err)
	}
	if res == nil || res.URL == "" {
		t.Fatal("no image came back")
	}
}
