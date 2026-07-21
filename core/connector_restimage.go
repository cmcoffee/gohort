// The rest_image connector kind: a GENERIC, spec-declared image-GENERATION
// backend for any HTTP image API — a local ComfyUI or Automatic1111, a hosted
// Stable-Diffusion/Flux endpoint, DALL·E, Replicate — declared entirely from a
// spec, with no Go per backend.
//
// Where the built-in generate_image tool (core/image_gen.go) is hardwired to two
// providers (Gemini, OpenAI) compiled into the binary, rest_image is the
// unified-connector realization for image generation: the Builder authors a spec
// (or picks a preset), an admin approves it, and it materializes a per-connector
// chat tool `generate_image_<name>` that agents can be granted. Because the whole
// backend is just the Spec (endpoints + a request-body template + response
// dot-paths + an optional poll stage), a rest_image connector EXPORTS and IMPORTS
// through the same gohort.bundle machinery as every other connector — the
// "easily shared based on need" goal — with zero extra wiring: the credential is
// referenced by name (never a secret), so a bundle carries the whole capability.
//
// Two request shapes are covered:
//
//   - SYNCHRONOUS (Automatic1111, DALL·E): one POST returns the image inline. The
//     response carries base64 (image_b64_path, e.g. A1111 "images.0") or a URL
//     (image_url_path, e.g. DALL·E "data.0.url").
//
//   - ASYNC / POLL (ComfyUI): submit returns a job id (submit_id_path), then we
//     poll poll_url (with {id} substituted) until poll_ready_path is present, and
//     pull the result out of the poll response — as base64, a URL, or a URL BUILT
//     from response fields (poll_url_template + poll_fields, for ComfyUI's
//     /view?filename=…&subfolder=…&type=… dance) which we then fetch to bytes.
//
// Approval is MANDATORY (no ConnectorAutoApprover): a rest_image backend makes an
// unattended outbound call to an arbitrary (and, with no_auth, un-credentialed)
// host and can incur cost, so it stays pending until an admin approves it in
// Admin > Connectors — the right gate for an imported bundle to land behind, too.
package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RestImageConnectorKind is the Kind value for a spec-declared image backend.
const RestImageConnectorKind = "rest_image"

// restImageToolPrefix names the per-connector chat tool a rest_image materializes.
const restImageToolPrefix = "generate_image_"

// RestImageSpec is the Spec payload for a rest_image connector. Every field is
// data — no secret lives here; Credential names a registered SecureAPI credential
// (or "no_auth" / empty for an unauthenticated local endpoint like a LAN ComfyUI).
type RestImageSpec struct {
	Credential string `json:"credential,omitempty"` // SecureAPI credential name, or "no_auth"/empty for local/public

	// Submit: the generation request.
	SubmitURL    string `json:"submit_url"`              // absolute endpoint
	SubmitMethod string `json:"submit_method,omitempty"` // default POST
	SubmitBody   string `json:"submit_body,omitempty"`   // JSON template; {prompt}/{negative}/{model} are JSON-escaped, {width}/{height}/{steps}/{seed} inserted raw

	// Synchronous result (no poll): the SUBMIT response already carries the image.
	// Provide exactly one of these.
	ImageB64Path string `json:"image_b64_path,omitempty"` // dot-path to a base64 image in the submit response (A1111: "images.0")
	ImageURLPath string `json:"image_url_path,omitempty"` // dot-path to an image URL in the submit response (DALL·E: "data.0.url")

	// Async / poll (ComfyUI): submit returns a job id, then poll until done.
	SubmitIDPath  string `json:"submit_id_path,omitempty"`  // dot-path to the job id in the submit response (ComfyUI: "prompt_id")
	PollURL       string `json:"poll_url,omitempty"`        // URL template polled each tick; {id} = the submitted job id (ComfyUI: ".../history/{id}")
	PollMethod    string `json:"poll_method,omitempty"`     // default GET
	PollReadyPath string `json:"poll_ready_path,omitempty"` // dot-path (may use {id}) that becomes NON-EMPTY once the job is done

	// Poll result extraction — provide exactly one:
	PollB64Path     string            `json:"poll_b64_path,omitempty"`     // dot-path (may use {id}) to a base64 image in the poll response
	PollURLPath     string            `json:"poll_url_path,omitempty"`     // dot-path (may use {id}) to an image URL in the poll response
	PollURLTemplate string            `json:"poll_url_template,omitempty"` // a URL built from poll_fields tokens, then FETCHED to bytes (ComfyUI /view)
	PollFields      map[string]string `json:"poll_fields,omitempty"`       // token -> dot-path (may use {id}), resolved against the poll response to fill poll_url_template

	PollIntervalSecs int `json:"poll_interval_secs,omitempty"` // poll cadence (default 2, min 1)
	PollMaxSecs      int `json:"poll_max_secs,omitempty"`      // give-up deadline (default 120)

	// Defaults for generation params, used when the caller omits them.
	DefaultNegative string `json:"default_negative,omitempty"`
	DefaultWidth    int    `json:"default_width,omitempty"`
	DefaultHeight   int    `json:"default_height,omitempty"`
	DefaultSteps    int    `json:"default_steps,omitempty"`
	DefaultModel    string `json:"default_model,omitempty"`
}

func init() { RegisterConnectorKind(RestImageConnectorKind, restImageHandler{}) }

type restImageHandler struct{}

func (restImageHandler) parse(c Connector) (RestImageSpec, error) {
	var s RestImageSpec
	if len(c.Spec) > 0 {
		if err := json.Unmarshal(c.Spec, &s); err != nil {
			return s, fmt.Errorf("bad rest_image spec: %w", err)
		}
	}
	s.Credential = strings.TrimSpace(s.Credential)
	s.SubmitURL = strings.TrimSpace(s.SubmitURL)
	return s, nil
}

func (h restImageHandler) Validate(c Connector) error {
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(s.SubmitURL, "https://") && !strings.HasPrefix(s.SubmitURL, "http://") {
		return fmt.Errorf("submit_url must be http(s) — got %q (did you fill the preset var, e.g. base_url?)", s.SubmitURL)
	}
	// A named credential must already exist (no_auth/empty is the local/public path).
	if s.Credential != "" && s.Credential != "no_auth" && s.Credential != "none" {
		if exists, _, _ := Secure().CredentialStatus(s.Credential); !exists {
			return fmt.Errorf("no credential named %q — draft it first (draft_api_credential / draft_oauth_credential) and have the admin enable it, or use \"no_auth\" for an unauthenticated local endpoint", s.Credential)
		}
	}
	// Exactly one result path must be declared, distinguishing sync vs poll.
	polling := s.SubmitIDPath != "" || s.PollURL != ""
	if polling {
		if s.PollURL == "" {
			return fmt.Errorf("poll_url is required when submit_id_path is set (the endpoint polled until the job completes)")
		}
		if s.PollReadyPath == "" {
			return fmt.Errorf("poll_ready_path is required for a poll backend (a dot-path that becomes non-empty when the image is ready)")
		}
		if s.PollB64Path == "" && s.PollURLPath == "" && s.PollURLTemplate == "" {
			return fmt.Errorf("a poll backend needs one of poll_b64_path, poll_url_path, or poll_url_template to locate the finished image")
		}
	} else {
		if s.ImageB64Path == "" && s.ImageURLPath == "" {
			return fmt.Errorf("a synchronous backend needs image_b64_path (base64 in the response, e.g. A1111 \"images.0\") or image_url_path (a URL in the response); for an async backend set submit_id_path + poll_url")
		}
	}
	return nil
}

// Materialize registers the per-connector generate_image_<name> chat tool. The
// registry is append-only (mirrors MCP proxies), so we register a live-resolving
// proxy ONCE per name; it reads the connector's spec from RootDB at call time, so
// an edit (re-materialize) or an Unapprove/Delete takes effect without touching
// the registry — a torn-down connector's tool simply errors on use.
func (h restImageHandler) Materialize(c Connector) error {
	if _, err := h.parse(c); err != nil {
		return err
	}
	name := strings.TrimSpace(c.Name)
	// Native backend: makes this connector usable as an image PROVIDER across the
	// whole app (the default generate_image tool, writer-app illustrations via
	// GenerateImage*, the admin image-provider setting) — not only as the
	// generate_image_<name> chat tool. Idempotent; the closure resolves the spec
	// live so an edit takes effect without re-registering.
	RegisterImageBackend(name, func(_ context.Context, prompt string, landscape bool) (*ImageGenResult, error) {
		return generateRestImageNative(name, prompt, landscape)
	})
	restImageMu.Lock()
	defer restImageMu.Unlock()
	if registeredRestImageTools[name] {
		return nil
	}
	RegisterChatTool(&restImageTool{connector: name})
	registeredRestImageTools[name] = true
	return nil
}

// Teardown is a no-op on the registry (append-only): the proxy live-resolves the
// connector, so once this connector is unapproved/deleted the tool returns an
// "unavailable" error. Mirrors the MCP kind.
func (restImageHandler) Teardown(c Connector) error { return nil }

func (h restImageHandler) Summary(c Connector) string {
	s, _ := h.parse(c)
	cred := s.Credential
	if cred == "" {
		cred = "no_auth"
	}
	mode := "synchronous"
	if s.PollURL != "" {
		mode = "poll"
	}
	url := s.SubmitURL
	if url == "" {
		url = "(no url)"
	}
	return fmt.Sprintf("generate images via %s (%s, credential %s) → tool %s%s", url, mode, cred, restImageToolPrefix, restImageToolName(c.Name)[len(restImageToolPrefix):])
}

var (
	restImageMu              sync.Mutex
	registeredRestImageTools = map[string]bool{}
)

// restImageToolName is the chat-tool name for a connector. Hyphens (legal in a
// connector name) become underscores so the tool name stays a clean snake_case
// identifier the tool-call parser won't choke on.
func restImageToolName(connector string) string {
	return restImageToolPrefix + strings.ReplaceAll(strings.TrimSpace(connector), "-", "_")
}

// --- the materialized tool ---------------------------------------------------

// restImageTool is the per-connector generate_image_<name> chat tool. It holds
// only the connector name and resolves the spec live from RootDB on each call.
type restImageTool struct{ connector string }

func (t *restImageTool) Name() string { return restImageToolName(t.connector) }

func (t *restImageTool) Desc() string {
	return fmt.Sprintf("Generate a NEW image from a text description via the %q image backend (a ComfyUI / Automatic1111 / hosted diffusion endpoint declared as a connector) and attach it to your reply. USE ONLY when the user asks to CREATE, DRAW, MAKE, or GENERATE a fresh image through this specific backend. Not for finding or downloading existing images.", t.connector)
}

func (t *restImageTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"prompt":   {Type: "string", Description: "A detailed description of the image to generate."},
		"negative": {Type: "string", Description: "Optional: what to avoid in the image (negative prompt). Backends that don't support it ignore this."},
		"width":    {Type: "number", Description: "Optional: image width in pixels."},
		"height":   {Type: "number", Description: "Optional: image height in pixels."},
		"steps":    {Type: "number", Description: "Optional: number of diffusion steps."},
		"seed":     {Type: "number", Description: "Optional: seed for reproducibility (-1 or omit = random)."},
	}
}

func (t *restImageTool) Caps() []Capability   { return []Capability{CapNetwork} }
func (t *restImageTool) IsInternetTool() bool { return true }

func (t *restImageTool) Run(args map[string]any) (string, error) {
	return t.RunWithSession(args, nil)
}

// RunWithSession runs the declared request (submit, then optional poll), extracts
// the image, and — for base64 / fetched-bytes results — appends it to the session
// so it rides out as an attachment. Returns a SHORT reference so megabytes of
// image data never enter LLM context (the whole reason we read past the text cap
// via the pipe path below without ever handing the body back to the model).
func (t *restImageTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	c, ok := GetConnector(RootDB, t.connector)
	if !ok {
		return "", fmt.Errorf("image backend %q no longer exists", t.connector)
	}
	if !c.Approved {
		return "", fmt.Errorf("image backend %q is not approved — an admin enables it in Admin > Connectors", t.connector)
	}
	s, err := restImageHandler{}.parse(c)
	if err != nil {
		return "", err
	}
	prompt := strings.TrimSpace(stringFromArg(args["prompt"]))
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	negative := stringFromArg(args["negative"])
	if negative == "" {
		negative = s.DefaultNegative
	}
	out, err := s.generate(sess, restImageParams{
		prompt:   prompt,
		negative: negative,
		width:    intArgOr(args["width"], s.DefaultWidth),
		height:   intArgOr(args["height"], s.DefaultHeight),
		steps:    intArgOr(args["steps"], s.DefaultSteps),
		seed:     intArgOr(args["seed"], -1),
	})
	if err != nil {
		return "", err
	}
	// Deliver: a plain URL rides out as an IMAGE ref the frontend renders; inline
	// or fetched bytes attach to the session. The short ref keeps the payload out
	// of LLM context.
	if out.url != "" {
		return "IMAGE:" + out.url, nil
	}
	if sess != nil {
		sess.AppendImage(out.b64)
	}
	return "IMAGE:generated", nil
}

// restImageParams carries one generation request's inputs, decoupled from the
// tool-args map so the native image pipeline (which passes only a prompt + an
// aspect) can drive the same core.
type restImageParams struct {
	prompt   string
	negative string
	width    int
	height   int
	steps    int
	seed     int
}

// restImageOutcome is the raw result of a backend call: EITHER inline/fetched
// image bytes (b64) OR a plain image URL. How it's delivered — attach, render, or
// save to a file — is the caller's choice.
type restImageOutcome struct {
	b64 string
	url string
}

// generate runs the declared request (submit, then poll for async backends) and
// extracts the finished image. sess is threaded only into the governed dispatch
// (for its workspace context) and may be nil for the native pipeline.
func (s RestImageSpec) generate(sess *ToolSession, p restImageParams) (restImageOutcome, error) {
	var out restImageOutcome
	// Token map: strings are JSON-escaped (they land inside a JSON string literal
	// in the body template), numerics are inserted raw.
	fields := map[string]string{
		"prompt":   jsonInner(p.prompt),
		"negative": jsonInner(p.negative),
		"model":    jsonInner(s.DefaultModel),
		"width":    strconv.Itoa(p.width),
		"height":   strconv.Itoa(p.height),
		"steps":    strconv.Itoa(p.steps),
		"seed":     strconv.Itoa(p.seed),
	}
	cred := s.Credential
	if cred == "" {
		cred = "no_auth"
	}
	method := firstNonEmpty(s.SubmitMethod, "POST")
	body := substituteTokens(s.SubmitBody, fields)

	// Submit. Read via the pipe path for its higher byte cap — a base64 image
	// blows past the 256KB text cap — but the response never reaches the LLM.
	raw, err := Secure().DispatchToolCallForPipe(sess, cred, s.SubmitURL, method, body)
	if err != nil {
		return out, fmt.Errorf("image submit failed: %w", err)
	}
	status, jsonBody := parseHTTPDispatchResult(raw)
	if status != 0 && (status < 200 || status >= 300) {
		return out, fmt.Errorf("image backend returned HTTP %d: %s", status, truncateForError(jsonBody))
	}
	var submitNode any
	if err := json.Unmarshal([]byte(jsonBody), &submitNode); err != nil {
		return out, fmt.Errorf("image backend response was not JSON: %s", truncateForError(jsonBody))
	}

	// Synchronous backend: the image is already in the submit response.
	if s.PollURL == "" {
		return extractOutcome(submitNode, s.ImageB64Path, s.ImageURLPath, "", nil)
	}

	// Async backend: poll until ready, then extract from the poll response.
	id := restJSONString(submitNode, s.SubmitIDPath)
	if s.SubmitIDPath != "" && id == "" {
		return out, fmt.Errorf("image backend gave no job id at %q; response: %s", s.SubmitIDPath, truncateForError(jsonBody))
	}
	idTok := map[string]string{"id": id}
	pollURL := substituteTokens(s.PollURL, idTok)
	pollMethod := firstNonEmpty(s.PollMethod, "GET")
	readyPath := substituteTokens(s.PollReadyPath, idTok)

	interval := s.PollIntervalSecs
	if interval < 1 {
		interval = 2
	}
	maxSecs := s.PollMaxSecs
	if maxSecs <= 0 {
		maxSecs = 120
	}
	deadline := time.Now().Add(time.Duration(maxSecs) * time.Second)
	var pollNode any
	ready := false
	for {
		praw, perr := Secure().DispatchToolCallForPipe(sess, cred, pollURL, pollMethod, "")
		if perr == nil {
			_, pbody := parseHTTPDispatchResult(praw)
			if json.Unmarshal([]byte(pbody), &pollNode) == nil {
				if restJSONString(pollNode, readyPath) != "" {
					ready = true
					break
				}
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Duration(interval) * time.Second)
	}
	if !ready {
		return out, fmt.Errorf("image generation timed out after %ds waiting on %q", maxSecs, readyPath)
	}
	return extractOutcome(pollNode,
		substituteTokens(s.PollB64Path, idTok),
		substituteTokens(s.PollURLPath, idTok),
		s.PollURLTemplate, resolvePollFields(s.PollFields, pollNode, idTok))
}

// extractOutcome pulls the finished image out of node by the given locators:
// base64 → decode-verify + return bytes; a plain URL → return the URL; a URL
// built from template+fields → fetch it to bytes.
func extractOutcome(node any, b64Path, urlPath, urlTemplate string, tmplVars map[string]string) (restImageOutcome, error) {
	var out restImageOutcome
	if b64Path != "" {
		b64 := strings.TrimSpace(restJSONString(node, b64Path))
		if b64 == "" {
			return out, fmt.Errorf("no base64 image at %q in the backend response", b64Path)
		}
		b64 = stripDataURIPrefix(b64)
		if _, err := base64.StdEncoding.DecodeString(b64); err != nil {
			return out, fmt.Errorf("backend returned invalid base64 at %q: %w", b64Path, err)
		}
		out.b64 = b64
		return out, nil
	}
	if urlPath != "" {
		url := strings.TrimSpace(restJSONString(node, urlPath))
		if url == "" {
			return out, fmt.Errorf("no image URL at %q in the backend response", urlPath)
		}
		out.url = url
		return out, nil
	}
	if urlTemplate != "" {
		url := substituteTokens(urlTemplate, tmplVars)
		if strings.Contains(url, "{") {
			return out, fmt.Errorf("could not fill image URL template %q — a poll_fields dot-path resolved empty (got %q)", urlTemplate, url)
		}
		data, err := httpGetImageBytes(url)
		if err != nil {
			return out, fmt.Errorf("fetching generated image %s: %w", url, err)
		}
		out.b64 = base64.StdEncoding.EncodeToString(data)
		return out, nil
	}
	return out, fmt.Errorf("no image locator configured for this backend")
}

// generateRestImageNative bridges a rest_image connector into the native image
// pipeline (core/image_gen.go): resolve the connector live, map the native
// (prompt, landscape) request onto the spec's default dimensions, run the same
// generate core, and return the ImageGenResult shape the native providers do — a
// local file for inline/fetched bytes, or a URL passthrough.
func generateRestImageNative(connector, prompt string, landscape bool) (*ImageGenResult, error) {
	c, ok := GetConnector(RootDB, connector)
	if !ok || !c.Approved {
		return nil, fmt.Errorf("image backend %q is unavailable", connector)
	}
	s, err := restImageHandler{}.parse(c)
	if err != nil {
		return nil, err
	}
	w, h := resolveImageDims(landscape, s.DefaultWidth, s.DefaultHeight)
	out, err := s.generate(nil, restImageParams{
		prompt:   prompt,
		negative: s.DefaultNegative,
		width:    w,
		height:   h,
		steps:    s.DefaultSteps,
		seed:     -1,
	})
	if err != nil {
		return nil, err
	}
	if out.url != "" {
		return &ImageGenResult{URL: out.url, Prompt: prompt}, nil
	}
	data, err := base64.StdEncoding.DecodeString(out.b64)
	if err != nil {
		return nil, fmt.Errorf("backend returned invalid base64: %w", err)
	}
	path, err := writeImageTemp(data)
	if err != nil {
		return nil, err
	}
	return &ImageGenResult{URL: path, Prompt: prompt}, nil
}

// resolveImageDims maps the native aspect flag onto the spec's default size:
// 512² when unset; landscape orients wide (width ≥ height). Per the chosen
// "spec defaults + aspect only" mapping — the backend's configured size wins.
func resolveImageDims(landscape bool, defW, defH int) (int, int) {
	w, h := defW, defH
	if w <= 0 {
		w = 512
	}
	if h <= 0 {
		h = 512
	}
	if landscape && h > w {
		w, h = h, w
	}
	return w, h
}

// writeImageTemp saves image bytes to a PNG in ImageDir(), mirroring how the
// Gemini provider persists its inline result. Returns the file path.
func writeImageTemp(data []byte) (string, error) {
	dir := ImageDir()
	os.MkdirAll(dir, 0755)
	path := filepath.Join(dir, UUIDv4()+".png")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("saving generated image: %w", err)
	}
	return path, nil
}

// --- presets -----------------------------------------------------------------
//
// A preset fills the fiddly backend-specific fields so a Builder supplies only the
// credential + a couple of vars (base_url). An explicit spec field overrides its
// preset value — a preset is defaults, not a lock.

var restImagePresets = map[string]RestImageSpec{
	// Automatic1111 (stable-diffusion-webui) txt2img — fully turnkey. Var:
	// base_url (e.g. http://localhost:7860). Runs synchronously and returns
	// base64 PNGs in "images". Local install → credential "no_auth"; put it
	// behind a bearer credential if the API is exposed.
	"a1111": {
		SubmitURL:     "{base_url}/sdapi/v1/txt2img",
		SubmitMethod:  "POST",
		SubmitBody:    `{"prompt":"{prompt}","negative_prompt":"{negative}","width":{width},"height":{height},"steps":{steps},"seed":{seed}}`,
		ImageB64Path:  "images.0",
		DefaultWidth:  512,
		DefaultHeight: 512,
		DefaultSteps:  20,
	},

	// ComfyUI — STARTING TEMPLATE exercising the poll stage. Var: base_url (e.g.
	// http://localhost:8188). submit_body is a minimal SD1.5 txt2img graph; you
	// will likely need to edit it to match your installed checkpoint and node
	// ids. The poll paths assume the SaveImage node has id "9" (as in this
	// graph); if you re-wire the graph, update poll_ready_path / poll_fields to
	// your SaveImage node id. ComfyUI needs no auth by default → "no_auth".
	"comfyui": {
		SubmitURL:    "{base_url}/prompt",
		SubmitMethod: "POST",
		SubmitBody: `{"prompt":{` +
			`"3":{"class_type":"KSampler","inputs":{"seed":{seed},"steps":{steps},"cfg":7,"sampler_name":"euler","scheduler":"normal","denoise":1,"model":["4",0],"positive":["6",0],"negative":["7",0],"latent_image":["5",0]}},` +
			`"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"v1-5-pruned-emaonly.safetensors"}},` +
			`"5":{"class_type":"EmptyLatentImage","inputs":{"width":{width},"height":{height},"batch_size":1}},` +
			`"6":{"class_type":"CLIPTextEncode","inputs":{"text":"{prompt}","clip":["4",1]}},` +
			`"7":{"class_type":"CLIPTextEncode","inputs":{"text":"{negative}","clip":["4",1]}},` +
			`"8":{"class_type":"VAEDecode","inputs":{"samples":["3",0],"vae":["4",2]}},` +
			`"9":{"class_type":"SaveImage","inputs":{"filename_prefix":"gohort","images":["8",0]}}}}`,
		SubmitIDPath:    "prompt_id",
		PollURL:         "{base_url}/history/{id}",
		PollMethod:      "GET",
		PollReadyPath:   "{id}.outputs.9.images.0.filename",
		PollURLTemplate: "{base_url}/view?filename={filename}&subfolder={subfolder}&type={type}",
		PollFields: map[string]string{
			"filename":  "{id}.outputs.9.images.0.filename",
			"subfolder": "{id}.outputs.9.images.0.subfolder",
			"type":      "{id}.outputs.9.images.0.type",
		},
		PollIntervalSecs: 2,
		PollMaxSecs:      180,
		DefaultWidth:     512,
		DefaultHeight:    512,
		DefaultSteps:     20,
	},
}

// RestImagePreset returns a preset spec by name.
func RestImagePreset(name string) (RestImageSpec, bool) {
	p, ok := restImagePresets[strings.ToLower(strings.TrimSpace(name))]
	return p, ok
}

// RestImagePresetNames lists the available preset names.
func RestImagePresetNames() []string {
	out := make([]string, 0, len(restImagePresets))
	for k := range restImagePresets {
		out = append(out, k)
	}
	return out
}

// ApplyRestImagePreset overlays `over` (explicit args) onto the named preset (empty
// preset = no defaults), then substitutes {var} tokens (e.g. base_url) into the
// URL/body/path fields. Runtime tokens ({prompt}, {id}, {filename}, …) are left for
// later substitution — substituteTokens only replaces keys present in vars.
func ApplyRestImagePreset(preset string, over RestImageSpec, vars map[string]string) (RestImageSpec, error) {
	out := over
	if p := strings.TrimSpace(preset); p != "" {
		base, ok := RestImagePreset(p)
		if !ok {
			return out, fmt.Errorf("unknown rest_image preset %q (known: %s)", p, strings.Join(RestImagePresetNames(), ", "))
		}
		out = MergeRestImageSpec(base, over)
	}
	out.SubmitURL = substituteTokens(out.SubmitURL, vars)
	out.SubmitBody = substituteTokens(out.SubmitBody, vars)
	out.PollURL = substituteTokens(out.PollURL, vars)
	out.PollURLTemplate = substituteTokens(out.PollURLTemplate, vars)
	out.PollReadyPath = substituteTokens(out.PollReadyPath, vars)
	out.PollB64Path = substituteTokens(out.PollB64Path, vars)
	out.PollURLPath = substituteTokens(out.PollURLPath, vars)
	out.ImageB64Path = substituteTokens(out.ImageB64Path, vars)
	out.ImageURLPath = substituteTokens(out.ImageURLPath, vars)
	for k, v := range out.PollFields {
		out.PollFields[k] = substituteTokens(v, vars)
	}
	return out, nil
}

// MergeRestImageSpec returns `over` with any empty field filled from `base`.
// Explicit (`over`) values win. Used to apply a preset's defaults and to
// partial-patch a stored spec on update. An empty field means "keep base", so a
// field can't be cleared through this merge; re-author to clear.
func MergeRestImageSpec(base, over RestImageSpec) RestImageSpec {
	out := over
	fill := func(dst *string, src string) {
		if strings.TrimSpace(*dst) == "" {
			*dst = src
		}
	}
	fillI := func(dst *int, src int) {
		if *dst == 0 {
			*dst = src
		}
	}
	fill(&out.Credential, base.Credential)
	fill(&out.SubmitURL, base.SubmitURL)
	fill(&out.SubmitMethod, base.SubmitMethod)
	fill(&out.SubmitBody, base.SubmitBody)
	fill(&out.ImageB64Path, base.ImageB64Path)
	fill(&out.ImageURLPath, base.ImageURLPath)
	fill(&out.SubmitIDPath, base.SubmitIDPath)
	fill(&out.PollURL, base.PollURL)
	fill(&out.PollMethod, base.PollMethod)
	fill(&out.PollReadyPath, base.PollReadyPath)
	fill(&out.PollB64Path, base.PollB64Path)
	fill(&out.PollURLPath, base.PollURLPath)
	fill(&out.PollURLTemplate, base.PollURLTemplate)
	fill(&out.DefaultNegative, base.DefaultNegative)
	fill(&out.DefaultModel, base.DefaultModel)
	fillI(&out.PollIntervalSecs, base.PollIntervalSecs)
	fillI(&out.PollMaxSecs, base.PollMaxSecs)
	fillI(&out.DefaultWidth, base.DefaultWidth)
	fillI(&out.DefaultHeight, base.DefaultHeight)
	fillI(&out.DefaultSteps, base.DefaultSteps)
	if len(out.PollFields) == 0 && len(base.PollFields) > 0 {
		out.PollFields = map[string]string{}
		for k, v := range base.PollFields {
			out.PollFields[k] = v
		}
	}
	return out
}

// --- small helpers -----------------------------------------------------------

// resolvePollFields resolves each poll_fields dot-path (after {id} substitution)
// against the poll response, yielding the token map for poll_url_template.
func resolvePollFields(fields map[string]string, node any, idTok map[string]string) map[string]string {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]string, len(fields))
	for tok, path := range fields {
		out[tok] = restJSONString(node, substituteTokens(path, idTok))
	}
	return out
}

// restJSONString resolves a dot-path against a decoded JSON tree and coerces the
// value to its string form. Path segments are map keys or array indices; a key
// containing dots is matched literally first (for keys like Graph's
// "@odata.deltaLink" — harmless here, but keeps parity with the messaging mapper).
func restJSONString(node any, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	for {
		if path == "" {
			break
		}
		if m, ok := node.(map[string]any); ok {
			if v, ok := m[path]; ok {
				node = v
				break
			}
		}
		seg, rest := path, ""
		if i := strings.IndexByte(path, '.'); i >= 0 {
			seg, rest = path[:i], path[i+1:]
		}
		switch n := node.(type) {
		case map[string]any:
			v, ok := n[seg]
			if !ok {
				return ""
			}
			node = v
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(n) {
				return ""
			}
			node = n[idx]
		default:
			return ""
		}
		path = rest
	}
	switch v := node.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case json.Number:
		return v.String()
	case nil:
		return ""
	default:
		return ""
	}
}

// parseHTTPDispatchResult peels the "HTTP <status> …" header line that SecureAPI's
// dispatch prepends and drops a trailing truncation marker, leaving the body.
// Mirrors the bridges poller's parseDispatchResult (core can't import bridges).
func parseHTTPDispatchResult(s string) (int, string) {
	nl := strings.IndexByte(s, '\n')
	if nl < 0 {
		return 0, s
	}
	var status int
	fmt.Sscanf(s[:nl], "HTTP %d", &status)
	body := s[nl+1:]
	if i := strings.Index(body, "\n... [TRUNCATED"); i >= 0 {
		body = body[:i]
	}
	return status, body
}

// jsonInner JSON-escapes s for embedding inside a JSON string literal, WITHOUT the
// surrounding quotes (the body template already supplies them: "prompt":"{prompt}").
func jsonInner(s string) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return ""
	}
	return string(b[1 : len(b)-1])
}

// stripDataURIPrefix removes a leading data:image/...;base64, wrapper if present,
// leaving the bare base64 payload.
func stripDataURIPrefix(s string) string {
	if strings.HasPrefix(s, "data:image/") {
		if i := strings.Index(s, ";base64,"); i >= 0 {
			return s[i+len(";base64,"):]
		}
	}
	return s
}

// httpGetImageBytes fetches raw image bytes from a URL (ComfyUI /view or a
// direct image link), capped at the SecureAPI save-byte limit and the SecureAPI
// request timeout. Used only for the poll_url_template path, where the finished
// image is a separate binary endpoint the governed poll already located.
func httpGetImageBytes(url string) ([]byte, error) {
	client := &http.Client{Timeout: secureAPIRequestTimeout()}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, secureAPIMaxSaveBytes()))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty image body")
	}
	return data, nil
}

// truncateForError shortens a response body for an error message.
func truncateForError(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// firstNonEmpty returns a if non-blank, else b.
func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// stringFromArg coerces a tool arg to a trimmed string.
func stringFromArg(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// intArgOr coerces a numeric tool arg (JSON number or numeric string) to an int,
// falling back to def when absent or unparseable.
func intArgOr(v any, def int) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i
		}
	}
	return def
}
