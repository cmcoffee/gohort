package service

import (
	"encoding/json"
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
// no template made has its credential made a secret setting; a page still on
// this instance's own server runs on the connection provided for it. A
// credential nothing names is deleted, one a tool uses is kept. It runs once.
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

	RootDB.Set(TranscribeTable, "current", media.TranscribeConfig{Enabled: true, Endpoint: "http://stt.example:8089/v1/audio/transcriptions", Model: "whisper-1"})

	MigrateConnections(RootDB)

	var search string
	RootDB.Get(SearchTable, "provider", &search)
	var emb EmbeddingConfig
	RootDB.Get(EmbeddingTable, "current", &emb)
	le, _ := GetConnector(RootDB, "local-embeddings")
	if emb.Provider != "embed:local-embeddings" || emb.Endpoint != "http://gpu.example:8080/v1" || emb.Model != "e5-large" ||
		ConnectionConfig(RootDB, le)["base_url"] != "http://gpu.example:8080/v1" || ConnectionConfig(RootDB, le)["model"] != "e5-large" {
		t.Errorf("embeddings: %+v, on %v", emb, ConnectionConfig(RootDB, le))
	}
	sc, _ := GetConnector(RootDB, "serper-search")
	if s, _ := Parse(sc.Spec); search != "search:serper-search" || s.Provider.Auth.Secret == "" || sc.ID != "c_old3" || keptKey(t, "serper-search", s.Provider.Auth.Secret) != "serp-1" {
		t.Errorf("serper: on %q, %s, id %s", search, sc.Spec, sc.ID)
	}
	ac, _ := GetConnector(RootDB, "acme-images")
	if s, _ := Parse(ac.Spec); s.Provider.Auth.Secret != "api_key" || keptKey(t, "acme-images", "api_key") != "acme-1" {
		t.Errorf("acme: %s", ac.Spec)
	}
	var tr media.TranscribeConfig
	RootDB.Get(TranscribeTable, "current", &tr)
	lt, _ := GetConnector(RootDB, "local-transcription")
	if tr.Provider != "transcribe:local-transcription" || ConnectionConfig(RootDB, lt)["base_url"] != "http://stt.example:8089/v1" || ConnectionConfig(RootDB, lt)["model"] != "whisper-1" {
		t.Errorf("transcription: %+v on %v", tr, ConnectionConfig(RootDB, lt))
	}
	for cred, gone := range map[string]bool{"serper_search": true, "acme_key": false} {
		if _, ok := Secure().Load(cred); ok == gone {
			t.Errorf("credential %s: still there %v", cred, ok)
		}
	}
	var mk migrate.MigrationMarker
	if !RootDB.Get(migrate.MigrationsTable, "core:connections:v2:", &mk) || !mk.Done || mk.Error != "" || mk.Changed == 0 {
		t.Errorf("the marker: %+v", mk)
	}
	RootDB.Set(SearchTable, "provider", "service:serper-search")
	MigrateConnections(RootDB)
	if RootDB.Get(SearchTable, "provider", &search); search != "service:serper-search" {
		t.Error("it ran again")
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
	if _, there := GetConnector(RootDB, "embeddings"); emb.Provider != "embed:ollama-embeddings" || emb.Endpoint != "http://gpu.example:11434/api" ||
		ConnectionConfig(RootDB, oe)["base_url"] != "http://gpu.example:11434" || ConnectionConfig(RootDB, oe)["model"] != "nomic-embed-text" || there {
		t.Errorf("embeddings: %+v on %v, the old one kept %v", emb, ConnectionConfig(RootDB, oe), there)
	}
	var tr media.TranscribeConfig
	RootDB.Get(TranscribeTable, "current", &tr)
	tc, _ := GetConnector(RootDB, "transcription")
	if s, _ := Parse(tc.Spec); tr.Provider != "transcribe:transcription" || s.Provider.Auth.Secret == "" || keptKey(t, "transcription", s.Provider.Auth.Secret) != "stt-1" {
		t.Errorf("transcription: on %q, %s", tr.Provider, tc.Spec)
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

// The settings saved as service:<name> are named for their jobs, once. An
// embedding config keeps the endpoint that names its vectors' space, and the
// stamp on its stored vectors reads the same, so nothing is re-embedded.
func TestSettingsAreNamedForTheirJobs(t *testing.T) {
	world(t)
	RootDB.Set(LLMTable, "provider", "service:vllm")
	RootDB.Set(LeadLLMTable, "provider", "peer:den")
	RootDB.Set(SearchTable, "provider", "service:serper-search")
	RootDB.Set(MailTable, "provider", "smtp")
	RootDB.Set(BrowseTable, "source", "service:reader")
	before := EmbeddingConfig{Enabled: true, Provider: "service:vec", Endpoint: "service:vec"}
	RootDB.Set(EmbeddingTable, "current", before)
	SetEmbeddingConfig(before)
	version := EmbedVersion()
	RootDB.Set(TranscribeTable, "current", media.TranscribeConfig{Enabled: true, Provider: "service:local-transcription"})
	RootDB.Set(media.SpeechTable, "current", media.SpeechConfig{Enabled: true, Provider: "service:local-speech"})

	if n := nameChoicesForJobs(RootDB); n != 5 {
		t.Errorf("%d settings renamed, want 5", n)
	}
	// The LLM tiers are left alone: they no longer run on connections.
	for _, c := range []struct{ table, field, want string }{
		{LLMTable, "provider", "service:vllm"}, {LeadLLMTable, "provider", "peer:den"}, {SearchTable, "provider", "search:serper-search"},
		{MailTable, "provider", "smtp"}, {BrowseTable, "source", "render:reader"},
	} {
		var v string
		if RootDB.Get(c.table, c.field, &v); v != c.want {
			t.Errorf("%s %s = %q, want %q", c.table, c.field, v, c.want)
		}
	}
	var emb EmbeddingConfig
	RootDB.Get(EmbeddingTable, "current", &emb)
	if emb.Provider != "embed:vec" || emb.Endpoint != "service:vec" {
		t.Errorf("embeddings: %+v", emb)
	}
	if EmbedVersion() != version {
		t.Errorf("the vectors' space moved: %q, was %q", EmbedVersion(), version)
	}
	var tr media.TranscribeConfig
	var sp media.SpeechConfig
	if RootDB.Get(TranscribeTable, "current", &tr); tr.Provider != "transcribe:local-transcription" {
		t.Errorf("transcription: %q", tr.Provider)
	}
	if RootDB.Get(media.SpeechTable, "current", &sp); sp.Provider != "speak:local-speech" {
		t.Errorf("speech: %q", sp.Provider)
	}
	if n := nameChoicesForJobs(RootDB); n != 0 {
		t.Errorf("a second run renamed %d", n)
	}
}

// The renaming runs with the startup migration.
func TestTheStartupMigrationNamesSettingsForTheirJobs(t *testing.T) {
	world(t)
	RootDB.Set(BrowseTable, "source", "service:reader")
	MigrateConnections(RootDB)
	var v string
	if RootDB.Get(BrowseTable, "source", &v); v != "render:reader" {
		t.Errorf("page rendering after the startup migration: %q", v)
	}
}
