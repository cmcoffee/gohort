package service

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/migrate"
)

// planBConnector is a connector record as plan B stored one gohort
// provided, carrying the mark the record no longer has.
type planBConnector struct {
	Name     string          `json:"name"`
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	Spec     json.RawMessage `json:"spec"`
	Provided bool            `json:"provided"`
}

// migrated marks the connection migration as run, which clearing waits for.
func migrated(t *testing.T) {
	t.Helper()
	m := migrate.MigrationMarker{App: "core", Name: IntegrationMigration, Done: true}
	RootDB.Set(migrate.MigrationsTable, m.Key(), m)
}

// Clearing removes a connection gohort once provided only when its spec no
// longer validates and no page uses it, and a model connection no page
// uses, each with its space and key; a space no connection owns; the
// markers of migrations that are gone; and saved flows, retired. A provided
// one that still works, one a page uses, an ordinary one never provided, an
// owned space and any other marker stay. The dry run's walk is the delete's.
func TestClearingRemovesOnlyOldConnectionRecords(t *testing.T) {
	world(t)
	migrated(t)
	llm := json.RawMessage(`{"provider":{"transport":"http","base_url":"https://api.example.test","auth":{"none":true},"operations":{"chat":{"method":"POST","path":"/v1/chat"}}},"provides":[{"capability":"llm.chat","steps":[{"call":"chat"}]}]}`)
	searchConnector(t, "local-search", "")
	works, _ := GetConnector(RootDB, "local-search")
	for _, c := range []planBConnector{
		{Name: "llm-anthropic", ID: "c_llm", Kind: Kind, Spec: llm, Provided: true},
		{Name: "llm-offered", ID: "c_offered", Kind: Kind, Spec: llm, Provided: true},
		{Name: "local-search", ID: works.ID, Kind: Kind, Spec: works.Spec, Provided: true},
		{Name: "hand-made", ID: "c_hand", Kind: Kind, Spec: works.Spec},
		{Name: "anthropic-model", ID: "c_model", Kind: Kind, Spec: llm},
	} {
		RootDB.Set("connectors", c.Name, c)
	}
	RootDB.Set(MusicTable, "agents", []string{"llm-offered"})
	llmConn, _ := GetConnector(RootDB, "llm-anthropic")
	SetConnectionSecret(RootDB, llmConn, "api_key", "sk-planb", []string{"https://api.example.test"})
	RootDB.Sub("connection:c_gone").Set("secret", "api_key", "sk-orphan")
	SetConnectionSecret(RootDB, works, "api_key", "sk-kept", []string{"https://find.example.test"})
	for _, name := range []string{"connections:v2", "images:v1", "something_else:v1"} {
		m := migrate.MigrationMarker{App: "core", Name: name, Done: true}
		RootDB.Set(migrate.MigrationsTable, m.Key(), m)
	}

	RootDB.Set("service_flows", "announce", map[string]string{"name": "announce"})
	old := findOldRecords(RootDB)
	if got := strings.Join(old.flows, ","); got != "announce" {
		t.Errorf("flows: %s", got)
	}
	if got := strings.Join(old.connections, ","); got != "anthropic-model,llm-anthropic" {
		t.Errorf("connections: %s", got)
	}
	if got := strings.Join(old.spaces, ","); got != "c_gone" {
		t.Errorf("spaces: %s", got)
	}
	if got := strings.Join(old.markers, ","); got != "core:connections:v2:,core:images:v1:" {
		t.Errorf("markers: %s", got)
	}

	n, err := clearOldRecords(RootDB)
	if err != nil || n != old.count() {
		t.Fatalf("cleared %d of %d: %v", n, old.count(), err)
	}
	for _, gone := range []string{"llm-anthropic", "anthropic-model"} {
		if _, ok := GetConnector(RootDB, gone); ok {
			t.Errorf("%s is still there", gone)
		}
	}
	if _, ok := GetConnectionSecret(RootDB, llmConn, "api_key"); ok {
		t.Error("its key outlived it")
	}
	for _, name := range []string{"llm-offered", "local-search", "hand-made"} {
		if _, ok := GetConnector(RootDB, name); !ok {
			t.Errorf("%s was removed", name)
		}
	}
	if kept, ok := GetConnectionSecret(RootDB, works, "api_key"); !ok || kept.Value != "sk-kept" {
		t.Error("an owned space lost its key")
	}
	for _, tbl := range RootDB.AllTables() {
		if strings.HasPrefix(tbl, "connection:c_gone") {
			t.Errorf("the orphaned space is still there: %q", tbl)
		}
	}
	var m migrate.MigrationMarker
	if RootDB.Get(migrate.MigrationsTable, "core:connections:v2:", &m) || !RootDB.Get(migrate.MigrationsTable, "core:something_else:v1:", &m) {
		t.Error("the wrong markers went")
	}
	if again := findOldRecords(RootDB); again.count() != 0 {
		t.Errorf("left after clearing: %v", again.lines())
	}
}

// While a setting needs setting again, clearing is refused, naming it, and
// removes nothing: the keys these records hold are what setting it replaces.
func TestClearingWaitsForSettingsToBeSetAgain(t *testing.T) {
	world(t)
	RootDB.Set(LLMTable, "provider", "llm:anthropic")
	RootDB.Sub("connection:c_gone").Set("secret", "api_key", "sk-orphan")
	if n, err := clearOldRecords(RootDB); err == nil || n != 0 || !strings.Contains(err.Error(), IntegrationMigration) {
		t.Errorf("before the migration ran, cleared %d: %v", n, err)
	}
	migrated(t)
	n, err := clearOldRecords(RootDB)
	if err == nil || n != 0 || !strings.Contains(err.Error(), "Worker LLM") {
		t.Errorf("cleared %d: %v", n, err)
	}
	if findOldRecords(RootDB).count() != 1 {
		t.Error("a refused clear removed something")
	}
	RootDB.Set(LLMTable, "provider", "anthropic")
	if n, err := clearOldRecords(RootDB); err != nil || n != 1 {
		t.Errorf("once set again: %d %v", n, err)
	}
}
