package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An app goes out as one file and comes back as a folder: its page, scripts,
// shared modules, notes and assets, with nothing installed by the unpack, and
// a publish of the unpacked folder over the live app refused.
func TestAnAppPacksToAFileAndUnpacksToAFolder(t *testing.T) {
	turn, ws := folderTestTurn(t)
	page := "<!DOCTYPE html><html><body><img src=\"assets/ship.png\"><script>app.data('now').then(function(d){});</script></body></html>"
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Voidrunner", "notes": "The DM is run_agent.",
		"libraries":    map[string]any{"engine": "def price(n):\n    return n\n"},
		"sections":     []any{map[string]any{"id": "page", "kind": "html", "html": page}},
		"data_sources": []any{map[string]any{"name": "now", "script": "from engine import price\nprint('{}')"}}}, false); err != nil {
		t.Fatal(err)
	}
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("x", 16)
	if _, err := SaveAppAsset("u", "voidrunner", "ship.png", []byte(png)); err != nil {
		t.Fatal(err)
	}
	out, err := turn.appDefPack(map[string]any{"id": "voidrunner"})
	if err != nil || !strings.Contains(out, "voidrunner.gohortapp") {
		t.Fatalf("pack: %q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(ws, "voidrunner.gohortapp")); err != nil {
		t.Fatal("no bundle file written")
	}
	out, err = turn.appDefUnpack(map[string]any{"file": "voidrunner.gohortapp", "dir": "copy.app"})
	if err != nil || !strings.Contains(out, "Nothing was installed") {
		t.Fatalf("unpack: %q %v", out, err)
	}
	dir := filepath.Join(ws, "copy.app")
	for f, want := range map[string]string{
		"page.html":       "assets/ship.png",
		"data/now.py":     "from engine import price",
		"lib/engine.py":   "def price",
		"NOTES.md":        "run_agent",
		"assets/ship.png": png,
	} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil || !strings.Contains(string(b), want) {
			t.Errorf("%s: %v %q", f, err, b)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, appFolderBaseFile)); !os.IsNotExist(err) {
		t.Error("an unpacked folder claims to hold the live app")
	}
	if _, err := turn.appDefPublish(map[string]any{"dir": "copy.app"}); err == nil || !strings.Contains(err.Error(), "no record") {
		t.Errorf("publishing an unpacked folder over the live app went through: %v", err)
	}
	os.WriteFile(filepath.Join(ws, "notes.gohortapp"), []byte(`{"bundle": "gohort.bundle/v1", "artifacts": [{"type": "skill", "name": "x", "recipe": "{}"}]}`), 0o644)
	if _, err := turn.appDefUnpack(map[string]any{"file": "notes.gohortapp"}); err == nil || !strings.Contains(err.Error(), "holds no app") {
		t.Errorf("a bundle with no app: %v", err)
	}
}

// A publish asks for the notes it should have left: a starter NOTES.md, or an
// app whose code changed while its notes did not. It never refuses over them.
func TestAPublishAsksForItsNotes(t *testing.T) {
	turn, ws := folderTestTurn(t)
	if _, err := turn.appDefCheckout(map[string]any{"name": "Dice"}); err != nil {
		t.Fatal(err)
	}
	out, err := turn.appDefPublish(map[string]any{"dir": "dice.app"})
	if err != nil || !strings.Contains(out, "NOTES.md is still the starter") {
		t.Fatalf("a starter's publish did not ask for notes: %v\n%s", err, out)
	}
	os.WriteFile(filepath.Join(ws, "dice.app", "NOTES.md"), []byte("Rolls dice; history kept as records."), 0o644)
	if out, _ := turn.appDefPublish(map[string]any{"dir": "dice.app"}); strings.Contains(out, "NOTES:") {
		t.Errorf("written notes still asked for: %s", out)
	}
	page, _ := os.ReadFile(filepath.Join(ws, "dice.app", "page.html"))
	os.WriteFile(filepath.Join(ws, "dice.app", "page.html"), []byte(strings.Replace(string(page), "</h1>", " (d20)</h1>", 1)), 0o644)
	if out, _ := turn.appDefPublish(map[string]any{"dir": "dice.app"}); !strings.Contains(out, "changed and dice.app/NOTES.md did not") {
		t.Errorf("a change with unchanged notes was not flagged: %s", out)
	}
}
