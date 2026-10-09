package temptool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A script tool goes out as one file and comes back as a folder: its script,
// helpers and notes, with nothing installed by the unpack.
func TestAToolPacksToAFileAndUnpacksToAFolder(t *testing.T) {
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
	sess := &ToolSession{Username: "alice", ChatSessionID: "s1", WorkspaceDir: t.TempDir(), DB: RootDB, CanScopeGlobal: true}
	mustCreate(t, sess, map[string]any{"name": "wx", "description": "weather for a city", "mode": "shell",
		"script_body": "from units import c\nimport os\nprint(c(os.environ.get('city','')))\n", "script_name": "run.py",
		"params":          map[string]any{"city": map[string]any{"type": "string", "description": "a city"}},
		"notes":           "Kelvin; conversion in units.py.",
		"workspace_files": []any{map[string]any{"path": "units.py", "content": "def c(x):\n    return x\n"}}})
	out, err := toolPack(map[string]any{"name": "wx"}, sess)
	if err != nil || !strings.Contains(out, "wx.gohorttool") {
		t.Fatalf("pack: %q %v", out, err)
	}
	out, err = toolUnpack(map[string]any{"file": "wx.gohorttool", "dir": "copy.tool"}, sess)
	if err != nil || !strings.Contains(out, "Nothing was installed") {
		t.Fatalf("unpack: %q %v", out, err)
	}
	dir := filepath.Join(sess.WorkspaceDir, "copy.tool")
	for f, want := range map[string]string{"run.py": "from units import c", "units.py": "def c", "NOTES.md": "Kelvin", "tool.json": `"city"`} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil || !strings.Contains(string(b), want) {
			t.Errorf("%s: %v %q", f, err, b)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, toolFolderBaseFile)); !os.IsNotExist(err) {
		t.Error("an unpacked folder claims to hold the live tool")
	}
}

// A tool's publish asks for notes the same way: a starter, or a script that
// changed while the notes did not.
func TestAToolPublishAsksForItsNotes(t *testing.T) {
	sess := folderSession(t)
	if _, err := toolCheckout(map[string]any{"name": "count"}, sess); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(sess.WorkspaceDir, "count.tool")
	os.WriteFile(filepath.Join(dir, "tool.json"), []byte(`{"name":"count","description":"count words","script":"script.py","params":{}}`), 0o644)
	out, err := toolPublish(map[string]any{"name": "count"}, sess)
	if err != nil || !strings.Contains(out, "NOTES.md is still the starter") {
		t.Fatalf("a starter's publish did not ask for notes: %v\n%s", err, out)
	}
	os.WriteFile(filepath.Join(dir, "NOTES.md"), []byte("Counts words split on whitespace."), 0o644)
	if out, _ := toolPublish(map[string]any{"name": "count"}, sess); strings.Contains(out, "NOTES:") {
		t.Errorf("written notes still asked for: %s", out)
	}
	os.WriteFile(filepath.Join(dir, "script.py"), []byte("print(2)\n"), 0o644)
	if out, _ := toolPublish(map[string]any{"name": "count"}, sess); !strings.Contains(out, "changed and count.tool/NOTES.md did not") {
		t.Errorf("a changed script with unchanged notes was not flagged: %s", out)
	}
}
