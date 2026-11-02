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
	if err := ApproveConnector(RootDB, name); err != nil {
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

	// Another saved leaves the first served, and the tool an agent was
	// granted before the move draws through it too.
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
	if out, err := run.RunWithSession(map[string]any{"prompt": "a green fox"}, &ToolSession{}); err != nil || !strings.Contains(out, "generated") {
		t.Errorf("its tool: %q %v", out, err)
	}
	// Out of use, it draws no more, and is no longer offered as a backend.
	UnapproveConnector(RootDB, "studio_open")
	if _, err := GenerateImageWithBackend(context.Background(), "studio_open", "a fox", false); err == nil {
		t.Error("an unused connection still draws")
	}
	if ImageBackendRegistered("studio_open") {
		t.Error("an unused connection is still registered as a backend")
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
	if err := ApproveConnector(RootDB, name); err != nil {
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
	if err := ApproveConnector(RootDB, "peer-den-flux"); err != nil {
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
