package appassets

import (
	"strings"
	"testing"
)

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

// A file saved under an extension it does not match fails in the browser, a
// long way from the write; it is refused at the write, saying what it is.
func TestAssetBytesMustMatchTheExtension(t *testing.T) {
	good := map[string]string{
		"a.png":   "\x89PNG\r\n\x1a\n....",
		"a.gif":   "GIF89a..",
		"a.wav":   "RIFF\x00\x00\x00\x00WAVEfmt ",
		"a.webp":  "RIFF\x00\x00\x00\x00WEBPVP8 ",
		"a.mp3":   "ID3\x03....",
		"a.ogg":   "OggS....",
		"a.glb":   "glTF\x02\x00\x00\x00",
		"a.gltf":  ` {"asset":{"version":"2.0"}}`,
		"a.svg":   `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`,
		"a.bin":   "\x00\x01anything",
		"a.m4a":   "\x00\x00\x00\x20ftypM4A ",
		"a.woff2": "wOF2....",
	}
	for name, data := range good {
		if err := assetMatchesType(name, []byte(data)); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
	// The incident: base64 of a GIF, written as text, named .png.
	err := assetMatchesType("wood-grain.png", []byte("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"))
	if err == nil || !strings.Contains(err.Error(), "not a png file") {
		t.Fatalf("base64 text saved as a png: %v", err)
	}
	for _, name := range []string{"a.wav", "a.jpg", "a.glb", "a.svg"} {
		if assetMatchesType(name, []byte("hello")) == nil {
			t.Errorf("%s accepted plain text", name)
		}
	}
}
