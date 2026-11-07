package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The search, mail, embedding and transcription GETs returned their keys in
// the clear to the browser. Mail's reads as a placeholder now, and the
// placeholder coming back on a save keeps the stored key. Web search,
// transcription and image generation show none: the key is the connector's
// or the peer's.
func TestAdminSecretsAreMaskedAndKept(t *testing.T) {
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	sub := http.NewServeMux()
	a.registerNetConfigRoutes(sub)
	a.registerMediaRoutes(sub)

	const secret = "sk-test-0123456789"
	a.db.Set(SearchTable, "provider", "brave")
	a.db.Set(SearchTable, "api_key", secret)
	a.db.Set(MailTable, "password", secret)
	a.db.Set(TranscribeTable, "current", TranscribeConfig{Endpoint: "http://127.0.0.1:1", APIKey: secret})

	for _, path := range []string{"/api/web-search", "/api/mail", "/api/transcribe"} {
		keyless := path != "/api/mail"
		w := httptest.NewRecorder()
		sub.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("GET %s returned the stored secret: %s", path, w.Body.String())
		}
		if !keyless && !strings.Contains(w.Body.String(), secretUnchanged) {
			t.Errorf("GET %s should show that a secret is set: %s", path, w.Body.String())
		}
	}

	post := func(path string, body map[string]any) {
		t.Helper()
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		sub.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(b))))
		if w.Code >= 300 {
			t.Fatalf("POST %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	post("/api/mail", map[string]any{"server": "smtp.example.com:587", "password": secretUnchanged})
	if got := a.storedString(MailTable, "password"); got != secret {
		t.Errorf("the placeholder overwrote the mail password with %q", got)
	}
}

// Web Search's Test searches with the connector chosen in the form before a
// save puts it to use, and leaves it out of use.
func TestWebSearchTestTriesTheChoiceBeforeItIsSaved(t *testing.T) {
	saved, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { RootDB, AuthDB = saved, savedAuth })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hits":[{"title":"A result","url":"https://example.org/a"}]}`))
	}))
	defer srv.Close()
	spec := `{"provider":{"transport":"http","base_url":"` + srv.URL + `","auth":{"none":true},"operations":{"find":{"path":"/s?q={query}"}}},
		"provides":[{"capability":"web.search","steps":[{"call":"find"}],"output":{"results":{"path":"hits","fields":{"title":"title","url":"url"}}}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "finder", Kind: "service", Spec: json.RawMessage(spec)}); err != nil {
		t.Fatal(err)
	}
	a := &AdminApp{db: RootDB}
	out, _ := a.handleWebSearchTest(&jsonapi.Call{R: httptest.NewRequest("POST", "/api/web-search/test", nil)},
		withSettings[webSearchForm]{Form: webSearchForm{WebSearchConfig: WebSearchConfig{Source: "local", Provider: "finder"}}})
	if body, _ := out.(map[string]any); body["ok"] != true {
		t.Errorf("the test of a choice not yet saved: %+v", out)
	}
	if c, _ := GetConnector(RootDB, "finder"); c.Approved {
		t.Error("testing it put it to use")
	}
}

// Mail's Test sends with the connector chosen in the form before a save puts
// it to use, and leaves it out of use.
func TestMailTestTriesTheChoiceBeforeItIsSaved(t *testing.T) {
	saved, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { RootDB, AuthDB = saved, savedAuth })
	sent := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"m-1"}`))
	}))
	defer srv.Close()
	spec := `{"provider":{"transport":"http","base_url":"` + srv.URL + `","auth":{"none":true},
		"operations":{"send":{"method":"POST","path":"/emails","body":{"to":"{to}","subject":"{subject}","text":"{body}"}}}},
		"provides":[{"capability":"mail.send","steps":[{"call":"send"}],"output":{"id":{"path":"id"}}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "postbox", Kind: "service", Spec: json.RawMessage(spec)}); err != nil {
		t.Fatal(err)
	}
	a := &AdminApp{db: RootDB}
	out, _ := a.handleMailTest(&jsonapi.Call{R: httptest.NewRequest("POST", "/api/mail/test", nil)},
		withSettings[MailConfig]{Form: MailConfig{Provider: "postbox", From: "gohort@example.test", Recipient: "admin@example.test"}})
	if body, _ := out.(map[string]any); body["ok"] != true || sent != 1 {
		t.Errorf("the test of a choice not yet saved: %+v, %d sent", out, sent)
	}
	if c, _ := GetConnector(RootDB, "postbox"); c.Approved {
		t.Error("testing it put it to use")
	}
}

// Web Search offers the engines; a research source is never listed as one.
// "Use a research source…" and the list under it pick one, and what is saved
// is the source's own connector, shown back the same way.
func TestWebSearchKeepsResearchSourcesApart(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	search := func(base string) json.RawMessage {
		return json.RawMessage(`{"provider":{"transport":"http","base_url":"` + base + `","auth":{"none":true},"operations":{"find":{"path":"/s?q={query}"}}},"provides":[{"capability":"web.search","steps":[{"call":"find"}],"output":{"results":{"path":"hits","fields":{"title":"t","url":"u"}}}}]}`)
	}
	for name, base := range map[string]string{"engine": "https://engine.example.test", "caselaw": "https://law.example.test"} {
		if err := SaveConnector(RootDB, Connector{Name: name, Kind: service.Kind, Spec: search(base)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.SaveResearchSource(RootDB, service.ResearchSource{Connector: "caselaw", Always: true}); err != nil {
		t.Fatal(err)
	}
	fields := webSearchFormFields()
	provider, _ := findField(fields, "provider")
	var values []string
	for _, o := range choicesOf(t, provider, "web.search") {
		values = append(values, o.Value)
	}
	if strings.Join(values, ",") != "duckduckgo,engine,@research" {
		t.Errorf("the engines: %v", values)
	}
	if f, ok := findField(fields, "research_source"); !ok || f.ShowWhen != "provider:@research" || len(f.Options) != 1 || f.Options[0].Value != "caselaw" {
		t.Errorf("the research source list: %+v", f)
	}

	a := &AdminApp{db: RootDB}
	call := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/web-search", nil)}
	save := func(f webSearchForm) error {
		_, err := a.handleWebSearchSave(call, withSettings[webSearchForm]{Form: f})
		return err
	}
	if err := save(webSearchForm{WebSearchConfig: WebSearchConfig{Provider: "@research"}}); err == nil || !strings.Contains(err.Error(), "which research source") {
		t.Errorf("no source chosen: %v", err)
	}
	if err := save(webSearchForm{WebSearchConfig: WebSearchConfig{Provider: "@research"}, ResearchSource: "caselaw"}); err != nil {
		t.Fatal(err)
	}
	if got := a.storedString(SearchTable, "provider"); got != "caselaw" {
		t.Errorf("stored: %q", got)
	}
	out, _ := a.handleWebSearch(call)
	if got := out.(map[string]any); got["provider"] != "@research" || got["research_source"] != "caselaw" {
		t.Errorf("shown back: %+v", got)
	}
	if err := save(webSearchForm{WebSearchConfig: WebSearchConfig{Provider: "none"}}); err != nil {
		t.Fatal(err)
	}
	if got := a.storedString(SearchTable, "provider"); got != "none" {
		t.Errorf("disabled, stored: %q", got)
	}
}

// Disabled turns a job off and keeps what was chosen before, so choosing it
// again starts from it; the page shows Disabled while it is off.
func TestDisabledKeepsTheEarlierChoice(t *testing.T) {
	srv, _ := settingsWorld(t)
	embedConnector(t, "vec", srv.URL, "m")
	a := &AdminApp{db: RootDB}
	call := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/embeddings", nil)}
	save := func(provider string) {
		t.Helper()
		if _, err := a.handleEmbeddingsSave(call, withSettings[EmbeddingConfig]{Form: EmbeddingConfig{Provider: provider}}); err != nil {
			t.Fatalf("saving %s: %v", provider, err)
		}
	}
	save("vec")
	if cfg := a.storedEmbeddingConfig(); !cfg.Enabled || cfg.Provider != "vec" {
		t.Fatalf("on: %+v", cfg)
	}
	save("none")
	if cfg := a.storedEmbeddingConfig(); cfg.Enabled || cfg.Provider != "vec" {
		t.Errorf("off, the earlier choice kept: %+v", cfg)
	}
	out, _ := a.handleEmbeddings(call)
	if got := out.(map[string]any)["provider"]; got != "none" {
		t.Errorf("shown while off: %v", got)
	}
}
