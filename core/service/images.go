package service

// Image backends kept as service connections: a ComfyUI workflow (the comfyui
// builtin) and an A1111-shaped server (the a1111 builtin: Automatic1111 and the
// servers that speak its API, and a peer's renderer, which speaks it too). The
// operation's options say how the backend runs, and the machinery that runs
// it (writing values into a workflow's nodes, taking out photo inputs left
// empty, staging a blend of more photos than it takes, masks, the face pass,
// sizes and seeds) stays the Go that has always run it (core
// connector_restimage.go, comfyui_workflow.go). What the connection adds is
// what every connection has: its key kept with it for its address, the
// self-check, and who may reach it.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// The image builtins.
const (
	comfyBuiltin = "comfyui"
	a1111Builtin = "a1111"
)

// imageBuiltin reports whether name is an image builtin.
func imageBuiltin(name string) bool { return name == comfyBuiltin || name == a1111Builtin }

// comfyOptions are a comfyui operation's options: the workflow and its node
// map, and the rest of how the backend runs by the names a rest_image
// connector gave them.
type comfyOptions struct {
	Workflow         string       `json:"workflow"`
	Map              ComfyNodeMap `json:"map"`
	DefaultNegative  string       `json:"default_negative,omitempty"`
	DefaultWidth     int          `json:"default_width,omitempty"`
	DefaultHeight    int          `json:"default_height,omitempty"`
	DefaultSteps     int          `json:"default_steps,omitempty"`
	PromptSuffix     string       `json:"prompt_suffix,omitempty"`
	PromptGuidance   string       `json:"prompt_guidance,omitempty"`
	MaxInputImages   int          `json:"max_input_images,omitempty"`
	PollIntervalSecs int          `json:"poll_interval_secs,omitempty"`
	PollMaxSecs      int          `json:"poll_max_secs,omitempty"`
	// UploadURL is where source photos go when it is not <base>/upload/image.
	UploadURL string `json:"upload_url,omitempty"`
}

// a1111Options are an a1111 operation's options: its mode, txt2img (draw from
// a prompt) or img2img (change a photo), and the rest by the names a
// rest_image connector gave them. SubmitURL and SubmitBody are given only
// where the server is not A1111 itself (a peer's renderer, under a path of its
// own and naming which renderer); the mode's own are used otherwise.
type a1111Options struct {
	Mode            string `json:"mode"`
	SubmitURL       string `json:"submit_url,omitempty"`
	SubmitBody      string `json:"submit_body,omitempty"`
	DefaultNegative string `json:"default_negative,omitempty"`
	DefaultWidth    int    `json:"default_width,omitempty"`
	DefaultHeight   int    `json:"default_height,omitempty"`
	DefaultSteps    int    `json:"default_steps,omitempty"`
	PromptSuffix    string `json:"prompt_suffix,omitempty"`
	PromptGuidance  string `json:"prompt_guidance,omitempty"`
	MaxInputImages  int    `json:"max_input_images,omitempty"`
	PollMaxSecs     int    `json:"poll_max_secs,omitempty"`
}

// a1111Presets are the presets each mode starts from.
var a1111Presets = map[string]string{"txt2img": "a1111", "img2img": "a1111_img2img"}

// decodeOptions reads options into out, refusing a name it does not take.
func decodeOptions(builtin string, options map[string]any, out any) error {
	raw, err := json.Marshal(options)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("its %s options: %w", builtin, err)
	}
	return nil
}

// imageRestSpec is the image backend at base that operation op describes, as
// the machinery runs it.
func imageRestSpec(base string, op Operation) (RestImageSpec, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	switch op.Builtin {
	case comfyBuiltin:
		var o comfyOptions
		if err := decodeOptions(comfyBuiltin, op.Options, &o); err != nil {
			return RestImageSpec{}, err
		}
		s, err := ApplyRestImagePreset("comfyui", RestImageSpec{}, map[string]string{"base_url": base})
		if err != nil {
			return s, err
		}
		s.ComfyWorkflow, s.ComfyMap = o.Workflow, o.Map
		s.SubmitBody, s.PollReadyPath, s.PollFields = "", "", nil
		s.DefaultNegative, s.DefaultWidth, s.DefaultHeight, s.DefaultSteps = o.DefaultNegative, o.DefaultWidth, o.DefaultHeight, o.DefaultSteps
		s.PromptSuffix, s.PromptGuidance, s.MaxInputImages = o.PromptSuffix, o.PromptGuidance, o.MaxInputImages
		s.PollIntervalSecs, s.PollMaxSecs = o.PollIntervalSecs, o.PollMaxSecs
		if u := strings.TrimSpace(o.UploadURL); u != "" {
			s.UploadURL = u
		}
		return s, nil
	case a1111Builtin:
		var o a1111Options
		if err := decodeOptions(a1111Builtin, op.Options, &o); err != nil {
			return RestImageSpec{}, err
		}
		preset, ok := a1111Presets[o.Mode]
		if !ok {
			return RestImageSpec{}, fmt.Errorf("its a1111 mode %q: it is txt2img or img2img", o.Mode)
		}
		s, err := ApplyRestImagePreset(preset, RestImageSpec{}, map[string]string{"base_url": base})
		if err != nil {
			return s, err
		}
		if u := strings.TrimSpace(o.SubmitURL); u != "" {
			if strings.HasPrefix(u, "/") {
				u = base + u
			}
			s.SubmitURL = u
		}
		if b := strings.TrimSpace(o.SubmitBody); b != "" {
			s.SubmitBody = b
		}
		if o.DefaultWidth > 0 {
			s.DefaultWidth = o.DefaultWidth
		}
		if o.DefaultHeight > 0 {
			s.DefaultHeight = o.DefaultHeight
		}
		if o.DefaultSteps > 0 {
			s.DefaultSteps = o.DefaultSteps
		}
		s.DefaultNegative, s.PromptSuffix, s.PromptGuidance = o.DefaultNegative, o.PromptSuffix, o.PromptGuidance
		s.MaxInputImages, s.PollMaxSecs = o.MaxInputImages, o.PollMaxSecs
		return s, nil
	}
	return RestImageSpec{}, fmt.Errorf("%q is not an image builtin", op.Builtin)
}

// validateImage checks an image builtin's operation as a rest_image spec was
// checked: what its options name, and that the backend they describe can run.
func validateImage(base, name string, op Operation) error {
	if op.Method != "" || op.Path != "" || op.URL != "" || op.Send != "" || op.Body != nil || op.Read != "" || len(op.Run) > 0 {
		return fmt.Errorf("operation %q runs an image backend, which makes its own requests: it has no method, path, url, send, body, read or run", name)
	}
	s, err := imageRestSpec(base, op)
	if err != nil {
		return fmt.Errorf("operation %q: %w", name, err)
	}
	if op.Builtin == comfyBuiltin && strings.TrimSpace(s.ComfyWorkflow) == "" {
		return fmt.Errorf("operation %q: its options need the workflow (ComfyUI's Save (API Format) export)", name)
	}
	if err := s.Validate(); err != nil {
		return fmt.Errorf("operation %q: %w", name, err)
	}
	return nil
}

// imageOperation is the spec's image builtin operation, when it has one.
func imageOperation(s Spec) (Operation, bool) {
	for _, op := range s.Provider.Operations {
		if imageBuiltin(op.Builtin) {
			return op, true
		}
	}
	return Operation{}, false
}

// renderImage runs the backend for one call: the call's prompt and sizes, and
// its source picture when it gives one (a Test of an editing backend).
func (t transport) renderImage(name string, op Operation, vars map[string]any) (reply, error) {
	s, err := imageRestSpec(t.p.BaseURL, op)
	if err != nil {
		return reply{}, fmt.Errorf("%s: %w", name, err)
	}
	cred, err := t.connection()
	if err != nil {
		return reply{}, err
	}
	if cred != nil {
		s = s.Via(*cred)
	}
	var images [][]byte
	if f, err := fileVar(vars, "image"); err == nil {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return reply{}, fmt.Errorf("%s: %w", name, err)
		}
		images = append(images, data)
	}
	res, err := s.Render(t.sess, vars, images)
	if err != nil {
		return reply{}, fmt.Errorf("%s: %w", name, err)
	}
	f, err := fileVar(map[string]any{"picture": res.URL}, "picture")
	if err != nil {
		return reply{}, fmt.Errorf("%s: the picture did not come back as a file", name)
	}
	return reply{status: 200, file: f}, nil
}

// ImageSpec is connection c's image backend as the image machinery runs it
// (core connector_restimage.go), its calls going through c's key, and false
// when c runs none.
func (handler) ImageSpec(c Connector) (RestImageSpec, bool, error) {
	s, err := Parse(c.Spec)
	if err != nil {
		return RestImageSpec{}, false, nil
	}
	if _, ok := imageOperation(s); !ok {
		return RestImageSpec{}, false, nil
	}
	// A change waiting on it holds it, as it holds every call (tieins.go):
	// until it is accepted the backend is not served.
	if held(c.Name) {
		return RestImageSpec{}, true, fmt.Errorf("image backend %q has a change waiting: an admin accepts it by saving a page that uses it", c.Name)
	}
	settled, err := settledFor(context.Background(), c.Name, s)
	if err != nil {
		return RestImageSpec{}, true, err
	}
	op, _ := imageOperation(settled)
	rest, err := imageRestSpec(settled.Provider.BaseURL, op)
	if err != nil {
		return rest, true, err
	}
	cred, err := settled.transport(nil).connection()
	if err != nil {
		return rest, true, err
	}
	if cred != nil {
		rest = rest.Via(*cred)
	}
	return rest, true, nil
}

// ImageConnector is the spec of a connection keeping image backend r, its key
// sent as send ("" for the backend's usual way).
func (handler) ImageConnector(r RestImageSpec, send string) (json.RawMessage, error) {
	var s Spec
	var err error
	if strings.TrimSpace(r.ComfyWorkflow) != "" {
		s, err = comfyService(r, send)
	} else {
		s, err = a1111Service(r, send)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

// imageService is the service spec keeping a backend at base run by builtin
// with options: a key it may take, sent as send, and image.generate, or
// image.edit when it takes photos.
func imageService(base, builtin string, options any, edits bool, send, keyLabel, keyHelp string) (Spec, error) {
	raw, err := json.Marshal(options)
	if err != nil {
		return Spec{}, err
	}
	var opts map[string]any
	if err := json.Unmarshal(raw, &opts); err != nil {
		return Spec{}, err
	}
	capability := "image.generate"
	if edits {
		capability = "image.edit"
	}
	return Spec{
		Config: []ConfigField{{Name: "api_key", Label: keyLabel, Type: "secret", Send: send, Help: keyHelp}},
		Provider: Provider{Transport: "http", BaseURL: base, Auth: Auth{Secret: "api_key"},
			Operations: map[string]Operation{"render": {Builtin: builtin, Options: opts}}},
		Provides: []Binding{{Capability: capability, Steps: []Step{{Call: "render"}},
			Output: map[string]Output{"image": {From: "body"}}}},
	}, nil
}

// comfyService is the service spec keeping ComfyUI backend r: its address, a
// key it may take (sent as send, bearer by default; a local ComfyUI takes
// none), and the workflow.
func comfyService(r RestImageSpec, send string) (Spec, error) {
	base := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(r.SubmitURL), "/"), "/prompt")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return Spec{}, fmt.Errorf("its ComfyUI address %q is not an http(s) address", r.SubmitURL)
	}
	if strings.TrimSpace(r.ComfyWorkflow) == "" {
		return Spec{}, fmt.Errorf("it runs no ComfyUI workflow")
	}
	o := comfyOptions{Workflow: r.ComfyWorkflow, Map: r.ComfyMap, DefaultNegative: r.DefaultNegative,
		DefaultWidth: r.DefaultWidth, DefaultHeight: r.DefaultHeight, DefaultSteps: r.DefaultSteps,
		PromptSuffix: r.PromptSuffix, PromptGuidance: r.PromptGuidance, MaxInputImages: r.MaxInputImages,
		PollIntervalSecs: r.PollIntervalSecs, PollMaxSecs: r.PollMaxSecs}
	if u := strings.TrimSpace(r.UploadURL); u != "" && u != base+"/upload/image" {
		o.UploadURL = u
	}
	if send == "" {
		send = "bearer"
	}
	return imageService(base, comfyBuiltin, o, r.SupportsImageInput(), send, "API key",
		"Only for a ComfyUI that asks for one: a local server takes none.")
}

// a1111Service is the service spec keeping A1111-shaped backend r: its
// address, a key it may take (sent as send; A1111's own --api-auth login is
// user:password, sent as basic), its mode, and its own path and request only
// where they are not the mode's.
func a1111Service(r RestImageSpec, send string) (Spec, error) {
	u, err := url.Parse(strings.TrimSpace(r.SubmitURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return Spec{}, fmt.Errorf("its address %q is not an http(s) address", r.SubmitURL)
	}
	mode := "txt2img"
	if r.SupportsImageInput() {
		mode = "img2img"
	}
	preset, _ := ApplyRestImagePreset(a1111Presets[mode], RestImageSpec{}, map[string]string{"base_url": ""})
	// The builtin reads the picture where an A1111 server answers with it;
	// a backend that is polled for it, or answers elsewhere, is not one.
	if r.SubmitIDPath != "" || r.PollURL != "" || r.ImageURLPath != "" || r.ImageB64Path != preset.ImageB64Path {
		return Spec{}, fmt.Errorf("it reads its picture otherwise than an A1111 server does, which no builtin runs")
	}
	base := u.Scheme + "://" + u.Host
	o := a1111Options{Mode: mode, DefaultNegative: r.DefaultNegative, PromptSuffix: r.PromptSuffix,
		PromptGuidance: r.PromptGuidance, MaxInputImages: r.MaxInputImages, PollMaxSecs: r.PollMaxSecs}
	// The mode's own path under a path of its own is the server's address
	// with that path; anything else is the backend's own path.
	if prefix, ok := strings.CutSuffix(u.Path, preset.SubmitURL); ok {
		base += strings.TrimRight(prefix, "/")
	} else {
		o.SubmitURL = u.Path
	}
	if r.SubmitBody != preset.SubmitBody {
		o.SubmitBody = r.SubmitBody
	}
	if r.DefaultWidth != preset.DefaultWidth {
		o.DefaultWidth = r.DefaultWidth
	}
	if r.DefaultHeight != preset.DefaultHeight {
		o.DefaultHeight = r.DefaultHeight
	}
	if r.DefaultSteps != preset.DefaultSteps {
		o.DefaultSteps = r.DefaultSteps
	}
	if send == "" {
		send = "basic"
	}
	return imageService(base, a1111Builtin, o, r.SupportsImageInput(), send, "API login",
		"Only for a server started with --api-auth: its user:password. A local one takes none.")
}
