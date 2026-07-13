package core

// Tests for the "custom_app" artifact type: recipe shape (agent ref
// normalized to a name, owner/timestamps/disabled stripped, scripts travel
// inline), the disabled-draft import gate, slug collision rules, and the
// dependency walk (bound agent + fetch_via credentials).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// customAppTestDB pins RootDB to a fresh mem store (appSpecStore resolves
// through it) and stubs the agent-name resolver the export normalization
// uses, restoring both on cleanup.
func customAppTestDB(t *testing.T, resolve func(owner, key string) (string, bool)) {
	t.Helper()
	savedRoot, savedResolve := RootDB, ResolveAgentNameForExport
	RootDB = &DBase{Store: kvlite.MemStore()}
	ResolveAgentNameForExport = resolve
	t.Cleanup(func() { RootDB, ResolveAgentNameForExport = savedRoot, savedResolve })
}

func seedAppSpec(t *testing.T) AppSpec {
	t.Helper()
	spec := SaveAppSpec(AppSpec{
		Slug:      "tickets",
		Name:      "Ticket Tracker",
		Desc:      "Tracks tickets.",
		Owner:     "alice",
		AgentID:   "agent-123",
		Page:      json.RawMessage(`{"title":"Tickets"}`),
		RecordKey: "id",
		DataSources: []AppDataSource{{
			Name: "board", Script: "print('[]')",
			Capabilities: []string{"log", "fetch_via:jira"},
		}},
		Actions: []AppAction{{
			Name: "sync", Script: "print('{}')",
			Capabilities: []string{"fetch_via:no_auth"},
		}},
	})
	if spec.Created == "" {
		t.Fatal("SaveAppSpec should stamp Created")
	}
	return spec
}

func TestCustomAppArtifact_ExportNormalizesAndStrips(t *testing.T) {
	customAppTestDB(t, func(owner, key string) (string, bool) {
		if owner != "alice" || key != "agent-123" {
			t.Fatalf("resolver called with (%q, %q)", owner, key)
		}
		return "Helper", true
	})
	seedAppSpec(t)

	recipe, err := customAppArtifact{}.ExportArtifact(nil, "tickets", "alice")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var got AppSpec
	if err := json.Unmarshal(recipe, &got); err != nil {
		t.Fatalf("recipe: %v", err)
	}
	if got.AgentID != "Helper" {
		t.Fatalf("agent ID ref must normalize to the agent's name, got %q", got.AgentID)
	}
	if got.Owner != "" || got.Created != "" || got.Updated != "" || got.Disabled {
		t.Fatalf("owner/timestamps/disabled must not travel: %+v", got)
	}
	if got.Slug != "tickets" || len(got.DataSources) != 1 || got.DataSources[0].Script == "" || len(got.Actions) != 1 {
		t.Fatalf("app shape + scripts must travel intact: %+v", got)
	}
	// The stored spec keeps its ID ref — normalization operates on a copy.
	stored, _ := LoadAppSpec("alice", "tickets")
	if stored.AgentID != "agent-123" {
		t.Fatalf("export must not mutate the stored spec: %+v", stored)
	}

	// Resolves by display name too (the preview's probes use the recipe's
	// "name"), and errors without an owner or on a miss.
	if _, err := (customAppArtifact{}).ExportArtifact(nil, "ticket tracker", "alice"); err != nil {
		t.Fatalf("export by display name: %v", err)
	}
	if _, err := (customAppArtifact{}).ExportArtifact(nil, "tickets", ""); err == nil {
		t.Fatal("export without an owner must error")
	}
	if _, err := (customAppArtifact{}).ExportArtifact(nil, "nope", "alice"); err == nil {
		t.Fatal("export of a missing app must error")
	}
}

func TestCustomAppArtifact_ImportLandsDisabledDraft(t *testing.T) {
	customAppTestDB(t, nil)
	recipe, _ := json.Marshal(AppSpec{
		Slug: "notes", Name: "Notes", RecordKey: "id",
		Page:    json.RawMessage(`{}`),
		Actions: []AppAction{{Name: "tidy", Script: "print('{}')"}},
	})

	name, skip, err := customAppArtifact{}.ImportArtifact(nil, recipe, "bob")
	if err != nil || skip != "" || name != "notes" {
		t.Fatalf("import: name=%q skip=%q err=%v", name, skip, err)
	}
	got, ok := LoadAppSpec("bob", "notes")
	if !ok {
		t.Fatal("imported app not found in owner's store")
	}
	if !got.Disabled {
		t.Fatal("an imported app must land disabled for review")
	}
	if got.Owner != "bob" || got.Created == "" {
		t.Fatalf("owner/created must be the importer's: %+v", got)
	}

	// Same slug again → skip, never clobber.
	_, skip, err = customAppArtifact{}.ImportArtifact(nil, recipe, "bob")
	if err != nil || !strings.Contains(skip, "slug already exists") {
		t.Fatalf("same-slug import must skip: skip=%q err=%v", skip, err)
	}

	// A recipe without a slug is a hard error.
	bad, _ := json.Marshal(AppSpec{Name: "No Slug"})
	if _, _, err = (customAppArtifact{}).ImportArtifact(nil, bad, "bob"); err == nil {
		t.Fatal("slugless recipe must error")
	}
}

func TestCustomAppArtifact_Dependencies(t *testing.T) {
	customAppTestDB(t, func(owner, key string) (string, bool) { return "Helper", true })
	seedAppSpec(t)

	// Store-side walk: the bound agent (normalized) + the jira credential;
	// the bootstrap no_auth sentinel is filtered.
	deps := customAppArtifact{}.Dependencies(nil, "tickets", "alice")
	if len(deps) != 2 {
		t.Fatalf("expected [agent, credential], got %+v", deps)
	}
	if deps[0].Type != "agent" || deps[0].Name != "Helper" || deps[0].Owner != "alice" {
		t.Fatalf("agent dep must carry the normalized name + owner: %+v", deps[0])
	}
	if deps[1].Type != "credential" || deps[1].Name != "jira" {
		t.Fatalf("fetch_via cred must be a dependency: %+v", deps[1])
	}

	// Recipe-side walk sees the same references straight from the recipe.
	recipe, err := customAppArtifact{}.ExportArtifact(nil, "tickets", "alice")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	rdeps := customAppArtifact{}.RecipeDependencies(nil, recipe, "alice", nil)
	if len(rdeps) != 2 || rdeps[0].Name != "Helper" || rdeps[1].Name != "jira" {
		t.Fatalf("recipe deps must match store deps: %+v", rdeps)
	}
}
