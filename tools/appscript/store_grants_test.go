package appscript

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
)

// fakeStores grants the app one store to write and one to read, and records
// the commands it is asked to run.
type fakeStores struct {
	write, read string
	ran         []string
}

func (f *fakeStores) GrantsFor(owner, slug string) []appstores.Grant {
	if owner != "alice" || slug != "kwprep" {
		return nil
	}
	return []appstores.Grant{
		{Store: "intake", Name: "Intake", Path: f.write, Read: true, Write: true, Commands: true},
		{Store: "analysis", Name: "Analysis", Path: f.read, Read: true, Pull: true},
	}
}
func (f *fakeStores) RunCommand(ctx context.Context, owner, slug, store, folder, command, input string) (string, error) {
	f.ran = append(f.ran, store+"/"+folder+"/"+command+"/"+input)
	if input == "" {
		return "CHALLENGE 4711", nil
	}
	return "extracted", nil
}
func (f *fakeStores) Pull(ctx context.Context, owner, slug, store, caseName string, replace bool) (string, error) {
	return "Copied " + caseName, nil
}

// A real script in the real sandbox: it finds its stores in GOHORT_STORES,
// writes into the one it may write, cannot write into the one it may only
// read, and reaches the store's command through the hook.
func TestAnAppScriptWorksInTheStoresItWasGranted(t *testing.T) {
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache directory for folders outside /tmp")
	}
	root, err := os.MkdirTemp(cache, "gohort-appstores-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	prevWS := WorkspacesDir()
	SetWorkspacesDir(filepath.Join(root, "ws"))
	t.Cleanup(func() { SetWorkspacesDir(prevWS) })
	f := &fakeStores{write: filepath.Join(root, "intake"), read: filepath.Join(root, "analysis")}
	os.MkdirAll(filepath.Join(f.write, "CASE-1"), 0o755)
	os.MkdirAll(f.read, 0o755)
	prev := appstores.Provider
	appstores.Provider = f
	t.Cleanup(func() { appstores.Provider = prev })

	script := `
import json, os
from gohort import store_path, store_command
stores = json.loads(os.environ["GOHORT_STORES"])
intake = store_path("intake")
open(os.path.join(intake, "CASE-1", "dump.zip"), "w").write("raw")
try:
    open(os.path.join(store_path("analysis"), "sneaky.txt"), "w").write("x")
    wrote_read_only = True
except OSError:
    wrote_read_only = False
challenge = store_command("intake", "CASE-1", "extract")
done = store_command("intake", "CASE-1", "extract", input="s3cret")
print(json.dumps({"message": "ok", "result": {"challenge": str(challenge), "done": str(done), "wrote_read_only": wrote_read_only, "rights": stores["analysis"]["pull"]}}))
`
	out, err := Job{Owner: "alice", Slug: "kwprep", Kind: "action", Name: "prep", Script: script, Args: map[string]any{}}.Run()
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if strings.Contains(out, "sandbox") && strings.Contains(out, "not") && !strings.Contains(out, "CHALLENGE") {
		t.Skipf("no sandbox here: %s", out)
	}
	for _, want := range []string{`"challenge": "CHALLENGE 4711"`, `"done": "extracted"`, `"wrote_read_only": false`, `"rights": true`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(f.write, "CASE-1", "dump.zip")); string(b) != "raw" {
		t.Error("the script could not write into the store it may write")
	}
	if _, err := os.Stat(filepath.Join(f.read, "sneaky.txt")); err == nil {
		t.Error("the script wrote into a store it may only read")
	}
	if len(f.ran) != 2 || f.ran[1] != "intake/CASE-1/extract/s3cret" {
		t.Errorf("commands asked for: %v", f.ran)
	}

	// An app with no grants gets no stores.
	out, _ = Job{Owner: "alice", Slug: "other", Kind: "action", Name: "x", Script: `import os; print('{"message":"%s"}' % ("GOHORT_STORES" in os.environ))`, Args: map[string]any{}}.Run()
	if !strings.Contains(out, `"message":"False"`) {
		t.Errorf("an app with no grant saw stores: %s", out)
	}
}
