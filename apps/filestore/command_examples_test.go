package filestore

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An argument line splits the way a person typing it means it, and nothing
// in it is expanded.
func TestArgumentLinesSplitLikeTyping(t *testing.T) {
	for in, want := range map[string]string{
		`download --src=/Diag/dump.zip --dst=RUN-1`: "download|--src=/Diag/dump.zip|--dst=RUN-1",
		`download --src="/Diag/My Dumps/a b.zip"`:   "download|--src=/Diag/My Dumps/a b.zip",
		`list 'one two' three\ four`:                "list|one two|three four",
		`echo $HOME * ; rm -rf /`:                   "echo|$HOME|*|;|rm|-rf|/",
		`   `:                                       "",
	} {
		got, err := splitArgs(in)
		if err != nil || strings.Join(got, "|") != want {
			t.Errorf("splitArgs(%q) = %q %v, want %q", in, strings.Join(got, "|"), err, want)
		}
	}
	if _, err := splitArgs(`download "unclosed`); err == nil {
		t.Error("an unclosed quote was accepted")
	}
}

// The admin runs the command from Show it how: it runs in the store folder
// with the arguments as typed, streams its output, and the run is kept as an
// example the mapping reads through admin_examples.
func TestAnAdminsRunBecomesAnExample(t *testing.T) {
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	prevAuth := AuthDB
	authDB := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(authDB, "admin-a", "pw-admin-123", true)
	AuthDB = func() Database { return authDB }
	t.Cleanup(func() { AuthDB = prevAuth })
	token := AuthCreateSession(authDB, "admin-a")

	root := t.TempDir()
	st, _ := SaveStore(app.DB, Store{Name: "Intake", Path: root})
	bin := filepath.Join(t.TempDir(), "kb.sh")
	os.WriteFile(bin, []byte("#!/bin/sh\necho \"running in $(pwd)\"\nfor a in \"$@\"; do echo \"ARG<$a>\"; done\n"), 0o755)
	SaveStoreCommand(app.DB, StoreCommand{Slug: st.Slug, Name: "kb", Command: bin})

	r := httptest.NewRequest(http.MethodPost, "/filestore/api/map/try?id="+st.Slug+"/kb",
		bytes.NewBufferString(`{"args":"download --src=\"/Diag/My Dumps/dump 1.zip\" --dst=RUN-1","note":"the usual download"}`))
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
	w := httptest.NewRecorder()
	app.handleTryCommand(w, r)
	body := w.Body.String()
	if !strings.Contains(body, `"ok":true`) || !strings.Contains(body, "use 1, step 1") {
		t.Fatalf("run: %s", body)
	}
	cmd, _ := LoadStoreCommand(app.DB, st.Slug, "kb")
	if len(cmd.Examples) != 1 {
		t.Fatalf("examples: %+v", cmd.Examples)
	}
	ex := cmd.Examples[0]
	if !strings.Contains(ex.Output, "ARG<--src=/Diag/My Dumps/dump 1.zip>") || !strings.Contains(ex.Output, "running in "+root) || ex.Exit != "0" {
		t.Errorf("example: %+v", ex)
	}
	text := cmd.examplesText()
	if !strings.Contains(text, "Use 1:\n  step 1: "+bin+" download --src=/Diag/My Dumps/dump 1.zip --dst=RUN-1") || !strings.Contains(text, "becomes a {placeholder}") ||
		!strings.Contains(text, "admin's note: the usual download") || !strings.Contains(text, "ASK the admin") {
		t.Errorf("what the mapping reads:\n%s", text)
	}
	var seen bool
	for _, td := range app.mappingTools(context.Background(), st, cmd) {
		if td.Tool.Name == "admin_examples" {
			seen = true
			out, _ := td.Handler(context.Background(), nil)
			if !strings.Contains(out, "dump 1.zip") {
				t.Errorf("admin_examples: %s", out)
			}
		}
		if td.Tool.Name == "probe_command" && !strings.Contains(td.Tool.Description, "read them with admin_examples") {
			t.Error("the mapping is not told examples exist")
		}
	}
	if !seen {
		t.Error("no admin_examples tool")
	}
}

// Runs marked as the next step join the last use, in order; a new use starts
// a new group. The mapping reads each use with its steps.
func TestRunsGroupIntoUsesWithSteps(t *testing.T) {
	cmd := StoreCommand{Command: "/opt/bin/unpack"}
	add := func(args, step, out string) {
		use := 1
		if n := len(cmd.Examples); n > 0 {
			use = cmd.Examples[n-1].Use + 1
			if step == "next" {
				use = cmd.Examples[n-1].Use
			}
		}
		cmd.Examples = append(cmd.Examples, CommandExample{Args: args, Output: out, Exit: "0", Use: use})
	}
	add("RUN-1", "new", "CHALLENGE 4711")
	add("RUN-1 s3cret", "next", "unpacked 12 files")
	add("--list", "new", "RUN-1")
	text := cmd.examplesText()
	for _, want := range []string{
		"Use 1:\n  step 1: /opt/bin/unpack RUN-1",
		"  step 2: /opt/bin/unpack RUN-1 s3cret",
		"Use 2:\n  step 1: /opt/bin/unpack --list",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

// The admin reviews the runs kept for a command: each listed as its use and
// step, a note added or changed, a run moved into another use, a mistaken one
// removed, and the mapping reads what is left.
func TestTheAdminReviewsTheRuns(t *testing.T) {
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	prevAuth := AuthDB
	authDB := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(authDB, "admin-a", "pw-admin-123", true)
	AuthDB = func() Database { return authDB }
	t.Cleanup(func() { AuthDB = prevAuth })
	token := AuthCreateSession(authDB, "admin-a")
	st, _ := SaveStore(app.DB, Store{Name: "Intake", Path: t.TempDir()})
	SaveStoreCommand(app.DB, StoreCommand{Slug: st.Slug, Name: "unpack", Command: "/opt/bin/unpack"})
	cmd, _ := LoadStoreCommand(app.DB, st.Slug, "unpack")
	cmd.Examples = []CommandExample{
		{ID: "a1", Args: "RUN-1", Output: "CHALLENGE 4711", Exit: "0", Use: 1},
		{ID: "b2", Args: "RUN-1 wrongkey", Output: "bad key", Exit: "exit status 2", Use: 2},
		{ID: "c3", Args: "RUN-1 s3cret", Output: "unpacked", Exit: "0", Use: 3},
	}
	app.DB.Set(commandsTable, commandKey(cmd.Slug, cmd.Name), cmd)
	do := func(method, q, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/filestore/api/map/examples?id="+st.Slug+"/unpack"+q, bytes.NewBufferString(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		w := httptest.NewRecorder()
		app.handleExamples(w, r)
		return w
	}
	if w := do(http.MethodGet, "", ""); !strings.Contains(w.Body.String(), `"where":"use 2, step 1"`) || !strings.Contains(w.Body.String(), `"exit":"exit status 2"`) {
		t.Fatalf("list: %s", w.Body.String())
	}
	do(http.MethodDelete, "&eid=b2", "")
	do(http.MethodPost, "&eid=c3", `{"use":"1","note":"step 2: the response from the support portal"}`)
	got := do(http.MethodGet, "", "").Body.String()
	if strings.Contains(got, "wrongkey") || !strings.Contains(got, `"where":"use 1, step 2"`) {
		t.Errorf("after removing one and moving one: %s", got)
	}
	cmd, _ = LoadStoreCommand(app.DB, st.Slug, "unpack")
	text := cmd.examplesText()
	if !strings.Contains(text, "Use 1:\n  step 1: /opt/bin/unpack RUN-1\n") || !strings.Contains(text, "  step 2: /opt/bin/unpack RUN-1 s3cret") ||
		!strings.Contains(text, "admin's note: step 2: the response from the support portal") || strings.Contains(text, "wrongkey") {
		t.Errorf("what the mapping reads:\n%s", text)
	}
}

// The row says where a command stands, so recording runs does not read as if
// it were enough: what is recorded, that Builder maps it next, that runs since
// a mapping mean mapping again, and that Agents is the last switch.
func TestTheRowSaysWhatComesNext(t *testing.T) {
	cmd := StoreCommand{Slug: "intake", Name: "unpack", Command: "/opt/bin/unpack"}
	if got := mappingState(cmd); !strings.Contains(got, "show it how, or map it with Builder") {
		t.Errorf("nothing yet: %q", got)
	}
	cmd.Examples = []CommandExample{{Args: "RUN-1", Use: 1, At: time.Now()}, {Args: "RUN-1 k", Use: 1, At: time.Now()}}
	if got := mappingState(cmd); got != "Not mapped: 2 runs recorded, map it with Builder next" {
		t.Errorf("runs, no mapping: %q", got)
	}
	cmd.Tools = []TempToolAction{{Name: "start", CommandTemplate: "/opt/bin/unpack {folder}"}}
	cmd.ToolDesc = "Unpacks."
	cmd.MappedAt = time.Now().Add(time.Minute)
	if got := mappingState(cmd); !strings.Contains(got, "Agents off: review it under Mapping, then turn Agents on") {
		t.Errorf("mapped, off: %q", got)
	}
	cmd.Approved = true
	if got := mappingState(cmd); !strings.HasSuffix(got, "agents can use it") {
		t.Errorf("mapped, on: %q", got)
	}
	cmd.Examples = append(cmd.Examples, CommandExample{Args: "--list", Use: 2, At: time.Now().Add(time.Hour)})
	if got := mappingState(cmd); !strings.Contains(got, "1 run recorded since, map again with Builder") {
		t.Errorf("runs since the mapping: %q", got)
	}
}

// A command with no uses written but runs recorded is mapped from those runs,
// and the lines become its uses.
func TestACommandIsMappedFromTheAdminsRuns(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	SaveStore(db, Store{Name: "Intake", Path: t.TempDir()})
	SaveStoreCommand(db, StoreCommand{Slug: "intake", Name: "extract", Command: "/opt/bin/extract"})
	cmd, _ := LoadStoreCommand(db, "intake", "extract")
	cmd.Examples = []CommandExample{
		{ID: "a", Argv: []string{"dump.enc"}, Use: 1},
		{ID: "b", Argv: []string{"dump.enc", "RESP123"}, Use: 1},
	}
	db.Set(commandsTable, commandKey(cmd.Slug, cmd.Name), cmd)
	file := ToolParam{Type: "string", PathScope: "files"}
	acts := []TempToolAction{
		{Name: "get_signature", CommandTemplate: "/opt/bin/extract {bundle}", Params: map[string]ToolParam{"bundle": file}, Required: []string{"bundle"}},
		{Name: "extract_with_response", CommandTemplate: "/opt/bin/extract {bundle} {response}",
			Params: map[string]ToolParam{"bundle": file, "response": {Type: "string"}}, Required: []string{"bundle", "response"}},
	}
	saved, err := SaveCommandTools(db, "intake", "extract", "Decrypts and extracts an archive in two steps.", acts)
	if err != nil {
		t.Fatalf("mapping from the runs was refused: %v", err)
	}
	if strings.Join(saved.AllowedUses, " | ") != "{bundle} | {bundle} {response}" || saved.Approved {
		t.Errorf("uses recorded: %v, approved %v", saved.AllowedUses, saved.Approved)
	}
}

// An agent's call to a command mapped from runs: a FILE in the store is passed
// as its full path, one argument, and a name outside the store is refused.
func TestAnAgentPassesAFileFromTheStore(t *testing.T) {
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	prev := registeredFileStoreApp
	registeredFileStoreApp = app
	t.Cleanup(func() { registeredFileStoreApp = prev })
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "dump(user@example.test).enc"), []byte("x"), 0o644)
	st, _ := SaveStore(app.DB, Store{Name: "Intake", Path: root})
	bin := filepath.Join(t.TempDir(), "extract.sh")
	os.WriteFile(bin, []byte("#!/bin/sh\nfor a in \"$@\"; do echo \"ARG<$a>\"; done\n"), 0o755)
	SaveStoreCommand(app.DB, StoreCommand{Slug: st.Slug, Name: "extract", Command: bin})
	cmd, _ := LoadStoreCommand(app.DB, st.Slug, "extract")
	cmd.Examples = []CommandExample{{ID: "a", Argv: []string{"dump(user@example.test).enc", "RESP"}, Use: 1}}
	app.DB.Set(commandsTable, commandKey(cmd.Slug, cmd.Name), cmd)
	if _, err := SaveCommandTools(app.DB, st.Slug, "extract", "Extracts.", []TempToolAction{{Name: "finish", Description: "Finish.",
		CommandTemplate: bin + " {bundle} {response}",
		Params:          map[string]ToolParam{"bundle": {Type: "string", PathScope: "files"}, "response": {Type: "string"}},
		Required:        []string{"bundle", "response"}}}); err != nil {
		t.Fatal(err)
	}
	SetCommandApproved(app.DB, st.Slug, "extract", true)
	var finish AgentToolDef
	for _, td := range (storeSource{app: app}).ItemToolsWithSession(&ToolSession{Ctx: context.Background(), Username: "alice"}, "alice", st.Slug) {
		if strings.HasSuffix(td.Tool.Name, "_finish") {
			finish = td
		}
	}
	if finish.Handler == nil {
		t.Fatal("no finish tool")
	}
	out, err := finish.Handler(context.Background(), map[string]any{"bundle": "dump(user@example.test).enc", "response": "s3cret"})
	if err != nil || !strings.Contains(out, "ARG<"+filepath.Join(root, "dump(user@example.test).enc")+">") || !strings.Contains(out, "ARG<s3cret>") {
		t.Errorf("ran as: %q %v", out, err)
	}
	if out, err := finish.Handler(context.Background(), map[string]any{"bundle": "../../etc/passwd", "response": "x"}); err == nil || strings.Contains(out, "ARG<") {
		t.Errorf("a file outside the store was passed: %q %v", out, err)
	}
}

// The admin's guide reaches Builder first in what it reads, and agents in the
// store's command list, along with whether a command is theirs to run; a
// guide-only change keeps the approval, a change to the uses does not.
func TestTheAdminsGuideReachesBuilderAndAgents(t *testing.T) {
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	prevAuth := AuthDB
	authDB := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(authDB, "admin-a", "pw-admin-123", true)
	AuthDB = func() Database { return authDB }
	t.Cleanup(func() { AuthDB = prevAuth })
	token := AuthCreateSession(authDB, "admin-a")
	st, _ := SaveStore(app.DB, Store{Name: "Intake", Path: t.TempDir()})
	SaveStoreCommand(app.DB, StoreCommand{Slug: st.Slug, Name: "extract", Command: "/opt/bin/extract",
		Guide: "Step 1 prints a SIGNATURE.\nSupport turns it into a **response**; it works once."})
	cmd, _ := LoadStoreCommand(app.DB, st.Slug, "extract")
	if !strings.HasPrefix(cmd.examplesText(), "THE ADMIN'S GUIDE TO THIS COMMAND") || !strings.Contains(cmd.examplesText(), "it works once") {
		t.Errorf("Builder does not read the guide first:\n%s", cmd.examplesText())
	}
	listing := describeStoreCommands(app.DB, st)
	if !strings.Contains(listing, "Support turns it into a **response**") || !strings.Contains(listing, "Not yours to run") {
		t.Errorf("agents' listing:\n%s", listing)
	}

	cmd.Tools = []TempToolAction{{Name: "start", CommandTemplate: "/opt/bin/extract {bundle}"}}
	cmd.ToolDesc, cmd.Approved, cmd.AllowedUses = "Extracts.", true, []string{"{bundle}"}
	app.DB.Set(commandsTable, commandKey(cmd.Slug, cmd.Name), cmd)
	// By tool name and the line it runs: "its X_* tools" left an agent to go
	// looking, and one that did not find them handed the work to another agent.
	if listing := describeStoreCommands(app.DB, st); !strings.Contains(listing, "Yours to run, with these tools:") ||
		!strings.Contains(listing, "extract_intake_start: /opt/bin/extract {bundle}") {
		t.Errorf("an approved command should read as the agent's to run, by tool:\n%s", listing)
	}
	post := func(body string) {
		r := httptest.NewRequest(http.MethodPost, "/filestore/api/commands/uses?id="+st.Slug+"/extract", bytes.NewBufferString(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		w := httptest.NewRecorder()
		app.handleCommandUses(w, r)
	}
	post(`{"usage":"specified","allowed_uses":"{bundle}","guide":"A better guide."}`)
	if c, _ := LoadStoreCommand(app.DB, st.Slug, "extract"); !c.Approved || c.Guide != "A better guide." {
		t.Errorf("a guide-only change: approved=%v guide=%q", c.Approved, c.Guide)
	}
	// How it runs is not what it runs: the approval stands.
	post(`{"usage":"specified","allowed_uses":"{bundle}","guide":"A better guide.","background":"Always"}`)
	if c, _ := LoadStoreCommand(app.DB, st.Slug, "extract"); !c.Approved || c.Background != "always" || c.detachAfter() != backgroundDetach {
		t.Errorf("background: approved=%v background=%q detach=%s", c.Approved, c.Background, c.detachAfter())
	}
	post(`{"usage":"specified","allowed_uses":"{bundle} {response}","guide":"A better guide."}`)
	if c, _ := LoadStoreCommand(app.DB, st.Slug, "extract"); c.Approved {
		t.Error("a change to the uses kept the approval")
	}
}
