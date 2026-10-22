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

// An update carrying actions:[one action] used to delete every other action
// the app had and report success. Leaving a stored script out of the list is
// now refused by name, unless confirm_rewrite says the deletion is meant.
func TestAppDroppedScriptsNamesWhatWouldBeDeleted(t *testing.T) {
	risk := appDroppedScripts("actions", "action", []string{"step", "reset"}, []string{"step"})
	for _, want := range []string{"reset", "REPLACES the whole actions list", "confirm_rewrite"} {
		if !strings.Contains(risk, want) {
			t.Errorf("the refusal must contain %q, got:\n%s", want, risk)
		}
	}
	if strings.Contains(risk, "step,") || strings.Contains(risk, ": step") {
		t.Errorf("a kept script is not a dropped one:\n%s", risk)
	}
	// Names compare slugified: re-sending balance_step keeps balance-step.
	if r := appDroppedScripts("data_sources", "data source", []string{"balance-step"}, []string{"balance_step"}); r != "" {
		t.Errorf("the same name in its unslugified spelling is not a drop: %s", r)
	}
	// Adding and keeping are ordinary edits.
	if r := appDroppedScripts("actions", "action", []string{"step"}, []string{"step", "reset"}); r != "" {
		t.Errorf("adding a script is not a drop: %s", r)
	}
	// An empty list sent on purpose deletes everything, so it is refused too.
	if r := appDroppedScripts("actions", "action", []string{"step"}, nil); !strings.Contains(r, "step") {
		t.Errorf("an empty list drops every stored script: %q", r)
	}
}

// The guard in place on the real update path: refused without confirm, saved
// with it, saved when the list keeps every stored name.
func TestUpdateRefusesToSilentlyDropAnAction(t *testing.T) {
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
			out = append(out, map[string]any{"name": n, "script": script})
		}
		return out
	}

	_, err := update(map[string]any{"actions": actions("step")})
	if err == nil || !strings.Contains(err.Error(), "reset") {
		t.Fatalf("dropping reset must be refused by name, got %v", err)
	}
	if spec, _ := LoadAppSpec("u", "balance"); len(spec.Actions) != 2 {
		t.Fatalf("a refused update must not save, have %d actions", len(spec.Actions))
	}

	// A rename note travels with the refusal too: the author fixing the list
	// needs to know new_one was stored as new-one.
	_, err = update(map[string]any{"actions": actions("step", "new_one")})
	if err == nil || !strings.Contains(err.Error(), "Heads up") || !strings.Contains(err.Error(), "new-one") {
		t.Fatalf("the refusal should carry the adjusted-input notes, got %v", err)
	}

	if _, err := update(map[string]any{"actions": actions("step", "reset", "undo")}); err != nil {
		t.Fatalf("keeping every stored action is an ordinary edit: %v", err)
	}
	if spec, _ := LoadAppSpec("u", "balance"); len(spec.Actions) != 3 {
		t.Fatalf("want 3 actions after the additive update, have %d", len(spec.Actions))
	}

	if _, err := update(map[string]any{"actions": actions("step"), "confirm_rewrite": true}); err != nil {
		t.Fatalf("confirm_rewrite must allow a deliberate deletion: %v", err)
	}
	if spec, _ := LoadAppSpec("u", "balance"); len(spec.Actions) != 1 || spec.Actions[0].Name != "step" {
		t.Fatalf("confirmed deletion should leave only step, have %+v", spec.Actions)
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
