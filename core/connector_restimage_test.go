package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		"images.0":                    "AAA",
		"images.1":                    "BBB",
		"data.n":                      "3",
		"data.ok":                     "true",
		"job-1.out.9.images.0.filename": "g.png", // id key with a dash, numeric node id, array index
		"missing.path":                "",
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
		"http://alpaca.snuglab.local:8188/prompt": "http://alpaca.snuglab.local:8188/**",
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
