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
