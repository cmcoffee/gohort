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

// installTestApp is an app "wx" made by Builder and checked out to a folder,
// and the app host a person's Install runs on.
func installTestApp(t *testing.T, turn *chatTurn) (T *OrchestrateApp, dir string) {
	t.Helper()
	if _, err := turn.appDefCreateOrUpdate(map[string]any{
		"name":         "Wx",
		"sections":     []any{map[string]any{"id": "page", "kind": "html", "html": "<!DOCTYPE html><html><body><h1>Wx</h1></body></html>"}},
		"data_sources": []any{map[string]any{"name": "now", "script": "print('{}')"}},
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.appDefCheckout(map[string]any{"id": "wx"}); err != nil {
		t.Fatal(err)
	}
	ws, _ := turn.turnWorkspace()
	T = &OrchestrateApp{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	return T, filepath.Join(ws, "wx.app")
}

// A person's Install, with no chat, carries what the folder changed, its
// note into the app's history, and nothing else.
func TestAPersonInstallsAnAppFolder(t *testing.T) {
	turn, _ := folderTestTurn(t)
	T, dir := installTestApp(t, turn)
	os.WriteFile(filepath.Join(dir, "data", "now.py"), []byte("print('{\"t\": 2}')"), 0o644)
	out, err := T.InstallAppFolder(context.Background(), "u", dir, "warmer")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	spec, _ := LoadAppSpec("u", "wx")
	if spec.DataSources[0].Script != "print('{\"t\": 2}')" || spec.ChangeNote != "warmer" || !strings.Contains(out, "Published wx.app") {
		t.Fatalf("installed %+v (note %q): %s", spec.DataSources, spec.ChangeNote, out)
	}
	// The folder now holds the live app, so a second Install is not stale.
	if _, err := T.InstallAppFolder(context.Background(), "u", dir, ""); err != nil {
		t.Fatalf("a second install of a current folder: %v", err)
	}
}

// A folder written before the live app changed would undo that change: its
// Install is refused, and the live app keeps what it has.
func TestAStaleFolderIsNotInstalled(t *testing.T) {
	turn, _ := folderTestTurn(t)
	T, dir := installTestApp(t, turn)
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"id": "wx",
		"data_sources": []any{map[string]any{"name": "now", "script": "print('{\"live\": 1}')"}}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := T.InstallAppFolder(context.Background(), "u", dir, ""); err == nil || !strings.Contains(err.Error(), "live app was changed") {
		t.Fatalf("a stale folder: %v", err)
	}
	if spec, _ := LoadAppSpec("u", "wx"); spec.DataSources[0].Script != "print('{\"live\": 1}')" {
		t.Fatalf("the live change was undone: %+v", spec.DataSources)
	}
}

// A shared app changed by an owner who is not an administrator leaves
// sharing until one approves it, whichever way the change came in.
func TestAPersonsInstallTakesASharedAppOutOfSharing(t *testing.T) {
	turn, _ := folderTestTurn(t)
	T, dir := installTestApp(t, turn)
	spec, _ := LoadAppSpec("u", "wx")
	spec.Shared = true
	SaveAppSpecAs(spec, AppSaveNoHistory)
	os.WriteFile(filepath.Join(dir, "data", "now.py"), []byte("print('{\"t\": 3}')"), 0o644)
	if _, err := T.InstallAppFolder(context.Background(), "u", dir, ""); err != nil {
		t.Fatal(err)
	}
	if spec, _ := LoadAppSpec("u", "wx"); spec.Shared {
		t.Error("a changed shared app stayed shared without an administrator's approval")
	}
}

// A change that breaks one of the app's checks is not installed, and the app
// keeps serving what it was.
func TestAnInstallThatBreaksACheckIsRefused(t *testing.T) {
	turn := checksTestApp(t)
	if _, err := turn.appDefCheckout(map[string]any{"id": "hours"}); err != nil {
		t.Fatal(err)
	}
	ws, _ := turn.turnWorkspace()
	dir := filepath.Join(ws, "hours.app")
	os.WriteFile(filepath.Join(dir, "data", "total.py"), []byte(strings.Replace(totalScript, "sum(", "2 * sum(", 1)), 0o644)
	T := &OrchestrateApp{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	if _, err := T.InstallAppFolder(context.Background(), "u", dir, ""); err == nil || !strings.Contains(err.Error(), "breaks a check") {
		t.Fatalf("an install that broke a check: %v", err)
	}
	if storedScript(t, "u") != totalScript {
		t.Fatal("the refused install was saved")
	}
}

// A build for a preview is the app as the folder says, page compiled, and
// saves nothing.
func TestABuildForAPreviewSavesNothing(t *testing.T) {
	turn, _ := folderTestTurn(t)
	T, dir := installTestApp(t, turn)
	os.WriteFile(filepath.Join(dir, "data", "now.py"), []byte("print('{\"t\": 4}')"), 0o644)
	spec, err := T.BuildAppFolder(context.Background(), "u", dir)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Owner != "u" || spec.Slug != "wx" || spec.DataSources[0].Script != "print('{\"t\": 4}')" || len(spec.Page) == 0 {
		t.Fatalf("built %+v page %d bytes", spec.DataSources, len(spec.Page))
	}
	if live, _ := LoadAppSpec("u", "wx"); live.DataSources[0].Script != "print('{}')" {
		t.Fatalf("a build saved the app: %+v", live.DataSources)
	}
	if _, err := T.BuildAppFolder(context.Background(), "", dir); err == nil {
		t.Error("a build for no one")
	}
}

// An app written to a folder is the live app as it is: installing the folder
// unchanged is not stale and changes nothing it serves.
func TestAnAppWrittenToAFolderInstallsBackUnchanged(t *testing.T) {
	turn, _ := folderTestTurn(t)
	T, _ := installTestApp(t, turn)
	dir := filepath.Join(t.TempDir(), "wx")
	if err := WriteAppFolder("u", "wx", dir); err != nil {
		t.Fatal(err)
	}
	before, _ := LoadAppSpec("u", "wx")
	if _, err := T.InstallAppFolder(context.Background(), "u", dir, ""); err != nil {
		t.Fatalf("installing an unchanged folder: %v", err)
	}
	after, _ := LoadAppSpec("u", "wx")
	if appLiveFingerprint(before) != appLiveFingerprint(after) {
		t.Errorf("an unchanged folder changed the app")
	}
	if err := WriteAppFolder("u", "nope", filepath.Join(t.TempDir(), "x")); err == nil {
		t.Error("wrote a folder for an app that does not exist")
	}
}

// Notes longer than the cap, saved before it, do not stop an install that
// leaves them as they are; notes written over the cap still do.
func TestUnchangedLongNotesDoNotStopAnInstall(t *testing.T) {
	turn, _ := folderTestTurn(t)
	T, _ := installTestApp(t, turn)
	spec, _ := LoadAppSpec("u", "wx")
	spec.Notes = "## Decisions\n" + strings.Repeat("A decision and why. ", 200)
	SaveAppSpecAs(spec, AppSaveNoHistory)
	dir := filepath.Join(t.TempDir(), "wx")
	if err := WriteAppFolder("u", "wx", dir); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "data", "now.py"), []byte("print('{\"t\": 5}')"), 0o644)
	if _, err := T.InstallAppFolder(context.Background(), "u", dir, ""); err != nil {
		t.Fatalf("unchanged long notes stopped the install: %v", err)
	}
	if live, _ := LoadAppSpec("u", "wx"); live.DataSources[0].Script != "print('{\"t\": 5}')" || live.Notes != spec.Notes {
		t.Errorf("installed %+v, notes kept %v", live.DataSources, live.Notes == spec.Notes)
	}
	os.WriteFile(filepath.Join(dir, "NOTES.md"), []byte(spec.Notes+" One more decision, and why it was made."), 0o644)
	if _, err := T.InstallAppFolder(context.Background(), "u", dir, ""); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Errorf("notes written over the cap: %v", err)
	}
}
