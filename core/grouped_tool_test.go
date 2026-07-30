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
		Handler:     func(map[string]any, *ToolSession) (string, error) { return "ok", nil },
	})
	return g
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
