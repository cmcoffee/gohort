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

// Builder maps a command freely, and every line it maps becomes one of the
// command's allowed uses; an agent runs only those lines, and a use the admin
// takes off the list stops running. A new mapping is never live until
// approved again.
func TestAgentsRunOnlyTheAllowedUses(t *testing.T) {
	kb := StoreCommand{Command: "/opt/bin/kb", AllowedUses: []string{"download   --src={source} --dst={folder}"}}
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
		t.Error("a specified command with no uses ran a line")
	}

	db := &DBase{Store: kvlite.MemStore()}
	SaveStore(db, Store{Name: "Intake", Path: t.TempDir()})
	saved, err := SaveStoreCommand(db, StoreCommand{Slug: "intake", Name: "kb", Command: "/opt/bin/kb", AllowedUses: []string{"download --src={source} --dst={folder}\nlist {folder}"}})
	if err != nil || saved.Usage != usageCurrent || len(saved.AllowedUses) != 2 {
		t.Fatalf("a new command: %+v %v", saved, err)
	}
	// Builder is not held to the written uses: what it maps joins them, for
	// the admin to approve.
	if c, err := SaveCommandTools(db, "intake", "kb", "File-sharing client.", []TempToolAction{{Name: "list_all", CommandTemplate: "/opt/bin/kb list --all"}}); err != nil || !strings.Contains(strings.Join(c.AllowedUses, "|"), "list --all") || c.Approved {
		t.Errorf("a line beyond the written uses: %v %v, approved %v", c.AllowedUses, err, c.Approved)
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
		{Command: "/opt/bin/kb"},
		{Command: "/opt/bin/kb", AllowedUses: []string{"download --src={source} --dst={folder}"}},
	} {
		for _, td := range app.mappingTools(context.Background(), Store{Name: "Intake"}, cmd) {
			if !strings.Contains(td.Tool.Description, "illustrates the SYNTAX") {
				t.Errorf("%s (%s) does not say examples are syntax", td.Tool.Name, cmd.Usage)
			}
		}
	}
	for _, cmd := range []StoreCommand{
		{Command: "/opt/bin/kb"},
		{Command: "/opt/bin/kb", AllowedUses: []string{"download --src={source}"}},
	} {
		for _, td := range app.mappingTools(context.Background(), Store{Name: "Intake"}, cmd) {
			if td.Tool.Name == "admin_examples" {
				continue // reads the admin's runs; the mapping rules are on the other two
			}
			if !strings.Contains(td.Tool.Description, "their values do not exist yet") {
				t.Errorf("%s (%s) does not say placeholders are filled later", td.Tool.Name, cmd.Usage)
			}
		}
	}
	// Builder explores freely, but is told a probe is real.
	for _, td := range app.mappingTools(context.Background(), Store{Name: "Intake"}, StoreCommand{Command: "/opt/bin/kb"}) {
		if td.Tool.Name == "probe_command" && !strings.Contains(td.Tool.Description, "ask the admin before one that would change") {
			t.Error("probe_command does not say a probe runs for real")
		}
	}
}

// Builder probes the command it maps with any arguments: the limit is on what
// agents run, not on what the admin's mapping conversation may try.
func TestBuilderProbesACommandFreely(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "kb.sh")
	os.WriteFile(bin, []byte("#!/bin/sh\necho \"ARGS<$*>\"\n"), 0o755)
	cmd := StoreCommand{Command: bin, AllowedUses: []string{"download --src={source}"}}
	out, err := (&FileStoreApp{}).probeCommand(context.Background(), t.TempDir(), cmd, "list --all")
	if err != nil || !strings.Contains(out, "ARGS<list --all>") {
		t.Errorf("probe beyond the uses: %q %v", out, err)
	}
}

// A command from when there were two modes is brought onto the one: an "open"
// (or never-marked) command's mapped lines become its uses, so agents keep the
// same lines, and an approved one is switched off, since those lines will now
// run directly rather than in the sandbox. A second pass changes nothing.
func TestOpenCommandsAreFoldedIntoTheirUses(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	SaveStore(db, Store{Name: "Intake", Path: t.TempDir()})
	for name, usage := range map[string]string{"cap": "open", "old": ""} {
		db.Set(commandsTable, commandKey("intake", name), StoreCommand{Slug: "intake", Name: name, Command: "/opt/bin/cap", Usage: usage, Approved: true,
			ToolDesc: "Reads a bundle.", Tools: []TempToolAction{{Name: "report", CommandTemplate: "/opt/bin/cap report  '{folder}'"}}})
	}
	db.Set(commandsTable, commandKey("intake", "kb"), StoreCommand{Slug: "intake", Name: "kb", Command: "/opt/bin/kb", Usage: usageCurrent, Approved: true,
		AllowedUses: []string{"list"}, Tools: []TempToolAction{{Name: "list", CommandTemplate: "/opt/bin/kb list"}}})
	MigrateCommandUses(db)
	MigrateCommandUses(db)
	for _, name := range []string{"cap", "old"} {
		c, _ := LoadStoreCommand(db, "intake", name)
		if c.Usage != usageCurrent || c.Approved || strings.Join(c.AllowedUses, "|") != "report '{folder}'" || len(c.runnableActions()) != 1 {
			t.Errorf("%s after the fold: usage %q approved %v uses %v", name, c.Usage, c.Approved, c.AllowedUses)
		}
	}
	if c, _ := LoadStoreCommand(db, "intake", "kb"); !c.Approved || len(c.AllowedUses) != 1 {
		t.Errorf("a current command was touched: %+v", c)
	}
}

// A mapped line is split the way a person typing it means it, so a quoted
// placeholder is still one argument and its quotes do not reach the binary.
func TestAQuotedPlaceholderRunsAsOneArgument(t *testing.T) {
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	prev := registeredFileStoreApp
	registeredFileStoreApp = app
	t.Cleanup(func() { registeredFileStoreApp = prev })
	st, _ := SaveStore(app.DB, Store{Name: "Intake", Path: t.TempDir()})
	bin := filepath.Join(t.TempDir(), "cap.sh")
	os.WriteFile(bin, []byte("#!/bin/sh\nfor a in \"$@\"; do echo \"ARG<$a>\"; done\n"), 0o755)
	SaveStoreCommand(app.DB, StoreCommand{Slug: st.Slug, Name: "cap", Command: bin})
	if _, err := SaveCommandTools(app.DB, st.Slug, "cap", "Tags.", []TempToolAction{{Name: "tag", Description: "Tag.",
		CommandTemplate: bin + " tag --note \"a b\" '{label}'", Params: map[string]ToolParam{"label": {Type: "string"}}, Required: []string{"label"}}}); err != nil {
		t.Fatal(err)
	}
	SetCommandApproved(app.DB, st.Slug, "cap", true)
	var tag AgentToolDef
	for _, td := range (storeSource{app: app}).ItemToolsWithSession(&ToolSession{Ctx: context.Background(), Username: "alice"}, "alice", st.Slug) {
		if strings.HasSuffix(td.Tool.Name, "_tag") {
			tag = td
		}
	}
	if tag.Handler == nil {
		t.Fatal("no tag tool")
	}
	out, err := tag.Handler(context.Background(), map[string]any{"label": "x y"})
	if err != nil || !strings.Contains(out, "ARG<a b>") || !strings.Contains(out, "ARG<x y>") {
		t.Errorf("ran as: %q %v", out, err)
	}
}
