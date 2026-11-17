package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// One round's app_def calls run in the order asked, and with the workspace
// edits around them. Two updates to one app in parallel lost each other's
// writes, and a folder edit beside a publish let the publish read the folder
// before the edit landed, so it said "Published" without the change. app_def
// is serial-fire and the loop orders a round that writes (core
// dispatchTools); a lane per app would keep it outside that order.
func TestAppDefAndWorkspaceCallsKeepTheirOrder(t *testing.T) {
	td := (&chatTurn{}).appDefToolDef()
	if !td.SerialFirePerBatch || td.BatchLane != nil {
		t.Fatal("app_def must be serial-fire, not laned per app: a workspace edit in the same round has to stay in its order with it")
	}
	byName := appDefSlug(map[string]any{"action": "create", "name": "Balance Game"})
	byID := appDefSlug(map[string]any{"action": "update", "id": "balance-game"})
	if byName == "" || byName != byID {
		t.Fatalf("one app must resolve to one slug across spellings: name=%q id=%q", byName, byID)
	}
}

// An update changes what its lists name and keeps what they leave out: it
// used to replace a whole list, so a list leaving a script out was refused by
// name, and every change to one action re-sent all of them, scripts in full.
// The reply says what was kept; confirm_rewrite replaces a list, which is
// how a script is deleted.
func TestAnUpdateMergesItsListsByName(t *testing.T) {
	pinRootDB(t)
	script := "import json\nprint(json.dumps({\"message\": \"ok\"}))\n"
	SaveAppSpecAs(AppSpec{Slug: "balance", Name: "Balance", Owner: "u", RecordKey: "id",
		Actions: []AppAction{{Name: "step", Script: script}, {Name: "reset", Script: script}}}, "create")
	turn := &chatTurn{user: "u"}
	update := func(extra map[string]any) (string, error) {
		args := map[string]any{"id": "balance"}
		for k, v := range extra {
			args[k] = v
		}
		return turn.appDefCreateOrUpdate(args, true)
	}
	actions := func(names ...string) []any {
		var out []any
		for _, n := range names {
			out = append(out, map[string]any{"name": n, "script": script, "label": "L " + n})
		}
		return out
	}
	names := func() string {
		spec, _ := LoadAppSpec("u", "balance")
		var out []string
		for _, a := range spec.Actions {
			out = append(out, a.Name+"="+a.Label)
		}
		return strings.Join(out, ",")
	}

	out, err := update(map[string]any{"actions": actions("step")})
	if err != nil || !strings.Contains(out, "kept the 1 action(s)") || !strings.Contains(out, "reset") {
		t.Fatalf("an update naming one action did not keep the other and say so: %v\n%s", err, out)
	}
	if got := names(); got != "step=L step,reset=" {
		t.Fatalf("after changing step: %s", got)
	}
	// A new name is added after the rest, its rename noted.
	out, err = update(map[string]any{"actions": actions("new_one")})
	if err != nil || !strings.Contains(out, "new-one") {
		t.Fatalf("adding one action: %v\n%s", err, out)
	}
	if got := names(); got != "step=L step,reset=,new-one=L new_one" {
		t.Fatalf("after adding new_one: %s", got)
	}
	// confirm_rewrite replaces the list: the way to delete.
	if _, err := update(map[string]any{"actions": actions("step"), "confirm_rewrite": true}); err != nil {
		t.Fatalf("confirm_rewrite: %v", err)
	}
	if got := names(); got != "step=L step" {
		t.Fatalf("confirm_rewrite should leave only step, have %s", got)
	}
}

// A failure reported after the save must still say what the framework renamed:
// that is the moment the author is reading closely, and a page fetching the
// unslugified name is often the very thing that failed.
func TestParseNotesRideOnFailureMessages(t *testing.T) {
	notes := []string{`data source "balance_step" is registered as "balance-step"`}
	got := appWithParseNotes("Updated app \"G\", BUT a data source FAILED to run", notes)
	if !strings.Contains(got, "Heads up, the framework adjusted your input") || !strings.Contains(got, "balance-step") {
		t.Fatalf("notes block missing: %s", got)
	}
	if appWithParseNotes("ok", nil) != "ok" {
		t.Fatal("no notes means no block")
	}
	// Every early return in the save path goes through the helper.
	src := readSourceFile(t, "app_def_tool_create.go")
	for _, marker := range []string{"DOES NOT PARSE", "CALLS CODE IT NEVER DEFINES", "FAILS IN A REAL BROWSER", "a data source FAILED"} {
		i := strings.Index(src, marker)
		if i < 0 {
			t.Fatalf("marker %q not found", marker)
		}
		line := src[strings.LastIndex(src[:i], "\n"):i]
		if !strings.Contains(line, "appWithParseNotes(") {
			t.Errorf("the %q failure is built without the parse notes", marker)
		}
	}
}
