package service

// A tier on AWS Bedrock kept its region, its Bedrock API, an AWS profile or
// a Bedrock API key, and a private endpoint in the LLM settings, and signed
// its requests in Go with the machine's AWS identity. Signing is now a kind
// of credential (aws_sigv4), so Bedrock is a model connector like the others:
// its address is the host the client calls, its credential signs as the
// machine (or holds a Bedrock API key), and the tier names it. The Bedrock
// clients stay Go: the connector names the bedrock dialect.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/recipes"
)

// bedrockTemplate is the template a Bedrock tier's connector is made from.
const bedrockTemplate = "bedrock-model"

// bedrockDefaultModel is what the Bedrock client ran when a tier named no
// model.
const bedrockDefaultModel = "anthropic.claude-sonnet-5"

// BedrockSettings are a tier's Bedrock settings, as the LLM form and the
// stored tier hold them. A blank API is the Messages API, which a tier set
// up before the choice existed speaks; Key is a Bedrock API key, sent as a
// bearer token in place of signing; Endpoint is a private endpoint's host.
type BedrockSettings struct {
	Region, Profile, API, Endpoint, Model, Key string
}

// api is the Bedrock API the settings speak.
func (s BedrockSettings) api() string {
	if a := strings.ToLower(strings.TrimSpace(s.API)); bedrockAPIs[a] {
		return a
	}
	return "messages"
}

// region is the settings' region, else the environment's, as the client
// read it, else us-east-1.
func (s BedrockSettings) region() string {
	for _, r := range []string{s.Region, os.Getenv("AWS_REGION"), os.Getenv("AWS_DEFAULT_REGION")} {
		if r = strings.TrimSpace(r); r != "" {
			return r
		}
	}
	return "us-east-1"
}

// base is the address the client calls: a private endpoint, or the regional
// host of the settings' API.
func (s BedrockSettings) base() string {
	if ep := strings.TrimSpace(s.Endpoint); ep != "" {
		ep = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(ep, "https://"), "http://"), "/")
		return "https://" + ep
	}
	if s.api() == "messages" {
		return "https://bedrock-mantle." + s.region() + ".api.aws"
	}
	return "https://bedrock-runtime." + s.region() + ".amazonaws.com"
}

// BedrockConnectorFor gives the approved model connector a tier on Bedrock
// with settings s runs on: current (the one it runs on now) when it is at
// the same address and API and no new key is given; one already signing the
// same way (as the machine under the same profile, or with the same key);
// current given the new key, when no other tier runs on it (shared false);
// else the Bedrock template installed with s. from names the settings, for
// the connector's description. It returns the connector's name and how many
// records it wrote.
func BedrockConnectorFor(db Database, s BedrockSettings, current string, shared bool, from string) (string, int, error) {
	key := strings.TrimSpace(s.Key)
	if current != "" && bedrockAt(db, current, s) {
		if key == "" {
			return current, 0, nil
		}
	}
	for _, c := range ListConnectors(db) {
		if c.Kind == Kind && c.Template == bedrockTemplate && c.Approved && bedrockAt(db, c.Name, s) && bedrockSigns(db, c.Name, s) {
			return c.Name, 0, nil
		}
	}
	if key != "" && current != "" && !shared && bedrockAt(db, current, s) {
		if cred, ok := bedrockCredential(db, current); ok && cred.Type == SecureCredBearer {
			if err := Secure().Save(cred, key); err != nil {
				return "", 0, fmt.Errorf("saving the new key in credential %s: %w", cred.Name, err)
			}
			return current, 1, nil
		}
	}
	return installBedrock(db, s, from)
}

// BedrockAt reports whether connector name is a Bedrock model at the
// settings' address, speaking their API: the one a form showing them means.
func BedrockAt(db Database, name string, s BedrockSettings) bool { return bedrockAt(db, name, s) }

// bedrockAt reports whether connector name is a Bedrock model at the
// settings' address, speaking their API.
func bedrockAt(db Database, name string, s BedrockSettings) bool {
	c, ok := GetConnector(db, name)
	if !ok || c.Kind != Kind {
		return false
	}
	spec, err := specOf(c)
	if err != nil {
		return false
	}
	b, _, err := spec.binding("llm.chat")
	if err != nil || b.Dialect != "bedrock" {
		return false
	}
	api := "converse"
	if a := fmt.Sprint(b.Defaults["api"]); bedrockAPIs[a] {
		api = a
	}
	return strings.TrimRight(strings.TrimSpace(spec.Provider.BaseURL), "/") == s.base() && api == s.api()
}

// bedrockCredential is the credential connector name signs with.
func bedrockCredential(db Database, name string) (SecureCredential, bool) {
	c, _ := GetConnector(db, name)
	spec, err := specOf(c)
	if err != nil || spec.Provider.Auth.Credential == "" {
		return SecureCredential{}, false
	}
	return Secure().Load(spec.Provider.Auth.Credential)
}

// bedrockSigns reports whether connector name signs as the settings do:
// with their key, or with none as the machine under their profile.
func bedrockSigns(db Database, name string, s BedrockSettings) bool {
	cred, ok := bedrockCredential(db, name)
	if !ok || cred.Disabled {
		return false
	}
	if key := strings.TrimSpace(s.Key); key != "" {
		k, err := Secure().ModelKey(cred.Name, s.base()+"/")
		return err == nil && k == key
	}
	return cred.Type == SecureCredAWS && cred.AWSMachine && strings.TrimSpace(cred.AWSProfile) == strings.TrimSpace(s.Profile)
}

// installBedrock installs the Bedrock template for settings s, approved: at
// their address and API, signing as the machine under their profile, or,
// with a key, sending it as a bearer token from a credential of its own.
func installBedrock(db Database, s BedrockSettings, from string) (string, int, error) {
	r, _, ok := recipes.Get(db, bedrockTemplate)
	if !ok {
		return "", 0, fmt.Errorf("the template %s is not on this gohort", bedrockTemplate)
	}
	model := strings.TrimSpace(s.Model)
	if model == "" {
		model = bedrockDefaultModel
	}
	// The template asks for an API it serves at the runtime host; the
	// Messages API's host, or a private endpoint, is set below.
	bundle, _, _, err := recipes.Fill(r, map[string]string{"region": s.region(), "api": "converse", "profile": strings.TrimSpace(s.Profile), "model": model})
	if err != nil {
		return "", 0, fmt.Errorf("the template %s: %w", bedrockTemplate, err)
	}
	var cred SecureCredential
	var conn Connector
	for _, a := range bundle.Artifacts {
		switch a.Type {
		case "credential":
			err = json.Unmarshal(a.Recipe, &cred)
		case "connector":
			err = json.Unmarshal(a.Recipe, &conn)
		}
		if err != nil {
			return "", 0, fmt.Errorf("the template %s: %w", bedrockTemplate, err)
		}
	}
	base := s.base()
	if key := strings.TrimSpace(s.Key); key != "" {
		cred = SecureCredential{Name: "bedrock_token", Type: SecureCredBearer, BaseURL: base,
			Description: "A Bedrock API key, sent as a bearer token, from the " + from + " settings."}
	} else {
		cred.BaseURL = base
		// An address that does not say its region and service (a private
		// endpoint) is signed for the settings' region.
		if strings.TrimSpace(s.Endpoint) != "" {
			cred.AWSRegion, cred.AWSService = s.region(), "bedrock"
			if s.api() == "messages" {
				cred.AWSService = "bedrock-mantle"
			}
		}
	}
	cred.Name, cred.Disabled = freeCredentialName(cred.Name), false
	if err := Secure().Save(cred, strings.TrimSpace(s.Key)); err != nil {
		return "", 0, fmt.Errorf("saving credential %s: %w", cred.Name, err)
	}
	var spec map[string]any
	if err := json.Unmarshal(conn.Spec, &spec); err != nil {
		return "", 1, err
	}
	provider, _ := spec["provider"].(map[string]any)
	provider["base_url"] = base
	provider["auth"] = map[string]any{"credential": cred.Name}
	if provides, _ := spec["provides"].([]any); len(provides) > 0 {
		if b, ok := provides[0].(map[string]any); ok {
			if d, ok := b["defaults"].(map[string]any); ok {
				d["api"] = s.api()
			}
		}
	}
	if conn.Spec, err = json.Marshal(spec); err != nil {
		return "", 1, err
	}
	conn.Name, conn.Kind = freeConnectorName(db, conn.Name), Kind
	conn.Desc += ", from the " + from + " settings"
	if err := SaveConnector(db, conn); err != nil {
		return "", 1, fmt.Errorf("saving connector %s: %w", conn.Name, err)
	}
	if err := ApproveConnector(db, conn.Name); err != nil {
		return "", 1, fmt.Errorf("approving connector %s: %w", conn.Name, err)
	}
	return conn.Name, 2, nil
}

// MigrateBedrockTiers moves the worker and lead tiers on Bedrock onto model
// connectors, once (Admin > Migrations shows it ran).
func MigrateBedrockTiers(db Database) {
	NewMigrationRunner("core", "").Once("bedrock_tiers_to_connectors:v1", func() int {
		changed, err := migrateBedrockTiers(db)
		if err != nil {
			// Recorded on the marker, where Admin > Migrations shows it.
			panic(fmt.Sprintf("a Bedrock tier was not moved to a connector: %v", err))
		}
		return changed
	})
}

// migrateBedrockTiers is MigrateBedrockTiers' work: each tier on Bedrock
// names a model connector at its address, speaking its API and signing as it
// did (both tiers alike share one), names the model it ran, and its Bedrock
// rows are gone. A tier with no key signed as the machine, so every Bedrock
// tier moves. It reports how many records it changed.
func migrateBedrockTiers(db Database) (int, error) {
	if db == nil {
		return 0, nil
	}
	total := 0
	for _, t := range []struct{ table, title string }{{LLMTable, "Worker LLM"}, {LeadLLMTable, "Lead LLM"}} {
		var provider string
		var s BedrockSettings
		db.Get(t.table, "provider", &provider)
		if strings.ToLower(strings.TrimSpace(provider)) != "bedrock" {
			continue
		}
		db.Get(t.table, "aws_region", &s.Region)
		db.Get(t.table, "aws_profile", &s.Profile)
		db.Get(t.table, "bedrock_api", &s.API)
		db.Get(t.table, "endpoint", &s.Endpoint)
		db.Get(t.table, "model", &s.Model)
		db.Get(t.table, "api_key", &s.Key)
		name, changed, err := BedrockConnectorFor(db, s, "", true, t.title)
		total += changed
		if err != nil {
			return total, fmt.Errorf("the %s: %w", t.title, err)
		}
		db.Set(t.table, "provider", media.ServiceProviderPrefix+name)
		if strings.TrimSpace(s.Model) == "" {
			db.Set(t.table, "model", bedrockDefaultModel)
		}
		for _, k := range []string{"api_key", "endpoint", "aws_region", "aws_profile", "bedrock_api"} {
			db.Unset(t.table, k)
		}
		total++
		Log("[migration] the %s now runs on model connector %s (Bedrock %s at %s, as the settings had it)", t.title, name, s.api(), s.base())
	}
	return total, nil
}
