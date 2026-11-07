package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/recipes"
)

// comfyStub is a ComfyUI server: /prompt queues a graph, /history says it is
// done with its picture on the output node, /view serves the picture, and
// /upload/image takes a source photo. It records what each request carried.
type comfyStub struct {
	mu      sync.Mutex
	output  string
	seen    []string // path, Authorization, X-Api-Key
	graphs  []map[string]any
	uploads int
}

func (s *comfyStub) serve(t *testing.T) *httptest.Server {
	t.Helper()
	pic := pngBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.seen = append(s.seen, r.URL.Path+" "+r.Header.Get("Authorization")+" "+r.Header.Get("X-Api-Key"))
		switch r.URL.Path {
		case "/prompt":
			var body struct {
				Prompt map[string]any `json:"prompt"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			s.graphs = append(s.graphs, body.Prompt)
			w.Write([]byte(`{"prompt_id":"p1"}`))
		case "/history/p1":
			fmt.Fprintf(w, `{"p1":{"outputs":{%q:{"images":[{"filename":"out.png","subfolder":"","type":"output"}]}}}}`, s.output)
		case "/view":
			w.Header().Set("Content-Type", "image/png")
			w.Write(pic)
		case "/upload/image":
			s.uploads++
			fmt.Fprintf(w, `{"name":"in-%d.png","subfolder":""}`, s.uploads)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// comfyConnection keeps the ComfyUI workflow (blank: the default text graph)
// at srv as service connection name, in use.
func comfyConnection(t *testing.T, srv *httptest.Server, stub *comfyStub, name, workflow string) Connector {
	t.Helper()
	r, _, err := NewComfyImageSpec(srv.URL, "", workflow, "")
	if err != nil {
		t.Fatal(err)
	}
	stub.output = r.ComfyMap.OutputNode
	kind, raw, err := r.AsService("")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveConnector(RootDB, Connector{Name: name, Kind: kind, Template: "comfyui", Spec: raw}); err != nil {
		t.Fatal(err)
	}
	c, _ := GetConnector(RootDB, name)
	return c
}

// A ComfyUI workflow kept as a connection draws as the image machinery does:
// the prompt written into its graph, the picture read off its output node.
// Its key goes on every request, the picture's fetch too; one with no key
// sends none.
func TestAComfyUIConnectionDraws(t *testing.T) {
	world(t)
	stub := &comfyStub{}
	srv := stub.serve(t)
	c := comfyConnection(t, srv, stub, "studio", "")
	if c.Kind != Kind {
		t.Fatalf("kept as %s", c.Kind)
	}
	SetConnectionSecret(RootDB, c, "api_key", "k-comfy", []string{srv.URL})
	res, err := GenerateImageWithBackend(context.Background(), "studio", "a red fox", false)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(res.URL)
	if data, err := os.ReadFile(res.URL); err != nil || len(data) == 0 {
		t.Fatalf("no picture at %s: %v", res.URL, err)
	}
	graph, _ := json.Marshal(stub.graphs)
	if !strings.Contains(string(graph), "a red fox") {
		t.Errorf("the prompt is not in the graph: %s", graph)
	}
	for _, want := range []string{"/prompt Bearer k-comfy", "/history/p1 Bearer k-comfy", "/view Bearer k-comfy"} {
		if !strings.Contains(strings.Join(stub.seen, "\n"), want) {
			t.Errorf("no %q in %v", want, stub.seen)
		}
	}

	stub.seen = nil
	comfyConnection(t, srv, stub, "studio_open", "")
	if _, err := GenerateImageWithBackend(context.Background(), "studio_open", "a blue fox", false); err != nil {
		t.Fatal(err)
	}
	for _, s := range stub.seen {
		if strings.Contains(s, "Bearer") {
			t.Errorf("a connection with no key sent one: %v", stub.seen)
		}
	}

	// Another saved leaves the first served. The tool an agent was granted
	// before the move draws through it only while Image Generation offers
	// it: the grant alone is no reference.
	if _, err := GenerateImageWithBackend(context.Background(), "studio", "a fox again", false); err != nil {
		t.Errorf("after another was saved: %v", err)
	}
	tool, ok := LookupChatTool("generate_image_studio")
	if !ok {
		t.Fatal("no generate_image_studio tool")
	}
	run, _ := tool.(interface {
		RunWithSession(map[string]any, *ToolSession) (string, error)
	})
	if out, err := run.RunWithSession(map[string]any{"prompt": "a green fox"}, &ToolSession{}); err == nil || !strings.Contains(err.Error(), "not offered") {
		t.Errorf("its tool, not offered: %q %v", out, err)
	}
	RootDB.Set(ImageTable, "agents", []string{"studio"})
	if out, err := run.RunWithSession(map[string]any{"prompt": "a green fox"}, &ToolSession{}); err != nil || !strings.Contains(out, "generated") {
		t.Errorf("its tool: %q %v", out, err)
	}
	// Held by a change, it draws no more, by name or by its tool.
	MarkChanged(RootDB, "studio", "someone")
	if _, err := GenerateImageWithBackend(context.Background(), "studio", "a fox", false); err == nil || !strings.Contains(err.Error(), "change waiting") {
		t.Errorf("a held connection still draws: %v", err)
	}
	if _, err := run.RunWithSession(map[string]any{"prompt": "a fox"}, &ToolSession{}); err == nil {
		t.Error("a held connection still draws through its tool")
	}
}

// A blend workflow kept as a connection takes two photos as the image
// machinery does: each uploaded, each written into its own photo node. It is
// offered to agents as an editing backend of two, unless the agent is barred
// from the connection.
func TestAComfyUIConnectionBlends(t *testing.T) {
	world(t)
	stub := &comfyStub{}
	srv := stub.serve(t)
	blender := comfyConnection(t, srv, stub, "blender", ComfyBlendDefaultGraph())
	SetConnectionSecret(RootDB, blender, "api_key", "k-blend", []string{srv.URL})
	RootDB.Set(ImageTable, "agents", []string{"blender"})
	dir := t.TempDir()
	for _, f := range []string{"a.png", "b.png"} {
		os.WriteFile(filepath.Join(dir, f), pngBytes(t), 0644)
	}
	sess := &ToolSession{WorkspaceDir: dir}
	var offered *ImageBackendChoice
	for _, b := range ReachableImageBackends(sess) {
		if b.Name == "blender" {
			offered = &b
		}
	}
	if offered == nil || !offered.Edits || offered.MaxImages != 2 {
		t.Fatalf("offered to agents as: %+v", offered)
	}
	barred := &ToolSession{WorkspaceDir: dir, DeniedCredentials: map[string]bool{"connection:blender": true}}
	for _, b := range ReachableImageBackends(barred) {
		if b.Name == "blender" {
			t.Errorf("offered to an agent barred from it")
		}
	}

	res, err := EditImageWithBackend(sess, EditImageRequest{Backend: "blender", Prompt: "blend them", Images: []string{"a.png", "b.png"}})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(res.URL)
	if stub.uploads != 2 {
		t.Errorf("%d photos uploaded, want 2", stub.uploads)
	}
	graph, _ := json.Marshal(stub.graphs)
	if !strings.Contains(string(graph), "in-1.png") || !strings.Contains(string(graph), "in-2.png") {
		t.Errorf("the photos are not in the graph: %s", graph)
	}
	if !strings.Contains(strings.Join(stub.seen, "\n"), "/upload/image Bearer k-blend") {
		t.Errorf("the photos went up without its key: %v", stub.seen)
	}

	// A call through the connection (its Test) sends the picture it gives.
	before := stub.uploads
	f := &File{Path: filepath.Join(dir, "a.png"), Type: "image/png"}
	if _, err := Call(context.Background(), nil, "blender", "image.edit", map[string]any{"prompt": "blend", "image": f}); err != nil {
		t.Fatal(err)
	}
	if stub.uploads != before+1 {
		t.Errorf("%d photos went up for one call, want 1", stub.uploads-before)
	}
}

// The ComfyUI template makes a connection, and reads one back into its form
// as it was made.
func TestTheComfyUITemplateMakesAConnection(t *testing.T) {
	world(t)
	tpl, ok := GetConnectorTemplate("comfyui")
	if !ok || tpl.Kind != Kind {
		t.Fatalf("the template keeps it as %q", tpl.Kind)
	}
	raw, _, err := tpl.BuildSpec(map[string]any{"base_url": "http://comfy.example:8188", "prompt_suffix": "crisp", "default_width": 768})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse(raw)
	if err == nil {
		err = s.Validate()
	}
	if err != nil {
		t.Fatal(err)
	}
	vals := tpl.ReadValues(raw)
	if vals["base_url"] != "http://comfy.example:8188" || vals["prompt_suffix"] != "crisp" || vals["default_width"] != 768 ||
		vals["output_node"] == "" || vals["workflow"] == "" {
		t.Errorf("read back: %v", vals)
	}
	again, _, err := tpl.BuildSpec(vals)
	if err != nil || string(again) != string(raw) {
		t.Errorf("built again from what it read: %v\n%s\nwas\n%s", err, again, raw)
	}
}

// A comfyui operation is checked as the image machinery checks a backend: what its options
// name, a workflow, the node its picture comes from, and what it gives. A
// built-in search takes no options.
func TestAComfyUIOperationIsChecked(t *testing.T) {
	world(t)
	r, _, err := NewComfyImageSpec("http://comfy.example:8188", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want string
		change     func(*Spec)
	}{
		{"as made", "", func(*Spec) {}},
		{"a misspelt option", "unknown field", func(s *Spec) { s.Provider.Operations["render"].Options["prompt_sufix"] = "x" }},
		{"no workflow", "need the workflow", func(s *Spec) { s.Provider.Operations["render"].Options["workflow"] = "" }},
		{"no output node", "output_node is required", func(s *Spec) {
			m := s.Provider.Operations["render"].Options["map"].(map[string]any)
			delete(m, "output_node")
		}},
		{"a request of its own", "makes its own requests", func(s *Spec) {
			op := s.Provider.Operations["render"]
			op.Path = "/prompt"
			s.Provider.Operations["render"] = op
		}},
		{"giving a search", "image.generate or image.edit", func(s *Spec) { s.Provides[0].Capability = "web.search" }},
	} {
		s, err := comfyService(r, "")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(s)
		s, _ = Parse(raw) // options as stored: plain JSON
		tc.change(&s)
		err = s.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
	search := Spec{Provider: Provider{Transport: "http", BaseURL: "https://search.example", Auth: Auth{None: true},
		Operations: map[string]Operation{"find": {Builtin: "pubmed", Options: map[string]any{"x": 1}}}},
		Provides: []Binding{{Capability: "web.search", Steps: []Step{{Call: "find"}},
			Output: map[string]Output{"results": {Path: "results", Fields: map[string]string{"title": "title", "url": "url"}}}}}}
	if err := search.Validate(); err == nil || !strings.Contains(err.Error(), "takes no options") {
		t.Errorf("a built-in search with options: %v", err)
	}
}

// The ComfyUI starter wires the default graph into a connection that lands
// out of use, pointed at the address given; a workflow that cannot be read
// is refused before anything lands.
func TestTheComfyUIRecipeAddsAWiredConnection(t *testing.T) {
	world(t)
	res, err := recipes.Install(RootDB, "comfyui", "admin", map[string]string{"base_url": "http://192.168.1.20:8188/"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 1 {
		t.Fatalf("one connection lands: %+v", res)
	}
	c, ok := GetConnector(RootDB, "comfyui")
	if !ok || c.Kind != Kind || c.Template != "comfyui" || c.Approved {
		t.Fatalf("a comfyui connection, out of use: %+v", c)
	}
	spec, comfy, err := handler{}.ImageSpec(c)
	if err != nil || !comfy {
		t.Fatalf("runs a ComfyUI workflow: %v %v", comfy, err)
	}
	if !strings.HasPrefix(spec.PollURLTemplate, "http://192.168.1.20:8188/view?") || spec.ComfyMap.OutputNode != "9" || len(spec.ComfyMap.PromptNodes) == 0 {
		t.Errorf("the workflow is wired to the server: %+v", spec)
	}
	if _, err := recipes.Install(RootDB, "comfyui", "admin", map[string]string{"base_url": "http://box:8188", "name": "comfy2", "workflow": "{not json"}); err == nil {
		t.Error("a workflow that cannot be read is refused before anything lands")
	}
}

// a1111Stub is an A1111-shaped server: it records each request's path,
// Authorization and peer key, and the body it carried, and answers with a
// picture inline.
type a1111Stub struct {
	mu     sync.Mutex
	seen   []string
	bodies []map[string]any
}

func (s *a1111Stub) serve(t *testing.T) *httptest.Server {
	t.Helper()
	pic := b64(pngBytes(t))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.seen = append(s.seen, r.URL.Path+" "+r.Header.Get("Authorization")+" "+r.Header.Get("X-Gohort-Peer-Key"))
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		s.bodies = append(s.bodies, body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"images":[%q]}`, pic)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// a1111Connection is what template tpl makes from values, kept as service
// connection name, in use.
func a1111Connection(t *testing.T, tpl, name string, values map[string]any) Connector {
	t.Helper()
	tp, ok := GetConnectorTemplate(tpl)
	if !ok || tp.Kind != Kind {
		t.Fatalf("the %s template keeps it as %q", tpl, tp.Kind)
	}
	raw, _, err := tp.BuildSpec(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveConnector(RootDB, Connector{Name: name, Kind: Kind, Template: tpl, Spec: raw}); err != nil {
		t.Fatal(err)
	}
	c, _ := GetConnector(RootDB, name)
	return c
}

// An A1111 server kept as a connection draws from its txt2img endpoint with
// the prompt suffix on the prompt and the mode's sizes, its --api-auth login
// sent as basic; one made for img2img edits a photo sent inline. Each reads
// back into its template's form as it was made, and stores only its mode and
// what differs from it.
func TestAnA1111ConnectionDrawsAndEdits(t *testing.T) {
	world(t)
	stub := &a1111Stub{}
	srv := stub.serve(t)
	c := a1111Connection(t, "a1111", "sd", map[string]any{"base_url": srv.URL, "prompt_suffix": "crisp"})
	if strings.Contains(string(c.Spec), "submit_url") || strings.Contains(string(c.Spec), "submit_body") || !strings.Contains(string(c.Spec), `"mode":"txt2img"`) {
		t.Errorf("stores more than its mode: %s", c.Spec)
	}
	SetConnectionSecret(RootDB, c, "api_key", "me:pw", []string{srv.URL})
	res, err := GenerateImageWithBackend(context.Background(), "sd", "a red fox", false)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(res.URL)
	if len(stub.seen) != 1 || stub.seen[0] != "/sdapi/v1/txt2img Basic bWU6cHc= " {
		t.Errorf("requests: %q", stub.seen)
	}
	if p, _ := stub.bodies[0]["prompt"].(string); !strings.Contains(p, "a red fox") || !strings.Contains(p, "crisp") {
		t.Errorf("prompt sent: %q", p)
	}
	if stub.bodies[0]["steps"] != float64(20) {
		t.Errorf("steps sent: %v", stub.bodies[0]["steps"])
	}
	// A call through the connection (its Test) draws the same way.
	stub.seen = nil
	if _, err := Call(context.Background(), nil, "sd", "image.generate", map[string]any{"prompt": "a grey fox"}); err != nil {
		t.Fatal(err)
	}
	if len(stub.seen) != 1 || stub.seen[0] != "/sdapi/v1/txt2img Basic bWU6cHc= " {
		t.Errorf("a call through the connection: %q", stub.seen)
	}
	tpl, _ := GetConnectorTemplate("a1111")
	vals := tpl.ReadValues(c.Spec)
	if vals["base_url"] != srv.URL || vals["prompt_suffix"] != "crisp" {
		t.Errorf("read back: %v", vals)
	}
	if again, _, err := tpl.BuildSpec(vals); err != nil || string(again) != string(c.Spec) {
		t.Errorf("built again from what it read: %v\n%s\nwas\n%s", err, again, c.Spec)
	}

	edit := a1111Connection(t, "a1111_img2img", "sd_edit", map[string]any{"base_url": srv.URL})
	if !strings.Contains(string(edit.Spec), `"mode":"img2img"`) || !strings.Contains(string(edit.Spec), "image.edit") {
		t.Errorf("an img2img connection: %s", edit.Spec)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.png"), pngBytes(t), 0644)
	stub.seen, stub.bodies = nil, nil
	out, err := EditImageWithBackend(&ToolSession{WorkspaceDir: dir}, EditImageRequest{Backend: "sd_edit", Prompt: "make it blue", Images: []string{"a.png"}})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(out.URL)
	if len(stub.seen) != 1 || !strings.HasPrefix(stub.seen[0], "/sdapi/v1/img2img  ") {
		t.Errorf("requests: %q (an edit with no key sends none)", stub.seen)
	}
	if imgs, _ := stub.bodies[0]["init_images"].([]any); len(imgs) != 1 {
		t.Errorf("the photo is not in the request: %v", stub.bodies[0]["init_images"])
	}
}

// A peer's renderer is an A1111-shaped server under a path of its own,
// naming which renderer: kept as a connection, it keeps that path and body
// and sends its key in the peer key header.
func TestAPeerShapedA1111ConnectionKeepsItsPath(t *testing.T) {
	world(t)
	stub := &a1111Stub{}
	srv := stub.serve(t)
	r := RestImageSpec{SubmitURL: srv.URL + "/api/peer/v1/images/render", SubmitMethod: "POST",
		SubmitBody:   `{"prompt":"{prompt}","backend":"flux","steps":{steps},"seed":{seed},"width":{width},"height":{height}}`,
		ImageB64Path: "images.0"}
	kind, raw, err := r.AsService("header:X-Gohort-Peer-Key")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveConnector(RootDB, Connector{Name: "peer-den-flux", Kind: kind, Template: "peer:den", Spec: raw}); err != nil {
		t.Fatal(err)
	}
	c, _ := GetConnector(RootDB, "peer-den-flux")
	SetConnectionSecret(RootDB, c, "api_key", "peer-key", []string{srv.URL})
	res, err := GenerateImageWithBackend(context.Background(), "peer-den-flux", "a fox", false)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(res.URL)
	if len(stub.seen) != 1 || stub.seen[0] != "/api/peer/v1/images/render  peer-key" {
		t.Errorf("requests: %q", stub.seen)
	}
	if stub.bodies[0]["backend"] != "flux" {
		t.Errorf("the body does not name the renderer: %v", stub.bodies[0])
	}
}

// An a1111 operation is checked as the image machinery checks a backend: its
// mode, what its options name, and what it gives.
func TestAnA1111OperationIsChecked(t *testing.T) {
	world(t)
	r, err := ApplyRestImagePreset("a1111", RestImageSpec{}, map[string]string{"base_url": "http://sd.example:7860"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want string
		change     func(*Spec)
	}{
		{"as made", "", func(*Spec) {}},
		{"no mode", "txt2img or img2img", func(s *Spec) { delete(s.Provider.Operations["render"].Options, "mode") }},
		{"a misspelt option", "unknown field", func(s *Spec) { s.Provider.Operations["render"].Options["prompt_sufix"] = "x" }},
		{"a request of its own", "makes its own requests", func(s *Spec) {
			op := s.Provider.Operations["render"]
			op.Method = "POST"
			s.Provider.Operations["render"] = op
		}},
		{"giving a search", "image.generate or image.edit", func(s *Spec) { s.Provides[0].Capability = "web.search" }},
	} {
		s, err := a1111Service(r, "")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(s)
		s, _ = Parse(raw)
		tc.change(&s)
		err = s.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}

// The Automatic1111 starters add a connection out of use, pointed at the
// address given, its prompt suffix kept.
func TestTheA1111RecipesAddAConnection(t *testing.T) {
	world(t)
	for _, tc := range []struct{ recipe, name, url string }{
		{"a1111", "a1111", "http://192.168.1.20:7860/sdapi/v1/txt2img"},
		{"a1111-img2img", "a1111_edit", "http://192.168.1.20:7860/sdapi/v1/img2img"},
	} {
		if _, err := recipes.Install(RootDB, tc.recipe, "admin", map[string]string{"base_url": "http://192.168.1.20:7860", "prompt_suffix": "crisp"}); err != nil {
			t.Fatalf("%s: %v", tc.recipe, err)
		}
		c, ok := GetConnector(RootDB, tc.name)
		if !ok || c.Kind != Kind || c.Approved {
			t.Fatalf("%s: a connection, out of use: %+v", tc.recipe, c)
		}
		spec, ok, err := handler{}.ImageSpec(c)
		if err != nil || !ok || spec.SubmitURL != tc.url || spec.PromptSuffix != "crisp" {
			t.Errorf("%s: %v %v %+v", tc.recipe, ok, err, spec)
		}
		// It runs what Configure's form builds from the same answers.
		tpl, ok := TemplateForConnector(c)
		if !ok || tpl.Name != c.Template {
			t.Fatalf("%s: Configure would not open its form", tc.recipe)
		}
		raw, _, err := tpl.BuildSpec(map[string]any{"base_url": "http://192.168.1.20:7860", "prompt_suffix": "crisp"})
		if err != nil {
			t.Fatal(err)
		}
		form, _, _ := handler{}.ImageSpec(Connector{Name: "form", Kind: Kind, Spec: raw})
		if form.SubmitURL != spec.SubmitURL || form.SubmitBody != spec.SubmitBody || form.DefaultSteps != spec.DefaultSteps ||
			form.DefaultWidth != spec.DefaultWidth || form.ImageB64Path != spec.ImageB64Path || form.PromptSuffix != spec.PromptSuffix {
			t.Errorf("%s: the recipe's connection differs from the form's:\n got %+v\nwant %+v", tc.recipe, spec, form)
		}
	}
}

// A connection carries no approval: it is served from its save and again at
// start. A change holds it, so it draws nothing, and a save elsewhere then
// stops serving it; accepted, it is served again at once, not at the next
// start.
func TestAnImageConnectionIsServedWithoutApproval(t *testing.T) {
	world(t)
	stub := &a1111Stub{}
	srv := stub.serve(t)
	a1111Connection(t, "a1111", "sd", map[string]any{"base_url": srv.URL})
	if c, _ := GetConnector(RootDB, "sd"); c.Approved || !ImageBackendRegistered("sd") {
		t.Fatalf("saved: approved %v, served %v", c.Approved, ImageBackendRegistered("sd"))
	}
	c, _ := GetConnector(RootDB, "sd")
	c.StopImageBackend()
	if ImageBackendRegistered("sd") {
		t.Fatal("stopped, still served")
	}
	ReloadApprovedConnectors(RootDB)
	if !ImageBackendRegistered("sd") {
		t.Error("not served again at start")
	}

	MarkChanged(RootDB, "sd", "someone")
	a1111Connection(t, "a1111", "other", map[string]any{"base_url": srv.URL})
	if ImageBackendRegistered("sd") {
		t.Error("a held connection is still served after a save elsewhere")
	}
	AcceptChanges(RootDB, "sd")
	if !ImageBackendRegistered("sd") {
		t.Fatal("accepted, not served again")
	}
	res, err := GenerateImageWithBackend(context.Background(), "sd", "a fox", false)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(res.URL)
}

// The rules every connection keeps, held on an image backend's own paths: a
// key goes only to the address it was given for (moved, the call is refused
// and nothing reaches the backend, and its self-check says so), and a
// backend's own
// generate_image_<name> tool is refused to an agent switched off the
// connection and to a user it is not limited to.
func TestAnImageConnectionKeepsItsKeyAndItsScope(t *testing.T) {
	world(t)
	stub := &a1111Stub{}
	srv := stub.serve(t)
	c := a1111Connection(t, "a1111", "sd", map[string]any{"base_url": srv.URL})
	SetConnectionSecret(RootDB, c, "api_key", "me:pw", []string{"https://elsewhere.example"})
	RootDB.Set(ImageTable, "agents", []string{"sd"})
	if _, err := GenerateImageWithBackend(context.Background(), "sd", "a fox", false); err == nil || !strings.Contains(err.Error(), "again for the new address") {
		t.Errorf("a key given for another address: %v", err)
	}
	if len(stub.seen) != 0 {
		t.Errorf("a call reached the backend with its key bound elsewhere: %q", stub.seen)
	}
	if got := strings.Join(SelfCheck(RootDB, c), " | "); !strings.Contains(got, "so they are refused") {
		t.Errorf("the self-check: %s", got)
	}
	SetConnectionSecret(RootDB, c, "api_key", "me:pw", []string{srv.URL})

	tool, _ := LookupChatTool("generate_image_sd")
	run := tool.(interface {
		RunWithSession(map[string]any, *ToolSession) (string, error)
	})
	in := map[string]any{"prompt": "a fox"}
	stub.seen = nil
	if _, err := run.RunWithSession(in, &ToolSession{Username: "alice", DeniedCredentials: map[string]bool{"connection:sd": true}}); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Errorf("an agent switched off it: %v", err)
	}
	c.SetAccess(RootDB, []string{"bob"})
	if _, err := run.RunWithSession(in, &ToolSession{Username: "alice"}); err == nil || !strings.Contains(err.Error(), "limited to named users") {
		t.Errorf("a user it is not limited to: %v", err)
	}
	if len(stub.seen) != 0 {
		t.Errorf("a refused call reached the backend: %q", stub.seen)
	}
	if out, err := run.RunWithSession(in, &ToolSession{Username: "bob"}); err != nil || !strings.Contains(out, "generated") {
		t.Errorf("bob: %q %v", out, err)
	}
	if len(stub.seen) != 1 || !strings.Contains(stub.seen[0], "Basic") {
		t.Errorf("bob's call, with the key given for its address: %q", stub.seen)
	}
}

// Used for, on Media Generation, keeps a backend that both makes and edits
// pictures to one of them: agents are offered it for that alone, and a call
// for the other is refused, saying where it was set.
func TestUsedForKeepsABothWaysBackendToOneJob(t *testing.T) {
	world(t)
	png := pngBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	}))
	defer srv.Close()
	op := func(path, body string) map[string]any {
		return map[string]any{"method": "POST", "path": path, "body": map[string]any{"prompt": "{prompt}", "x": body}, "read": "file"}
	}
	install(t, "both", map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
			"operations": map[string]any{"make": op("/make", "{prompt}"), "edit": op("/edit", "{image.base64}")}},
		"provides": []any{
			map[string]any{"capability": "image.generate", "steps": []any{map[string]any{"call": "make"}}, "output": map[string]any{"image": map[string]any{"from": "body"}}},
			map[string]any{"capability": "image.edit", "steps": []any{map[string]any{"call": "edit"}}, "output": map[string]any{"image": map[string]any{"from": "body"}}},
		},
	})
	RootDB.Set(ImageTable, "agents", []string{"both"})
	choice := func() ImageBackendChoice {
		for _, b := range ReachableImageBackends(&ToolSession{}) {
			if b.Name == "both" {
				return b
			}
		}
		t.Fatal("not offered")
		return ImageBackendChoice{}
	}
	if c := choice(); !c.Edits || !c.Generates {
		t.Fatalf("used for both, offered as %+v", c)
	}

	RootDB.Set(ImageTable, "uses", map[string]string{"both": ImageUseEdit})
	if c := choice(); !c.Edits || c.Generates {
		t.Errorf("used for editing only, offered as %+v", c)
	}
	if _, err := GenerateImageWithBackend(context.Background(), "both", "a red circle", false); err == nil || !strings.Contains(err.Error(), "editing and blending only") {
		t.Errorf("a picture from text alone on an editing-only backend: %v", err)
	}

	RootDB.Set(ImageTable, "uses", map[string]string{"both": ImageUseGenerate})
	if c := choice(); c.Edits || c.Generates {
		t.Errorf("used for generating only, offered as %+v (an editor, or one that also generates)", c)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.png"), png, 0o600)
	_, err := EditImageWithBackend(&ToolSession{WorkspaceDir: dir}, EditImageRequest{Backend: "both", Prompt: "blend", Images: []string{"a.png"}})
	if err == nil || !strings.Contains(err.Error(), "generating only") {
		t.Errorf("an edit on a generating-only backend: %v", err)
	}
	res, err := GenerateImageWithBackend(context.Background(), "both", "a red circle", false)
	if err != nil {
		t.Fatalf("generating on a generating-only backend: %v", err)
	}
	os.Remove(res.URL)
}
