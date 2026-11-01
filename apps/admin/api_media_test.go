package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// embedConnector saves and approves a keyless service connector named name
// that embeds at base (base/embeddings, OpenAI's shape), in model, and points
// embeddings at it: the shape every instance embeds in. It needs RootDB and
// AuthDB (the dispatch reads the credential store) set by the caller.
func embedConnector(t *testing.T, name, base, model string) {
	t.Helper()
	spec := `{"provider":{"transport":"http","base_url":"` + base + `","auth":{"none":true},
		"operations":{"embed":{"method":"POST","path":"/embeddings","body":{"model":"{model}","input":"{texts}"}}}},
		"provides":[{"capability":"embed.text","steps":[{"call":"embed"}],"output":{"vectors":{"path":"data","each":"embedding","as":"json"}},
		"defaults":{"model":"` + model + `"}}]}`
	if err := SaveConnector(RootDB, Connector{Name: name, Kind: "service", Spec: json.RawMessage(spec)}); err != nil {
		t.Fatal(err)
	}
	if err := ApproveConnector(RootDB, name); err != nil {
		t.Fatal(err)
	}
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Provider: "embed:" + name, Endpoint: "embed:" + name, Model: model})
}

// transcribeConnector saves and approves a keyless service connector named
// name that transcribes at base (base/audio/transcriptions, OpenAI's shape),
// with model when one is given: the shape the migration writes. It needs
// RootDB and AuthDB set by the caller.
func transcribeConnector(t *testing.T, name, base, model string) {
	t.Helper()
	defaults := ""
	if model != "" {
		defaults = `,"defaults":{"model":"` + model + `"}`
	}
	spec := `{"provider":{"transport":"http","base_url":"` + base + `","auth":{"none":true},
		"operations":{"transcribe":{"method":"POST","path":"/audio/transcriptions","send":"multipart","body":{"file":"{audio}","model":"{model}","prompt":"{prompt}"}}}},
		"provides":[{"capability":"speech.transcribe","steps":[{"call":"transcribe"}],"output":{"text":{"path":"text","as":"text"}}` + defaults + `}]}`
	if err := SaveConnector(RootDB, Connector{Name: name, Kind: "service", Spec: json.RawMessage(spec)}); err != nil {
		t.Fatal(err)
	}
	if err := ApproveConnector(RootDB, name); err != nil {
		t.Fatal(err)
	}
}

// speakConnector saves and approves a keyless service connector named name
// that speaks at base (base/audio/speech, OpenAI's shape), with model when
// one is given, and lists its voices (base/audio/voices): the shape the
// migration writes for a Kokoro-FastAPI. It needs RootDB and AuthDB set by
// the caller.
func speakConnector(t *testing.T, name, base, model string) {
	t.Helper()
	defaults := ""
	if model != "" {
		defaults = `,"defaults":{"model":"` + model + `"}`
	}
	spec := `{"provider":{"transport":"http","base_url":"` + base + `","auth":{"none":true},
		"operations":{"speak":{"method":"POST","path":"/audio/speech","body":{"input":"{text}","model":"{model}","voice":"{voice}","response_format":"mp3"},"read":"file"},
			"voices":{"path":"/audio/voices"}}},
		"provides":[{"capability":"speech.synthesize","steps":[{"call":"speak"}],"output":{"audio":{"from":"body"}}` + defaults + `},
			{"capability":"speech.voices","steps":[{"call":"voices"}],"output":{"voices":{"path":"voices","as":"json"}}}]}`
	if err := SaveConnector(RootDB, Connector{Name: name, Kind: "service", Spec: json.RawMessage(spec)}); err != nil {
		t.Fatal(err)
	}
	if err := ApproveConnector(RootDB, name); err != nil {
		t.Fatal(err)
	}
}

// A speech or transcription setting may name any service connector that
// provides it (naming it puts it to use); the settings offer exactly those.
func TestSpeechSettingsTakeAServiceThatSpeaks(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	spec := `{"provider":{"transport":"http","base_url":"https://api.example.test","auth":{"none":true},
		"operations":{"speak":{"method":"POST","path":"/v1/speak","body":{"text":"{text}"},"read":"file"}}},
		"provides":[{"capability":"speech.synthesize","steps":[{"call":"speak"}],"output":{"audio":{"from":"body"}}}]}`
	SaveConnector(RootDB, Connector{Name: "voice", Kind: "service", Spec: json.RawMessage(spec)})
	if err := serviceProviderOK("speak:voice", "speech.synthesize"); err != nil {
		t.Errorf("a service that speaks, in use nowhere yet: %v", err)
	}
	if err := serviceProviderOK("speak:voice", "speech.transcribe"); err == nil {
		t.Error("a service that only speaks was taken for transcription")
	}
	if err := serviceProviderOK("speak:nothing", "speech.synthesize"); err == nil {
		t.Error("a missing connector was taken")
	}
	if err := serviceProviderOK("local", "speech.synthesize"); err != nil {
		t.Errorf("this instance: %v", err)
	}
	opts := serviceProviderOptions("speech.synthesize", "Speak")
	if len(opts) != 1 || opts[0].Value != "speak:voice" {
		t.Errorf("speech choices: %+v", opts)
	}
	if len(serviceProviderOptions("speech.transcribe", "Transcribe")) != 0 {
		t.Error("a service that only speaks is offered for transcription")
	}
}

// Embeddings may be made by any service that provides them. Picking
// one stores the service as the endpoint and the model its spec declares,
// whatever model the hidden local field still held, so the stored config
// names the space its vectors are in.
func TestEmbeddingSettingsTakeAServiceThatEmbeds(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	savedCfg := GetEmbeddingConfig()
	t.Cleanup(func() { RootDB = saved; SetEmbeddingConfig(savedCfg) })
	spec := `{"provider":{"transport":"http","base_url":"https://api.example.test","auth":{"none":true},
		"operations":{"embed":{"method":"POST","path":"/v1/embeddings","body":{"model":"{model}","input":"{texts}"}}}},
		"provides":[{"capability":"embed.text","steps":[{"call":"embed"}],"output":{"vectors":{"path":"data","each":"embedding"}},
		"defaults":{"model":"embed-small"}}]}`
	SaveConnector(RootDB, Connector{Name: "vec", Kind: "service", Spec: json.RawMessage(spec)})
	req := EmbeddingConfig{Enabled: true, Provider: "embed:vec", Endpoint: "http://localhost:11434/api", Model: "nomic-embed-text", APIKey: "x"}
	got, err := resolveEmbedding(req, nil)
	if err != nil || got.Endpoint != "embed:vec" || got.Model != "embed-small" || got.APIKey != "" {
		t.Errorf("resolved: %+v %v", got, err)
	}
	if _, err := resolveEmbedding(EmbeddingConfig{Enabled: true, Provider: "embed:nothing"}, nil); err == nil {
		t.Error("a missing connector was taken")
	}
	found := false
	for _, f := range embeddingFormFields() {
		if f.Field != "provider" {
			continue
		}
		for _, o := range f.Options {
			found = found || o.Value == "embed:vec"
		}
	}
	if !found {
		t.Error("the Embed on choices do not offer the service")
	}
}

// While nothing else can embed, transcribe or speak, each page says where to
// get something instead of offering a choice of none (or, for transcription
// and speech, of this instance alone); once a connector is approved, the
// choice replaces the pointer.
func TestCapabilityPagesPointToAnAlternative(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	read := func(fields []ui.FormField) (help string, choice bool) {
		for _, f := range fields {
			if f.Field == "enabled" {
				help = f.Help
			}
			choice = choice || f.Field == "provider"
		}
		return
	}
	for name, fields := range map[string]func() []ui.FormField{"embeddings": embeddingFormFields, "transcription": transcribeFormFields, "speech": speechFormFields} {
		if help, choice := read(fields()); choice || !strings.Contains(help, "Admin > Extensions > Templates") || !strings.Contains(help, "Admin > Capabilities > Peers") {
			t.Errorf("%s with nothing else: %q, choice %v", name, help, choice)
		}
	}
	spec := `{"provider":{"transport":"http","base_url":"https://api.example.test","auth":{"none":true},
		"operations":{"embed":{"method":"POST","path":"/v1/embeddings","body":{"input":"{texts}"}}}},
		"provides":[{"capability":"embed.text","steps":[{"call":"embed"}],"output":{"vectors":{"path":"data","each":"embedding"}}}]}`
	SaveConnector(RootDB, Connector{Name: "vectors", Kind: "service", Spec: json.RawMessage(spec)})
	ApproveConnector(RootDB, "vectors")
	if help, choice := read(embeddingFormFields()); !choice || help != "" {
		t.Errorf("embeddings with a connector to choose: %q, choice %v", help, choice)
	}
	if _, choice := read(speechFormFields()); choice {
		t.Error("a connector that embeds made speech a choice")
	}
}

// Image Generation is set to an approved connector that makes pictures, or
// Disabled. With none, the form points to the templates, Add image backend
// and peers. A name that is not such a connector, the old built-in ones
// included, is refused on save and by the Test, and the form keeps no key.
func TestImageGenerationTakesAConnectorThatMakesPictures(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	a := &AdminApp{db: RootDB}
	sub := http.NewServeMux()
	a.registerMediaRoutes(sub)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		sub.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}

	// A connector that does something else is not offered.
	embedSpec := `{"provider":{"transport":"http","base_url":"https://api.example.test","auth":{"none":true},
		"operations":{"embed":{"method":"POST","path":"/v1/embeddings","body":{"input":"{texts}"}}}},
		"provides":[{"capability":"embed.text","steps":[{"call":"embed"}],"output":{"vectors":{"path":"data","each":"embedding"}}}]}`
	SaveConnector(RootDB, Connector{Name: "vectors", Kind: "service", Spec: json.RawMessage(embedSpec)})
	ApproveConnector(RootDB, "vectors")
	f := imageFormFields()
	if len(f) != 1 || len(f[0].Options) != 1 || f[0].Options[0].Value != "none" ||
		!strings.Contains(f[0].Help, "Admin > Extensions > Templates") || !strings.Contains(f[0].Help, "Add image backend") || !strings.Contains(f[0].Help, "Admin > Capabilities > Peers") {
		t.Errorf("with nothing that makes pictures: %+v", f)
	}
	if w := call("GET", "/api/image-gen", ""); strings.TrimSpace(w.Body.String()) != `{"agents":[],"provider":"none"}` {
		t.Errorf("never set: %s", w.Body.String())
	}
	for _, name := range []string{"gemini", "openai", "nothing"} {
		if w := call("POST", "/api/image-gen", `{"provider":"`+name+`"}`); w.Code != http.StatusBadRequest {
			t.Errorf("%s was saved: %d", name, w.Code)
		}
		if w := call("POST", "/api/image-gen/test", `{"provider":"`+name+`"}`); !strings.Contains(w.Body.String(), "no connector named "+name+" makes pictures") {
			t.Errorf("%s tested: %s", name, w.Body.String())
		}
	}

	spec := `{"provider":{"transport":"http","base_url":"https://api.example.test","auth":{"none":true},
		"operations":{"make":{"method":"POST","path":"/v1/images","body":{"prompt":"{prompt}"}}}},
		"provides":[{"capability":"image.generate","steps":[{"call":"make"}],"output":{"image":{"path":"data.0.b64_json","as":"base64"}}}]}`
	SaveConnector(RootDB, Connector{Name: "painter", Kind: "service", Spec: json.RawMessage(spec)})
	t.Cleanup(func() { UnapproveConnector(RootDB, "painter"); UnapproveConnector(RootDB, "sketcher") })
	SaveConnector(RootDB, Connector{Name: "sketcher", Kind: "service", Spec: json.RawMessage(strings.Replace(spec, "/v1/images", "/v2/images", 1))})
	f = imageFormFields()
	if len(f) != 2 || len(f[0].Options) != 3 || f[0].Options[0].Value != "painter" || !strings.Contains(f[0].Detail, "Admin > Extensions > Templates") || f[1].Field != "agents" || len(f[1].Options) != 2 {
		t.Errorf("with a connector to choose: %+v", f)
	}
	if w := call("POST", "/api/image-gen", `{"provider":"painter","agents":["sketcher","vectors"]}`); w.Code != http.StatusBadRequest {
		t.Errorf("a connector that does not make pictures offered to agents: %d", w.Code)
	}
	if w := call("POST", "/api/image-gen", `{"provider":"painter","agents":["sketcher","painter"],"api_key":"sk-stray"}`); w.Code >= 300 {
		t.Fatalf("a connector that makes pictures was refused: %d %s", w.Code, w.Body.String())
	}
	for _, n := range []string{"painter", "sketcher"} {
		if c, _ := GetConnector(RootDB, n); !c.Approved {
			t.Errorf("%s is not in use", n)
		}
	}
	savedProvider := ImageProviderFunc
	ImageProviderFunc = func() string { return a.storedString(ImageTable, "provider") }
	t.Cleanup(func() { ImageProviderFunc = savedProvider })
	if got := ReachableImageBackends(&ToolSession{}); len(got) != 2 {
		t.Errorf("agents are offered %+v, want the default and the one also offered", got)
	}
	if a.storedString(ImageTable, "provider") != "painter" || a.storedString(ImageTable, "api_key") != "" {
		t.Errorf("stored: %q, key %q", a.storedString(ImageTable, "provider"), a.storedString(ImageTable, "api_key"))
	}
	if w := call("GET", "/api/image-gen", ""); strings.TrimSpace(w.Body.String()) != `{"agents":["sketcher"],"provider":"painter"}` {
		t.Errorf("set: %s", w.Body.String())
	}
	if w := call("POST", "/api/image-gen/test", `{"provider":"painter"}`); !strings.Contains(w.Body.String(), "is in use") {
		t.Errorf("tested: %s", w.Body.String())
	}
	if w := call("POST", "/api/image-gen", `{"provider":"none"}`); w.Code >= 300 || a.storedString(ImageTable, "provider") != "none" {
		t.Errorf("turning it off: %d", w.Code)
	}
	if c, _ := GetConnector(RootDB, "painter"); c.Approved {
		t.Error("a connector no page names any more is still in use")
	}
}

// A local Web Search engine is DuckDuckGo, a Source Hook or an approved
// connector that searches; SearXNG's shape is a peer's, and the engines
// that were written in Go are refused by name.
func TestWebSearchTakesOnlyAnEngine(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	for _, c := range []struct {
		cfg WebSearchConfig
		ok  bool
	}{
		{WebSearchConfig{}, true},
		{WebSearchConfig{Provider: "duckduckgo"}, true},
		{WebSearchConfig{Provider: "hook"}, false},
		{WebSearchConfig{Provider: "searxng", Source: "peer:den"}, true},
		{WebSearchConfig{Provider: "searxng"}, false},
		{WebSearchConfig{Provider: "searxng", Source: "local"}, false},
		{WebSearchConfig{Provider: "brave"}, false},
		{WebSearchConfig{Provider: "search:nothing"}, false},
	} {
		if err := searchEngineOK(c.cfg); (err == nil) != c.ok {
			t.Errorf("%+v: %v", c.cfg, err)
		}
	}
}

// Web Search offers every connector that searches as a provider; choosing
// one puts it to use.
func TestWebSearchTakesAConnectorThatSearches(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	spec := `{"provider":{"transport":"http","base_url":"https://search.example.test","auth":{"none":true},
		"operations":{"search":{"url":"/find?q={query}"}}},
		"provides":[{"capability":"web.search","steps":[{"call":"search"}],"output":{"results":{"path":"hits","fields":{"title":"name","url":"href"}}}}]}`
	SaveConnector(RootDB, Connector{Name: "finder", Kind: "service", Spec: json.RawMessage(spec)})
	offered := func() bool {
		for _, f := range webSearchFormFields() {
			for _, o := range f.Options {
				if f.Field == "provider" && o.Value == "search:finder" {
					return true
				}
			}
		}
		return false
	}
	a := &AdminApp{db: RootDB}
	if !offered() {
		t.Error("a connector no page uses yet is not offered: choosing it is what puts it to use")
	}
	if _, err := a.handleWebSearchSave(&jsonapi.Call{R: httptest.NewRequest("POST", "/api/web-search", nil)}, withSettings[WebSearchConfig]{Form: WebSearchConfig{Provider: "search:finder"}}); err != nil {
		t.Errorf("choosing it: %v", err)
	}
	if c, _ := GetConnector(RootDB, "finder"); !c.Approved {
		t.Error("choosing it did not put it to use")
	}
	if _, err := a.handleWebSearchSave(&jsonapi.Call{R: httptest.NewRequest("POST", "/api/web-search", nil)}, withSettings[WebSearchConfig]{Form: WebSearchConfig{Provider: "search:finder"}}); err != nil {
		t.Errorf("saving it: %v", err)
	}
	if got := a.storedString(SearchTable, "provider"); got != "search:finder" {
		t.Errorf("stored provider %q", got)
	}
}

// Page Rendering offers every connector that renders pages, puts the one
// chosen to use, and routes browsing to it.
func TestPageRenderingTakesAConnectorThatRenders(t *testing.T) {
	saved, prevCfg := RootDB, LoadBrowseConfig()
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved; SetBrowseConfig(prevCfg) })
	spec := `{"provider":{"transport":"http","base_url":"https://reader.example.test","auth":{"none":true},
		"operations":{"read":{"method":"POST","path":"/","body":{"url":"{url}"}}}},
		"provides":[{"capability":"page.render","steps":[{"call":"read"}],"output":{"text":{"path":"data.content"}}}]}`
	SaveConnector(RootDB, Connector{Name: "reader", Kind: "service", Spec: json.RawMessage(spec)})
	offered := func() bool {
		for _, o := range browseFormFields()[0].Options {
			if o.Value == "render:reader" {
				return true
			}
		}
		return false
	}
	a := &AdminApp{db: RootDB}
	call := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/browse", nil)}
	if !offered() {
		t.Error("a connector no page uses yet is not offered: choosing it is what puts it to use")
	}
	if _, err := a.handleBrowseSave(call, withSettings[BrowseConfig]{Form: BrowseConfig{Source: "render:reader"}}); err != nil {
		t.Errorf("choosing it: %v", err)
	}
	if c, _ := GetConnector(RootDB, "reader"); !c.Approved {
		t.Error("choosing it did not put it to use")
	}
	if _, err := a.handleBrowseSave(call, withSettings[BrowseConfig]{Form: BrowseConfig{Source: "render:reader"}}); err != nil || LoadBrowseConfig().Source != "render:reader" {
		t.Errorf("saving it: %v %+v", err, LoadBrowseConfig())
	}
}

// Mail offers every connector that sends mail beside SMTP, keeping the SMTP
// fields to SMTP, and points to Templates while SMTP is all there is; the one
// saved is what sends, and saving it puts it to use.
func TestMailTakesAConnectorThatSends(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	field := func(name string) (ui.FormField, bool) {
		for _, f := range mailFormFields() {
			if f.Field == name {
				return f, true
			}
		}
		return ui.FormField{}, false
	}
	if _, ok := field("provider"); ok {
		t.Error("a choice of one: SMTP alone")
	}
	if f, _ := field("server"); f.ShowWhen != "" || !strings.Contains(f.Help, "Admin > Extensions > Templates") {
		t.Errorf("SMTP alone: %+v", f)
	}
	spec := `{"provider":{"transport":"http","base_url":"https://mail.example.test","auth":{"none":true},
		"operations":{"send":{"method":"POST","path":"/emails","body":{"to":"{to}","subject":"{subject}","text":"{body}"}}}},
		"provides":[{"capability":"mail.send","steps":[{"call":"send"}],"output":{"id":{"path":"id"}}}]}`
	SaveConnector(RootDB, Connector{Name: "postbox", Kind: "service", Spec: json.RawMessage(spec)})
	a := &AdminApp{db: RootDB}
	call := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/mail", nil)}
	if f, ok := field("provider"); !ok || len(f.Options) != 2 || f.Options[1].Value != "mail:postbox" {
		t.Errorf("the choice: %+v", f)
	}
	if f, _ := field("server"); f.ShowWhen != "provider:smtp" || f.Help != "" {
		t.Errorf("SMTP's server with a choice: %+v", f)
	}
	if f, _ := field("from"); f.ShowWhen != "" {
		t.Error("From is hidden, though a connector uses it too")
	}
	got, _ := a.handleMail(call)
	if got.(map[string]any)["provider"] != "smtp" {
		t.Errorf("nothing saved reads as SMTP: %+v", got)
	}
	if _, err := a.handleMailSave(call, withSettings[MailConfig]{Form: MailConfig{Provider: "mail:postbox", From: "alerts@mail.example"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.handleMail(call); got.(map[string]any)["provider"] != "mail:postbox" {
		t.Errorf("saved: %+v", got)
	}
	if c, _ := GetConnector(RootDB, "postbox"); !c.Approved {
		t.Error("choosing it did not put it to use")
	}
}

// The LLM tiers offer every model connector as a provider; choosing one puts
// it to use.
func TestLLMTiersTakeAModelConnector(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	spec := `{"provider":{"transport":"http","base_url":"https://llm.example.test/v1","auth":{"none":true},
		"operations":{"models":{"path":"/models"}}},
		"provides":[{"capability":"llm.chat","dialect":"openai","defaults":{"model":"fast-model"}}]}`
	SaveConnector(RootDB, Connector{Name: "hosted", Kind: "service", Spec: json.RawMessage(spec)})
	offered := func() bool {
		for _, o := range LLMProviderOptions(false) {
			if o.Value == "llm:hosted" {
				return true
			}
		}
		return false
	}
	a := &AdminApp{db: RootDB}
	call := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/worker-llm", nil)}
	if !offered() {
		t.Error("a connector no page uses yet is not offered: choosing it is what puts it to use")
	}
	if _, err := a.handleLLMConfigSave(LLMTable, true)(call, withSettings[llmConfigSave]{Form: llmConfigSave{Provider: "llm:hosted"}}); err != nil {
		t.Errorf("choosing it: %v", err)
	}
	if c, _ := GetConnector(RootDB, "hosted"); !c.Approved {
		t.Error("choosing it did not put it to use")
	}
}

// A capability page's Test tries the connector chosen in its form before a
// save puts it to use, as testing before saving needs; the test leaves it
// out of use, and nothing outside the test reaches it.
func TestATestTriesTheChoiceBeforeItIsSaved(t *testing.T) {
	saved, savedAuth, savedCfg := RootDB, AuthDB, GetEmbeddingConfig()
	RootDB = &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { RootDB, AuthDB = saved, savedAuth; SetEmbeddingConfig(savedCfg) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer srv.Close()
	embedConnector(t, "vec", srv.URL, "m")
	if err := UnapproveConnector(RootDB, "vec"); err != nil {
		t.Fatal(err)
	}
	SetEmbeddingConfig(EmbeddingConfig{})
	a := &AdminApp{db: RootDB}
	out, _ := a.handleEmbeddingsTest(&jsonapi.Call{R: httptest.NewRequest("POST", "/api/embeddings/test", nil)},
		withSettings[EmbeddingConfig]{Form: EmbeddingConfig{Enabled: true, Provider: "embed:vec"}})
	if body, _ := out.(map[string]any); body["ok"] != true {
		t.Fatalf("the test of a choice not yet saved: %+v", out)
	}
	if c, _ := GetConnector(RootDB, "vec"); c.Approved {
		t.Error("testing it put it to use")
	}
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Provider: "embed:vec", Endpoint: "embed:vec", Model: "m"})
	if _, err := Embed(context.Background(), "outside the test"); err == nil {
		t.Error("an embedding outside the test reached a connector no page has put to use")
	}
}
