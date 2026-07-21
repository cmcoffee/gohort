package core

import (
	"encoding/base64"
	"encoding/json"
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
	tool := &restImageTool{connector: "img"}
	sess := &ToolSession{}
	var node any
	json.Unmarshal([]byte(`{"images":["`+tinyPNG+`"]}`), &node)
	out, err := tool.extractAndAttach(sess, node, "images.0", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "IMAGE:generated" {
		t.Errorf("unexpected ref: %q", out)
	}
	if len(sess.Images) != 1 || sess.Images[0] != tinyPNG {
		t.Errorf("image not attached to session: %v", sess.Images)
	}
}

func TestExtractBase64Invalid(t *testing.T) {
	tool := &restImageTool{connector: "img"}
	var node any
	json.Unmarshal([]byte(`{"images":["not_valid_base64_!!!"]}`), &node)
	if _, err := tool.extractAndAttach(&ToolSession{}, node, "images.0", "", "", nil); err == nil {
		t.Error("expected error decoding invalid base64")
	}
}

func TestExtractURL(t *testing.T) {
	tool := &restImageTool{connector: "img"}
	var node any
	json.Unmarshal([]byte(`{"data":[{"url":"https://cdn.example/img.png"}]}`), &node)
	out, err := tool.extractAndAttach(&ToolSession{}, node, "", "data.0.url", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "IMAGE:https://cdn.example/img.png" {
		t.Errorf("unexpected URL ref: %q", out)
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

	tool := &restImageTool{connector: "img"}
	sess := &ToolSession{}
	tmpl := srv.URL + "/view?filename={filename}&subfolder={subfolder}&type={type}"
	vars := map[string]string{"filename": "g.png", "subfolder": "", "type": "output"}
	out, err := tool.extractAndAttach(sess, nil, "", "", tmpl, vars)
	if err != nil {
		t.Fatal(err)
	}
	if out != "IMAGE:generated" {
		t.Errorf("unexpected ref: %q", out)
	}
	if len(sess.Images) != 1 || sess.Images[0] != tinyPNG {
		t.Errorf("fetched image not attached: got %d images", len(sess.Images))
	}
}

func TestExtractURLTemplateUnresolvedToken(t *testing.T) {
	tool := &restImageTool{connector: "img"}
	// A field resolved empty leaves an unfilled {type} token → clear error, no fetch.
	_, err := tool.extractAndAttach(&ToolSession{}, nil, "", "",
		"http://x/view?f={filename}&t={type}", map[string]string{"filename": "g.png"})
	if err == nil || !strings.Contains(err.Error(), "template") {
		t.Errorf("expected template-token error, got %v", err)
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

func TestRestImageToolName(t *testing.T) {
	if got := restImageToolName("my-comfy"); got != "generate_image_my_comfy" {
		t.Errorf("hyphen not normalized: %q", got)
	}
}
