package servitor

import (
	"os"
	"strings"
	"testing"
)

// A set of read tools authored so ONE system can be investigated through them
// has no business appearing in every chat the user has. Disabled turns a tool
// off everywhere including its binder; BuilderOnly reserves it for authoring.
// Neither says "reachable only where it was deliberately attached".

// TestABoundOnlyToolStaysBindable — the half that must NOT change. Hiding the
// tool from agents while also hiding it from the thing that binds it would make
// the setting useless: servitor's own toolset resolves out of the same pool.
func TestABoundOnlyToolStaysBindable(t *testing.T) {
	src, err := os.ReadFile("toolset.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "func bindableTools(")
	if i < 0 {
		t.Fatal("bindableTools has moved")
	}
	block := body[i:]
	if j := strings.Index(block[1:], "\nfunc "); j > 0 {
		block = block[:j]
	}
	for _, filter := range []string{"BoundOnly", "BuilderOnly", "Disabled"} {
		if strings.Contains(block, filter) {
			t.Errorf("bindableTools filters on %s — a tool reserved for bound targets would be "+
				"withheld from the very binding it exists for", filter)
		}
	}
}

// TestAgentsDoNotSeeBoundOnlyToolsExceptBuilder — hidden from ordinary agents,
// still loaded by Builder.
//
// The carve-out is not an oversight: Builder has to load, RUN and fix these
// tools, and one it cannot run is one nobody can repair. A set of read tools
// authored for a single system is exactly what somebody asks Builder to fix, so
// reserving it away from the only agent that could would trade a real capability
// for a tidier catalog.
func TestAgentsDoNotSeeBoundOnlyToolsExceptBuilder(t *testing.T) {
	src, err := os.ReadFile("../orchestrate/runner.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	const rule = "if (p.Tool.Disabled || p.Tool.BuilderOnly || p.Tool.BoundOnly) && !isBuilderAgent(t.agent.ID) {"
	if !strings.Contains(body, rule) {
		t.Error("bound-only tools are no longer filtered alongside Disabled and Builder-only, " +
			"or the Builder carve-out was dropped — either way one of the two rules is now wrong")
	}
}
