package filestore

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A run is watched for growth where its output lands: each folder it was
// given, the folder of each file it was given, or its working folder when it
// was given neither.
func TestARunWatchesWhereItsOutputLands(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "CASE-1")
	os.MkdirAll(caseDir, 0o755)
	archive := filepath.Join(root, "drop", "dump.zip")
	os.MkdirAll(filepath.Dir(archive), 0o755)
	os.WriteFile(archive, []byte("x"), 0o644)
	act := TempToolAction{Params: map[string]ToolParam{
		"dst": {PathScope: "files:intake"}, "archive": {PathScope: "files:intake"}, "source": {}}}
	got := growthDirs(act, map[string][]string{"dst": {caseDir}, "archive": {archive}, "source": {"remote/file"}}, root)
	sort.Strings(got)
	want := []string{caseDir, filepath.Dir(archive)}
	sort.Strings(want)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("watched %v, want %v", got, want)
	}
	if got := growthDirs(act, map[string][]string{"source": {"x"}}, root); len(got) != 1 || got[0] != root {
		t.Errorf("with no folder given: %v", got)
	}
}

// The stored setting is "always" or nothing, however it was typed.
func TestBackgroundSettingIsNormalized(t *testing.T) {
	for in, want := range map[string]string{"always": "always", " Always ": "always", "": "", "sometimes": ""} {
		if got := backgroundMode(in); got != want {
			t.Errorf("backgroundMode(%q) = %q, want %q", in, got, want)
		}
	}
	if (StoreCommand{}).detachAfter() != 0 {
		t.Error("a command left to the deployment set its own detach time")
	}
}
