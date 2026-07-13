package core

// Tests for the "source_hook" artifact type: secret-free recipes (AuthKey
// never travels, endpoint-embedded keys refuse export), the disabled-draft
// import gate and its enforcement at every consultation path, and the
// skill-closure fallback that resolves hook-backed tool names into hook
// dependencies instead of silently dropping them.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// sourceHookTestDB pins RootDB to a fresh mem store and empties the global
// hook registry for the test's duration, restoring both on cleanup.
// SaveSourceHook writes the db AND reloads the registry, so seeding through
// it exercises the real persistence path.
func sourceHookTestDB(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	savedRoot := RootDB
	RootDB = db
	sourceHookRegistry.mu.Lock()
	savedHooks := sourceHookRegistry.hooks
	sourceHookRegistry.hooks = nil
	sourceHookRegistry.mu.Unlock()
	t.Cleanup(func() {
		RootDB = savedRoot
		sourceHookRegistry.mu.Lock()
		sourceHookRegistry.hooks = savedHooks
		sourceHookRegistry.mu.Unlock()
	})
	return db
}

func TestSourceHookArtifact_ExportStripsSecret(t *testing.T) {
	db := sourceHookTestDB(t)
	SaveSourceHook(db, SourceHook{
		Name: "PubMed", Type: HookTypeAPI,
		Endpoint: "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esearch.fcgi",
		AuthType: SourceHookAuth("api_key"), AuthKey: "sk-live-abcdef123456",
		QueryParam: "term", ToolName: "pubmed_search",
		TriggerDomains: []string{"medical"},
		Disabled:       true,
	})

	recipe, err := sourceHookArtifact{}.ExportArtifact(db, "pubmed", "")
	if err != nil {
		t.Fatalf("export (case-insensitive name): %v", err)
	}
	var got SourceHook
	if err := json.Unmarshal(recipe, &got); err != nil {
		t.Fatalf("recipe: %v", err)
	}
	if got.AuthKey != "" {
		t.Fatal("AuthKey must never travel")
	}
	if got.Disabled {
		t.Fatal("Disabled is a local mute, not part of the hook's shape")
	}
	if got.Name != "PubMed" || got.ToolName != "pubmed_search" || len(got.TriggerDomains) != 1 {
		t.Fatalf("hook shape must travel intact: %+v", got)
	}

	if _, err := (sourceHookArtifact{}).ExportArtifact(db, "nope", ""); err == nil {
		t.Fatal("export of a missing hook must error")
	}
}

func TestSourceHookArtifact_ExportRefusesEndpointSecret(t *testing.T) {
	db := sourceHookTestDB(t)
	SaveSourceHook(db, SourceHook{
		Name: "Leaky", Type: HookTypeAPI,
		Endpoint: "https://api.example.com/v1/search?api_key=sk12345678abc",
	})
	if _, err := (sourceHookArtifact{}).ExportArtifact(db, "Leaky", ""); err == nil || !strings.Contains(err.Error(), "auth key") {
		t.Fatalf("endpoint-embedded key must refuse export, got err=%v", err)
	}
}

func TestSourceHookArtifact_ImportLandsDisabledSecretFree(t *testing.T) {
	db := sourceHookTestDB(t)
	recipe, _ := json.Marshal(SourceHook{
		Name: "OpenAlex", Type: HookTypeAPI,
		Endpoint: "https://api.openalex.org/works",
		AuthKey:  "a-traveled-secret-is-a-lie",
	})

	name, skip, err := sourceHookArtifact{}.ImportArtifact(db, recipe, "bob")
	if err != nil || skip != "" || name != "OpenAlex" {
		t.Fatalf("import: name=%q skip=%q err=%v", name, skip, err)
	}
	got, ok := findSourceHookForExport("OpenAlex")
	if !ok {
		t.Fatal("imported hook not in registry")
	}
	if !got.Disabled {
		t.Fatal("an imported hook must land disabled for review")
	}
	if got.AuthKey != "" {
		t.Fatal("a traveled AuthKey must be dropped on import")
	}

	// Same name again → skip, never clobber (SaveSourceHook overwrites by
	// name, so this check is what keeps import non-destructive).
	_, skip, err = sourceHookArtifact{}.ImportArtifact(db, recipe, "bob")
	if err != nil || !strings.Contains(skip, "already exists") {
		t.Fatalf("same-named import must skip: skip=%q err=%v", skip, err)
	}

	bad, _ := json.Marshal(SourceHook{Endpoint: "https://x"})
	if _, _, err = (sourceHookArtifact{}).ImportArtifact(db, bad, "bob"); err == nil {
		t.Fatal("nameless recipe must error")
	}
}

func TestSourceHook_DisabledIsMutedEverywhere(t *testing.T) {
	db := sourceHookTestDB(t)
	SaveSourceHook(db, SourceHook{
		Name: "Muted", Type: HookTypeAPI,
		Endpoint: "https://api.example.com/search", QueryParam: "q",
		AlwaysActive: true, ExposeToLLM: true, ToolName: "muted_search",
		Disabled: true,
	})
	SaveSourceHook(db, SourceHook{
		Name: "Paywalled", Type: HookTypePaywall,
		Domains:  []string{"example.com"},
		Disabled: true,
	})

	if got := ActiveSourceHooks(); len(got) != 0 {
		t.Fatalf("ActiveSourceHooks must exclude disabled hooks: %+v", got)
	}
	if len(RegisteredSourceHooks()) != 2 {
		t.Fatal("RegisteredSourceHooks must still list disabled hooks (management surfaces)")
	}
	if got := HooksForTopic([]string{"anything"}); len(got) != 0 {
		t.Fatalf("HooksForTopic must skip disabled hooks: %+v", got)
	}
	if h := HookForDomain("https://www.example.com/article"); h != nil {
		t.Fatalf("HookForDomain must skip disabled hooks: %+v", h)
	}
	if defs := BuildSourceHookAgentToolDefs(db); len(defs) != 0 {
		t.Fatalf("disabled hooks must not become LLM tools: %+v", defs)
	}
	if _, ok := SourceHookToolDefByName("muted_search"); ok {
		t.Fatal("a skill-granted tool name must not resolve to a disabled hook")
	}
	// The dependency walk still resolves it — a reference exists whether or
	// not the hook may fire.
	if _, ok := FindSourceHookByToolName("muted_search"); !ok {
		t.Fatal("FindSourceHookByToolName must include disabled hooks (export closure)")
	}
}

func TestSkillClosure_CarriesSourceHook(t *testing.T) {
	db := sourceHookTestDB(t)
	SaveSourceHook(db, SourceHook{
		Name: "PubMed", Type: HookTypeAPI,
		Endpoint: "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esearch.fcgi",
		ToolName: "pubmed_search",
	})
	if _, err := SaveSkill(db, "alice", SkillRecord{
		Name: "clinical", Description: "Use for clinical questions.",
		AllowedTools: []string{"pubmed_search"},
	}); err != nil {
		t.Fatalf("save skill: %v", err)
	}

	b, err := ExportArtifactBundle(db, []ArtifactSel{{Type: "skill", Name: "clinical", Owner: "alice"}})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(b.Artifacts) != 2 || b.Artifacts[0].Type != "skill" || b.Artifacts[1].Type != "source_hook" {
		t.Fatalf("expected [skill, source_hook], got %v", bundleNames(b))
	}
	if b.Artifacts[1].Name != "PubMed" {
		t.Fatalf("hook-backed tool name must fold in the hook by its display name: %+v", b.Artifacts[1])
	}
}
