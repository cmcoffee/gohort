package core

import (
	"context"
	"strings"
	"testing"
)

// An app calling this in a deployment with no agent-aware package must get an
// error naming the CAUSE. "request failed" sends an operator looking at the
// agent; "dispatch is unavailable" is a deployment fact they can act on.
func TestAskAgentWithoutADispatcher(t *testing.T) {
	RegisterAgentAsk(nil)
	_, err := AskAgent(context.Background(), "craig", "some-agent", "hello")
	if err == nil {
		t.Fatal("expected an error with no dispatcher registered")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("error should name the cause, got %q", err)
	}
}

// Both identifiers are required: a run with no owner would read somebody's
// store by accident, and one with no agent has nothing to run.
func TestAskAgentRequiresOwnerAndAgent(t *testing.T) {
	RegisterAgentAsk(func(ctx context.Context, owner, agentID, q string) (string, error) {
		return "should not be called", nil
	})
	defer RegisterAgentAsk(nil)
	for _, tc := range [][2]string{{"", "a"}, {"craig", ""}} {
		if _, err := AskAgent(context.Background(), tc[0], tc[1], "q"); err == nil {
			t.Errorf("owner=%q agent=%q should have been refused", tc[0], tc[1])
		}
	}
}

// The list degrades to empty rather than panicking, so a host renders an empty
// picker instead of failing to render at all.
func TestListAgentsForDegrades(t *testing.T) {
	RegisterAgentList(nil)
	if got := ListAgentsFor("craig"); got != nil {
		t.Errorf("want nil with no enumerator, got %v", got)
	}
	RegisterAgentList(func(owner string) []AgentChoice {
		return []AgentChoice{{ID: "a1", Name: "Enginseer"}}
	})
	defer RegisterAgentList(nil)
	if got := ListAgentsFor(""); got != nil {
		t.Errorf("an empty owner has no agents, got %v", got)
	}
	if got := ListAgentsFor("craig"); len(got) != 1 || got[0].ID != "a1" {
		t.Errorf("want the registered list, got %v", got)
	}
}
