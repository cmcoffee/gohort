package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A file in the workspace becomes one of the app's assets, listed by get and
// removable; a file outside the workspace, a missing one or a disallowed type
// is refused.
func TestAWorkspaceFileBecomesAnAppAsset(t *testing.T) {
	prevRoot, prevAssets, prevWS := RootDB, AppAssetsDir(), WorkspacesDir()
	RootDB = &DBase{Store: kvlite.MemStore()}
	SetAppAssetsDir(t.TempDir())
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { RootDB = prevRoot; SetAppAssetsDir(prevAssets); SetWorkspacesDir(prevWS) })

	SaveAppSpec(AppSpec{Owner: "u", Slug: "game", Name: "Game"})
	turn := &chatTurn{user: "u", agent: AgentRecord{ID: "builder"}}
	dir, _, _ := turn.turnWorkspace()
	if err := os.MkdirAll(filepath.Join(dir, "art"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "art", "hero.png"), []byte("\x89PNG\r\n\x1a\nIHDR"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o644)

	out, err := turn.appDefAddAsset(map[string]any{"id": "game", "path": "art/hero.png"})
	if err != nil || !strings.Contains(out, "assets/hero.png") {
		t.Fatalf("add: %q %v", out, err)
	}
	if data, _, err := ReadAppAsset("u", "game", "hero.png"); err != nil || string(data) != "\x89PNG\r\n\x1a\nIHDR" {
		t.Fatalf("the asset reads %q %v", data, err)
	}
	if _, err := turn.appDefAddAsset(map[string]any{"id": "game", "path": "art/hero.png", "asset": "boss.png"}); err != nil {
		t.Fatalf("add under another name: %v", err)
	}
	if names := appAssetNames("u", "game"); strings.Join(names, ",") != "boss.png,hero.png" {
		t.Errorf("assets = %v", names)
	}
	for _, bad := range []map[string]any{
		{"id": "game", "path": "notes.txt"},
		{"id": "game", "path": "missing.png"},
		{"id": "game", "path": "../../etc/passwd", "asset": "x.png"},
		{"id": "nope", "path": "art/hero.png"},
	} {
		if _, err := turn.appDefAddAsset(bad); err == nil {
			t.Errorf("%v was accepted", bad)
		}
	}
	if _, err := turn.appDefRemoveAsset(map[string]any{"id": "game", "asset": "boss.png"}); err != nil {
		t.Fatal(err)
	}
	if names := appAssetNames("u", "game"); strings.Join(names, ",") != "hero.png" {
		t.Errorf("after remove: %v", names)
	}
}

// With an image generator configured, add_asset prompt= draws the picture
// straight into the app; without one it says to make or find it instead. The
// name follows what came back, and renders stop at the turn's ceiling.
func TestAnAssetCanBeGenerated(t *testing.T) {
	prevRoot, prevAssets := RootDB, AppAssetsDir()
	RootDB = &DBase{Store: kvlite.MemStore()}
	SetAppAssetsDir(t.TempDir())
	prevAvail, prevGen := assetImageAvailable, assetImageGenerate
	t.Cleanup(func() {
		RootDB = prevRoot
		SetAppAssetsDir(prevAssets)
		assetImageAvailable, assetImageGenerate = prevAvail, prevGen
	})
	SaveAppSpec(AppSpec{Owner: "u", Slug: "wx", Name: "Weather"})
	turn := &chatTurn{user: "u", agent: AgentRecord{ID: "builder"}}

	assetImageAvailable = func() bool { return false }
	if _, err := turn.appDefAddAsset(map[string]any{"id": "wx", "prompt": "rain clouds"}); err == nil || !strings.Contains(err.Error(), "no image generator") {
		t.Fatalf("no generator: %v", err)
	}

	assetImageAvailable = func() bool { return true }
	var asked []string
	assetImageGenerate = func(_ context.Context, backend, prompt string, _ bool) (*ImageGenResult, error) {
		asked = append(asked, prompt)
		f := filepath.Join(t.TempDir(), "out.bin")
		os.WriteFile(f, []byte("\xff\xd8\xff\xe0JFIF....."), 0o644)
		return &ImageGenResult{URL: f}, nil
	}
	out, err := turn.appDefAddAsset(map[string]any{"id": "wx", "prompt": "rain clouds over hills", "asset": "rain.png"})
	if err != nil || !strings.Contains(out, "assets/rain.jpg") || !strings.Contains(out, "not rain.png") {
		t.Fatalf("generate: %q %v", out, err)
	}
	if data, _, err := ReadAppAsset("u", "wx", "rain.jpg"); err != nil || !strings.HasPrefix(string(data), "\xff\xd8\xff") {
		t.Fatalf("the generated asset reads %q %v", data, err)
	}
	if len(asked) != 1 || asked[0] != "rain clouds over hills" {
		t.Fatalf("prompts = %q", asked)
	}
	turn.assetRenders = ImageGenHardCap()
	if _, err := turn.appDefAddAsset(map[string]any{"id": "wx", "prompt": "snow"}); err == nil || !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("past the ceiling: %v", err)
	}
}
