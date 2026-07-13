package core

import (
	"encoding/json"
	"strings"
	"testing"
)

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
