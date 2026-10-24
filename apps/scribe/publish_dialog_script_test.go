package scribe

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The dialogs' scripts parse, with el() and the helpers they are handed
// defined around them. A syntax slip in a Go string is invisible until a
// browser refuses the whole script and the button does nothing.
func TestPublishDialogScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping JS syntax check")
	}
	for name, src := range map[string]string{
		"publish":  "var act = " + guidePublishAction + ";\n",
		"settings": "var act = " + guideSettingsAction + ";\n",
		"share":    "var act = " + guideShareAction + ";\n",
		"import":   "var act = " + scribeImportPageAction + ";\n",
		"update":   "var act = " + guideUpdateSourcesAction + ";\n",
		"image":    "var act = " + scribeImageAction + ";\n",
		"changes":  "var render = " + scribeChangesBlockJS + ";\n",
		"sections": guideSectionCode + "\n",
	} {
		f := filepath.Join(t.TempDir(), name+".js")
		if err := os.WriteFile(f, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
			t.Errorf("the %s script does not parse:\n%s", name, out)
		}
	}
}
