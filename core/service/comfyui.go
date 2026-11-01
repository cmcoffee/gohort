package service

// A ComfyUI backend kept as a service connection. Its operation is the
// comfyui builtin: the workflow and its node map are the operation's options,
// and the machinery that runs a workflow (writing values into its nodes,
// taking out photo inputs left empty, staging a blend of more photos than it
// takes, masks, the face pass) stays the Go that has always run it (core
// connector_restimage.go, comfyui_workflow.go). What the connection adds is
// what every connection has: its key kept with it for its address, the
// self-check, and who may reach it.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// comfyBuiltin names the builtin that runs a ComfyUI workflow.
const comfyBuiltin = "comfyui"

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

// comfyRestSpec is the ComfyUI backend at base that options describe, as
// the machinery runs it: the comfyui preset's endpoints, and the workflow
// wired by its map.
func comfyRestSpec(base string, options map[string]any) (RestImageSpec, error) {
	raw, err := json.Marshal(options)
	if err != nil {
		return RestImageSpec{}, err
	}
	var o comfyOptions
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return RestImageSpec{}, fmt.Errorf("its comfyui options: %w", err)
	}
	s, err := ApplyRestImagePreset("comfyui", RestImageSpec{}, map[string]string{"base_url": strings.TrimRight(strings.TrimSpace(base), "/")})
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
}

// validateComfy checks a comfyui operation as a rest_image connector's spec
// was checked: a workflow, the node its picture is read from, a prompt or a
// photo node, and photo inputs it can write.
func validateComfy(base, name string, op Operation) error {
	if op.Method != "" || op.Path != "" || op.URL != "" || op.Send != "" || op.Body != nil || op.Read != "" || len(op.Run) > 0 {
		return fmt.Errorf("operation %q runs a ComfyUI workflow, which makes its own requests: it has no method, path, url, send, body, read or run", name)
	}
	s, err := comfyRestSpec(base, op.Options)
	if err != nil {
		return fmt.Errorf("operation %q: %w", name, err)
	}
	if strings.TrimSpace(s.ComfyWorkflow) == "" {
		return fmt.Errorf("operation %q: its options need the workflow (ComfyUI's Save (API Format) export)", name)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := ValidateConnector(Connector{Kind: RestImageConnectorKind, Spec: raw}); err != nil {
		return fmt.Errorf("operation %q: %w", name, err)
	}
	return nil
}

// comfyOperation is the spec's comfyui operation, when it has one.
func comfyOperation(s Spec) (Operation, bool) {
	for _, op := range s.Provider.Operations {
		if op.Builtin == comfyBuiltin {
			return op, true
		}
	}
	return Operation{}, false
}

// comfy runs the workflow for one call: the call's prompt and sizes, and its
// source picture when it gives one (a Test of an editing backend).
func (t transport) comfy(name string, op Operation, vars map[string]any) (reply, error) {
	s, err := comfyRestSpec(t.p.BaseURL, op.Options)
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

// ComfySpec is connection c's ComfyUI backend as the image machinery runs it
// (core connector_restimage.go), its calls going through c's key, and false
// when c runs no ComfyUI workflow.
func (handler) ComfySpec(c Connector) (RestImageSpec, bool, error) {
	s, err := Parse(c.Spec)
	if err != nil {
		return RestImageSpec{}, false, nil
	}
	if _, ok := comfyOperation(s); !ok {
		return RestImageSpec{}, false, nil
	}
	settled, err := settledFor(context.Background(), c.Name, s)
	if err != nil {
		return RestImageSpec{}, true, err
	}
	op, _ := comfyOperation(settled)
	rest, err := comfyRestSpec(settled.Provider.BaseURL, op.Options)
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

// ComfyConnector is the spec of a connection keeping ComfyUI backend r.
func (handler) ComfyConnector(r RestImageSpec) (json.RawMessage, error) {
	s, err := comfyService(r, "")
	if err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

// comfyService is the service spec keeping ComfyUI backend r: its address,
// a key it may take (sent as send, bearer by default; a local ComfyUI takes
// none), and the workflow as an image.generate, or an image.edit when it
// takes photos.
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
	raw, err := json.Marshal(o)
	if err != nil {
		return Spec{}, err
	}
	var options map[string]any
	if err := json.Unmarshal(raw, &options); err != nil {
		return Spec{}, err
	}
	capability := "image.generate"
	if r.SupportsImageInput() {
		capability = "image.edit"
	}
	if send == "" {
		send = "bearer"
	}
	return Spec{
		Config: []ConfigField{{Name: "api_key", Label: "API key", Type: "secret", Send: send,
			Help: "Only for a ComfyUI that asks for one: a local server takes none."}},
		Provider: Provider{Transport: "http", BaseURL: base, Auth: Auth{Secret: "api_key"},
			Operations: map[string]Operation{"render": {Builtin: comfyBuiltin, Options: options}}},
		Provides: []Binding{{Capability: capability, Steps: []Step{{Call: "render"}},
			Output: map[string]Output{"image": {From: "body"}}}},
	}, nil
}
