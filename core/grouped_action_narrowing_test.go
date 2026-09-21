package core

// Withholding ONE action of a grouped tool while keeping the rest.
//
// A grouped tool is one grant with several jobs inside it: workspace reads
// files, writes them and RUNS COMMANDS. Caps() is the union — one action
// needing CapExecute hides the whole tool — so an owner who did not want the
// shell could only take the file access away with it.

import (
	"strings"
	"testing"
)

func narrowingFixture() *GroupedTool {
	g := NewGroupedTool("demo", "A demo tool.")
	g.AddAction("read", &GroupedToolAction{
		Description: "Read a thing. Harmless.",
		Params:      map[string]ToolParam{"path": {Type: "string"}},
		Caps:        []Capability{CapRead},
		Handler:     func(map[string]any, *ToolSession) (string, error) { return "read ok", nil },
	})
	g.AddAction("run", &GroupedToolAction{
		Description: "Run a command.",
		Params:      map[string]ToolParam{"cmd": {Type: "string"}},
		Caps:        []Capability{CapExecute},
		Handler:     func(map[string]any, *ToolSession) (string, error) { return "ran", nil },
	})
	return g
}

// The MODEL is not offered what it may not call. That is the half that changes
// behaviour: an action it cannot see is one it does not plan around.
func TestAWithheldActionLeavesTheSchema(t *testing.T) {
	g := narrowingFixture()
	sess := &ToolSession{Username: "u"}
	sess.SetWithheldActions([]string{"demo/run"})

	desc, params := g.SchemaWithSession(sess)
	if strings.Contains(desc, "run") {
		t.Errorf("the withheld action is still advertised: %q", desc)
	}
	if !strings.Contains(desc, "read") {
		t.Errorf("the kept action vanished with it: %q", desc)
	}
	// A param that existed only to serve the withheld action goes too.
	if _, ok := params["cmd"]; ok {
		t.Error("a param only the withheld action uses is still advertised")
	}
	if _, ok := params["path"]; !ok {
		t.Error("the kept action lost its param")
	}
}

// And it is refused at the call, for a name guessed or carried over from
// earlier in the conversation.
func TestAWithheldActionIsRefusedAtTheCall(t *testing.T) {
	g := narrowingFixture()
	sess := &ToolSession{Username: "u"}
	sess.SetWithheldActions([]string{"demo/run"})

	out, err := g.RunWithSession(map[string]any{"action": "run", "cmd": "echo hi"}, sess)
	if err == nil {
		t.Fatalf("the withheld action ran: %q", out)
	}
	// Said as a settled fact, not a failure to work around.
	if !strings.Contains(err.Error(), "switched off") || !strings.Contains(err.Error(), "retrying will not") {
		t.Errorf("the refusal invites a retry: %v", err)
	}
	// The kept action still works.
	if _, err := g.RunWithSession(map[string]any{"action": "read", "path": "x"}, sess); err != nil {
		t.Errorf("the kept action was refused too: %v", err)
	}
}

// help is how a model finds out what it HAS, so it must not advertise what it
// would be refused.
func TestHelpListsOnlyWhatTheCallerMayUse(t *testing.T) {
	g := narrowingFixture()
	sess := &ToolSession{Username: "u"}
	sess.SetWithheldActions([]string{"demo/run"})

	out, err := g.RunWithSession(map[string]any{"action": "help"}, sess)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, `action="run"`) {
		t.Errorf("help advertises an action it would refuse:\n%s", out)
	}
	if !strings.Contains(out, `action="read"`) {
		t.Errorf("help lost the action the caller has:\n%s", out)
	}
}

// Nothing withheld behaves exactly as before, including the byte-stable schema
// the prompt cache depends on.
func TestNothingWithheldIsTheOldBehaviour(t *testing.T) {
	g := narrowingFixture()
	for _, sess := range []*ToolSession{nil, {Username: "u"}} {
		desc, params := g.SchemaWithSession(sess)
		if desc != g.Desc() {
			t.Errorf("description drifted with nothing withheld:\n got %q\nwant %q", desc, g.Desc())
		}
		if len(params) != len(g.Params()) {
			t.Errorf("params drifted with nothing withheld: %d vs %d", len(params), len(g.Params()))
		}
	}
}

// A pair naming a different tool does not touch this one.
func TestWithholdingIsScopedToItsOwnTool(t *testing.T) {
	g := narrowingFixture()
	sess := &ToolSession{Username: "u"}
	sess.SetWithheldActions([]string{"other/run", "demo", "/run", ""})
	if sess.ActionWithheld("demo", "run") {
		t.Error("a pair for another tool, or a malformed one, narrowed this tool")
	}
	if desc, _ := g.SchemaWithSession(sess); desc != g.Desc() {
		t.Error("a malformed pair changed the schema")
	}
}
