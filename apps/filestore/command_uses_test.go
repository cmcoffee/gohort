package filestore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A command limited to the uses an admin wrote: Builder may read only their
// help and map only those lines; an open command may be explored and mapped
// freely. A new mapping is never live until approved again.
func TestACommandIsUsedOnlyAsTheAdminSpecified(t *testing.T) {
	kb := StoreCommand{Command: "/opt/bin/kb", AllowedUses: []string{"download   --src={source} --dst={folder}"}}
	for argv, ok := range map[string]bool{
		"--help":                  true,
		"--version":               true,
		"download --help":         true,
		"download -h":             true,
		"upload --help":           false,
		"download --src=x":        false,
		"delete --all":            false,
		"download --src=x --help": false,
	} {
		if err := kb.probeAllowed(strings.Fields(argv)); (err == nil) != ok {
			t.Errorf("probe %q: allowed=%v, want %v", argv, err == nil, ok)
		}
	}
	for tmpl, ok := range map[string]bool{
		"/opt/bin/kb download --src={source} --dst={folder}":        true,
		"/opt/bin/kb download  --src={source}  --dst={folder}":      true,
		"/opt/bin/kb download --src={source} --dst={folder} --move": false,
		"/opt/bin/kb delete {folder}":                               false,
	} {
		if err := kb.templateAllowed(tmpl); (err == nil) != ok {
			t.Errorf("template %q: allowed=%v, want %v (%v)", tmpl, err == nil, ok, err)
		}
	}
	if (StoreCommand{Command: "/opt/bin/kb"}).templateAllowed("/opt/bin/kb download") == nil {
		t.Error("a specified command with no uses written could be mapped")
	}
	open := StoreCommand{Command: "/opt/bin/kb", Usage: UsageOpen}
	if open.probeAllowed([]string{"delete", "--all"}) != nil || open.templateAllowed("/opt/bin/kb anything {x}") != nil {
		t.Error("an open command was restricted")
	}

	db := &DBase{Store: kvlite.MemStore()}
	SaveStore(db, Store{Name: "Intake", Path: t.TempDir()})
	saved, err := SaveStoreCommand(db, StoreCommand{Slug: "intake", Name: "kb", Command: "/opt/bin/kb", AllowedUses: []string{"download --src={source} --dst={folder}\nlist {folder}"}})
	if err != nil || saved.Usage != UsageSpecified || len(saved.AllowedUses) != 2 {
		t.Fatalf("a new command: %+v %v", saved, err)
	}
	if _, err := SaveCommandTools(db, "intake", "kb", "File-sharing client.", []TempToolAction{{Name: "wipe", CommandTemplate: "/opt/bin/kb delete --all"}}); err == nil {
		t.Error("a mapping outside the allowed uses was saved")
	}
	act := TempToolAction{Name: "download", CommandTemplate: "/opt/bin/kb download --src={source} --dst={folder}",
		Params: map[string]ToolParam{"source": {Type: "string"}, "folder": {Type: "string", PathScope: "files"}}, Required: []string{"source", "folder"}}
	if _, err := SaveCommandTools(db, "intake", "kb", "File-sharing client.", []TempToolAction{act}); err != nil {
		t.Fatal(err)
	}
	SetCommandApproved(db, "intake", "kb", true)
	if _, err := SaveCommandTools(db, "intake", "kb", "File-sharing client, corrected.", []TempToolAction{act}); err != nil {
		t.Fatal(err)
	}
	if c, _ := LoadStoreCommand(db, "intake", "kb"); c.Approved {
		t.Error("a re-map stayed approved")
	}
}

// An agent's call to a specified command runs the admin's line as written,
// directly: each value is ONE argument (spaces and all), a folder is proved
// inside the store and handed over as its full path, and a value that would
// stand alone as an option is refused.
func TestAnAgentRunsASpecifiedCommandExactlyAsWritten(t *testing.T) {
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	prev := registeredFileStoreApp
	registeredFileStoreApp = app
	t.Cleanup(func() { registeredFileStoreApp = prev })
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "CASE-1"), 0o755)
	st, _ := SaveStore(app.DB, Store{Name: "Intake", Path: root})
	bin := filepath.Join(t.TempDir(), "kb.sh")
	os.WriteFile(bin, []byte("#!/bin/sh\nfor a in \"$@\"; do echo \"ARG<$a>\"; done\n"), 0o755)
	SaveStoreCommand(app.DB, StoreCommand{Slug: st.Slug, Name: "kb", Command: bin,
		AllowedUses: []string{"download --src={source} --dst={folder}", "verify {name}"}})
	acts := []TempToolAction{
		{Name: "download", Description: "Download a dump.", CommandTemplate: bin + " download --src={source} --dst={folder}",
			Params: map[string]ToolParam{"source": {Type: "string"}, "folder": {Type: "string", PathScope: "files"}}, Required: []string{"source", "folder"}},
		{Name: "verify", Description: "Verify one.", CommandTemplate: bin + " verify {name}",
			Params: map[string]ToolParam{"name": {Type: "string"}}, Required: []string{"name"}},
	}
	if _, err := SaveCommandTools(app.DB, st.Slug, "kb", "File-sharing client.", acts); err != nil {
		t.Fatal(err)
	}
	SetCommandApproved(app.DB, st.Slug, "kb", true)

	src := storeSource{app: app}
	sess := &ToolSession{Ctx: context.Background(), Username: "alice"}
	tools := map[string]AgentToolDef{}
	for _, td := range src.ItemToolsWithSession(sess, "alice", st.Slug) {
		tools[td.Tool.Name] = td
	}
	dl, ok := tools["kb_intake_download"]
	if !ok {
		names := []string{}
		for n := range tools {
			names = append(names, n)
		}
		t.Fatalf("no kb_intake_download tool among %v", names)
	}
	if !strings.Contains(dl.Tool.Description, "Runs exactly: "+bin+" download --src={source} --dst={folder}") {
		t.Errorf("description: %s", dl.Tool.Description)
	}
	out, err := dl.Handler(context.Background(), map[string]any{"source": "Diag Dumps/dump 1.zip", "folder": "CASE-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := "ARG<download>\nARG<--src=Diag Dumps/dump 1.zip>\nARG<--dst=" + filepath.Join(root, "CASE-1") + ">"
	if !strings.Contains(out, want) {
		t.Errorf("ran as:\n%s\nwant:\n%s", out, want)
	}
	if out, err := dl.Handler(context.Background(), map[string]any{"source": "x", "folder": "../../etc"}); err == nil || strings.Contains(out, "ARG<") {
		t.Errorf("a folder outside the store ran: %q %v", out, err)
	}
	if _, err := tools["kb_intake_verify"].Handler(context.Background(), map[string]any{"name": "--purge"}); err == nil {
		t.Error("a value that is an option was passed")
	}
}

// The mapping is told that example values are syntax and placeholders are
// filled later, in both modes, so it does not go looking for an example's file.
func TestTheMappingIsToldExamplesAreSyntax(t *testing.T) {
	app := &FileStoreApp{}
	for _, cmd := range []StoreCommand{
		{Command: "/opt/bin/kb", Usage: UsageOpen},
		{Command: "/opt/bin/kb", AllowedUses: []string{"download --src={source} --dst={folder}"}},
	} {
		for _, td := range app.mappingTools(context.Background(), Store{Name: "Intake"}, cmd) {
			if !strings.Contains(td.Tool.Description, "illustrates the SYNTAX") {
				t.Errorf("%s (%s) does not say examples are syntax", td.Tool.Name, cmd.Usage)
			}
		}
	}
	for _, td := range app.mappingTools(context.Background(), Store{Name: "Intake"}, StoreCommand{Command: "/opt/bin/kb", AllowedUses: []string{"download --src={source}"}}) {
		if !strings.Contains(td.Tool.Description, "its value does not exist yet") {
			t.Errorf("%s does not say placeholders are filled later", td.Tool.Name)
		}
	}
}
