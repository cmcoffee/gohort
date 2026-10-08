package appassets

import "testing"

// A WebGL app ships its models as assets: a .glb alone, or a .gltf with the
// .bin it names. Code still never qualifies.
func TestModelFilesAreAssets(t *testing.T) {
	for name, want := range map[string]string{
		"ship.glb":   "model/gltf-binary",
		"scene.gltf": "model/gltf+json",
		"scene.bin":  "application/octet-stream",
	} {
		got, ok := AppAssetContentType(name)
		if !ok || got != want || !ValidAppAssetName(name) {
			t.Errorf("%s: %q %v, want %q", name, got, ok, want)
		}
	}
	for _, name := range []string{"game.js", "page.html", "x.mjs"} {
		if ValidAppAssetName(name) {
			t.Errorf("%s must not be an asset", name)
		}
	}
}
