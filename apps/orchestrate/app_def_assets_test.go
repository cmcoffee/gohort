package orchestrate

import (
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
