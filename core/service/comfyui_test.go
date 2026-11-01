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
	kind, raw, err := r.AsService()
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

// A ComfyUI workflow kept as a connection draws as a rest_image backend drew:
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

// A blend workflow kept as a connection takes two photos as a rest_image
// backend did: each uploaded, each written into its own photo node. It is
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

// A ComfyUI backend kept as a rest_image connector moves onto a connection:
// its name, use and template stay, its key moves from its credential into
// the connection and goes the way it did, and the credential, named by
// nothing else, goes. Another rest_image backend stays. Run again, it
// changes nothing.
func TestAComfyUIBackendMovesOntoAConnection(t *testing.T) {
	world(t)
	stub := &comfyStub{}
	srv := stub.serve(t)
	if err := Secure().Save(SecureCredential{Name: "comfy_key", Type: SecureCredHeader, ParamName: "X-Api-Key", BaseURL: srv.URL}, "k-old"); err != nil {
		t.Fatal(err)
	}
	r, _, err := NewComfyImageSpec(srv.URL, "comfy_key", "", "")
	if err != nil {
		t.Fatal(err)
	}
	stub.output = r.ComfyMap.OutputNode
	raw, _ := json.Marshal(r)
	if err := SaveConnector(RootDB, Connector{Name: "studio", Kind: RestImageConnectorKind, Spec: raw}); err != nil {
		t.Fatal(err)
	}
	ApproveConnector(RootDB, "studio")
	RootDB.Set(ImageTable, "provider", "studio")
	a, err := ApplyRestImagePreset("a1111", RestImageSpec{Credential: "no_auth"}, map[string]string{"base_url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(a)
	if err := SaveConnector(RootDB, Connector{Name: "webui", Kind: RestImageConnectorKind, Spec: raw}); err != nil {
		t.Fatal(err)
	}

	if n := comfyToService(RootDB); n == 0 {
		t.Fatal("nothing moved")
	}
	c, _ := GetConnector(RootDB, "studio")
	if c.Kind != Kind || c.Template != "comfyui" || !c.Approved {
		t.Errorf("moved as: kind %s, template %q, in use %v", c.Kind, c.Template, c.Approved)
	}
	if kept, ok := GetConnectionSecret(RootDB, c, "api_key"); !ok || kept.Value != "k-old" {
		t.Errorf("its key: %+v %v", kept, ok)
	}
	if _, ok := Secure().Load("comfy_key"); ok {
		t.Errorf("the credential only it named is still there")
	}
	if w, _ := GetConnector(RootDB, "webui"); w.Kind != RestImageConnectorKind {
		t.Errorf("the Automatic1111 backend moved too: %s", w.Kind)
	}
	if _, err := GenerateImageWithBackend(context.Background(), "studio", "a red fox", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(stub.seen, "\n"), "/prompt  k-old") {
		t.Errorf("its key did not go as its credential sent it: %v", stub.seen)
	}
	if n := comfyToService(RootDB); n != 0 {
		t.Errorf("a second run changed %d", n)
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

// A comfyui operation is checked as a rest_image spec was: what its options
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
	spec, comfy, err := handler{}.ComfySpec(c)
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
