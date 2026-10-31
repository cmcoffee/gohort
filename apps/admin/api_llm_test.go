package admin

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A new Bedrock setup starts on Converse; a tier already on Bedrock with the
// historical blank keeps the Messages API it has been running.
func TestBedrockAPIShown(t *testing.T) {
	for _, c := range []struct{ provider, stored, want string }{
		{"", "", "converse"},
		{"vllm", "", "converse"},
		{"bedrock", "", "messages"},
		{"bedrock", "invoke", "invoke"},
		{"bedrock", "converse", "converse"},
	} {
		if got := bedrockAPIShown(c.provider, c.stored); got != c.want {
			t.Errorf("bedrockAPIShown(%q, %q) = %q, want %q", c.provider, c.stored, got, c.want)
		}
	}
	if bedrockUsesMantle("converse") || bedrockUsesMantle("invoke") || !bedrockUsesMantle("") || !bedrockUsesMantle("messages") {
		t.Error("the mantle endpoint check applies to the wrong modes")
	}
}

// Saving a tier on Anthropic, OpenAI or Gemini with a key makes a model
// connector from that provider's template, the key in its credential, and
// the tier names it and stores no key. The same key again gives the same
// connector; no key keeps the connector the tier runs on, and is refused for
// a tier on anything else. A blank model is the provider's default.
func TestAHostedTierSavesAsAModelConnector(t *testing.T) {
	saved, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	t.Cleanup(func() {
		RootDB, AuthDB = saved, savedAuth
		for _, n := range []string{"anthropic_model", "anthropic_model_2", "anthropic_model_3"} {
			Secure().Delete(n)
		}
	})
	a := &AdminApp{db: RootDB}
	a.db.CryptSet(LLMTable, "api_key", "a key from before")
	save := func(table, body string) (int, string) {
		w := httptest.NewRecorder()
		a.llmConfigRoutes(table, table == LLMTable)(w, httptest.NewRequest("POST", "/api/llm", strings.NewReader(body)))
		return w.Code, w.Body.String()
	}
	stored := func(table string) (provider, model string, keyed bool) {
		var key string
		a.db.Get(table, "provider", &provider)
		a.db.Get(table, "model", &model)
		return provider, model, a.db.Get(table, "api_key", &key)
	}

	if code, body := save(LLMTable, `{"provider":"anthropic","model":"claude-opus-5","api_key":"sk-ant-1"}`); code != 204 {
		t.Fatalf("save: %d %s", code, body)
	}
	if p, m, keyed := stored(LLMTable); p != "service:anthropic-model" || m != "claude-opus-5" || keyed {
		t.Fatalf("the worker: %q %q key stored %v", p, m, keyed)
	}
	c, ok := GetConnector(RootDB, "anthropic-model")
	if !ok || !c.Approved || c.Template != "anthropic-model" {
		t.Fatalf("the connector: %+v %v", c, ok)
	}
	// The form, saved again as it stood, and with the key cleared.
	for _, body := range []string{`{"provider":"anthropic","model":"claude-opus-5","api_key":"sk-ant-1"}`, `{"provider":"anthropic","model":"claude-opus-5"}`} {
		if code, out := save(LLMTable, body); code != 204 {
			t.Fatalf("save again: %d %s", code, out)
		}
	}
	// The lead, on the same key with no model: the same connector, running
	// the provider's default rather than the worker's model.
	if code, body := save(LeadLLMTable, `{"provider":"anthropic","api_key":"sk-ant-1"}`); code != 204 {
		t.Fatalf("lead save: %d %s", code, body)
	}
	if p, m, _ := stored(LeadLLMTable); p != "service:anthropic-model" || m != "claude-sonnet-5" {
		t.Errorf("the lead: %q %q", p, m)
	}
	if n := len(ListConnectors(RootDB)); n != 1 {
		t.Errorf("%d connectors for one key", n)
	}
	// Another key is another connector.
	if code, body := save(LeadLLMTable, `{"provider":"anthropic","model":"claude-opus-5","api_key":"sk-ant-2"}`); code != 204 {
		t.Fatalf("second key: %d %s", code, body)
	}
	if p, _, _ := stored(LeadLLMTable); p != "service:anthropic-model-2" {
		t.Errorf("the lead on a second key: %q", p)
	}
	// The worker's connector is its own now: a new key typed on its form
	// replaces the one in it, and no connector is added.
	if code, body := save(LLMTable, `{"provider":"anthropic","model":"claude-opus-5","api_key":"sk-ant-3"}`); code != 204 {
		t.Fatalf("new key: %d %s", code, body)
	}
	if p, _, _ := stored(LLMTable); p != "service:anthropic-model" {
		t.Errorf("the worker after a new key: %q", p)
	}
	if k, err := Secure().ModelKey("anthropic_model", "https://api.anthropic.com/"); err != nil || k != "sk-ant-3" {
		t.Errorf("the worker's credential: %q %v", k, err)
	}
	if k, _ := Secure().ModelKey("anthropic_model_2", "https://api.anthropic.com/"); k != "sk-ant-2" {
		t.Errorf("the lead's key changed: %q", k)
	}
	// A key another connector holds is that connector, and the worker's
	// own keeps its key.
	if code, body := save(LLMTable, `{"provider":"anthropic","model":"claude-opus-5","api_key":"sk-ant-2"}`); code != 204 {
		t.Fatalf("the lead's key: %d %s", code, body)
	}
	if p, _, _ := stored(LLMTable); p != "service:anthropic-model-2" {
		t.Errorf("the worker on the lead's key: %q", p)
	}
	if k, _ := Secure().ModelKey("anthropic_model", "https://api.anthropic.com/"); k != "sk-ant-3" {
		t.Errorf("a key another connector held was written over: %q", k)
	}
	if n := len(ListConnectors(RootDB)); n != 2 {
		t.Errorf("%d connectors", n)
	}
	// Both now on one connector: a new key for the worker leaves the
	// lead's alone and gives the worker a connector of its own.
	if code, body := save(LLMTable, `{"provider":"anthropic","model":"claude-opus-5","api_key":"sk-ant-4"}`); code != 204 {
		t.Fatalf("a shared connector's tier: %d %s", code, body)
	}
	if p, _, _ := stored(LLMTable); p != "service:anthropic-model-3" {
		t.Errorf("the worker off a shared connector: %q", p)
	}
	if k, _ := Secure().ModelKey("anthropic_model_2", "https://api.anthropic.com/"); k != "sk-ant-2" {
		t.Errorf("the shared connector's key changed: %q", k)
	}
	// No key, for a tier not on that provider's connector: refused, saying
	// what to do, and nothing stored changes.
	a.db.Set(LLMTable, "provider", "llama.cpp")
	a.db.Set(LLMTable, "api_key", "local-key")
	code, body := save(LLMTable, `{"provider":"gemini","model":"gemini-3-pro"}`)
	if code != 400 || !strings.Contains(body, "needs its API key") || !strings.Contains(body, "gemini-model") {
		t.Errorf("no key: %d %s", code, body)
	}
	if p, _, keyed := stored(LLMTable); p != "llama.cpp" || !keyed {
		t.Errorf("a refused save changed the worker: %q %v", p, keyed)
	}
	// A local server still keeps its own key.
	if code, body := save(LLMTable, `{"provider":"vllm","api_key":"vk"}`); code != 204 {
		t.Fatalf("vllm save: %d %s", code, body)
	}
	var key string
	if a.db.Get(LLMTable, "api_key", &key); key != "vk" {
		t.Errorf("vLLM's key: %q", key)
	}
}

// With no key typed, a hosted provider's model list and test go to the
// connector the tier runs on, when it is that provider's, and never take a
// key the tier stored for another provider; anything else takes the stored
// key, and a typed key is used as typed.
func TestAHostedTierWithNoKeyUsesItsConnector(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	a := &AdminApp{db: RootDB}
	if err := SaveConnector(RootDB, Connector{Name: "claude", Kind: "service", Template: "anthropic-model", Spec: json.RawMessage(`{"provider":{"transport":"http","base_url":"https://api.anthropic.com","auth":{"credential":"anthropic_model"},"operations":{"models":{"path":"/v1/models"}}},"provides":[{"capability":"llm.chat","dialect":"anthropic"}]}`)}); err != nil {
		t.Fatal(err)
	}
	a.db.Set(LLMTable, "provider", "service:claude")
	a.db.Set(LLMTable, "api_key", "stored")
	for _, c := range []struct{ provider, key, wantProvider, wantKey string }{
		{"anthropic", "", "service:claude", ""},
		{"anthropic", "typed", "anthropic", "typed"},
		{"openai", "", "openai", ""},
		{"vllm", "", "vllm", "stored"},
		{"vllm", "typed", "vllm", "typed"},
	} {
		if p, k := a.formTierTarget(LLMTable, c.provider, c.key); p != c.wantProvider || k != c.wantKey {
			t.Errorf("%s with key %q: %q %q", c.provider, c.key, p, k)
		}
	}
	a.db.Set(LLMTable, "provider", "llama.cpp")
	if p, k := a.formTierTarget(LLMTable, "anthropic", ""); p != "anthropic" || k != "" {
		t.Errorf("a tier on llama.cpp: %q %q", p, k)
	}
}

// Saving a tier on AWS Bedrock makes a model connector at its address,
// signing as the machine under its profile, and stores the tier on it with
// no Bedrock rows of its own; saving it again finds that connector, other
// settings find or make another, and a form showing the tier's settings
// with no key tests and lists through it.
func TestABedrockTierSavesAsAModelConnector(t *testing.T) {
	saved, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	t.Cleanup(func() {
		RootDB, AuthDB = saved, savedAuth
		for _, n := range []string{"bedrock_model", "bedrock_model_2", "bedrock_token"} {
			Secure().Delete(n)
		}
	})
	a := &AdminApp{db: RootDB}
	save := func(table, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		a.llmConfigRoutes(table, table == LLMTable)(w, httptest.NewRequest("POST", "/api/llm", strings.NewReader(body)))
		if w.Code != 204 {
			t.Fatalf("save: %d %s", w.Code, w.Body.String())
		}
	}
	save(LLMTable, `{"provider":"bedrock","aws_region":"us-west-2","aws_profile":"prod","bedrock_api":"converse"}`)
	var provider, model, region string
	a.db.Get(LLMTable, "provider", &provider)
	a.db.Get(LLMTable, "model", &model)
	if provider != "service:bedrock-model" || model != "anthropic.claude-sonnet-5" || a.db.Get(LLMTable, "aws_region", &region) && region != "" {
		t.Fatalf("the worker: %q %q region %q", provider, model, region)
	}
	if cred, _ := Secure().Load("bedrock_model"); !cred.AWSMachine || cred.AWSProfile != "prod" {
		t.Errorf("the credential: %+v", cred)
	}
	save(LeadLLMTable, `{"provider":"bedrock","aws_region":"us-west-2","aws_profile":"prod","bedrock_api":"converse","model":"us.anthropic.claude-opus-5"}`)
	save(LLMTable, `{"provider":"bedrock","aws_region":"us-west-2","aws_profile":"prod","bedrock_api":"converse"}`)
	if n := len(ListConnectors(RootDB)); n != 1 {
		t.Errorf("%d connectors for one set of settings", n)
	}
	s := service.BedrockSettings{Region: "us-west-2", Profile: "prod", API: "converse"}
	if name, ok := a.keptBedrock(LLMTable, s); !ok || name != "bedrock-model" {
		t.Errorf("Test and List models with the tier's settings: %q %v", name, ok)
	}
	for _, other := range []service.BedrockSettings{{Region: "us-east-1", Profile: "prod", API: "converse"}, {Region: "us-west-2", API: "converse", Profile: "prod", Key: "typed"}} {
		if _, ok := a.keptBedrock(LLMTable, other); ok {
			t.Errorf("%+v tests through the tier's connector", other)
		}
	}
	save(LLMTable, `{"provider":"bedrock","aws_region":"us-west-2","bedrock_api":"invoke","api_key":"bedrock-key"}`)
	a.db.Get(LLMTable, "provider", &provider)
	if provider == "service:bedrock-model" || len(ListConnectors(RootDB)) != 2 {
		t.Errorf("other settings: %q, %d connectors", provider, len(ListConnectors(RootDB)))
	}
	var key string
	if a.db.Get(LLMTable, "api_key", &key) {
		t.Errorf("the tier keeps a key: %q", key)
	}
}
