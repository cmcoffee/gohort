package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// fakeArtifact is a registry-only ArtifactType backed by an in-memory map, so
// closure behavior can be exercised without any store. deps wires up the
// dependency edges; missing names simulate an unresolvable dependency.
type fakeArtifact struct {
	typ     string
	recipes map[string]string        // name -> recipe payload (absent = export fails)
	deps    map[string][]ArtifactSel // name -> declared dependencies
}

func (f *fakeArtifact) ArtifactType() string                 { return f.typ }
func (f *fakeArtifact) ListArtifacts(Database) []ArtifactSel { return nil }

func (f *fakeArtifact) ExportArtifact(_ Database, name, _ string) (json.RawMessage, error) {
	payload, ok := f.recipes[name]
	if !ok {
		return nil, Error("no such " + f.typ + ": " + name)
	}
	return json.Marshal(payload)
}

// ImportArtifact registers the artifact as present (recipe is a JSON string =
// the artifact's name), so the post-import dependency pass can tell an imported
// or pre-existing reference from a missing one. A same-named artifact skips.
func (f *fakeArtifact) ImportArtifact(_ Database, recipe json.RawMessage, _ string) (string, string, error) {
	var name string
	_ = json.Unmarshal(recipe, &name)
	if name == "" {
		return "", "", Error("missing name")
	}
	if f.recipes == nil {
		f.recipes = map[string]string{}
	}
	if _, exists := f.recipes[name]; exists {
		return name, "already exists", nil
	}
	f.recipes[name] = name
	return name, "", nil
}

func (f *fakeArtifact) Dependencies(_ Database, name, _ string) []ArtifactSel {
	return f.deps[name]
}

// withFakeTypes swaps the global artifact registry for the duration of a test,
// restoring it afterward so no other test sees the fakes.
func withFakeTypes(t *testing.T, types ...ArtifactType) {
	t.Helper()
	saved := artifactTypes
	artifactTypes = map[string]ArtifactType{}
	for _, at := range types {
		RegisterArtifactType(at)
	}
	t.Cleanup(func() { artifactTypes = saved })
}

func bundleNames(b ArtifactBundle) []string {
	out := make([]string, 0, len(b.Artifacts))
	for _, a := range b.Artifacts {
		out = append(out, a.Type+"/"+a.Name)
	}
	return out
}

func TestExportClosure_PullsTransitiveDeps(t *testing.T) {
	tool := &fakeArtifact{
		typ:     "tool",
		recipes: map[string]string{"weather": "wx"},
		deps:    map[string][]ArtifactSel{"weather": {{Type: "credential", Name: "openweather"}}},
	}
	cred := &fakeArtifact{typ: "credential", recipes: map[string]string{"openweather": "key"}}
	withFakeTypes(t, tool, cred)

	b, err := ExportArtifactBundle(nil, []ArtifactSel{{Type: "tool", Name: "weather", Owner: "u"}})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	got := bundleNames(b)
	if len(got) != 2 || got[0] != "tool/weather" || got[1] != "credential/openweather" {
		t.Fatalf("expected tool then its credential, got %v", got)
	}
}

func TestExportClosure_TransitiveThreeLevels(t *testing.T) {
	// agent → tool → credential: the chain agent-level closure introduces. One
	// explicit agent selection must pull the tool AND the tool's credential.
	agent := &fakeArtifact{
		typ:     "agent",
		recipes: map[string]string{"scout": "a"},
		deps:    map[string][]ArtifactSel{"scout": {{Type: "tool", Name: "weather", Owner: "u"}}},
	}
	tool := &fakeArtifact{
		typ:     "tool",
		recipes: map[string]string{"weather": "wx"},
		deps:    map[string][]ArtifactSel{"weather": {{Type: "credential", Name: "openweather"}}},
	}
	cred := &fakeArtifact{typ: "credential", recipes: map[string]string{"openweather": "key"}}
	withFakeTypes(t, agent, tool, cred)

	b, err := ExportArtifactBundle(nil, []ArtifactSel{{Type: "agent", Name: "scout", Owner: "u"}})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	got := bundleNames(b)
	want := []string{"agent/scout", "tool/weather", "credential/openweather"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestExportShallow_SkipsDeps(t *testing.T) {
	// The "Include dependencies" opt-out path: exactly the selection, no closure.
	tool := &fakeArtifact{
		typ:     "tool",
		recipes: map[string]string{"weather": "wx"},
		deps:    map[string][]ArtifactSel{"weather": {{Type: "credential", Name: "openweather"}}},
	}
	cred := &fakeArtifact{typ: "credential", recipes: map[string]string{"openweather": "key"}}
	withFakeTypes(t, tool, cred)

	b, err := ExportArtifactBundleShallow(nil, []ArtifactSel{{Type: "tool", Name: "weather", Owner: "u"}})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if got := bundleNames(b); len(got) != 1 || got[0] != "tool/weather" {
		t.Fatalf("shallow export must not pull dependencies, got %v", got)
	}
	// The explicit selection is still strict even without closure.
	if _, err := ExportArtifactBundleShallow(nil, []ArtifactSel{{Type: "tool", Name: "nope", Owner: "u"}}); err == nil {
		t.Fatal("expected an error for a missing explicit selection in a shallow export")
	}
}

func TestExportClosure_DedupsAndIsIdempotent(t *testing.T) {
	// Two tools sharing one credential must not emit the credential twice.
	tool := &fakeArtifact{
		typ:     "tool",
		recipes: map[string]string{"a": "1", "b": "2"},
		deps: map[string][]ArtifactSel{
			"a": {{Type: "credential", Name: "shared"}},
			"b": {{Type: "credential", Name: "shared"}},
		},
	}
	cred := &fakeArtifact{typ: "credential", recipes: map[string]string{"shared": "k"}}
	withFakeTypes(t, tool, cred)

	b, err := ExportArtifactBundle(nil, []ArtifactSel{
		{Type: "tool", Name: "a", Owner: "u"},
		{Type: "tool", Name: "b", Owner: "u"},
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	seen := 0
	for _, a := range b.Artifacts {
		if a.Type == "credential" && a.Name == "shared" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("shared credential should appear exactly once, saw %d (%v)", seen, bundleNames(b))
	}
}

func TestExportClosure_MissingDepSkippedNotFatal(t *testing.T) {
	// The tool references a credential that doesn't exist. The export must still
	// succeed with the tool; the unresolvable dependency is silently dropped.
	tool := &fakeArtifact{
		typ:     "tool",
		recipes: map[string]string{"t": "1"},
		deps:    map[string][]ArtifactSel{"t": {{Type: "credential", Name: "ghost"}}},
	}
	cred := &fakeArtifact{typ: "credential", recipes: map[string]string{}}
	withFakeTypes(t, tool, cred)

	b, err := ExportArtifactBundle(nil, []ArtifactSel{{Type: "tool", Name: "t", Owner: "u"}})
	if err != nil {
		t.Fatalf("a missing dependency must not fail the export: %v", err)
	}
	if got := bundleNames(b); len(got) != 1 || got[0] != "tool/t" {
		t.Fatalf("expected just the tool, got %v", got)
	}
}

func TestExportClosure_ExplicitTypoStillErrors(t *testing.T) {
	// The dependency tier is best-effort, but an explicit selection typo must
	// still be a hard error.
	withFakeTypes(t, &fakeArtifact{typ: "tool", recipes: map[string]string{}})
	if _, err := ExportArtifactBundle(nil, []ArtifactSel{{Type: "tool", Name: "nope", Owner: "u"}}); err == nil {
		t.Fatal("expected an error for a missing explicit selection")
	}
	if _, err := ExportArtifactBundle(nil, []ArtifactSel{{Type: "bogus", Name: "x"}}); err == nil {
		t.Fatal("expected an error for an unknown explicit type")
	}
}

// importBundleBytes builds bundle JSON whose artifacts each carry their name as
// the recipe (the shape fakeArtifact.ImportArtifact expects).
func importBundleBytes(t *testing.T, sels ...ArtifactSel) []byte {
	t.Helper()
	b := ArtifactBundle{Bundle: ArtifactBundleFormat}
	for _, s := range sels {
		recipe, _ := json.Marshal(s.Name)
		b.Artifacts = append(b.Artifacts, PortableArtifact{Type: s.Type, Name: s.Name, Recipe: recipe})
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	return data
}

func TestImportWarns_MissingDependency(t *testing.T) {
	tool := &fakeArtifact{typ: "tool", deps: map[string][]ArtifactSel{
		"weather": {{Type: "credential", Name: "openweather"}}}}
	cred := &fakeArtifact{typ: "credential"}
	withFakeTypes(t, tool, cred)

	// Bundle carries the tool but NOT its credential, and the install has none.
	res, err := ImportArtifactBundle(nil, importBundleBytes(t, ArtifactSel{Type: "tool", Name: "weather"}), "u")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Imported != 1 || len(res.Warnings) != 1 {
		t.Fatalf("expected 1 imported + 1 warning, got imported=%d warnings=%v", res.Imported, res.Warnings)
	}
	if len(res.Outcomes) != 1 || len(res.Outcomes[0].Warnings) != 1 {
		t.Fatalf("warning should attach to the tool's outcome, got %+v", res.Outcomes)
	}
	if !strings.Contains(res.Summary(), "Warning:") {
		t.Fatalf("summary should surface the warning, got %q", res.Summary())
	}
}

func TestImportNoWarn_DependencyInBundle(t *testing.T) {
	tool := &fakeArtifact{typ: "tool", deps: map[string][]ArtifactSel{
		"weather": {{Type: "credential", Name: "openweather"}}}}
	cred := &fakeArtifact{typ: "credential"}
	withFakeTypes(t, tool, cred)

	// Credential travels in the same bundle (after the tool, as closure emits it).
	res, err := ImportArtifactBundle(nil, importBundleBytes(t,
		ArtifactSel{Type: "tool", Name: "weather"},
		ArtifactSel{Type: "credential", Name: "openweather"}), "u")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Imported != 2 || len(res.Warnings) != 0 {
		t.Fatalf("a bundled dependency must not warn, got imported=%d warnings=%v", res.Imported, res.Warnings)
	}
}

func TestImportNoWarn_DependencyAlreadyPresent(t *testing.T) {
	tool := &fakeArtifact{typ: "tool", deps: map[string][]ArtifactSel{
		"weather": {{Type: "credential", Name: "openweather"}}}}
	// Credential already on the install (not in the bundle).
	cred := &fakeArtifact{typ: "credential", recipes: map[string]string{"openweather": "openweather"}}
	withFakeTypes(t, tool, cred)

	res, err := ImportArtifactBundle(nil, importBundleBytes(t, ArtifactSel{Type: "tool", Name: "weather"}), "u")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("a dependency already present must not warn, got %v", res.Warnings)
	}
}

func TestExportableCredential(t *testing.T) {
	for _, name := range []string{"", "none", "no_auth", "NO_AUTH", " none "} {
		if exportableCredential(name) {
			t.Errorf("%q should not be an exportable credential dependency", name)
		}
	}
	for _, name := range []string{"openweather", "github_api"} {
		if !exportableCredential(name) {
			t.Errorf("%q should be an exportable credential dependency", name)
		}
	}
}

func TestConnectorCredentialRefs(t *testing.T) {
	restPoll := json.RawMessage(`{"credential":"stripe","url":"https://x"}`)
	if got := connectorCredentialRefs(restPoll); len(got) != 1 || got[0] != "stripe" {
		t.Fatalf("rest_poll: expected [stripe], got %v", got)
	}
	mcp := json.RawMessage(`{"auth_mode":"secure_api","secure_cred":"atlassian"}`)
	if got := connectorCredentialRefs(mcp); len(got) != 1 || got[0] != "atlassian" {
		t.Fatalf("mcp: expected [atlassian], got %v", got)
	}
	if got := connectorCredentialRefs(json.RawMessage(`{"kind":"desktop_command"}`)); got != nil {
		t.Fatalf("no-credential spec: expected nil, got %v", got)
	}
}

// fakeRecipeArtifact extends fakeArtifact with recipe-side dependency
// declarations keyed by the artifact's name (the recipe is a JSON string =
// the name, same convention as fakeArtifact).
type fakeRecipeArtifact struct {
	fakeArtifact
	recipeDeps map[string][]ArtifactSel
}

func (f *fakeRecipeArtifact) RecipeDependencies(_ Database, recipe json.RawMessage, _ string, _ func(typ, name string) bool) []ArtifactSel {
	var name string
	_ = json.Unmarshal(recipe, &name)
	return f.recipeDeps[name]
}

func TestPreview_PredictsImportAndSkip(t *testing.T) {
	// "weather" is new; "old" already exists on the install; "mystery" has an
	// unregistered type.
	tool := &fakeArtifact{typ: "tool", recipes: map[string]string{"old": "old"}}
	withFakeTypes(t, tool)

	data := importBundleBytes(t,
		ArtifactSel{Type: "tool", Name: "weather"},
		ArtifactSel{Type: "tool", Name: "old"},
		ArtifactSel{Type: "bogus", Name: "mystery"},
	)
	res, err := PreviewArtifactBundle(nil, data, "u")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if res.WouldImport != 1 || res.WouldSkip != 2 {
		t.Fatalf("expected 1 import / 2 skips, got %d/%d (%+v)", res.WouldImport, res.WouldSkip, res.Items)
	}
	byName := map[string]ArtifactPreviewItem{}
	for _, it := range res.Items {
		byName[it.Name] = it
	}
	if byName["weather"].Action != "import" {
		t.Fatalf("new artifact should predict import: %+v", byName["weather"])
	}
	if it := byName["old"]; it.Action != "skip" || !strings.Contains(it.Detail, "already exists") {
		t.Fatalf("existing artifact should predict skip: %+v", it)
	}
	if it := byName["mystery"]; it.Action != "skip" || it.Detail != "unknown artifact type" {
		t.Fatalf("unknown type should predict skip: %+v", it)
	}
	// Preview must not write: the store still only knows "old".
	if len(tool.recipes) != 1 {
		t.Fatalf("preview wrote to the store: %v", tool.recipes)
	}
}

func TestPreview_WarnsUnmetRecipeDependency(t *testing.T) {
	tool := &fakeRecipeArtifact{
		fakeArtifact: fakeArtifact{typ: "tool"},
		recipeDeps: map[string][]ArtifactSel{
			"weather": {{Type: "credential", Name: "openweather"}}},
	}
	cred := &fakeArtifact{typ: "credential"}
	withFakeTypes(t, tool, cred)

	// Credential in neither the bundle nor the install → warning on the item
	// and on the aggregate, same message shape as the import result's.
	res, err := PreviewArtifactBundle(nil, importBundleBytes(t, ArtifactSel{Type: "tool", Name: "weather"}), "u")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(res.Warnings) != 1 || len(res.Items) != 1 || len(res.Items[0].Warnings) != 1 {
		t.Fatalf("expected exactly one warning on item + aggregate, got %+v", res)
	}
	if want := missingDepWarning(
		ArtifactSel{Type: "tool", Name: "weather", Owner: "u"},
		ArtifactSel{Type: "credential", Name: "openweather"}); res.Warnings[0] != want {
		t.Fatalf("warning must match the import pass's shape:\n got %q\nwant %q", res.Warnings[0], want)
	}
}

func TestPreview_NoWarnWhenDependencySatisfied(t *testing.T) {
	tool := &fakeRecipeArtifact{
		fakeArtifact: fakeArtifact{typ: "tool"},
		recipeDeps: map[string][]ArtifactSel{
			"weather": {{Type: "credential", Name: "openweather"}}},
	}

	// Case 1: the credential travels in the same bundle.
	cred := &fakeArtifact{typ: "credential"}
	withFakeTypes(t, tool, cred)
	res, err := PreviewArtifactBundle(nil, importBundleBytes(t,
		ArtifactSel{Type: "tool", Name: "weather"},
		ArtifactSel{Type: "credential", Name: "openweather"}), "u")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("an in-bundle dependency must not warn: %v", res.Warnings)
	}

	// Case 2: the credential is already on the install.
	credPresent := &fakeArtifact{typ: "credential", recipes: map[string]string{"openweather": "k"}}
	withFakeTypes(t, tool, credPresent)
	res, err = PreviewArtifactBundle(nil, importBundleBytes(t, ArtifactSel{Type: "tool", Name: "weather"}), "u")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("an already-present dependency must not warn: %v", res.Warnings)
	}
}

func TestPreview_EmptyAndInvalid(t *testing.T) {
	withFakeTypes(t, &fakeArtifact{typ: "tool"})
	if _, err := PreviewArtifactBundle(nil, []byte(`{"bundle":"gohort.bundle/v1","artifacts":[]}`), "u"); err == nil {
		t.Fatal("empty bundle must error, same as import")
	}
	if _, err := PreviewArtifactBundle(nil, []byte("not json"), "u"); err == nil {
		t.Fatal("unparsable bytes must error")
	}
}

func TestArtifactRecipeName(t *testing.T) {
	// Recipe's own name wins over the envelope label.
	a := PortableArtifact{Name: "label", Recipe: json.RawMessage(`{"name":"real"}`)}
	if got := artifactRecipeName(a); got != "real" {
		t.Fatalf("expected recipe name, got %q", got)
	}
	// Non-object recipe (the fakes use a JSON string) falls back to the label.
	b := PortableArtifact{Name: "label", Recipe: json.RawMessage(`"weather"`)}
	if got := artifactRecipeName(b); got != "label" {
		t.Fatalf("expected envelope label fallback, got %q", got)
	}
}

func TestPreview_InBundleIDReferenceDoesNotWarn(t *testing.T) {
	// Cross-artifact references can be traveled IDs, not names (a skill's
	// AttachedCollections, an agent's AttachedPipelines). When the referenced
	// artifact rides in the same bundle, preview must match it by its recipe's
	// traveled ID — matching only by name would false-warn on every such pair.
	collectionTestDB(t)
	skillRec, _ := json.Marshal(SkillRecord{
		Name: "law", Description: "Use for case law.",
		AttachedCollections: []string{"coll-42"},
	})
	collRec, _ := json.Marshal(PortableCollection{ID: "coll-42", Name: "Case Law"})
	data, _ := json.Marshal(ArtifactBundle{Bundle: ArtifactBundleFormat, Artifacts: []PortableArtifact{
		{Type: "skill", Name: "law", Recipe: skillRec},
		{Type: "collection", Name: "Case Law", Recipe: collRec},
	}})
	res, err := PreviewArtifactBundle(RootDB, data, "bob")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if res.WouldImport != 2 {
		t.Fatalf("both artifacts should predict import: %+v", res.Items)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("collection travels in-bundle (referenced by ID) — no warning expected: %v", res.Warnings)
	}
}
