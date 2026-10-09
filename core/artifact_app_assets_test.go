package core

import (
	"strings"
	"testing"
)

// An app's assets travel with it: exported into the recipe, and saved again
// on import, so a game arrives with its sprites.
func TestAnAppBundleCarriesItsAssets(t *testing.T) {
	customAppTestDB(t, nil)
	prev := AppAssetsDir()
	SetAppAssetsDir(t.TempDir())
	t.Cleanup(func() { SetAppAssetsDir(prev) })
	spec := seedAppSpec(t)
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("x", 32)
	if _, err := SaveAppAsset(spec.Owner, spec.Slug, "ship.png", []byte(png)); err != nil {
		t.Fatal(err)
	}
	recipe, err := customAppArtifact{}.ExportArtifact(nil, spec.Slug, spec.Owner)
	if err != nil {
		t.Fatal(err)
	}
	if got := appRecipeAssets(recipe); string(got["ship.png"]) != png {
		t.Fatalf("the recipe does not carry the asset: %v", got)
	}
	if _, reason, err := (customAppArtifact{}).ImportArtifact(nil, recipe, "bob"); err != nil || reason != "" {
		t.Fatalf("import: %q %v", reason, err)
	}
	if data, _, err := ReadAppAsset("bob", spec.Slug, "ship.png"); err != nil || string(data) != png {
		t.Errorf("the asset did not land with the imported app: %v", err)
	}
}
