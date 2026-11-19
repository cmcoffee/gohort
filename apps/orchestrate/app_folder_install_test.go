package orchestrate

import (
	"context"
	"encoding/json"
	"github.com/cmcoffee/gohort/core/agenthost"
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
	makeLive(t, turn, "wx")
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
	makeLive(t, turn, "wx")
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

// An install applies what the app owns as the folder says: an agent's file
// changed in the folder is the agent changed (its people's choices kept), a
// new file is a new agent of the app, a file that does not parse stops the
// install before anything is saved, and an agent whose file is gone is kept
// and said: an item is removed with Builder, never by leaving out its file.
func TestAnInstallAppliesWhatTheAppOwns(t *testing.T) {
	turn, _ := folderTestTurn(t)
	T, dir := installTestApp(t, turn)
	prevHost := agenthost.Get()
	agenthost.Register(T)
	t.Cleanup(func() { agenthost.Register(prevHost) })
	udb := UserDB(T.DB, "u")
	helper, err := saveAgent(udb, AgentRecord{Owner: "u", Name: "Helper", OrchestratorPrompt: "Help.", OwningApp: "wx",
		AutoApproveTools: []string{"web_search"}, appEdit: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteAppFolder("u", "wx", dir); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "agents", "helper.json")
	raw, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(raw), "Help.") {
		t.Fatalf("the folder does not hold the app's agent: %v %s", err, raw)
	}
	var rec map[string]any
	json.Unmarshal(raw, &rec)
	rec["orchestrator_prompt"] = "Help kindly."
	edited, _ := json.MarshalIndent(rec, "", "  ")
	os.WriteFile(file, edited, 0o644)

	broken := filepath.Join(dir, "agents", "broken.json")
	os.WriteFile(broken, []byte("{"), 0o644)
	if _, err := T.InstallAppFolder(context.Background(), "u", dir, "Kinder"); err == nil || !strings.Contains(err.Error(), "does not read") {
		t.Fatalf("a file that does not parse: %v", err)
	}
	if got, _ := loadAgent(udb, helper.ID); got.OrchestratorPrompt != "Help." {
		t.Error("an install stopped by a bad file still changed the agent")
	}
	os.Remove(broken)

	os.WriteFile(filepath.Join(dir, "agents", "scout.json"), []byte(`{"name": "Scout", "orchestrator_prompt": "Scout ahead."}`), 0o644)
	out, err := T.InstallAppFolder(context.Background(), "u", dir, "Kinder, and a scout")
	if err != nil || !strings.Contains(out, "Also saved: agent Helper, agent Scout") {
		t.Fatalf("install: %v %s", err, out)
	}
	got, _ := loadAgent(udb, helper.ID)
	if got.OrchestratorPrompt != "Help kindly." || len(got.AutoApproveTools) != 1 {
		t.Errorf("the agent after: prompt %q, its people's choices %v", got.OrchestratorPrompt, got.AutoApproveTools)
	}
	scout, ok := findAgentByNameOrID(udb, "u", "Scout")
	if !ok || scout.OwningApp != "wx" {
		t.Errorf("the new file made no agent of the app: %+v", scout)
	}

	os.Remove(file)
	out, err = T.InstallAppFolder(context.Background(), "u", dir, "Drop the helper file")
	if err != nil || !strings.Contains(out, `Not in the folder, and kept: agent "Helper"`) {
		t.Fatalf("an install with the agent's file gone: %v %s", err, out)
	}
	if _, ok := loadAgent(udb, helper.ID); !ok {
		t.Error("an agent was deleted because its file was left out")
	}
}
