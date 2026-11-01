package service

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/sourcehooks"
)

// storeHook keeps a Source Hook as the hooks store did: by lowercased name
// with spaces as underscores, its key apart.
func storeHook(h sourcehooks.SourceHook) {
	key := strings.ToLower(strings.ReplaceAll(h.Name, " ", "_"))
	k := h.AuthKey
	h.AuthKey = ""
	RootDB.Set(sourceHookTable, key, h)
	if k != "" {
		RootDB.CryptSet(sourceHookTable, key+"_auth", k)
	}
}

func forgetHookCredentials(t *testing.T) {
	forgetCredentials(t, "news_wall_paywall")
}

// hookKey is the key connection name keeps, and how its spec sends it.
func hookKey(t *testing.T, name string) (ConnectionSecret, string) {
	t.Helper()
	c, _ := GetConnector(RootDB, name)
	sec, _ := GetConnectionSecret(RootDB, c, "api_key")
	s, _ := specOf(c)
	f, _ := s.secretField()
	return sec, f.Send
}

// A Source Hook in a bundle imports as its search connector, unapproved, and
// an off source, the connection waiting for its key, which never travels;
// nothing exports as one.
func TestABundledHookImportsAsAConnector(t *testing.T) {
	world(t)
	forgetHookCredentials(t)
	recipe, _ := json.Marshal(sourcehooks.SourceHook{Name: "Case API", Type: sourcehooks.HookTypeAPI, Endpoint: "https://cases.example/search",
		AuthType: sourcehooks.HookAuthBearer, AuthKey: "leaked", TriggerDomains: []string{"legal"}})
	name, skip, err := sourceHookArtifact{}.ImportArtifact(RootDB, recipe, "")
	if err != nil || skip != "" || name != "case-api" {
		t.Fatalf("import: %q %q %v", name, skip, err)
	}
	c, _ := GetConnector(RootDB, "case-api")
	rs, _ := GetResearchSource(RootDB, "case-api")
	if s, _ := specOf(c); c.Approved || !rs.Off || s.Provider.Auth.Secret != "api_key" {
		t.Errorf("landed: connector approved %v, source %+v, spec %s", c.Approved, rs, c.Spec)
	}
	if HasSecret(c, "api_key") {
		t.Error("the bundle's connection has a key to send")
	}
	if _, err := (sourceHookArtifact{}).ExportArtifact(RootDB, "case-api", ""); err == nil {
		t.Error("a hook exported")
	}
}
