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
		bytes.NewBufferString(`{"args":"download --src=\"/Diag/My Dumps/dump 1.zip\" --dst=RUN-1"}`))
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
	w := httptest.NewRecorder()
	app.handleTryCommand(w, r)
	body := w.Body.String()
	if !strings.Contains(body, `"ok":true`) || !strings.Contains(body, "Kept as example 1") {
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
	if !strings.Contains(text, "1. "+bin+" download --src=/Diag/My Dumps/dump 1.zip --dst=RUN-1") || !strings.Contains(text, "becomes a {placeholder}") {
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
