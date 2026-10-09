package temptool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func folderSession(t *testing.T) *ToolSession {
	t.Helper()
	return &ToolSession{Username: "alice", ChatSessionID: "s1", WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()}}
}

func mustCreate(t *testing.T, sess *ToolSession, args map[string]any) {
	t.Helper()
	if _, err := createGrouped(args, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
}

// A tool keeps notes for its next editor, and an edit that does not mention
// them keeps them: update re-runs create, which would otherwise drop them.
func TestToolNotesSurviveAnUpdate(t *testing.T) {
	sess := folderSession(t)
	mustCreate(t, sess, map[string]any{"name": "wx", "description": "weather for a city", "mode": "shell",
		"script_body": "import os\nprint(os.environ.get('city',''))\n", "script_name": "run.py",
		"params": map[string]any{"city": map[string]any{"type": "string", "description": "a city"}},
		"notes":  "The API returns Kelvin: convert."})
	if got, _ := loadExistingToolRecord(sess, "wx"); got.Notes != "The API returns Kelvin: convert." {
		t.Fatalf("notes after create: %q", got.Notes)
	}
	if _, err := updateGrouped(map[string]any{"name": "wx", "description": "weather for any city"}, sess); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadExistingToolRecord(sess, "wx"); got.Notes != "The API returns Kelvin: convert." {
		t.Errorf("an update that did not touch the notes dropped them: %q", got.Notes)
	}
	if _, err := updateGrouped(map[string]any{"name": "wx", "notes": ""}, sess); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadExistingToolRecord(sess, "wx"); got.Notes != "" {
		t.Errorf("an update clearing the notes kept them: %q", got.Notes)
	}
}

// A script tool goes out as a folder and comes back from it: the script, its
// helpers and its notes, with a change made to one file being what the saved
// tool carries.
func TestAToolRoundTripsThroughItsFolder(t *testing.T) {
	sess := folderSession(t)
	mustCreate(t, sess, map[string]any{"name": "wx", "description": "weather for a city", "mode": "shell",
		"script_body": "import os\nprint(os.environ.get('city',''))\n", "script_name": "run.py",
		"params": map[string]any{"city": map[string]any{"type": "string", "description": "a city"}},
		"notes":  "Kelvin."})
	out, err := toolCheckout(map[string]any{"name": "wx"}, sess)
	if err != nil || !strings.Contains(out, "wx.tool") {
		t.Fatalf("checkout: %q %v", out, err)
	}
	dir := filepath.Join(sess.WorkspaceDir, "wx.tool")
	for _, f := range []string{"tool.json", "run.py", "NOTES.md", ".live"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("checkout did not write %s", f)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "NOTES.md")); string(b) != "Kelvin." {
		t.Fatalf("NOTES.md = %q", b)
	}
	os.WriteFile(filepath.Join(dir, "run.py"), []byte("from units import c\nimport os\nprint(c(os.environ.get('city','')))\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "units.py"), []byte("def c(x):\n    return x\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "NOTES.md"), []byte("Kelvin. Conversion lives in units.py."), 0o644)
	if _, err := toolPublish(map[string]any{"name": "wx"}, sess); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, _ := loadExistingToolRecord(sess, "wx")
	if !strings.Contains(got.ScriptBody, "from units import c") || got.Notes != "Kelvin. Conversion lives in units.py." {
		t.Fatalf("after publish: script %q notes %q", got.ScriptBody, got.Notes)
	}
	if len(got.WorkspaceFiles) != 1 || got.WorkspaceFiles[0].Path != "units.py" {
		t.Fatalf("helpers after publish: %+v", got.WorkspaceFiles)
	}
	if _, ok := got.Params["city"]; !ok {
		t.Errorf("params lost on publish: %+v", got.Params)
	}
}

// The folder and the live tool stay one: a live edit of a tool its folder
// holds as it is goes to the folder, and a publish over a live change the
// folder does not have is refused until it says it has it.
func TestTheToolFolderAndTheLiveToolStayOne(t *testing.T) {
	sess := folderSession(t)
	mustCreate(t, sess, map[string]any{"name": "wx", "description": "weather", "mode": "shell",
		"script_body": "print(1)\n", "script_name": "run.py", "command_template": "python3 {workspace_dir}/run.py"})
	if err := toolFolderOwnsEdit(sess, "wx"); err != nil {
		t.Fatalf("no folder, yet a live edit was refused: %v", err)
	}
	if _, err := toolCheckout(map[string]any{"name": "wx"}, sess); err != nil {
		t.Fatal(err)
	}
	if err := toolFolderOwnsEdit(sess, "wx"); err == nil || !strings.Contains(err.Error(), "wx.tool/") {
		t.Fatalf("a live edit of a tool its folder holds went through: %v", err)
	}
	// Changed live anyway (another session, another author).
	if _, err := toolDefUpdate(map[string]any{"name": "wx", "description": "weather, edited live"}, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := toolPublish(map[string]any{"name": "wx"}, sess); err == nil || !strings.Contains(err.Error(), "NOT PUBLISHED") {
		t.Fatalf("a publish over a live change went through: %v", err)
	}
	if _, err := toolPublish(map[string]any{"name": "wx", "overwrite_live": true}, sess); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadExistingToolRecord(sess, "wx"); got.Description != "weather" {
		t.Errorf("overwrite_live did not publish the folder: %q", got.Description)
	}
}

// A new name starts a folder with a starter script, and publishing it creates
// the tool. A tool that is not a script tool is not checked out.
func TestANewToolStartsAsAFolder(t *testing.T) {
	sess := folderSession(t)
	if _, err := toolCheckout(map[string]any{"name": "word_count"}, sess); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(sess.WorkspaceDir, "word_count.tool")
	var m toolFolderManifest
	raw, _ := os.ReadFile(filepath.Join(dir, "tool.json"))
	json.Unmarshal(raw, &m)
	m.Description = "count the words in text"
	m.Params = json.RawMessage(`{"text": {"type": "string", "description": "the text"}}`)
	b, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(filepath.Join(dir, "tool.json"), b, 0o644)
	if _, err := toolPublish(map[string]any{"name": "word_count"}, sess); err != nil {
		t.Fatalf("publish a new folder: %v", err)
	}
	got, ok := loadExistingToolRecord(sess, "word_count")
	if !ok || !strings.Contains(got.ScriptBody, "ENVIRONMENT VARIABLE") || !strings.Contains(got.Notes, "whoever edits") {
		t.Fatalf("created: %v %+v", ok, got)
	}

	if err := sess.AppendTempTool(&TempTool{Name: "ping", Description: "ping an api", Mode: TempToolModeAPI,
		Credential: "no_auth", CommandTemplate: "https://example.com/ping"}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolCheckout(map[string]any{"name": "ping"}, sess); err == nil || !strings.Contains(err.Error(), "not a script tool") {
		t.Errorf("an api tool was checked out: %v", err)
	}
}
