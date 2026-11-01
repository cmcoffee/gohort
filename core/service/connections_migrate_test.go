package service

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/migrate"
)

// keyedCredential saves a global credential holding secret, enabled.
func keyedCredential(t *testing.T, c SecureCredential, secret string) {
	t.Helper()
	if err := Secure().Save(c, secret); err != nil {
		t.Fatal(err)
	}
	forgetCredentials(t, c.Name)
}

// kept is the key connection name keeps under setting.
func keptKey(t *testing.T, name, setting string) string {
	t.Helper()
	c, _ := GetConnector(RootDB, name)
	s, _ := GetConnectionSecret(RootDB, c, setting)
	return s.Value
}

// What predates connections moves onto them once: a plan-B connector with a
// connection provided in its place hands it its key and address and goes,
// the pages naming it moved; one with none is made again where it is; one
// no template made has its credential made a secret setting; a tier naming
// a provider directly, and a page still on this instance's own server, run
// on the connection provided for it. A credential nothing names is deleted,
// one a tool uses is kept. It runs once.
func TestWhatPredatesConnectionsMovesOntoThem(t *testing.T) {
	world(t)
	ProvideConnectors(RootDB)
	saved := CredentialToolsResolver
	CredentialToolsResolver = func(cred string) []CredentialToolRef {
		if cred == "acme_key" {
			return []CredentialToolRef{{}}
		}
		return nil
	}
	t.Cleanup(func() { CredentialToolsResolver = saved })
	store := func(c Connector) { RootDB.Set("connectors", c.Name, c) }

	keyedCredential(t, SecureCredential{Name: "anthropic_model", Type: SecureCredHeader, ParamName: "x-api-key", BaseURL: "https://api.anthropic.com"}, "sk-ant")
	store(Connector{Name: "anthropic-model", Kind: Kind, Template: "anthropic-model", Approved: true, ID: "c_old1",
		Spec: json.RawMessage(`{"provider":{"transport":"http","base_url":"https://api.anthropic.com","auth":{"credential":"anthropic_model"},"operations":{"models":{"path":"/v1/models"}}},"provides":[{"capability":"llm.chat","dialect":"anthropic","defaults":{"model":"claude-opus-5"}}]}`)})
	RootDB.Set(LeadLLMTable, "provider", "service:anthropic-model")

	store(Connector{Name: "embeddings", Kind: Kind, Template: "openai-compatible-embeddings", Approved: true, ID: "c_old2",
		Spec: json.RawMessage(`{"provider":{"transport":"http","base_url":"http://gpu.example:8080/v1","auth":{"none":true},"operations":{"embed":{"method":"POST","path":"/embeddings","body":{"model":"{model}","input":"{texts}"}}}},"provides":[{"capability":"embed.text","steps":[{"call":"embed"}],"output":{"vectors":{"path":"data","each":"embedding","as":"json"}},"defaults":{"model":"e5-large"}}]}`)})
	RootDB.Set(EmbeddingTable, "current", EmbeddingConfig{Enabled: true, Provider: "service:embeddings", Endpoint: "http://gpu.example:8080/v1", Model: "e5-large"})

	keyedCredential(t, SecureCredential{Name: "serper_search", Type: SecureCredHeader, ParamName: "X-API-KEY", BaseURL: "https://google.serper.dev"}, "serp-1")
	store(Connector{Name: "serper-search", Kind: Kind, Template: "serper-search", Approved: true, ID: "c_old3",
		Spec: json.RawMessage(`{"provider":{"transport":"http","base_url":"https://google.serper.dev","auth":{"credential":"serper_search"},"operations":{"search":{"method":"POST","path":"/search","body":{"q":"{query}"}}}},"provides":[{"capability":"web.search","steps":[{"call":"search"}],"output":{"results":{"path":"organic","fields":{"title":"title","url":"link","snippet":"snippet"}}}}]}`)})
	RootDB.Set(SearchTable, "provider", "service:serper-search")

	keyedCredential(t, SecureCredential{Name: "acme_key", Type: SecureCredBearer, BaseURL: "https://api.acme.example"}, "acme-1")
	m := imageSpec("https://api.acme.example")
	provider(m)["auth"] = map[string]any{"credential": "acme_key"}
	raw, _ := json.Marshal(m)
	store(Connector{Name: "acme-images", Kind: Kind, Approved: true, ID: "c_old4", Spec: raw})

	RootDB.Set(LLMTable, "provider", "llama.cpp")
	RootDB.Set(LLMTable, "endpoint", "http://box.example:8080/v1")
	RootDB.CryptSet(LLMTable, "api_key", "vk")
	RootDB.Set(TranscribeTable, "current", media.TranscribeConfig{Enabled: true, Endpoint: "http://stt.example:8089/v1/audio/transcriptions", Model: "whisper-1"})

	MigrateConnections(RootDB)

	var lead, worker, search string
	RootDB.Get(LeadLLMTable, "provider", &lead)
	RootDB.Get(LLMTable, "provider", &worker)
	RootDB.Get(SearchTable, "provider", &search)
	if _, there := GetConnector(RootDB, "anthropic-model"); lead != "service:anthropic" || keptKey(t, "anthropic", "api_key") != "sk-ant" || there {
		t.Errorf("the lead: %q, Anthropic's key %q, the old one kept %v", lead, keptKey(t, "anthropic", "api_key"), there)
	}
	var emb EmbeddingConfig
	RootDB.Get(EmbeddingTable, "current", &emb)
	le, _ := GetConnector(RootDB, "local-embeddings")
	if emb.Provider != "service:local-embeddings" || emb.Endpoint != "http://gpu.example:8080/v1" || emb.Model != "e5-large" ||
		ConnectionConfig(RootDB, le)["base_url"] != "http://gpu.example:8080/v1" || ConnectionConfig(RootDB, le)["model"] != "e5-large" {
		t.Errorf("embeddings: %+v, on %v", emb, ConnectionConfig(RootDB, le))
	}
	sc, _ := GetConnector(RootDB, "serper-search")
	if s, _ := Parse(sc.Spec); search != "service:serper-search" || s.Provider.Auth.Secret == "" || sc.ID != "c_old3" || keptKey(t, "serper-search", s.Provider.Auth.Secret) != "serp-1" {
		t.Errorf("serper: on %q, %s, id %s", search, sc.Spec, sc.ID)
	}
	ac, _ := GetConnector(RootDB, "acme-images")
	if s, _ := Parse(ac.Spec); s.Provider.Auth.Secret != "api_key" || keptKey(t, "acme-images", "api_key") != "acme-1" {
		t.Errorf("acme: %s", ac.Spec)
	}
	var key string
	lc, _ := GetConnector(RootDB, "llamacpp")
	if worker != "service:llamacpp" || ConnectionConfig(RootDB, lc)["base_url"] != "http://box.example:8080/v1" || keptKey(t, "llamacpp", "api_key") != "vk" || RootDB.Get(LLMTable, "api_key", &key) {
		t.Errorf("the worker: %q on %v, its own key %q", worker, ConnectionConfig(RootDB, lc), key)
	}
	var tr media.TranscribeConfig
	RootDB.Get(TranscribeTable, "current", &tr)
	lt, _ := GetConnector(RootDB, "local-transcription")
	if tr.Provider != "service:local-transcription" || ConnectionConfig(RootDB, lt)["base_url"] != "http://stt.example:8089/v1" || ConnectionConfig(RootDB, lt)["model"] != "whisper-1" {
		t.Errorf("transcription: %+v on %v", tr, ConnectionConfig(RootDB, lt))
	}
	for cred, gone := range map[string]bool{"anthropic_model": true, "serper_search": true, "acme_key": false} {
		if _, ok := Secure().Load(cred); ok == gone {
			t.Errorf("credential %s: still there %v", cred, ok)
		}
	}
	var mk migrate.MigrationMarker
	if !RootDB.Get(migrate.MigrationsTable, "core:connections:v2:", &mk) || !mk.Done || mk.Error != "" || mk.Changed == 0 {
		t.Errorf("the marker: %+v", mk)
	}
	RootDB.Set(LLMTable, "provider", "ollama")
	MigrateConnections(RootDB)
	if RootDB.Get(LLMTable, "provider", &worker); worker != "ollama" {
		t.Error("it ran again")
	}
}

// Two tiers named one provider directly with other settings: the second
// gets a connection of its own.
func TestTwoTiersOnOneProviderKeepTheirOwn(t *testing.T) {
	world(t)
	ProvideConnectors(RootDB)
	RootDB.Set(LLMTable, "provider", "vllm")
	RootDB.Set(LLMTable, "endpoint", "http://a.example:8000/v1")
	RootDB.Set(LeadLLMTable, "provider", "vllm")
	RootDB.Set(LeadLLMTable, "endpoint", "http://b.example:8000/v1")
	MigrateConnections(RootDB)
	var w, l string
	RootDB.Get(LLMTable, "provider", &w)
	RootDB.Get(LeadLLMTable, "provider", &l)
	wc, _ := GetConnector(RootDB, strings.TrimPrefix(w, "service:"))
	lc, _ := GetConnector(RootDB, strings.TrimPrefix(l, "service:"))
	if w == l || ConnectionConfig(RootDB, wc)["base_url"] != "http://a.example:8000/v1" || ConnectionConfig(RootDB, lc)["base_url"] != "http://b.example:8000/v1" {
		t.Errorf("worker %s %v, lead %s %v", w, ConnectionConfig(RootDB, wc), l, ConnectionConfig(RootDB, lc))
	}
}

// A connector this instance's own embedder or transcriber had its settings
// moved onto, with no template: keyless, the local server connection
// provided for what it does takes its address (Ollama's under /api) and its
// pages, the embeddings keeping their space; one sending a key the provided
// one cannot is made again where it is, keeping its key.
func TestASettingsConnectorMovesOntoTheLocalServer(t *testing.T) {
	world(t)
	ProvideConnectors(RootDB)
	RootDB.Set("connectors", "embeddings", Connector{Name: "embeddings", Kind: Kind, Approved: true, ID: "c_e",
		Desc: "Embeddings at http://gpu.example:11434/api (nomic-embed-text), moved here from the embeddings settings.",
		Spec: json.RawMessage(`{"provider":{"transport":"http","base_url":"http://gpu.example:11434/api","auth":{"none":true},"operations":{"embed":{"method":"POST","path":"/embed","body":{"model":"{model}","input":"{texts}"}}}},"provides":[{"capability":"embed.text","steps":[{"call":"embed"}],"output":{"vectors":{"path":"embeddings","as":"json"}},"defaults":{"model":"nomic-embed-text"}}]}`)})
	RootDB.Set(EmbeddingTable, "current", EmbeddingConfig{Enabled: true, Provider: "service:embeddings", Endpoint: "http://gpu.example:11434/api", Model: "nomic-embed-text"})
	keyedCredential(t, SecureCredential{Name: "transcription_key", Type: SecureCredBearer, BaseURL: "https://stt.example/v1"}, "stt-1")
	RootDB.Set("connectors", "transcription", Connector{Name: "transcription", Kind: Kind, Approved: true, ID: "c_t",
		Desc: "Transcription at https://stt.example/v1 (whisper-1), moved here from the transcription settings.",
		Spec: json.RawMessage(`{"provider":{"transport":"http","base_url":"https://stt.example/v1","auth":{"credential":"transcription_key"},"operations":{"transcribe":{"method":"POST","path":"/audio/transcriptions","send":"multipart","body":{"file":"{audio}","model":"{model}"}}}},"provides":[{"capability":"speech.transcribe","steps":[{"call":"transcribe"}],"output":{"text":{"path":"text"}},"defaults":{"model":"whisper-1"}}]}`)})
	RootDB.Set(TranscribeTable, "current", media.TranscribeConfig{Enabled: true, Provider: "service:transcription"})

	MigrateConnections(RootDB)

	var emb EmbeddingConfig
	RootDB.Get(EmbeddingTable, "current", &emb)
	oe, _ := GetConnector(RootDB, "ollama-embeddings")
	if _, there := GetConnector(RootDB, "embeddings"); emb.Provider != "service:ollama-embeddings" || emb.Endpoint != "http://gpu.example:11434/api" ||
		ConnectionConfig(RootDB, oe)["base_url"] != "http://gpu.example:11434" || ConnectionConfig(RootDB, oe)["model"] != "nomic-embed-text" || there {
		t.Errorf("embeddings: %+v on %v, the old one kept %v", emb, ConnectionConfig(RootDB, oe), there)
	}
	var tr media.TranscribeConfig
	RootDB.Get(TranscribeTable, "current", &tr)
	tc, _ := GetConnector(RootDB, "transcription")
	if s, _ := Parse(tc.Spec); tr.Provider != "service:transcription" || s.Provider.Auth.Secret == "" || keptKey(t, "transcription", s.Provider.Auth.Secret) != "stt-1" {
		t.Errorf("transcription: on %q, %s", tr.Provider, tc.Spec)
	}
}

// A plan-B Bedrock connector signing as this machine hands the Bedrock
// connection its region (from its host), its API and its profile, and no
// key: it goes on signing as the machine.
func TestABedrockConnectorMovesItsRegionAndProfile(t *testing.T) {
	world(t)
	ProvideConnectors(RootDB)
	keyedCredential(t, SecureCredential{Name: "bedrock_model", Type: SecureCredAWS, AWSMachine: true, AWSProfile: "ops",
		BaseURL: "https://bedrock-runtime.us-west-2.amazonaws.com"}, "")
	RootDB.Set("connectors", "bedrock-model", Connector{Name: "bedrock-model", Kind: Kind, Template: "bedrock-model", Approved: true, ID: "c_b",
		Spec: json.RawMessage(`{"provider":{"transport":"http","base_url":"https://bedrock-runtime.us-west-2.amazonaws.com","auth":{"credential":"bedrock_model"},"operations":{"models":{"url":"https://bedrock.us-west-2.amazonaws.com/foundation-models"}}},"provides":[{"capability":"llm.chat","dialect":"bedrock","defaults":{"model":"anthropic.claude-sonnet-5","api":"converse"}}]}`)})
	RootDB.Set(LeadLLMTable, "provider", "service:bedrock-model")
	MigrateConnections(RootDB)
	b, _ := GetConnector(RootDB, "bedrock")
	got := ConnectionConfig(RootDB, b)
	var lead string
	RootDB.Get(LeadLLMTable, "provider", &lead)
	if lead != "service:bedrock" || got["region"] != "us-west-2" || got["api"] != "converse" || got["profile"] != "ops" || HasSecret(b, "aws_keys") {
		t.Errorf("the lead on %q, Bedrock's settings %v, a key %v", lead, got, HasSecret(b, "aws_keys"))
	}
	if tg, err := modelTarget("bedrock", true, nil); err != nil || !tg.AWSMachine || tg.Profile != "ops" || tg.Region != "us-west-2" {
		t.Errorf("it signs: %+v %v", tg, err)
	}
}

// A page already moved onto the provided connection, still at its default
// address, takes the address its outdated connector had; one set on purpose
// keeps its own.
func TestACarriedAddressReplacesOnlyADefault(t *testing.T) {
	world(t)
	ProvideConnectors(RootDB)
	for name, base := range map[string]string{"speech": "http://kokoro.example:8880/v1", "transcription": "http://whisper.example:8089/v1"} {
		capability, path := "speech.synthesize", "/audio/speech"
		if name == "transcription" {
			capability, path = "speech.transcribe", "/audio/transcriptions"
		}
		RootDB.Set("connectors", name, Connector{Name: name, Kind: Kind, Approved: true, ID: "c_" + name,
			Desc: name + " at " + base + ", moved here from the " + name + " settings.",
			Spec: json.RawMessage(`{"provider":{"transport":"http","base_url":"` + base + `","auth":{"none":true},"operations":{"op":{"method":"POST","path":"` + path + `"}}},"provides":[{"capability":"` + capability + `","steps":[{"call":"op"}],"output":{"x":{"from":"body"}}}]}`)})
	}
	ls, _ := GetConnector(RootDB, "local-speech")
	SetConnectionConfig(RootDB, ls, map[string]string{"base_url": "http://localhost:8880/v1"})
	lt, _ := GetConnector(RootDB, "local-transcription")
	SetConnectionConfig(RootDB, lt, map[string]string{"base_url": "http://chosen.example:9000/v1"})
	MigrateConnections(RootDB)
	if got := ConnectionConfig(RootDB, ls)["base_url"]; got != "http://kokoro.example:8880/v1" {
		t.Errorf("speech at its default took %q", got)
	}
	if got := ConnectionConfig(RootDB, lt)["base_url"]; got != "http://chosen.example:9000/v1" {
		t.Errorf("transcription set on purpose became %q", got)
	}
	for _, n := range []string{"speech", "transcription"} {
		if _, there := GetConnector(RootDB, n); there {
			t.Errorf("%s was kept though nothing names it", n)
		}
	}
}
