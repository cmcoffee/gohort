package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// modelWorld is a store and a model server listing the one model it has,
// recording the key each listing was asked with; and a model connection,
// chat, that takes the server's address and its key from its settings,
// keeping the key sk-old for that address.
func modelWorld(t *testing.T) (*AdminApp, *httptest.Server, *[]string) {
	t.Helper()
	saved, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { RootDB, AuthDB = saved, savedAuth })
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path+" "+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"chat-small"}]}`))
	}))
	t.Cleanup(srv.Close)
	spec := `{"config":[{"name":"base_url","label":"Server address","type":"url","required":true},
			{"name":"api_key","label":"API key","type":"secret","send":"bearer"}],
		"provider":{"transport":"http","base_url":"{config.base_url}","auth":{"secret":"api_key"},
		"operations":{"models":{"path":"/models"}}},
		"provides":[{"capability":"llm.chat","dialect":"openai","defaults":{"model":"chat-small"}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "chat", Kind: service.Kind, Spec: json.RawMessage(spec)}); err != nil {
		t.Fatal(err)
	}
	c, _ := GetConnector(RootDB, "chat")
	if err := SetConnectionSecret(RootDB, c, "api_key", "sk-old", []string{srv.URL}); err != nil {
		t.Fatal(err)
	}
	return &AdminApp{db: RootDB}, srv, &got
}

// providedWorld is a store holding the model connections gohort provides, as
// startup installs them.
func providedWorld(t *testing.T) {
	t.Helper()
	saved, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { RootDB, AuthDB = saved, savedAuth })
	ProvideConnectors(RootDB)
}

// tierSave posts a tier's form as the page does.
func tierSave(a *AdminApp, table, body string) (int, string) {
	w := httptest.NewRecorder()
	a.llmConfigRoutes(table, table == LLMTable)(w, httptest.NewRequest("POST", "/api/llm", strings.NewReader(body)))
	return w.Code, w.Body.String()
}

// A tier sets up the model connection it chooses as the other pages do: the
// connection's settings and key show under the choice, a save keeps them in
// its space, and the tier stores no key. Its model runs on them, and a Test
// at another address is refused unless the key is given again for it.
func TestATierSetsUpItsModelConnector(t *testing.T) {
	a, srv, _ := modelWorld(t)
	fields := map[string]string{}
	for _, f := range tierFields([]ui.FormField{{Field: "provider"}, {Field: "model"}}) {
		fields[f.Field] = f.ShowWhen
		if strings.HasPrefix(f.Field, settingsPrefix) && !f.SaveOnBlur {
			t.Errorf("%s saves while typing", f.Field)
		}
	}
	if fields["config/chat/base_url"] != "provider:llm:chat" || fields["config/chat/api_key"] != "provider:llm:chat" {
		t.Errorf("the settings shown: %v", fields)
	}

	a.db.CryptSet(LLMTable, "api_key", "a local server's key")
	if code, body := tierSave(a, LLMTable, `{"provider":"llm:chat","model":"","config/chat/base_url":"`+srv.URL+`","config/chat/api_key":"sk-new"}`); code != 204 {
		t.Fatalf("save: %d %s", code, body)
	}
	c, _ := GetConnector(RootDB, "chat")
	var key string
	if config := ConnectionConfig(RootDB, c); config["base_url"] != srv.URL || len(config) != 1 {
		t.Errorf("the connection's settings: %v", config)
	}
	if a.db.Get(LLMTable, "api_key", &key) || a.db.Get(LLMTable, "config", &map[string]string{}) {
		t.Errorf("the tier kept a key or settings of its own: %q", key)
	}
	if sec, _ := GetConnectionSecret(RootDB, c, "api_key"); sec.Value != "sk-new" {
		t.Errorf("the connection's key: %+v", sec)
	}
	if c, _ := GetConnector(RootDB, "chat"); strings.Contains(string(c.Spec), srv.URL) {
		t.Errorf("the tier's values were written into the spec: %s", c.Spec)
	}
	out, err := a.handleLLMConfig(LLMTable, true)(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/worker-llm", nil)})
	if err != nil {
		t.Fatal(err)
	}
	read := out.(map[string]any)
	if read["config/chat/base_url"] != srv.URL || read["config/chat/api_key"] != secretUnchanged || read["aws_region"] != nil {
		t.Errorf("read back: %v", read)
	}

	// Its model: the connection's address and key, its default model
	// where the tier names none.
	cfg, err := ResolveModelProvider(LLMProviderConfig{Provider: "llm:chat"}, "llm:chat")
	if err != nil || cfg.Provider != "openai" || cfg.Endpoint != srv.URL || cfg.APIKey != "sk-new" || cfg.Model != "chat-small" {
		t.Errorf("the worker's model: %+v %v", cfg, err)
	}
	// A Test at another address: refused rather than the key sent there,
	// unless a key is typed with it.
	other := &media.ModelForm{Config: map[string]string{"base_url": "http://elsewhere.example"}}
	if _, err := ResolveModelProvider(LLMProviderConfig{Provider: "llm:chat", Form: other}, "llm:chat"); err == nil || !strings.Contains(err.Error(), "give the API key again") {
		t.Errorf("the key went to another address: %v", err)
	}
	other.Secrets = map[string]string{"api_key": "sk-there"}
	if cfg, err := ResolveModelProvider(LLMProviderConfig{Provider: "llm:chat", Form: other}, "llm:chat"); err != nil || cfg.APIKey != "sk-there" || cfg.Endpoint != "http://elsewhere.example" {
		t.Errorf("a Test with its key: %+v %v", cfg, err)
	}
}

// The models gohort provides are connections, local servers among them: a
// tier naming one directly is refused before it is provided, and runs on it
// after, a local server's address and key going to its connection. A tier
// on an outdated connection shows the one provided, with the address it
// kept, and saves onto it. Each is offered by its name first, and cannot be
// deleted.
func TestATierNamesAModelConnector(t *testing.T) {
	a, _, _ := modelWorld(t)
	for provider, pm := range providedModels {
		code, body := tierSave(a, LLMTable, `{"provider":"`+provider+`","api_key":"sk"}`)
		if code != 400 || !strings.Contains(body, pm.template+" template") {
			t.Errorf("%s: %d %s", provider, code, body)
		}
	}
	for _, o := range LLMProviderOptions(false) {
		if _, bare := providedModels[o.Value]; bare {
			t.Errorf("the provider list offers %s directly", o.Value)
		}
	}

	ProvideConnectors(RootDB)
	var order []string
	labels := map[string]string{}
	for _, o := range LLMProviderOptions(false) {
		order, labels[o.Value] = append(order, o.Value), o.Label
	}
	seen := map[string]bool{}
	for _, v := range order {
		if seen[v] {
			t.Errorf("%s is offered twice", v)
		}
		seen[v] = true
	}
	if len(order) < 7 || order[0] != "llm:ollama" || order[1] != "llm:llamacpp" || order[3] != "llm:anthropic" ||
		labels["llm:llamacpp"] != "llama.cpp" || labels["llm:bedrock"] != "AWS Bedrock" {
		t.Errorf("the choices: %v %v", order, labels)
	}
	if code, body := tierSave(a, LLMTable, `{"provider":"vllm","model":"qwen","endpoint":"http://gpu.example:8000/v1","api_key":"vk"}`); code != 204 {
		t.Fatalf("a bare local server: %d %s", code, body)
	}
	var worker, key string
	vllm, _ := GetConnector(RootDB, "vllm")
	sec, _ := GetConnectionSecret(RootDB, vllm, "api_key")
	if a.db.Get(LLMTable, "provider", &worker); worker != "llm:vllm" || ConnectionConfig(RootDB, vllm)["base_url"] != "http://gpu.example:8000/v1" ||
		sec.Value != "vk" || a.db.Get(LLMTable, "api_key", &key) {
		t.Errorf("the worker: %q, its connection %v %+v, a key of its own %q", worker, ConnectionConfig(RootDB, vllm), sec, key)
	}
	if cfg := ResolvedTier(LLMProviderConfig{Provider: worker, Model: "qwen"}); cfg.Provider != "vllm" || cfg.Endpoint != "http://gpu.example:8000/v1" || cfg.APIKey != "vk" {
		t.Errorf("the worker resolves to %+v", cfg)
	}
	if !ProviderIsLocal(worker) || ProviderHasNativeTools("llm:ollama") || !ProviderHasNativeTools(worker) {
		t.Error("a local server's connection is judged as another provider")
	}

	// A lead still naming a local server directly, with the address it
	// kept, and one on an outdated connection.
	a.db.Set(LeadLLMTable, "provider", "llama.cpp")
	a.db.Set(LeadLLMTable, "endpoint", "http://box.example:8080/v1")
	out, _ := a.handleLLMConfig(LeadLLMTable, false)(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/lead-llm", nil)})
	if read := out.(map[string]any); read["provider"] != "llm:llamacpp" || read["config/llamacpp/base_url"] != "http://box.example:8080/v1" {
		t.Errorf("a lead on llama.cpp reads as %v", read)
	}
	old := Connector{Name: "anthropic-model", Kind: service.Kind, Template: "anthropic-model",
		Spec: json.RawMessage(`{"provider":{"transport":"http","base_url":"https://api.anthropic.com","auth":{"credential":"anthropic_model"},"operations":{"models":{"path":"/v1/models"}}},"provides":[{"capability":"llm.chat","dialect":"anthropic"}]}`)}
	RootDB.Set("connectors", old.Name, old)
	a.db.Set(LeadLLMTable, "provider", "llm:anthropic-model")
	out, _ = a.handleLLMConfig(LeadLLMTable, false)(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/lead-llm", nil)})
	if read := out.(map[string]any); read["provider"] != "llm:anthropic" {
		t.Errorf("a lead on an outdated connection reads as %v", read["provider"])
	}
	for _, o := range LLMProviderOptions(true) {
		if o.Value == "llm:anthropic-model" {
			t.Errorf("the outdated connection is offered beside the one in its place: %v", o)
		}
	}
	if code, body := tierSave(a, LeadLLMTable, `{"provider":"llm:anthropic-model","model":"claude-opus-5"}`); code != 204 {
		t.Fatalf("saving the outdated one: %d %s", code, body)
	}
	var lead string
	if a.db.Get(LeadLLMTable, "provider", &lead); lead != "llm:anthropic" {
		t.Errorf("the lead runs on %q", lead)
	}

	rows, _ := a.handleConnectors(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/connectors", nil)})
	b, _ := json.Marshal(rows)
	var list []map[string]any
	json.Unmarshal(b, &list)
	for _, r := range list {
		if r["name"] == "openai" && (r["deletable"] == true || r["owner"] != "gohort" || r["revertable"] == true) {
			t.Errorf("a provided row: %v", r)
		}
	}
	openai, _ := GetConnector(RootDB, "openai")
	openai.Spec = json.RawMessage(strings.Replace(string(openai.Spec), `"every_mins":60`, `"every_mins":30`, 1))
	if err := SaveConnector(RootDB, openai); err != nil {
		t.Fatal(err)
	}
	if openai, _ = GetConnector(RootDB, "openai"); !service.Modified(openai) {
		t.Fatalf("the edit did not take: %s", openai.Spec)
	}
	if _, err := a.handleConnectorAction(&jsonapi.Call{R: httptest.NewRequest("POST", "/api/connectors?action=revert&name=openai", nil)}, nil); err != nil {
		t.Errorf("revert: %v", err)
	}
	if openai, _ = GetConnector(RootDB, "openai"); service.Modified(openai) {
		t.Error("the row's Revert left the edit")
	}
	if _, err := a.handleConnectorDelete(&jsonapi.Call{R: httptest.NewRequest("DELETE", "/api/connectors?name=vllm", nil)}, struct{}{}); err == nil {
		t.Error("a provided connection was deleted")
	}
}

// List models runs on the form, before a save: a connector not yet in use,
// at the address the form gives, with its credential's key; never a key the
// tier stored for a local server.
func TestATiersModelListRunsOnItsForm(t *testing.T) {
	a, srv, got := modelWorld(t)
	a.db.CryptSet(LLMTable, "api_key", "a local server's key")
	form := postedForm[llmModelsQuery](t, `{"provider":"llm:chat","config/chat/base_url":"`+srv.URL+`"}`)
	out, _ := a.handleLLMModels(LLMTable)(&jsonapi.Call{R: httptest.NewRequest("POST", "/api/worker-llm/models", nil)}, form)
	if body, _ := out.(map[string]any); body["error"] != nil || len(*got) != 1 || !strings.HasSuffix((*got)[0], "/models Bearer sk-old") {
		t.Errorf("the list: %v, asked %v", out, *got)
	}
	if c, _ := GetConnector(RootDB, "chat"); c.Approved {
		t.Error("listing its models put the connector to use")
	}
}
