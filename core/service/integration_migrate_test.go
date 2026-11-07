package service

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/recipes"
	"github.com/cmcoffee/gohort/core/sourcehooks"
)

// storedRecord keeps a connector record as an older build stored it,
// without the checks a save makes now: a plan B model connection, a spec
// naming a vault credential.
func storedRecord(t *testing.T, name string, rec map[string]any) Connector {
	t.Helper()
	rec["name"] = name
	if rec["id"] == nil {
		rec["id"] = "c_" + strings.ReplaceAll(name, "-", "_")
	}
	if rec["kind"] == nil {
		rec["kind"] = Kind
	}
	raw, _ := json.Marshal(rec)
	var stored struct {
		Name     string          `json:"name"`
		ID       string          `json:"id"`
		Kind     string          `json:"kind"`
		Spec     json.RawMessage `json:"spec"`
		Template string          `json:"template"`
		Provided bool            `json:"provided"`
		Approved bool            `json:"approved"`
	}
	json.Unmarshal(raw, &stored)
	RootDB.Set("connectors", name, stored)
	c, ok := GetConnector(RootDB, name)
	if !ok {
		t.Fatalf("%s did not store", name)
	}
	return c
}

// installed adds a connector from template, named name.
func installed(t *testing.T, template, name string) Connector {
	t.Helper()
	if _, err := recipes.Install(RootDB, template, "", map[string]string{"name": name}); err != nil {
		t.Fatal(err)
	}
	c, ok := GetConnector(RootDB, name)
	if !ok {
		t.Fatalf("%s made no %s", template, name)
	}
	return c
}

// sameSpace is "" when vectors made under before and after are in the same
// space (EmbedVersion), else the two.
func sameSpace(before, after EmbeddingConfig) string {
	prev := GetEmbeddingConfig()
	defer SetEmbeddingConfig(prev)
	SetEmbeddingConfig(before)
	was := EmbedVersion()
	SetEmbeddingConfig(after)
	if now := EmbedVersion(); now != was {
		return was + " -> " + now
	}
	return ""
}

// str is a string setting in table.
func str(table, key string) string {
	var s string
	RootDB.Get(table, key, &s)
	return s
}

// The install that runs plan B (as the owner's does): each tier off the
// connection it ran on, with its address, model and key, the key still
// going only where it went; the pages naming their connections plainly; the
// embeddings' space the same; nothing left needing setting again; and the
// model connections cleared by the cleanup after. Run again, it changes
// nothing.
func TestTheMigrationFromPlanB(t *testing.T) {
	world(t)
	vllm := storedRecord(t, "vllm", map[string]any{"provided": true, "spec": json.RawMessage(`{"config":[{"name":"base_url","type":"url","default":"http://localhost:8000/v1"},{"name":"api_key","type":"secret","send":"bearer"}],"provider":{"transport":"http","base_url":"{config.base_url}","auth":{"secret":"api_key"}},"provides":[{"capability":"llm.chat","dialect":"vllm"}]}`)})
	SetConnectionConfig(RootDB, vllm, map[string]string{"base_url": "http://gpu.example.test:8000/v1"})
	RootDB.Set(LLMTable, "provider", "llm:vllm")
	RootDB.Set(LLMTable, "model", "qwen-27b")
	lead := storedRecord(t, "anthropic-model", map[string]any{"spec": json.RawMessage(`{"config":[{"name":"api_key","type":"secret","send":"header:x-api-key"}],"provider":{"transport":"http","base_url":"https://api.anthropic.com","auth":{"secret":"api_key"}},"provides":[{"capability":"llm.chat","dialect":"anthropic","defaults":{"model":"claude-lead"}}]}`)})
	SetConnectionSecret(RootDB, lead, "api_key", "sk-lead", []string{"https://api.anthropic.com"})
	RootDB.Set(LeadLLMTable, "provider", "llm:anthropic-model")

	searchConnector(t, "serper-search", "")
	RootDB.Set(SearchTable, "provider", "search:serper-search")
	installed(t, "openai-compatible-embeddings", "local-embeddings")
	emb := EmbeddingConfig{Enabled: true, Provider: "embed:local-embeddings", Endpoint: "http://alpaca.example.test:8080/v1", Model: "nomic"}
	RootDB.Set(EmbeddingTable, "current", emb)
	installed(t, "openai-compatible-transcription", "local-transcription")
	RootDB.Set(TranscribeTable, "current", media.TranscribeConfig{Enabled: true, Provider: "transcribe:local-transcription"})
	installed(t, "openai-compatible-speech", "local-speech")
	RootDB.Set(media.SpeechTable, "current", media.SpeechConfig{Enabled: true, Provider: "speak:local-speech", Voice: "af_heart"})
	searchConnector(t, "mailer", "")
	RootDB.Set(MailTable, "provider", "service:mailer")
	RootDB.Set(MusicTable, "agents", []string{"service:mailer"})
	if len(StaleSettings(RootDB)) == 0 {
		t.Fatal("plan B's settings should read as needing setting again before the migration")
	}

	plan := IntegrationPlan(RootDB)
	if str(LLMTable, "provider") != "llm:vllm" || len(plan) != 8 {
		t.Fatalf("the dry run changed something, or planned %d:\n%s", len(plan), strings.Join(plan, "\n"))
	}
	MigrateIntegration(RootDB)

	if str(LLMTable, "provider") != "vllm" || str(LLMTable, "endpoint") != "http://gpu.example.test:8000/v1" || str(LLMTable, "model") != "qwen-27b" {
		t.Errorf("worker: %s %s %s", str(LLMTable, "provider"), str(LLMTable, "endpoint"), str(LLMTable, "model"))
	}
	if str(LeadLLMTable, "provider") != "anthropic" || str(LeadLLMTable, "endpoint") != "" || str(LeadLLMTable, "model") != "claude-lead" {
		t.Errorf("lead: %s %q %s", str(LeadLLMTable, "provider"), str(LeadLLMTable, "endpoint"), str(LeadLLMTable, "model"))
	}
	cfg := LLMProviderConfig{Provider: "anthropic"}
	if err := cfg.LoadKey(RootDB, LeadLLMTable); err != nil || cfg.APIKey != "sk-lead" {
		t.Errorf("the lead's key: %q %v", cfg.APIKey, err)
	}
	if cfg := (LLMProviderConfig{Provider: "anthropic", Endpoint: "https://elsewhere.example.test"}); cfg.LoadKey(RootDB, LeadLLMTable) == nil {
		t.Error("the moved key would go to another address")
	}
	for _, p := range []struct{ table, key, want string }{
		{SearchTable, "provider", "serper-search"}, {MailTable, "provider", "mailer"},
	} {
		if got := str(p.table, p.key); got != p.want {
			t.Errorf("%s: %q", p.table, got)
		}
	}
	var music []string
	RootDB.Get(MusicTable, "agents", &music)
	var after EmbeddingConfig
	RootDB.Get(EmbeddingTable, "current", &after)
	var tr media.TranscribeConfig
	RootDB.Get(TranscribeTable, "current", &tr)
	var sp media.SpeechConfig
	RootDB.Get(media.SpeechTable, "current", &sp)
	if strings.Join(music, ",") != "mailer" || after.Provider != "local-embeddings" || tr.Provider != "local-transcription" || sp.Provider != "local-speech" || sp.Voice != "af_heart" {
		t.Errorf("pages: %v %q %q %q", music, after.Provider, tr.Provider, sp.Provider)
	}
	if sameSpace(emb, after) != "" {
		t.Errorf("the embeddings' space moved: %s", sameSpace(emb, after))
	}
	if stale := StaleSettings(RootDB); len(stale) > 0 {
		t.Errorf("still needing setting again: %+v", stale)
	}
	if !IntegrationMigrated(RootDB) || len(IntegrationPlan(RootDB)) != 0 {
		t.Errorf("run again it would change: %v", IntegrationPlan(RootDB))
	}
	if n, err := clearOldRecords(RootDB); err != nil || n != 2 {
		t.Errorf("cleared %d: %v", n, err)
	}
	for _, gone := range []string{"vllm", "anthropic-model"} {
		if _, ok := GetConnector(RootDB, gone); ok {
			t.Errorf("the model connection %s outlived its tier's move", gone)
		}
	}
}

// A tier on a connection takes the address its spec defaults to and the
// model its connection was set to, when the tier names none; one on a
// connection speaking no provider gohort runs is left, needing setting
// again.
func TestATierFromAConnectionReadsItsDefaults(t *testing.T) {
	world(t)
	c := storedRecord(t, "llamacpp", map[string]any{"spec": json.RawMessage(`{"config":[{"name":"base_url","type":"url","default":"http://localhost:8080/v1"},{"name":"model"}],"provider":{"transport":"http","base_url":"{config.base_url}","auth":{"none":true}},"provides":[{"capability":"llm.chat","dialect":"llama.cpp"}]}`)})
	SetConnectionConfig(RootDB, c, map[string]string{"model": "gemma-local"})
	RootDB.Set(LLMTable, "provider", "llm:llamacpp")
	storedRecord(t, "odd-model", map[string]any{"spec": json.RawMessage(`{"provider":{"transport":"http","base_url":"https://odd.example.test","auth":{"none":true}},"provides":[{"capability":"llm.chat","dialect":"nonesuch"}]}`)})
	RootDB.Set(LeadLLMTable, "provider", "llm:odd-model")
	MigrateIntegration(RootDB)
	if str(LLMTable, "provider") != "llama.cpp" || str(LLMTable, "endpoint") != "http://localhost:8080/v1" || str(LLMTable, "model") != "gemma-local" {
		t.Errorf("worker: %s %s %s", str(LLMTable, "provider"), str(LLMTable, "endpoint"), str(LLMTable, "model"))
	}
	if str(LeadLLMTable, "provider") != "llm:odd-model" || !strings.Contains(stalePages(StaleSettings(RootDB)), "Lead LLM") {
		t.Errorf("lead: %s", str(LeadLLMTable, "provider"))
	}
}

// Embeddings at this instance's own server become the connector for its
// kind of server, at the address under which it answers.
func TestOwnEmbeddingServersKeepTheirAddress(t *testing.T) {
	for endpoint, want := range map[string]string{
		"http://gpu.example.test:8080/v1/embeddings": "local-embeddings http://gpu.example.test:8080/v1",
		"http://gpu.example.test:8080/v1/":           "local-embeddings http://gpu.example.test:8080/v1",
		"http://gpu.example.test:11434/api/embed":    "ollama-embeddings http://gpu.example.test:11434",
	} {
		world(t)
		RootDB.Set(EmbeddingTable, "current", EmbeddingConfig{Enabled: true, Provider: "local", Endpoint: endpoint})
		MigrateIntegration(RootDB)
		var emb EmbeddingConfig
		RootDB.Get(EmbeddingTable, "current", &emb)
		c, _ := GetConnector(RootDB, emb.Provider)
		if got := emb.Provider + " " + ConnectionConfig(RootDB, c)["base_url"]; got != want || emb.Endpoint != endpoint {
			t.Errorf("%s: %s (endpoint %s)", endpoint, got, emb.Endpoint)
		}
	}
}

// A tier's key kept by its connection for another address than the tier
// now goes to is not moved: it goes only where it was given for.
func TestATierKeyGivenElsewhereIsNotMoved(t *testing.T) {
	world(t)
	c := storedRecord(t, "openai-model", map[string]any{"spec": json.RawMessage(`{"provider":{"transport":"http","base_url":"https://gateway.example.test/v1","auth":{"secret":"api_key"}},"provides":[{"capability":"llm.chat","dialect":"openai"}]}`)})
	SetConnectionSecret(RootDB, c, "api_key", "sk-other", []string{"https://somewhere.example.test"})
	RootDB.Set(LLMTable, "provider", "llm:openai-model")
	MigrateIntegration(RootDB)
	if str(LLMTable, "provider") != "openai" || str(LLMTable, "endpoint") != "https://gateway.example.test/v1" {
		t.Errorf("tier: %s %s", str(LLMTable, "provider"), str(LLMTable, "endpoint"))
	}
	var key string
	if RootDB.Get(LLMTable, "api_key", &key) {
		t.Errorf("a key given for another address moved: %q", key)
	}
}

// An install from before plan B: the tier's key bound to where it was sent;
// engines and image providers gohort ran itself made connectors from their
// templates with their keys; its own servers made connectors at their
// addresses, the embeddings' space the same; a connector keyed by a vault
// credential keeping its key itself, the credential gone; research written
// into a spec and a Source Hook made Research Sources; image backends made
// connections; and nothing left needing setting again.
func TestTheMigrationFromBeforePlanB(t *testing.T) {
	world(t)
	RootDB.Set(LLMTable, "provider", "anthropic")
	RootDB.CryptSet(LLMTable, "api_key", "sk-old")
	RootDB.Set(SearchTable, "provider", "serper")
	RootDB.Set(SearchTable, "api_key", "serper-key")
	RootDB.Set(ImageTable, "provider", "openai")
	RootDB.Set(ImageTable, "api_key", "sk-img")
	emb := EmbeddingConfig{Enabled: true, Endpoint: "http://alpaca.example.test:11434/api", Model: "nomic-embed-text"}
	RootDB.Set(EmbeddingTable, "current", emb)
	RootDB.Set(TranscribeTable, "current", media.TranscribeConfig{Enabled: true, Provider: "local", Endpoint: "http://alpaca.example.test:8089/v1/audio/transcriptions", Model: "whisper-1"})
	RootDB.Set(media.SpeechTable, "current", media.SpeechConfig{Enabled: true, Endpoint: "http://alpaca.example.test:8880/v1", Model: "kokoro", Voice: "bm_george"})

	if err := Secure().Save(SecureCredential{Name: "acme_cred", Type: SecureCredHeader, ParamName: "X-Key", BaseURL: "https://api.acme.test"}, "acme-secret"); err != nil {
		t.Fatal(err)
	}
	if err := Secure().Save(SecureCredential{Name: "serper_cred", Type: SecureCredHeader, ParamName: "X-API-KEY", BaseURL: "https://google.serper.dev"}, "serper-old"); err != nil {
		t.Fatal(err)
	}
	storedRecord(t, "old-serper", map[string]any{"template": "serper-search", "spec": json.RawMessage(`{"provider":{"transport":"http","base_url":"https://google.serper.dev","auth":{"credential":"serper_cred"},"operations":{"find":{"url":"/search"}}},"provides":[{"capability":"web.search","steps":[{"call":"find"}],"output":{"results":{"path":"organic","fields":{"title":"title","url":"link"}}}}]}`)})
	storedRecord(t, "acme", map[string]any{"spec": json.RawMessage(`{"provider":{"transport":"http","base_url":"https://api.acme.test","auth":{"credential":"acme_cred"},"operations":{"find":{"url":"/find?q={query}"}}},"provides":[{"capability":"web.search","steps":[{"call":"find"}],"output":{"results":{"path":"hits","fields":{"title":"name","url":"href"}}}}]}`)})
	storedRecord(t, "cases", map[string]any{"approved": true, "spec": json.RawMessage(`{"provider":{"transport":"http","base_url":"https://cases.example.test","auth":{"none":true},"operations":{"find":{"url":"/find?q={query}"}}},"provides":[{"capability":"web.search","steps":[{"call":"find"}],"output":{"results":{"path":"hits","fields":{"title":"name","url":"href"}}},"research":{"topics":["legal"]},"tool":{"name":"case_search","description":"Court opinions."}}]}`)})
	RootDB.Set(sourceHookTable, "docs_index", sourcehooks.SourceHook{Name: "Docs Index", Type: sourcehooks.HookTypeAPI, Endpoint: "https://docs.example.test/search",
		QueryParam: "q", ResultsPath: "results", TriggerDomains: []string{"tech"}, ExposeToLLM: true, ToolName: "docs_search"})
	RootDB.Set(sourceHookTable, "westlaw", sourcehooks.SourceHook{Name: "Westlaw", Type: sourcehooks.HookTypePaywall})

	if err := Secure().Save(SecureCredential{Name: "sd_login", Type: SecureCredBasicAuth, Username: "artist", BaseURL: "http://sd.example.test:7860"}, "pw"); err != nil {
		t.Fatal(err)
	}
	a1111, _ := json.Marshal(RestImageSpec{Credential: "sd_login", SubmitURL: "http://sd.example.test:7860/sdapi/v1/txt2img", SubmitMethod: "POST",
		SubmitBody: `{"prompt":"{prompt}","steps":{steps},"seed":{seed},"width":{width},"height":{height}}`, ImageB64Path: "images.0"})
	storedRecord(t, "sd", map[string]any{"kind": restImageKind, "spec": json.RawMessage(a1111)})
	if err := Secure().Save(SecureCredential{Name: "comfy_key", Type: SecureCredHeader, ParamName: "X-Comfy-Key", BaseURL: "http://comfy.example.test:8188"}, "ck"); err != nil {
		t.Fatal(err)
	}
	comfy, _, err := NewComfyImageSpec("http://comfy.example.test:8188", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	comfy.Credential = "comfy_key"
	comfyRaw, _ := json.Marshal(comfy)
	storedRecord(t, "comfy", map[string]any{"kind": restImageKind, "spec": json.RawMessage(comfyRaw)})
	RootDB.Set(ImageTable, "agents", []string{"sd", "comfy"})
	mcp, _ := json.Marshal(map[string]any{"url": "http://sd.example.test:7860/mcp", "auth_mode": "secure_api", "secure_cred": "sd_login"})
	if err := SaveConnector(RootDB, Connector{Name: "sd-tools", Kind: RemoteMCPConnectorKind, Spec: mcp}); err != nil {
		t.Fatal(err)
	}
	RootDB.Set("remote_peers", "den", RemotePeer{Name: "den"})
	peerRenderer, _ := json.Marshal(RestImageSpec{Credential: "peer_den_key", SubmitURL: "https://den.example.test/api/peer/v1/images/render", ImageB64Path: "images.0"})
	storedRecord(t, "peer-den-flux", map[string]any{"kind": restImageKind, "spec": json.RawMessage(peerRenderer)})

	var whys []string
	for _, st := range StaleSettings(RootDB) {
		whys = append(whys, st.Page+": "+st.Why)
	}
	for _, want := range []string{"Worker LLM: its key was kept before keys were bound", "Embeddings: it was set up before every embedder",
		"Audio Transcription (STT): it was set up before every transcriber", "Speech (TTS): it was set up before every speech server",
		"Media Generation: it names openai, which gohort no longer runs itself", "Media Generation: offered to agents, it names sd, an image backend in a shape"} {
		if !strings.Contains(strings.Join(whys, "\n"), want) {
			t.Errorf("before the migration, missing %q in:\n%s", want, strings.Join(whys, "\n"))
		}
	}
	MigrateIntegration(RootDB)

	cfg := LLMProviderConfig{Provider: "anthropic"}
	if err := cfg.LoadKey(RootDB, LLMTable); err != nil || cfg.APIKey != "sk-old" {
		t.Errorf("the tier's key: %q %v", cfg.APIKey, err)
	}
	keyed := func(name, capability string) Connector {
		t.Helper()
		c, ok := GetConnector(RootDB, name)
		if !ok {
			t.Fatalf("no connector %s", name)
		}
		s, err := Parse(c.Spec)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if f, ok := s.secretField(); !ok || !HasSecret(c, f.Name) {
			t.Errorf("%s keeps no key", name)
		}
		return c
	}
	if p := str(SearchTable, "provider"); p != "serper-search" || str(SearchTable, "api_key") != "" {
		t.Errorf("search: %q", p)
	}
	keyed("serper-search", "web.search")
	if p := str(ImageTable, "provider"); p != "openai-images" || str(ImageTable, "api_key") != "" {
		t.Errorf("image: %q", p)
	}
	keyed("openai-images", "image.generate")

	var after EmbeddingConfig
	RootDB.Get(EmbeddingTable, "current", &after)
	if after.Provider != "ollama-embeddings" || sameSpace(emb, after) != "" {
		t.Errorf("embeddings: %+v %s", after, sameSpace(emb, after))
	}
	if c, _ := GetConnector(RootDB, "ollama-embeddings"); ConnectionConfig(RootDB, c)["base_url"] != "http://alpaca.example.test:11434" {
		t.Errorf("the embedder's address: %v", ConnectionConfig(RootDB, c))
	}
	var tr media.TranscribeConfig
	RootDB.Get(TranscribeTable, "current", &tr)
	if c, _ := GetConnector(RootDB, tr.Provider); tr.Provider != "local-transcription" || ConnectionConfig(RootDB, c)["base_url"] != "http://alpaca.example.test:8089/v1" {
		t.Errorf("transcription: %+v %v", tr, ConnectionConfig(RootDB, c))
	}
	var sp media.SpeechConfig
	RootDB.Get(media.SpeechTable, "current", &sp)
	if c, _ := GetConnector(RootDB, sp.Provider); sp.Provider != "local-speech" || sp.Voice != "bm_george" || ConnectionConfig(RootDB, c)["base_url"] != "http://alpaca.example.test:8880/v1" ||
		ConnectionConfig(RootDB, c)["model"] != "kokoro" || ConnectionConfig(RootDB, c)["voice"] != "bm_george" {
		t.Errorf("speech: %+v %v", sp, ConnectionConfig(RootDB, c))
	}

	oldSerper := keyed("old-serper", "web.search")
	if tmpl, _ := recipes.TemplateConnectorSpec("serper-search"); string(oldSerper.Spec) != string(tmpl) {
		t.Errorf("old-serper is not made again from its template: %s", oldSerper.Spec)
	}
	if kept, _ := GetConnectionSecret(RootDB, oldSerper, "api_key"); kept.Value != "serper-old" {
		t.Errorf("old-serper's key: %q", kept.Value)
	}
	if c, _ := GetConnector(RootDB, "comfy"); !strings.Contains(string(c.Spec), `"header:X-Comfy-Key"`) {
		t.Errorf("comfy sends its key otherwise than its credential did: %s", c.Spec)
	}
	acme := keyed("acme", "web.search")
	if kept, _ := GetConnectionSecret(RootDB, acme, "api_key"); kept.Value != "acme-secret" || !strings.Contains(string(acme.Spec), `"header:X-Key"`) {
		t.Errorf("acme's key: %q %s", kept.Value, acme.Spec)
	}
	if _, ok := Secure().Load("acme_cred"); ok {
		t.Error("the credential whose key moved is still in the vault")
	}
	if rs, ok := GetResearchSource(RootDB, "cases"); !ok || rs.Off || rs.ToolName != "case_search" || strings.Join(rs.Topics, ",") != "legal" {
		t.Errorf("cases: %+v", rs)
	}
	if c, _ := GetConnector(RootDB, "cases"); strings.Contains(string(c.Spec), "research") {
		t.Errorf("the research is still in the spec: %s", c.Spec)
	}
	if rs, ok := GetResearchSource(RootDB, "docs-index"); !ok || rs.Off || rs.ToolName != "docs_search" {
		t.Errorf("the Source Hook: %+v", rs)
	}
	var hook sourcehooks.SourceHook
	if RootDB.Get(sourceHookTable, "docs_index", &hook) || !RootDB.Get(sourceHookTable, "westlaw", &hook) {
		t.Error("the moved hook should be gone and the paywall hook left")
	}
	for _, name := range []string{"sd", "comfy"} {
		if c, _ := GetConnector(RootDB, name); c.Kind != Kind {
			t.Errorf("%s is still %s", name, c.Kind)
		}
	}
	keyed("sd", "image.generate")
	if _, ok := Secure().Load("sd_login"); !ok {
		t.Error("a credential another connector still names was deleted")
	}
	if c, _ := GetConnector(RootDB, "peer-den-flux"); c.Kind != restImageKind {
		t.Error("a peer's renderer was rewritten: its peer's sync does that")
	}
	if stale := StaleSettings(RootDB); len(stale) > 0 {
		t.Errorf("still needing setting again: %+v", stale)
	}
}

// Web Search's engine: one with a template and its key becomes that
// connector; the Source Hook that was the engine hands it to its connector;
// one with no template, or no key, is left, and needs setting again.
func TestOldEnginesMoveOrWaitToBeSetAgain(t *testing.T) {
	for _, tc := range []struct{ engine, key, hook, want string }{
		{"brave", "brave-key", "", "brave-search"},
		{"brave", "", "", "brave"},
		{"google", "g-key", "", "google"},
		{"hook", "", "Docs Index", "docs-index"},
	} {
		world(t)
		RootDB.Set(SearchTable, "provider", tc.engine)
		RootDB.Set(SearchTable, "api_key", tc.key)
		RootDB.Set(SearchTable, "hook", tc.hook)
		RootDB.Set(sourceHookTable, "docs_index", sourcehooks.SourceHook{Name: "Docs Index", Type: sourcehooks.HookTypeAPI,
			Endpoint: "https://docs.example.test/search", ResultsPath: "results"})
		MigrateIntegration(RootDB)
		got := str(SearchTable, "provider")
		stale := StaleWhy(RootDB, got)
		if got != tc.want || (got == tc.engine) != (stale != "") {
			t.Errorf("%s/%q: %q, stale %q", tc.engine, tc.key, got, stale)
		}
	}
}

// An image backend in a shape no builtin runs is left as it was, and the
// page naming it says it needs setting again.
func TestAnImageBackendNoBuiltinRunsWaits(t *testing.T) {
	for name, r := range map[string]RestImageSpec{
		"polled":    {SubmitURL: "https://render.example.test/jobs", SubmitIDPath: "id", PollURL: "https://render.example.test/jobs/{id}", PollReadyPath: "done", PollURLPath: "url"},
		"by-url":    {SubmitURL: "https://render.example.test/v1/images", ImageURLPath: "data.0.url"},
		"b64-there": {SubmitURL: "https://render.example.test/v1/images", ImageB64Path: "data.0.b64_json"},
	} {
		world(t)
		raw, _ := json.Marshal(r)
		storedRecord(t, name, map[string]any{"kind": restImageKind, "spec": json.RawMessage(raw)})
		RootDB.Set(ImageTable, "provider", name)
		MigrateIntegration(RootDB)
		if c, _ := GetConnector(RootDB, name); c.Kind != restImageKind {
			t.Errorf("%s was changed: %s", name, c.Kind)
		}
		if why := StaleWhy(RootDB, name); !strings.Contains(why, "no longer runs") {
			t.Errorf("%s: why %q", name, why)
		}
	}
}
