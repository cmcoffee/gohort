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
