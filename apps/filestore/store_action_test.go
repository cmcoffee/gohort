package filestore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An app runs a command's mapped action through its grant: the approved line
// with its values filled the way an agent's tool fills them, a folder proved
// inside the store, and nothing until the mapping is approved.
func TestAnAppRunsAMappedAction(t *testing.T) {
	app, intake, _ := pullFixture(t)
	bin := filepath.Join(t.TempDir(), "kb.sh")
	os.WriteFile(bin, []byte("#!/bin/sh\nfor a in \"$@\"; do echo \"ARG<$a>\"; done\n"), 0o755)
	SaveStoreCommand(app.DB, StoreCommand{Slug: intake.Slug, Name: "kb", Command: bin,
		AllowedUses: []string{"download --id={id} --dst={folder} {files}"}})
	if _, err := SaveCommandTools(app.DB, intake.Slug, "kb", "File-sharing client.", []TempToolAction{{
		Name: "download", Description: "Download a case's files.", CommandTemplate: bin + " download --id={id} --dst={folder} {files}",
		Params: map[string]ToolParam{"id": {Type: "number", Description: "The case id."}, "folder": {Type: "string", PathScope: "files"},
			"files": {Type: "array"}},
		Required: []string{"id", "folder"}}}); err != nil {
		t.Fatal(err)
	}
	intake.AppGrants = []AppGrant{{App: "alice/kwprep", Commands: true}}
	SaveStore(app.DB, intake)
	host := storeHost{app}
	ctx := context.Background()
	vals := map[string]any{"id": float64(12345678), "folder": "CASE-1", "files": []any{"a.zip", "b c.zip"}}

	if _, err := host.RunAction(ctx, "alice", "kwprep", intake.Slug, "kb", "download", vals); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("an unapproved mapping ran: %v", err)
	}
	SetCommandApproved(app.DB, intake.Slug, "kb", true)
	out, err := host.RunAction(ctx, "alice", "kwprep", intake.Slug, "kb", "download", vals)
	if err != nil {
		t.Fatal(err)
	}
	want := "ARG<download>\nARG<--id=12345678>\nARG<--dst=" + filepath.Join(intake.Path, "CASE-1") + ">\nARG<a.zip>\nARG<b c.zip>"
	if !strings.Contains(out, want) {
		t.Errorf("ran as:\n%s\nwant:\n%s", out, want)
	}
	if _, err := host.RunAction(ctx, "alice", "kwprep", intake.Slug, "kb", "download",
		map[string]any{"id": 1, "folder": "../../etc"}); err == nil {
		t.Error("a folder outside the store ran")
	}
	if _, err := host.RunAction(ctx, "alice", "kwprep", intake.Slug, "kb", "upload", vals); err == nil || !strings.Contains(err.Error(), "download") {
		t.Errorf("an unknown action should name the real ones: %v", err)
	}
	if _, err := host.RunAction(ctx, "alice", "other", intake.Slug, "kb", "download", vals); err == nil {
		t.Error("an app without a grant ran it")
	}

	// The author sees the action, its line and its values.
	for _, st := range host.Stores("alice", "kwprep") {
		if st.Store != intake.Slug {
			continue
		}
		if len(st.Commands) != 1 || len(st.Commands[0].Actions) != 1 {
			t.Fatalf("commands: %+v", st.Commands)
		}
		a := st.Commands[0].Actions[0]
		if a.Line != "download --id={id} --dst={folder} {files}" || len(a.Values) != 3 {
			t.Errorf("action: %+v", a)
		}
		for _, v := range a.Values {
			if v.Name == "folder" && (!v.Folder || !v.Required) || v.Name == "files" && v.Required {
				t.Errorf("value: %+v", v)
			}
		}
	}
}

// What the binary is handed, value by value: plain digits for a number, one
// argument per list item, an absent optional value taking its option with
// it, and a placeholder the action never declared refused.
func TestSpecifiedArgvTranslatesValues(t *testing.T) {
	act := TempToolAction{Name: "ls", CommandTemplate: "/bin/kb ls -l --src {source} --limit {limit} --tag={tags} '{\"raw\":1}'",
		Params: map[string]ToolParam{"source": {}, "limit": {}, "tags": {}}}
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"limit": float64(50)}, `ls -l --limit 50 {"raw":1}`},
		{map[string]any{"source": "a b", "limit": float64(1e7), "tags": []any{"x", "y"}}, `ls -l --src a b --limit 10000000 --tag=x --tag=y {"raw":1}`},
		{map[string]any{"tags": true}, `ls -l --tag=true {"raw":1}`},
	}
	for _, c := range cases {
		vals := map[string][]string{}
		for k, v := range c.args {
			vals[k] = argValues(v)
		}
		argv, err := specifiedArgv(act, vals)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(argv, " "); got != c.want {
			t.Errorf("%v:\n got %s\nwant %s", c.args, got, c.want)
		}
	}
	ghost := TempToolAction{Name: "show", CommandTemplate: "/bin/kb show {name} {nothere}", Params: map[string]ToolParam{"name": {}}}
	if _, err := specifiedArgv(ghost, map[string][]string{"name": {"x"}}); err == nil || !strings.Contains(err.Error(), "nothere") {
		t.Errorf("an undeclared placeholder was dropped: %v", err)
	}
}
