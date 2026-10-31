package service

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/migrate"
	"github.com/cmcoffee/gohort/core/recipes"
)

// bedrockTier stores a tier on Bedrock as the LLM form did before the move.
func bedrockTier(table, region, profile, api, endpoint, model, key string) {
	RootDB.Set(table, "provider", "bedrock")
	RootDB.Set(table, "aws_region", region)
	RootDB.Set(table, "aws_profile", profile)
	RootDB.Set(table, "bedrock_api", api)
	RootDB.Set(table, "endpoint", endpoint)
	RootDB.Set(table, "model", model)
	RootDB.Set(table, "api_key", key)
	RootDB.Set(table, "context_size", 0)
}

func forgetBedrockCredentials(t *testing.T) {
	forgetCredentials(t, "bedrock_model", "bedrock_model_2", "bedrock_model_3", "bedrock_token", "bedrock_token_2")
}

// Both tiers on Bedrock alike, signing as the machine under one profile,
// become one model connector from the Bedrock template: at the regional
// host of their API, its credential signing as the machine under that
// profile. Each tier names it and the model it ran, and its Bedrock rows are
// gone; its client is built as it was. Run again, it changes nothing.
func TestBedrockTiersBecomeAConnector(t *testing.T) {
	world(t)
	forgetBedrockCredentials(t)
	bedrockTier(LLMTable, "us-west-2", "prod", "converse", "", "us.anthropic.claude-opus-5", "")
	bedrockTier(LeadLLMTable, "us-west-2", "prod", "converse", "", "", "")

	MigrateBedrockTiers(RootDB)

	for _, table := range []string{LLMTable, LeadLLMTable} {
		var provider string
		RootDB.Get(table, "provider", &provider)
		if provider != "service:bedrock-model" {
			t.Errorf("%s: %q", table, provider)
		}
		for _, k := range []string{"api_key", "endpoint", "aws_region", "aws_profile", "bedrock_api"} {
			var v string
			if RootDB.Get(table, k, &v) {
				t.Errorf("%s keeps %s", table, k)
			}
		}
	}
	c, ok := GetConnector(RootDB, "bedrock-model")
	if !ok || !c.Approved || c.Template != "bedrock-model" || !strings.Contains(string(c.Spec), `"base_url":"https://bedrock-runtime.us-west-2.amazonaws.com"`) {
		t.Fatalf("the connector: %+v %v", c, ok)
	}
	if _, two := GetConnector(RootDB, "bedrock-model-2"); two {
		t.Error("tiers alike made two connectors")
	}
	cred, ok := Secure().Load("bedrock_model")
	if !ok || cred.Type != SecureCredAWS || !cred.AWSMachine || cred.AWSProfile != "prod" || cred.Disabled {
		t.Fatalf("the credential: %+v %v", cred, ok)
	}
	if line := ConnectorSummary(c); !strings.HasSuffix(line, "⇐ bedrock-runtime.us-west-2.amazonaws.com (signed as this machine by bedrock_model)") {
		t.Errorf("its row: %s", line)
	}
	w := resolved(t, LLMTable)
	if w.Provider != "bedrock" || w.BedrockAPI != "converse" || w.Region != "us-west-2" || w.Profile != "prod" ||
		w.Endpoint != "https://bedrock-runtime.us-west-2.amazonaws.com" || w.Model != "us.anthropic.claude-opus-5" || w.APIKey != "" {
		t.Errorf("the worker's client: %+v", w)
	}
	if l := resolved(t, LeadLLMTable); l.Model != "anthropic.claude-sonnet-5" {
		t.Errorf("the lead's model: %q", l.Model)
	}
	var m migrate.MigrationMarker
	if !RootDB.Get(migrate.MigrationsTable, "core:bedrock_tiers_to_connectors:v1:", &m) || !m.Done || m.Error != "" || m.Changed != 4 {
		t.Errorf("Admin > Migrations shows: %+v", m)
	}
	bedrockTier(LLMTable, "us-west-2", "prod", "converse", "", "", "")
	MigrateBedrockTiers(RootDB)
	var provider string
	if RootDB.Get(LLMTable, "provider", &provider); provider != "bedrock" {
		t.Error("the migration ran twice")
	}
}

// A tier set up before the API choice speaks the Messages API, at its
// bedrock-mantle host. A Bedrock API key goes into a bearer credential of
// its own, and a private endpoint is the connector's address, its machine
// signing named for the tier's region.
func TestBedrockKeysAPIsAndEndpointsMove(t *testing.T) {
	world(t)
	forgetBedrockCredentials(t)
	bedrockTier(LLMTable, "us-east-1", "", "", "", "", "bedrock-api-key")
	bedrockTier(LeadLLMTable, "eu-central-1", "", "invoke", "vpce-0a1.example.internal", "", "")
	if changed, err := migrateBedrockTiers(RootDB); err != nil || changed != 6 {
		t.Fatalf("migrate: %d %v", changed, err)
	}
	w := resolved(t, LLMTable)
	if w.Provider != "bedrock" || w.BedrockAPI != "messages" || w.Endpoint != "https://bedrock-mantle.us-east-1.api.aws" || w.APIKey != "bedrock-api-key" {
		t.Errorf("the worker's client: %+v", w)
	}
	if cred, _ := Secure().Load("bedrock_token"); cred.Type != SecureCredBearer || cred.BaseURL != "https://bedrock-mantle.us-east-1.api.aws" {
		t.Errorf("the key's credential: %+v", cred)
	}
	l := resolved(t, LeadLLMTable)
	if l.BedrockAPI != "invoke" || l.Endpoint != "https://vpce-0a1.example.internal" || l.Region != "eu-central-1" || l.APIKey != "" {
		t.Errorf("the lead's client: %+v", l)
	}
	var lead string
	RootDB.Get(LeadLLMTable, "provider", &lead)
	cred, _ := bedrockCredential(RootDB, strings.TrimPrefix(lead, "service:"))
	if !cred.AWSMachine || cred.AWSRegion != "eu-central-1" || cred.AWSService != "bedrock" {
		t.Errorf("the private endpoint's credential: %+v", cred)
	}
}

// The settings a tier saves find the connector already signing as they say,
// or the tier's own given a new key when no other tier runs on it, or a new
// one; a tier that is not on Bedrock is left as it is.
func TestBedrockConnectorForFindsOrMakes(t *testing.T) {
	world(t)
	forgetBedrockCredentials(t)
	s := BedrockSettings{Region: "us-west-2", API: "converse", Profile: "prod"}
	first, _, err := BedrockConnectorFor(RootDB, s, "", true, "test")
	if err != nil {
		t.Fatal(err)
	}
	if again, n, _ := BedrockConnectorFor(RootDB, s, "", true, "test"); again != first || n != 0 {
		t.Errorf("the same settings: %q %d", again, n)
	}
	for _, other := range []BedrockSettings{
		{Region: "us-east-1", API: "converse", Profile: "prod"},
		{Region: "us-west-2", API: "invoke", Profile: "prod"},
		{Region: "us-west-2", API: "converse", Profile: "dev"},
	} {
		if name, _, _ := BedrockConnectorFor(RootDB, other, "", true, "test"); name == first {
			t.Errorf("%+v took %s", other, first)
		}
	}
	keyed := BedrockSettings{Region: "us-west-2", API: "converse", Key: "k1"}
	own, _, _ := BedrockConnectorFor(RootDB, keyed, "", true, "test")
	// No key, the tier's own at the same address: kept, its key in its
	// credential.
	if kept, _, _ := BedrockConnectorFor(RootDB, BedrockSettings{Region: "us-west-2", API: "converse"}, own, true, "test"); kept != own {
		t.Errorf("no key kept %q, want %q", kept, own)
	}
	// A new key: the tier's own, when it is not shared, holds it now.
	if name, _, _ := BedrockConnectorFor(RootDB, BedrockSettings{Region: "us-west-2", API: "converse", Key: "k2"}, own, false, "test"); name != own {
		t.Errorf("a new key made %q", name)
	}
	if k, _ := Secure().ModelKey("bedrock_token", "https://bedrock-runtime.us-west-2.amazonaws.com/"); k != "k2" {
		t.Errorf("the key now: %q", k)
	}
	// Shared, a new key leaves it alone.
	if name, _, _ := BedrockConnectorFor(RootDB, BedrockSettings{Region: "us-west-2", API: "converse", Key: "k3"}, own, true, "test"); name == own {
		t.Error("a shared connector's key was replaced")
	}

	RootDB.Set(LLMTable, "provider", "vllm")
	RootDB.Set(LeadLLMTable, "provider", "anthropic")
	if n, err := migrateBedrockTiers(RootDB); n != 0 || err != nil {
		t.Errorf("tiers not on Bedrock moved: %d %v", n, err)
	}
}

// A bedrock binding says which API; a Bedrock model with no credential
// cannot sign, and another dialect takes no api.
func TestTheBedrockDialectIsChecked(t *testing.T) {
	spec := templateSpec(t, "bedrock-model", "https://bedrock-runtime.us-east-1.amazonaws.com")
	if err := validateSpec(t, spec); err == nil || !strings.Contains(err.Error(), "Bedrock signs every request") {
		t.Errorf("no credential: %v", err)
	}
	spec["provider"].(map[string]any)["auth"] = map[string]any{"credential": "bedrock_model"}
	if err := validateSpec(t, spec); err != nil {
		t.Errorf("the template: %v", err)
	}
	b := spec["provides"].([]any)[0].(map[string]any)
	b["defaults"].(map[string]any)["api"] = "chat"
	if err := validateSpec(t, spec); err == nil || !strings.Contains(err.Error(), "converse, messages and invoke") {
		t.Errorf("an api Bedrock lacks: %v", err)
	}
	b["defaults"].(map[string]any)["api"] = "converse"
	b["dialect"] = "openai"
	spec["provider"].(map[string]any)["base_url"] = "https://api.example.com/v1"
	if err := validateSpec(t, spec); err == nil || !strings.Contains(err.Error(), "model and context") {
		t.Errorf("api on another dialect: %v", err)
	}
}

// Added from Templates, the Bedrock template lands as the others do: its
// credential, signing as the machine, switched off until an admin enables
// it, and its connector unapproved, with a spec that saves.
func TestTheBedrockTemplateInstalls(t *testing.T) {
	world(t)
	forgetBedrockCredentials(t)
	res, err := recipes.Install(RootDB, "bedrock-model", "admin", map[string]string{"region": "eu-west-1", "profile": "ops"})
	if err != nil || res.Imported != 2 {
		t.Fatalf("install: %+v %v", res, err)
	}
	cred, ok := Secure().Load("bedrock_model")
	if !ok || !cred.Disabled || cred.Type != SecureCredAWS || !cred.AWSMachine || cred.AWSProfile != "ops" ||
		cred.BaseURL != "https://bedrock-runtime.eu-west-1.amazonaws.com" {
		t.Errorf("the credential: %+v %v", cred, ok)
	}
	c, ok := GetConnector(RootDB, "bedrock-model")
	if !ok || c.Approved || !strings.Contains(string(c.Spec), `"api":"converse"`) {
		t.Fatalf("the connector: %+v", c)
	}
	s, err := Parse(c.Spec)
	if err == nil {
		err = s.Validate()
	}
	if err != nil {
		t.Errorf("its spec: %v", err)
	}
}
