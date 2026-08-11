package core

import (
	"strings"
	"testing"
)

func runHintTool() *GroupedTool {
	g := NewGroupedTool("tool_def", "manage tools")
	g.AddAction("list", &GroupedToolAction{
		Description: "list tools",
		Handler:     func(map[string]any, *ToolSession) (string, error) { return "ok", nil },
	})
	g.AddAction("get", &GroupedToolAction{
		Description: "read one tool",
		Params:      map[string]ToolParam{"name": {Type: "string", Description: "tool name"}},
		Required:    []string{"name"},
		Handler:     func(map[string]any, *ToolSession) (string, error) { return "ok", nil },
	})
	return g
}

// action="help" ignores every other param. A model that writes
// help(name="get_top_stories") is asking about ONE tool and gets the generic
// authoring spec — a SUCCESS answering a different question, so the miss is
// invisible. Observed live: an agent asked twice, got the manual twice,
// concluded a published tool needed building, and ran the verifier three times
// (a real dispatch, three live fetches) before it thought to just call it.
func TestHelpWithParamsFlagsTheMissAndRoutesIt(t *testing.T) {
	g := runHintTool()
	out, err := g.Run(map[string]any{"action": "help", "name": "get_top_stories"})
	if err != nil {
		t.Fatalf("help should still return the spec: %v", err)
	}
	if !strings.Contains(out, "NOTE:") || !strings.Contains(out, "name") {
		t.Errorf("the ignored param should be named up front, got:\n%s", out)
	}
	// Route the caller to the action that actually takes "name".
	if !strings.Contains(out, `action="get"`) {
		t.Errorf("help should point at the action declaring the ignored param, got:\n%s", out)
	}
	// The banner heads the output — a model skims the top of a long dump.
	if idx := strings.Index(out, "NOTE:"); idx != 0 {
		t.Errorf("banner must lead the output, found at offset %d", idx)
	}
	// The spec itself is still there; the caller did ask for help.
	if !strings.Contains(out, "tool_def — usage:") {
		t.Errorf("the usage spec should still follow the banner, got:\n%s", out)
	}
}

// A bare help call is a legitimate request for the manual and must stay clean —
// no banner, no scolding.
func TestBareHelpHasNoBanner(t *testing.T) {
	g := runHintTool()
	out, err := g.Run(map[string]any{"action": "help"})
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if strings.Contains(out, "NOTE:") {
		t.Errorf("a bare help call should return the spec unadorned, got:\n%s", out)
	}
}

// An agent that inspected a custom tool through tool_def (list → get → test)
// then reached for action="run" got back only the list of valid actions, and
// spent the rest of the turn improvising: inventing routes, then trying to run
// the tool's underlying script by hand with guessed workspace paths. The one
// fact that would have unstuck it — call the tool directly by name, which the
// loop's lazy fallback resolves whether or not it is in the visible catalog —
// was never stated.
func TestUnknownRunActionPointsAtCallingDirectly(t *testing.T) {
	g := runHintTool()
	for _, action := range []string{"run", "execute", "invoke", "call", "use"} {
		_, err := g.Run(map[string]any{"action": action})
		if err == nil {
			t.Fatalf("action %q should be rejected", action)
		}
		if !strings.Contains(err.Error(), "DIRECTLY by its own name") {
			t.Errorf("action %q should point at calling the tool directly, got: %v", action, err)
		}
	}
}

// A plain typo is not a request to execute something — it gets the ordinary
// error without the extra sentence, so the hint keeps its meaning.
func TestUnknownNonRunActionStaysTerse(t *testing.T) {
	g := runHintTool()
	_, err := g.Run(map[string]any{"action": "lsit"})
	if err == nil {
		t.Fatal("a typo action should be rejected")
	}
	if strings.Contains(err.Error(), "DIRECTLY by its own name") {
		t.Errorf("a typo should not get the run hint, got: %v", err)
	}
	if !strings.Contains(err.Error(), "list") {
		t.Errorf("the error should still name the valid actions, got: %v", err)
	}
}

// A bare call is the shape a dropped-argument call arrives in. Answering it
// with the usage spec returns a long, successful-looking result that contains
// no data — the model cannot tell it from an answer, and re-calls. One observed
// turn burned 79 seconds that way across three tools.
func TestBareGroupedCallIsAnErrorNotHelp(t *testing.T) {
	g := runHintTool()
	out, err := g.Run(map[string]any{})
	if err == nil {
		t.Fatalf("a bare call must ERROR, not answer with help — got %d chars of result", len(out))
	}
	msg := err.Error()
	// Actionable on its own: name the actions, say plainly nothing happened.
	for _, want := range []string{"nothing was done", "list", "get", "help"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q: %s", want, msg)
		}
	}
	// And name the dropped-argument case, which is the actual cause when a
	// model that DID construct arguments lands here.
	if !strings.Contains(msg, "did not arrive") {
		t.Errorf("error should raise the dropped-argument possibility: %s", msg)
	}
}

// Explicit probing still works — discovery was never the problem.
func TestExplicitHelpStillReturnsTheSpec(t *testing.T) {
	g := runHintTool()
	out, err := g.Run(map[string]any{"action": "help"})
	if err != nil {
		t.Fatalf("action=help must still work: %v", err)
	}
	if !strings.Contains(out, "list") {
		t.Errorf("help should list the actions: %s", out)
	}
}
