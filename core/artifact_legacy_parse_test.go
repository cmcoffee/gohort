package core

// Old files and form uploads reach the bundle importer too: a type's bare
// recipe (from a per-app Export button) is recognized by the type itself, a
// form's {"recipe": "<file text>"} wrapper is unwrapped, and an ordinary
// user's import takes only the types they may import.

import (
	"encoding/json"
	"testing"
)

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
