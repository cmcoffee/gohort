package orchestrate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Builder's rarely used authoring tools, and schedule, sit behind load_tool:
// out of its inline catalog and its tool roster, listed in the index, and
// still callable directly. The ones it works with every day stay inline.
func TestBuildersLessUsedToolsLoadOnDemand(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	builder, ok := seedAgentByID("seed-builder")
	if !ok {
		t.Fatal("no Builder seed")
	}
	builder.Owner = "u"
	turn.agent = builder
	sess := turn.newToolSession()
	tools, names, err := turn.resolveWorkerTools(sess, true)
	if err != nil {
		t.Fatal(err)
	}
	size := func(list []AgentToolDef) int {
		n := 0
		for _, td := range list {
			b, _ := json.Marshal(td.Tool)
			n += len(b)
		}
		return n
	}
	// app_def, pipeline and machine join the other catalog (catalogKnowTools);
	// stand-ins check the rule for them on the same call.
	for _, n := range []string{"app_def", "pipeline", "machine"} {
		tools = append(tools, AgentToolDef{Tool: Tool{Name: n, Description: n + " manager."},
			Handler: func(context.Context, map[string]any) (string, error) { return "ok", nil }})
		names = append(names, n)
	}
	before := size(tools)
	kept, keptNames := turn.deferBuilderOnDemandTools(tools, names)
	in := map[string]bool{}
	for _, td := range kept {
		in[td.Tool.Name] = true
	}
	for _, n := range []string{"connector", "machine", "bridge", "skill_def", "draft_api_credential", "clone_agent", "tool_template"} {
		if in[n] {
			t.Errorf("%s is still inline", n)
		}
		if _, deferred := turn.deferredAuthoringDefs[n]; !deferred {
			t.Errorf("%s is not in the load-before-use index", n)
		}
		if !strings.Contains(turn.builderOnDemandIndexText(), "`"+n+"`") {
			t.Errorf("the index does not list %s", n)
		}
		if h, ok := turn.lazyToolFallback(n); !ok || h == nil {
			t.Errorf("a direct call to %s does not resolve", n)
		}
	}
	for _, n := range keptNames {
		if builderOnDemandTools[n] {
			t.Errorf("the roster still names %s as inline", n)
		}
	}
	for _, n := range []string{"app_def", "tool_def", "create_agent", "update_agent", "pipeline"} {
		if !in[n] {
			t.Errorf("%s, which Builder uses every day, was deferred", n)
		}
	}
	t.Logf("Builder's inline tool definitions: %d bytes, now %d (%d deferred)", before, size(kept), before-size(kept))

	if strings.Count(turn.builderOnDemandIndexText(), "## Building tools you use less often") != 1 {
		t.Error("the index should render under one heading")
	}

	// Any other agent is untouched.
	other := *turn
	other.agent.ID = "a1"
	if got, _ := other.deferBuilderOnDemandTools(tools, names); len(got) != len(tools) {
		t.Error("another agent's catalog was changed")
	}
}
