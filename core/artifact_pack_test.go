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
	// The summary is now the "what is left to do" list: it has to name the
	// missing credential AND what needed it.
	if sum := res.Summary(); !strings.Contains(sum, `credential "openweather"`) || !strings.Contains(sum, `needed by tool "weather"`) {
		t.Fatalf("summary should surface the missing reference and what needs it, got %q", sum)
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

// Old files and form uploads reach the bundle importer too: a type's bare
// recipe (from a per-app Export button) is recognized by the type itself, a
// form's {"recipe": "<file text>"} wrapper is unwrapped, and an ordinary
// user's import takes only the types they may import.

// sniffingFake is a fake type whose bare recipe is any object with "stages".
type sniffingFake struct{ fakeArtifact }

func (*sniffingFake) SniffsRecipe(fields map[string]json.RawMessage) bool {
	_, ok := fields["stages"]
	return ok
}
func (*sniffingFake) UserImportable() bool { return true }

func TestABareRecipeIsLiftedByTheTypeThatClaimsIt(t *testing.T) {
	withFakeTypes(t, &sniffingFake{fakeArtifact{typ: "pipeline"}}, &fakeArtifact{typ: "connector"})

	b, err := ParseArtifactBundle([]byte(`{"name": "Nightly", "stages": []}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(b.Artifacts) != 1 || b.Artifacts[0].Type != "pipeline" || b.Artifacts[0].Name != "Nightly" {
		t.Fatalf("a bare pipeline recipe should lift to one pipeline artifact, got %+v", b.Artifacts)
	}

	// Nothing claims it: the legacy connector reading still applies.
	b, err = ParseArtifactBundle([]byte(`{"name": "hook", "kind": "rest_poll"}`))
	if err != nil || len(b.Artifacts) != 1 || b.Artifacts[0].Type != "connector" {
		t.Fatalf("an unclaimed object should still read as a legacy connector: %+v %v", b.Artifacts, err)
	}
}

func TestAFormUploadWrapperIsUnwrapped(t *testing.T) {
	withFakeTypes(t, &sniffingFake{fakeArtifact{typ: "pipeline"}})
	inner := `{"name": "Nightly", "stages": []}`
	for _, key := range []string{"recipe", "pack"} {
		wrapped, _ := json.Marshal(map[string]string{key: inner})
		b, err := ParseArtifactBundle(wrapped)
		if err != nil || len(b.Artifacts) != 1 || b.Artifacts[0].Type != "pipeline" {
			t.Errorf("%s wrapper: %+v %v", key, b.Artifacts, err)
		}
		if got := string(UnwrapArtifactUpload(wrapped)); got != inner {
			t.Errorf("%s unwrap = %q", key, got)
		}
	}
	// A bare artifact's {"type", "recipe": {...}} is not a wrapper.
	bare := []byte(`{"type": "pipeline", "recipe": {"name": "X", "stages": []}}`)
	if string(UnwrapArtifactUpload(bare)) != string(bare) {
		t.Error("a bare artifact must not be unwrapped")
	}
}

func TestIsArtifactEnvelope(t *testing.T) {
	yes := []string{
		`{"bundle": "` + ArtifactBundleFormat + `", "artifacts": []}`,
		`{"artifacts": [{"type": "x", "recipe": {}}]}`,
		`{"type": "pipeline", "recipe": {"name": "X"}}`,
		`{"recipe": "{\"bundle\": \"` + ArtifactBundleFormat + `\", \"artifacts\": []}"}`,
	}
	no := []string{
		`{"name": "X", "stages": []}`,
		`{"recipe": "{\"name\": \"X\", \"stages\": []}"}`,
		`[]`, ``, `not json`,
	}
	for _, s := range yes {
		if !IsArtifactEnvelope([]byte(s)) {
			t.Errorf("should be an envelope: %s", s)
		}
	}
	for _, s := range no {
		if IsArtifactEnvelope([]byte(s)) {
			t.Errorf("should not be an envelope: %s", s)
		}
	}
}

// An ordinary user's bundle import takes their own kinds of artifact and
// reports the rest as needing an administrator, both in the import and in its
// preview.
func TestAUserImportTakesOnlyUserImportableTypes(t *testing.T) {
	userType := &sniffingFake{fakeArtifact{typ: "pipeline"}}
	adminType := &fakeArtifact{typ: "credential"}
	withFakeTypes(t, userType, adminType)
	bundle := []byte(`{"bundle": "` + ArtifactBundleFormat + `", "artifacts": [
		{"type": "pipeline", "name": "p", "recipe": "p"},
		{"type": "credential", "name": "c", "recipe": "c"}]}`)

	pv, err := PreviewArtifactBundleAsUser(nil, bundle, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if pv.WouldImport != 1 || pv.WouldSkip != 1 || pv.Items[1].Detail != adminOnlyArtifactDetail {
		t.Fatalf("preview: %+v", pv)
	}
	res, err := ImportArtifactBundleAsUser(nil, bundle, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 1 || res.Skipped != 1 {
		t.Fatalf("import: %+v", res)
	}
	if _, ok := adminType.recipes["c"]; ok {
		t.Error("an admin-only type was imported by a user")
	}
	if _, ok := userType.recipes["p"]; !ok {
		t.Error("the user's own type did not import")
	}
	// The admin importer takes both.
	adminType.recipes = nil
	userType.recipes = nil
	if res, _ := ImportArtifactBundle(nil, bundle, "root"); res.Imported != 2 {
		t.Errorf("admin import: %+v", res)
	}
}

// A user's export is their own artifacts and nothing deployment-wide: an admin
// kind is refused outright, every selector resolves in the user's namespace
// whatever owner it named, and the dependency closure leaves out
// deployment-wide dependencies.
func TestAUserExportStaysInsideTheUsersOwnNamespace(t *testing.T) {
	agentLike := &ownerFake{sniffingFake: sniffingFake{fakeArtifact{typ: "pipeline"}}}
	agentLike.byOwner = map[string]map[string]bool{"alice": {"mine": true, "helper": true}, "bob": {"theirs": true}}
	agentLike.deps = map[string][]ArtifactSel{
		"mine": {{Type: "credential", Name: "crm"}, {Type: "pipeline", Name: "helper", Owner: "bob"}},
	}
	cred := &fakeArtifact{typ: "credential", recipes: map[string]string{"crm": "crm"}}
	withFakeTypes(t, agentLike, cred)

	if _, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "credential", Name: "crm"}}, UserExportOptions{IncludeDeps: true}); err == nil {
		t.Error("a user exported an administrator's kind of artifact")
	}
	// Named with bob as owner: still resolves as alice's, and alice has no
	// "theirs", so the export fails rather than reading bob's store.
	if _, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "pipeline", Name: "theirs", Owner: "bob"}}, UserExportOptions{IncludeDeps: true}); err == nil {
		t.Error("a user exported another user's artifact by naming its owner")
	}
	b, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "pipeline", Name: "mine"}}, UserExportOptions{IncludeDeps: true})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range b.Artifacts {
		got = append(got, a.Type+":"+a.Name)
	}
	// The pipeline dependency was addressed to bob; it resolves in ALICE'S
	// namespace, where she has her own "helper". The credential stays out.
	if len(got) != 2 || got[0] != "pipeline:mine" || got[1] != "pipeline:helper" {
		t.Fatalf("got %v", got)
	}
	for _, o := range agentLike.resolvedFor {
		if o != "alice" {
			t.Fatalf("an export read %q's store", o)
		}
	}
}

// ownerFake resolves names per owner and records whose store each export read.
type ownerFake struct {
	sniffingFake
	byOwner     map[string]map[string]bool
	resolvedFor []string
}

func (o *ownerFake) ExportArtifact(_ Database, name, owner string) (json.RawMessage, error) {
	o.resolvedFor = append(o.resolvedFor, owner)
	if !o.byOwner[owner][name] {
		return nil, Error("no such pipeline")
	}
	return json.Marshal(name)
}

func (o *ownerFake) Dependencies(_ Database, name, _ string) []ArtifactSel { return o.deps[name] }

// The export dialog offers what an item depends on, by kind, and the ticks it
// sends back narrow the closure to those kinds.
func TestTheExportPlanAndTheKindsTheDialogTicks(t *testing.T) {
	pipes := &ownerFake{sniffingFake: sniffingFake{fakeArtifact{typ: "pipeline"}}}
	pipes.byOwner = map[string]map[string]bool{"alice": {"main": true}}
	pipes.deps = map[string][]ArtifactSel{"main": {{Type: "skill", Name: "triage"}, {Type: "machine", Name: "intake"}}}
	skills := &ownerFake{sniffingFake: sniffingFake{fakeArtifact{typ: "skill"}}}
	skills.byOwner = map[string]map[string]bool{"alice": {"triage": true}}
	machines := &ownerFake{sniffingFake: sniffingFake{fakeArtifact{typ: "machine"}}}
	machines.byOwner = map[string]map[string]bool{"alice": {"intake": true}}
	withFakeTypes(t, pipes, skills, machines)

	plan, err := ArtifactExportPlanAsUser(nil, "alice", []ArtifactSel{{Type: "pipeline", Name: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 {
		t.Fatalf("the plan should list both dependencies and not the item itself: %+v", plan)
	}

	b, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "pipeline", Name: "main"}},
		UserExportOptions{IncludeDeps: true, DepTypes: []string{"skill"}})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, a := range b.Artifacts {
		kinds = append(kinds, a.Type)
	}
	if len(kinds) != 2 || kinds[0] != "pipeline" || kinds[1] != "skill" {
		t.Fatalf("only the ticked kind should travel: %v", kinds)
	}
}

// Opt-in data kinds ride a closure only when named; attachments import after
// everything they could attach to; an item selected by id is not exported a
// second time when a dependency points back at it by name.
type dataFake struct{ ownerFake }

// canonFake names its recipes canonically, so an id and a name can reach the
// same recipe.
type canonFake struct {
	ownerFake
	canon map[string]string
}

func (c *canonFake) ExportArtifact(db Database, name, owner string) (json.RawMessage, error) {
	if _, err := c.ownerFake.ExportArtifact(db, name, owner); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{"name": c.canon[name]})
}

func (*dataFake) OptInDependency() bool { return true }
func (*dataFake) ImportsLate() bool     { return true }

func TestOptInDataLateImportAndBackReferences(t *testing.T) {
	agents := &canonFake{ownerFake: ownerFake{sniffingFake: sniffingFake{fakeArtifact{typ: "agent"}}},
		canon: map[string]string{"id-1": "Helper", "Helper": "Helper"}}
	agents.byOwner = map[string]map[string]bool{"alice": {"id-1": true, "Helper": true}}
	agents.deps = map[string][]ArtifactSel{"id-1": {{Type: "memory", Name: "Helper"}, {Type: "agent", Name: "Helper"}}}
	mem := &dataFake{ownerFake{sniffingFake: sniffingFake{fakeArtifact{typ: "memory"}}}}
	mem.byOwner = map[string]map[string]bool{"alice": {"Helper": true}}
	withFakeTypes(t, agents, mem)

	// Not named: the memory stays home, even though every other kind travels.
	b, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "agent", Name: "id-1"}}, UserExportOptions{IncludeDeps: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range b.Artifacts {
		if a.Type == "memory" {
			t.Fatal("opt-in data travelled without being asked for")
		}
	}
	// Selected by id, pointed back at by name: one agent in the bundle.
	b, err = ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "agent", Name: "id-1"}},
		UserExportOptions{IncludeDeps: true, DepTypes: []string{"memory", "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	agentsOut := 0
	for _, a := range b.Artifacts {
		if a.Type == "agent" {
			agentsOut++
		}
	}
	if agentsOut != 1 {
		t.Errorf("the agent travelled %d times", agentsOut)
	}

	// Import order: the memory is listed first, lands last.
	bundle := []byte(`{"bundle": "` + ArtifactBundleFormat + `", "artifacts": [
		{"type": "memory", "name": "m", "recipe": "m"},
		{"type": "agent", "name": "a", "recipe": "a"}]}`)
	res, err := ImportArtifactBundle(nil, bundle, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Outcomes) != 2 || res.Outcomes[0].Type != "agent" || res.Outcomes[1].Type != "memory" {
		t.Errorf("attachments should import after what they attach to: %+v", res.Outcomes)
	}
}

// A recipe with a key baked into it is refused at export, whether it was
// selected or pulled in as a dependency; content kinds are not scanned.
type secretFake struct {
	fakeArtifact
	recipe  map[string]string
	content bool
}

func (f *secretFake) ExportArtifact(_ Database, name, _ string) (json.RawMessage, error) {
	r, ok := f.recipe[name]
	if !ok {
		return nil, Error("no such")
	}
	return json.Marshal(map[string]string{"name": name, "body": r})
}
func (f *secretFake) UserImportable() bool { return true }
func (f *secretFake) ContentKind() bool    { return f.content }

func TestExportRefusesAHardcodedSecretButNotInContent(t *testing.T) {
	tools := &secretFake{fakeArtifact: fakeArtifact{typ: "tool"}, recipe: map[string]string{
		"clean": "curl {url}", "leaky": "curl -H 'Authorization: Bearer sk_live_abcdefghijklmnop' {url}",
	}}
	tools.deps = map[string][]ArtifactSel{"clean": {{Type: "tool", Name: "leaky"}}}
	docs := &secretFake{fakeArtifact: fakeArtifact{typ: "guide"}, content: true, recipe: map[string]string{
		"howto": "Set api_key=abcdefghijklmnop in your shell",
	}}
	withFakeTypes(t, &dependingSecretFake{tools}, docs)

	_, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "tool", Name: "leaky"}}, UserExportOptions{})
	if err == nil || !strings.Contains(err.Error(), "hardcoded secret (bearer)") || strings.Contains(err.Error(), "sk_live") {
		t.Fatalf("a baked-in key should be refused without repeating it: %v", err)
	}
	_, err = ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "tool", Name: "clean"}}, UserExportOptions{IncludeDeps: true})
	if err == nil || !strings.Contains(err.Error(), "depends on") {
		t.Fatalf("a dependency with a baked-in key should be refused and named: %v", err)
	}
	// Prose that merely names a key is not one.
	tools.recipe["prose"] = "Pass the token: provided by the caller, and the password: whatever they chose."
	if _, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "tool", Name: "prose"}}, UserExportOptions{}); err != nil {
		t.Fatalf("prose naming a key was refused: %v", err)
	}
	tools.recipe["kv"] = "X-Api-Key: api_key=a1b2c3d4e5f6g7h8"
	if _, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "tool", Name: "kv"}}, UserExportOptions{}); err == nil {
		t.Fatal("a key=value with a key-shaped value should be refused")
	}
	if _, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "guide", Name: "howto"}}, UserExportOptions{}); err != nil {
		t.Fatalf("a document quoting an example token is content, not a leak: %v", err)
	}
}

type dependingSecretFake struct{ *secretFake }

func (d *dependingSecretFake) Dependencies(_ Database, name, _ string) []ArtifactSel {
	return d.deps[name]
}

// An import says what is left to do: each inert thing's next step, and each
// reference the install does not have.
type followUpFake struct{ fakeArtifact }

func (*followUpFake) ImportFollowUp() string { return "Switched off. Turn it on." }

func TestAnImportReportsWhatIsLeftToDo(t *testing.T) {
	skills := &followUpFake{fakeArtifact{typ: "skill", deps: map[string][]ArtifactSel{"triage": {{Type: "tool", Name: "absent"}}}}}
	withFakeTypes(t, skills, &fakeArtifact{typ: "tool"})
	bundle := []byte(`{"bundle": "` + ArtifactBundleFormat + `", "artifacts": [{"type": "skill", "name": "triage", "recipe": "triage"}]}`)
	res, err := ImportArtifactBundle(nil, bundle, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Checklist) != 2 {
		t.Fatalf("want the skill's next step and the missing tool: %+v", res.Checklist)
	}
	if res.Checklist[0].Action != "Switched off. Turn it on." || res.Checklist[0].Missing {
		t.Errorf("follow-up: %+v", res.Checklist[0])
	}
	if !res.Checklist[1].Missing || res.Checklist[1].Name != "absent" {
		t.Errorf("missing reference: %+v", res.Checklist[1])
	}
	if !strings.Contains(res.Summary(), "What is left to do") {
		t.Errorf("summary: %s", res.Summary())
	}
}
