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
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// embedConnector saves a keyless service connector named name
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
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Provider: name, Endpoint: name, Model: model})
}

// transcribeConnector saves a keyless service connector named
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
}

// speakConnector saves a keyless service connector named name
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
	if err := serviceProviderOK("voice", "speech.synthesize"); err != nil {
		t.Errorf("a service that speaks, in use nowhere yet: %v", err)
	}
	if err := serviceProviderOK("voice", "speech.transcribe"); err == nil {
		t.Error("a service that only speaks was taken for transcription")
	}
	if err := serviceProviderOK("nothing", "speech.synthesize"); err == nil {
		t.Error("a missing connector was taken")
	}
	if err := serviceProviderOK("local", "speech.synthesize"); err != nil {
		t.Errorf("this instance: %v", err)
	}
	opts := serviceProviderOptions("speech.synthesize", "Speak")
	if len(opts) != 1 || opts[0].Value != "voice" {
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
	req := EmbeddingConfig{Enabled: true, Provider: "vec", Endpoint: "http://localhost:11434/api", Model: "nomic-embed-text", APIKey: "x"}
	got, err := resolveEmbedding(req, nil)
	if err != nil || got.Endpoint != "vec" || got.Model != "embed-small" || got.APIKey != "" {
		t.Errorf("resolved: %+v %v", got, err)
	}
	if _, err := resolveEmbedding(EmbeddingConfig{Enabled: true, Provider: "nothing"}, nil); err == nil {
		t.Error("a missing connector was taken")
	}
	found := false
	for _, f := range embeddingFormFields() {
		if f.Field != "provider" {
			continue
		}
		for _, o := range f.Options {
			found = found || o.Value == "vec"
		}
	}
	if !found {
		t.Error("the Embed on choices do not offer the service")
	}
}

// choicesOf are select f's options that are choices: its list ends with
// "+ Add another…" for need, a command that opens need's templates, and a
// list for a job that can be off starts with Disabled; both are checked here
// and left out.
func choicesOf(t *testing.T, f ui.FormField, need string) []ui.SelectOption {
	t.Helper()
	n := len(f.Options)
	if n == 0 || f.Options[n-1].Action != "connector_add_another" || f.Options[n-1].Value != need || f.Options[n-1].Label != "+ Add another…" {
		t.Errorf("%s does not end with + Add another… for %s: %+v", f.Field, need, f.Options)
		return f.Options
	}
	opts := f.Options[:n-1]
	if len(opts) > 0 && opts[0].Value == "none" && opts[0].Label == "Disabled" {
		opts = opts[1:]
	}
	return opts
}

// Each job that can be off starts its list with Disabled: images,
// embeddings, transcription, speech and web search. There is no separate
// switch.
func TestAJobThatCanBeOffStartsWithDisabled(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	for name, fields := range map[string]func() []ui.FormField{"images": imageFormFields, "embeddings": embeddingFormFields,
		"transcription": transcribeFormFields, "speech": speechFormFields, "web search": webSearchFormFields} {
		f, ok := findField(fields(), "provider")
		if !ok || len(f.Options) == 0 || f.Options[0].Value != "none" || f.Options[0].Label != "Disabled" {
			t.Errorf("%s does not start with Disabled: %+v", name, f.Options)
		}
		if _, ok := findField(fields(), "enabled"); ok {
			t.Errorf("%s still has a separate switch", name)
		}
	}
}

// Each page that chooses a connector always shows its list, ending with
// "+ Add another…" for its own kind: with nothing to choose yet, that is the
// list. A connector that embeds is offered on Embeddings, not on Speech.
func TestCapabilityPagesEndWithAddAnother(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	pages := []struct {
		name, need string
		fields     func() []ui.FormField
	}{{"embeddings", "embed.text", embeddingFormFields}, {"transcription", "speech.transcribe", transcribeFormFields}, {"speech", "speech.synthesize", speechFormFields}}
	for _, p := range pages {
		f, ok := findField(p.fields(), "provider")
		if !ok {
			t.Errorf("%s: no list", p.name)
			continue
		}
		if got := choicesOf(t, f, p.need); len(got) != 0 {
			t.Errorf("%s with nothing else: %+v", p.name, got)
		}
	}
	spec := `{"provider":{"transport":"http","base_url":"https://api.example.test","auth":{"none":true},
		"operations":{"embed":{"method":"POST","path":"/v1/embeddings","body":{"input":"{texts}"}}}},
		"provides":[{"capability":"embed.text","steps":[{"call":"embed"}],"output":{"vectors":{"path":"data","each":"embedding"}}}]}`
	SaveConnector(RootDB, Connector{Name: "vectors", Kind: "service", Spec: json.RawMessage(spec)})
	if f, _ := findField(embeddingFormFields(), "provider"); len(choicesOf(t, f, "embed.text")) != 1 {
		t.Errorf("embeddings with a connector to choose: %+v", f.Options)
	}
	if f, _ := findField(speechFormFields(), "provider"); len(choicesOf(t, f, "speech.synthesize")) != 0 {
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
	f := imageFormFields()
	if len(f) != 2 || f[0].Field != "_stale" || !strings.Contains(f[1].Help, "+ Add another…") || !strings.Contains(f[1].Help, "Admin > Integrations > Peers") {
		t.Errorf("with nothing that makes pictures: %+v", f)
	}
	if got := choicesOf(t, f[1], "image.generate"); len(got) != 0 {
		t.Errorf("with nothing that makes pictures, the choices: %+v", got)
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
	if len(f) != 3 || !strings.Contains(f[1].Detail, "Admin > Integrations > Peers") || f[2].Field != "agents" || len(f[2].Options) != 2 {
		t.Errorf("with a connector to choose: %+v", f)
	}
	if got := choicesOf(t, f[1], "image.generate"); len(got) != 2 || got[0].Value != "painter" {
		t.Errorf("with a connector to choose, the choices: %+v", got)
	}
	if w := call("POST", "/api/image-gen", `{"provider":"painter","agents":["sketcher","vectors"]}`); w.Code != http.StatusBadRequest {
		t.Errorf("a connector that does not make pictures offered to agents: %d", w.Code)
	}
	if w := call("POST", "/api/image-gen", `{"provider":"painter","agents":["sketcher","painter"],"api_key":"sk-stray"}`); w.Code >= 300 {
		t.Fatalf("a connector that makes pictures was refused: %d %s", w.Code, w.Body.String())
	}
	for _, n := range []string{"painter", "sketcher"} {
		if !service.Referenced(RootDB, n) {
			t.Errorf("%s is not referenced", n)
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
	if w := call("POST", "/api/image-gen/test", `{"provider":"painter"}`); !strings.Contains(w.Body.String(), "is ready") {
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
		{WebSearchConfig{Provider: "nothing"}, false},
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
				if f.Field == "provider" && o.Value == "finder" {
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
	if _, err := a.handleWebSearchSave(&jsonapi.Call{R: httptest.NewRequest("POST", "/api/web-search", nil)}, withSettings[webSearchForm]{Form: webSearchForm{WebSearchConfig: WebSearchConfig{Provider: "finder"}}}); err != nil {
		t.Errorf("choosing it: %v", err)
	}
	if !service.Referenced(RootDB, "finder") {
		t.Error("choosing it did not reference it")
	}
	if _, err := a.handleWebSearchSave(&jsonapi.Call{R: httptest.NewRequest("POST", "/api/web-search", nil)}, withSettings[webSearchForm]{Form: webSearchForm{WebSearchConfig: WebSearchConfig{Provider: "finder"}}}); err != nil {
		t.Errorf("saving it: %v", err)
	}
	if got := a.storedString(SearchTable, "provider"); got != "finder" {
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
		for _, o := range browseFormFields()[1].Options {
			if o.Value == "reader" {
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
	if _, err := a.handleBrowseSave(call, withSettings[BrowseConfig]{Form: BrowseConfig{Source: "reader"}}); err != nil {
		t.Errorf("choosing it: %v", err)
	}
	if !service.Referenced(RootDB, "reader") {
		t.Error("choosing it did not reference it")
	}
	if _, err := a.handleBrowseSave(call, withSettings[BrowseConfig]{Form: BrowseConfig{Source: "reader"}}); err != nil || LoadBrowseConfig().Source != "reader" {
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
	if f, ok := field("provider"); !ok || len(choicesOf(t, f, "mail.send")) != 1 || !strings.Contains(f.Help, "+ Add another…") {
		t.Errorf("SMTP alone: %+v", f)
	}
	if f, _ := field("server"); f.ShowWhen != "provider:smtp" {
		t.Errorf("SMTP's server: %+v", f)
	}
	spec := `{"provider":{"transport":"http","base_url":"https://mail.example.test","auth":{"none":true},
		"operations":{"send":{"method":"POST","path":"/emails","body":{"to":"{to}","subject":"{subject}","text":"{body}"}}}},
		"provides":[{"capability":"mail.send","steps":[{"call":"send"}],"output":{"id":{"path":"id"}}}]}`
	SaveConnector(RootDB, Connector{Name: "postbox", Kind: "service", Spec: json.RawMessage(spec)})
	a := &AdminApp{db: RootDB}
	call := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/mail", nil)}
	if f, ok := field("provider"); !ok || len(choicesOf(t, f, "mail.send")) != 2 || f.Options[1].Value != "postbox" {
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
	if _, err := a.handleMailSave(call, withSettings[MailConfig]{Form: MailConfig{Provider: "postbox", From: "alerts@mail.example"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.handleMail(call); got.(map[string]any)["provider"] != "postbox" {
		t.Errorf("saved: %+v", got)
	}
	if !service.Referenced(RootDB, "postbox") {
		t.Error("choosing it did not reference it")
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
	service.MarkChanged(RootDB, "vec", "someone")
	SetEmbeddingConfig(EmbeddingConfig{})
	a := &AdminApp{db: RootDB}
	out, _ := a.handleEmbeddingsTest(&jsonapi.Call{R: httptest.NewRequest("POST", "/api/embeddings/test", nil)},
		withSettings[EmbeddingConfig]{Form: EmbeddingConfig{Enabled: true, Provider: "vec"}})
	if body, _ := out.(map[string]any); body["ok"] != true {
		t.Fatalf("the test of a choice not yet saved: %+v", out)
	}
	if _, held := service.PendingChange(RootDB, "vec"); !held {
		t.Error("testing it accepted the change")
	}
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Provider: "vec", Endpoint: "vec", Model: "m"})
	if _, err := Embed(context.Background(), "outside the test"); err == nil {
		t.Error("an embedding outside the test reached a connector a change holds")
	}
}
