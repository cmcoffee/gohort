package service

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/migrate"
)

// tierSettings stores a tier as the LLM form did before the move.
func tierSettings(table, provider, model, key, endpoint string) {
	RootDB.Set(table, "provider", provider)
	RootDB.Set(table, "model", model)
	RootDB.Set(table, "api_key", key)
	RootDB.Set(table, "endpoint", endpoint)
	RootDB.Set(table, "context_size", 64000)
}

func forgetModelCredentials(t *testing.T) {
	forgetCredentials(t, "anthropic_model", "anthropic_model_2", "anthropic_model_3", "openai_model", "gemini_model", "openai_compatible", "openai_compatible_2")
}

// resolved is what a tier's model client is built with.
func resolved(t *testing.T, table string) LLMProviderConfig {
	t.Helper()
	var cfg LLMProviderConfig
	RootDB.Get(table, "provider", &cfg.Provider)
	RootDB.Get(table, "model", &cfg.Model)
	RootDB.Get(table, "context_size", &cfg.ContextSize)
	out, err := ResolveModelProvider(cfg, cfg.Provider)
	if err != nil {
		t.Fatalf("%s: %v", table, err)
	}
	return out
}

// Both tiers on Anthropic with one key become one model connector made from
// the Anthropic template, the key in its credential: each tier names it,
// keeps its own model and context, and has no key or address of its own. Its
// model client is built as it was. Run again, it changes nothing.
func TestTiersOnOneKeyShareAModelConnector(t *testing.T) {
	world(t)
	forgetModelCredentials(t)
	// The worker's address is left over from a local server: Anthropic's
	// client never used it.
	tierSettings(LLMTable, "anthropic", "claude-opus-5", "sk-ant-1", "http://localhost:8080/v1")
	tierSettings(LeadLLMTable, "anthropic", "", "sk-ant-1", "")

	MigrateLLMTiers(RootDB)

	for _, table := range []string{LLMTable, LeadLLMTable} {
		var provider, key, endpoint string
		RootDB.Get(table, "provider", &provider)
		if provider != "service:anthropic-model" || RootDB.Get(table, "api_key", &key) || RootDB.Get(table, "endpoint", &endpoint) {
			t.Errorf("%s after: %q, key %q, endpoint %q", table, provider, key, endpoint)
		}
	}
	c, ok := GetConnector(RootDB, "anthropic-model")
	if !ok || !c.Approved || c.Template != "anthropic-model" || !Provides(c, "llm.chat") {
		t.Fatalf("the connector: %+v %v", c, ok)
	}
	if _, two := GetConnector(RootDB, "anthropic-model-2"); two {
		t.Error("one key made two connectors")
	}
	if cred, ok := Secure().Load("anthropic_model"); !ok || cred.Disabled || cred.ParamName != "x-api-key" {
		t.Fatalf("the credential: %+v %v", cred, ok)
	}
	w := resolved(t, LLMTable)
	if w.Provider != "anthropic" || w.APIKey != "sk-ant-1" || w.Model != "claude-opus-5" || w.ContextSize != 64000 {
		t.Errorf("the worker's client: %+v", w)
	}
	// A tier that named no model runs the one the client ran with none.
	if l := resolved(t, LeadLLMTable); l.Model != "claude-sonnet-5" || l.APIKey != "sk-ant-1" {
		t.Errorf("the lead's client: %+v", l)
	}
	var m migrate.MigrationMarker
	if !RootDB.Get(migrate.MigrationsTable, "core:llm_tiers_to_connectors:v1:", &m) || !m.Done || m.Error != "" || m.Changed != 4 {
		t.Errorf("Admin > Migrations shows: %+v", m)
	}

	tierSettings(LLMTable, "anthropic", "", "sk-ant-1", "")
	MigrateLLMTiers(RootDB)
	var provider string
	if RootDB.Get(LLMTable, "provider", &provider); provider != "anthropic" {
		t.Error("the migration ran twice")
	}
}

// Tiers on different providers, or different keys, each get their own
// connector: Gemini's key as its header, OpenAI's at OpenAI.
func TestTiersOnTwoProvidersGetTwoConnectors(t *testing.T) {
	world(t)
	forgetModelCredentials(t)
	tierSettings(LLMTable, "gemini", "gemini-3-pro", "g-key", "")
	tierSettings(LeadLLMTable, "openai", "gpt-5", "sk-oa", "https://api.openai.com/v1/")
	if changed, err := migrateLLMTiers(RootDB); err != nil || changed != 6 {
		t.Fatalf("migrate: %d %v", changed, err)
	}
	if cred, _ := Secure().Load("gemini_model"); cred.Type != SecureCredHeader || cred.ParamName != "x-goog-api-key" {
		t.Errorf("Gemini's credential: %+v", cred)
	}
	if w := resolved(t, LLMTable); w.Provider != "gemini" || w.APIKey != "g-key" || w.Model != "gemini-3-pro" {
		t.Errorf("the worker's client: %+v", w)
	}
	if l := resolved(t, LeadLLMTable); l.Provider != "openai" || l.APIKey != "sk-oa" || l.Endpoint != "https://api.openai.com/v1" || l.Model != "gpt-5" {
		t.Errorf("the lead's client: %+v", l)
	}
	// OpenAI's own address is OpenAI's template, not an address of its own.
	var lead string
	if RootDB.Get(LeadLLMTable, "provider", &lead); lead != "service:openai-model" {
		t.Errorf("the lead: %q", lead)
	}
}

// An OpenAI tier sent to an address of its own sent its requests, and its
// key, there: it moves to the OpenAI-compatible template at that address.
// A second key on the same provider gets a second connector.
func TestOpenAIAtItsOwnAddressAndASecondKey(t *testing.T) {
	world(t)
	forgetModelCredentials(t)
	tierSettings(LLMTable, "openai", "local-model", "sk-proxy", "https://models.example/v1/")
	tierSettings(LeadLLMTable, "anthropic", "claude-opus-5", "sk-ant-2", "")
	if _, _, err := ModelConnectorFor(RootDB, "anthropic", "", "", "sk-ant-1", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateLLMTiers(RootDB); err != nil {
		t.Fatal(err)
	}
	if w := resolved(t, LLMTable); w.Provider != "openai" || w.Endpoint != "https://models.example/v1" || w.APIKey != "sk-proxy" {
		t.Errorf("the worker's client: %+v", w)
	}
	var lead string
	RootDB.Get(LeadLLMTable, "provider", &lead)
	if lead != "service:anthropic-model-2" || resolved(t, LeadLLMTable).APIKey != "sk-ant-2" {
		t.Errorf("the lead on a second key: %q", lead)
	}
	// Asked again for a key one holds, the connector holding it is given.
	if name, changed, err := ModelConnectorFor(RootDB, "anthropic", "", "", "sk-ant-1", "test"); err != nil || name != "anthropic-model" || changed != 0 {
		t.Errorf("the same key again: %q %d %v", name, changed, err)
	}
	// The same key at another address, or for OpenAI itself, is not the
	// connector at the first address: its requests would go there.
	if name, _, err := ModelConnectorFor(RootDB, "openai", "https://other.example/v1", "", "sk-proxy", "test"); err != nil || name == "openai-compatible" {
		t.Errorf("the key at another address: %q %v", name, err)
	}
	if name, _, err := ModelConnectorFor(RootDB, "openai", "", "", "sk-proxy", "test"); err != nil || name != "openai-model" {
		t.Errorf("the key for OpenAI itself: %q %v", name, err)
	}
	// One an admin has unapproved is not chosen for them.
	UnapproveConnector(RootDB, "anthropic-model")
	if name, _, err := ModelConnectorFor(RootDB, "anthropic", "", "", "sk-ant-1", "test"); err != nil || name == "anthropic-model" {
		t.Errorf("an unapproved connector: %q %v", name, err)
	}
	// A new key goes into a connector only when it was made from the
	// provider's template and no connector holds the key already.
	for _, c := range []struct{ provider, name, key string }{
		{"gemini", "anthropic-model-2", "sk-new"},
		{"anthropic", "openai-compatible", "sk-new"},
		{"anthropic", "anthropic-model-2", "sk-ant-1"},
		{"anthropic", "anthropic-model-2", ""},
	} {
		if done, err := RekeyModelConnector(RootDB, c.provider, c.name, c.key); done || err != nil {
			t.Errorf("%v was rekeyed: %v", c, err)
		}
	}
	if done, err := RekeyModelConnector(RootDB, "anthropic", "anthropic-model-2", "sk-ant-9"); !done || err != nil {
		t.Errorf("rekey: %v %v", done, err)
	}
	if resolved(t, LeadLLMTable).APIKey != "sk-ant-9" {
		t.Error("the lead does not run on the new key")
	}
	if _, _, err := ModelConnectorFor(RootDB, "bedrock", "", "", "k", "test"); err == nil {
		t.Error("Bedrock was given a connector")
	}
}

// A tier on a hosted provider with no key never ran, and is left as it is,
// as is every tier that is not on a hosted provider.
func TestTiersWithNothingToMove(t *testing.T) {
	world(t)
	for _, s := range [][4]string{
		{"", "", "", ""},
		{"anthropic", "claude-opus-5", "", ""},
		{"gemini", "", "  ", ""},
		{"vllm", "qwen", "local-key", "http://gpu.example:8000/v1"},
		{"bedrock", "us.anthropic.claude-opus-5", "", ""},
		{"peer:den", "qwen", "", ""},
		{"service:openrouter", "", "", ""},
	} {
		tierSettings(LLMTable, s[0], s[1], s[2], s[3])
		if changed, err := migrateLLMTiers(RootDB); err != nil || changed != 0 {
			t.Errorf("%v was moved: %d %v", s, changed, err)
		}
		var provider string
		if RootDB.Get(LLMTable, "provider", &provider); provider != s[0] {
			t.Errorf("%v became %q", s, provider)
		}
	}
	if n := len(ListConnectors(RootDB)); n != 0 {
		t.Errorf("%d connectors added", n)
	}
}

// The templates a tier moves to install, each speaking its provider's
// dialect at that provider's host.
func TestTheModelTemplatesSpeakTheirDialects(t *testing.T) {
	hosts := map[string]string{"anthropic": "https://api.anthropic.com", "openai": "https://api.openai.com/v1", "gemini": "https://generativelanguage.googleapis.com"}
	for provider, id := range modelTemplates {
		spec := templateSpec(t, id, hosts[provider])
		if err := validateSpec(t, spec); err != nil {
			t.Errorf("%s: %v", id, err)
		}
		b := spec["provides"].([]any)[0].(map[string]any)
		if b["dialect"] != provider || !strings.Contains(mustJSON(t, b["defaults"]), modelDefaults[provider]) {
			t.Errorf("%s: %v", id, b)
		}
	}
}
