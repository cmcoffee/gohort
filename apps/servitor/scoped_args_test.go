package servitor

// The dispatch has to hand the CALLING agent to the path-scope check.
// Passing only the user is what let an agent nobody had linked a store to
// run a command against it — the tool's own gate (the appliance
// connection) is a different grant from the folder's.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestScopedArgsCarryTheCallingAgent(t *testing.T) {
	prevHolds := AgentHoldsReference
	t.Cleanup(func() { AgentHoldsReference = prevHolds })
	AgentHoldsReference = func(user, agentID, kind, itemID string) bool { return agentID == "linked" }

	var sawUser, sawName, sawValue string
	RegisterPathScope("testscope", PathScope{
		Resolve: func(u, n, v string) (string, error) {
			sawUser, sawName, sawValue = u, n, v
			return "/abs/" + v, nil
		},
	})
	// The gate only applies to kinds that are ALSO attachable sources —
	// otherwise there is nothing to attach and requiring it would break a
	// scope with no picker behind it.
	RegisterReferenceSource(scopeTestSource{})

	tool := ApplianceTool{
		Name:   "parse_bundle",
		Params: map[string]ToolParam{"dir": {Type: "string", PathScope: "testscope:bundles"}},
	}
	args := map[string]any{"dir": "scan-1", "other": "left alone"}

	out, err := resolveScopedArgs("alice", "linked", tool, args)
	if err != nil {
		t.Fatalf("linked agent refused: %v", err)
	}
	if out["dir"] != "/abs/scan-1" {
		t.Errorf("scoped arg not substituted: %v", out["dir"])
	}
	if out["other"] != "left alone" {
		t.Errorf("unscoped arg was touched: %v", out["other"])
	}
	// The caller's map is what gets logged and echoed back; rewriting it
	// in place would make the record disagree with what the model asked.
	if args["dir"] != "scan-1" {
		t.Errorf("the caller's map was mutated: %v", args["dir"])
	}
	if sawUser != "alice" || sawName != "bundles" || sawValue != "scan-1" {
		t.Errorf("resolver got (%q,%q,%q)", sawUser, sawName, sawValue)
	}

	if _, err := resolveScopedArgs("alice", "unlinked", tool, args); err == nil {
		t.Fatal("an unlinked agent resolved a scoped path")
	} else if !strings.Contains(err.Error(), "dir") {
		t.Errorf("the refusal should name the parameter, got %q", err)
	}
}

type scopeTestSource struct{}

func (scopeTestSource) Kind() string                                         { return "testscope" }
func (scopeTestSource) Label() string                                        { return "Test scope" }
func (scopeTestSource) List(string) []ReferenceItem                          { return nil }
func (scopeTestSource) Fetch(context.Context, string, string, string) string { return "" }
