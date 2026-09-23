package orchestrate

// One target-side answer, asked by every route that reaches an agent.
//
// There were three routes and one of them asked. Authoring a machine or a
// pipeline that named a locked agent was a way around the lock, and Builder
// can author both.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestATargetAnswersTheSameWhicheverRouteAsked(t *testing.T) {
	pinRootDB(t)
	caller := AgentRecord{ID: "caller", Name: "Caller", Owner: "alice"}

	// Open by default: an agent that has answered nothing accepts anyone.
	open := AgentRecord{ID: "open", Name: "Open", Owner: "alice"}
	if refusal := targetAcceptsDispatch("alice", caller, open); refusal != "" {
		t.Errorf("an agent with no inbound rule refused a dispatch: %s", refusal)
	}

	// Accepts nothing.
	shut := AgentRecord{ID: "shut", Name: "Shut", Owner: "alice", InboundMode: inboundNone}
	refusal := targetAcceptsDispatch("alice", caller, shut)
	if refusal == "" {
		t.Fatal("an agent that accepts no dispatches took one")
	}
	if !strings.Contains(refusal, "Shut") {
		t.Errorf("the refusal does not name who refused: %s", refusal)
	}

	// Accepts a named list.
	picky := AgentRecord{ID: "picky", Name: "Picky", Owner: "alice",
		InboundMode: inboundOnly, AllowedCallers: []string{"someone_else"}}
	if targetAcceptsDispatch("alice", caller, picky) == "" {
		t.Error("an agent off the caller list was reached anyway")
	}
	picky.AllowedCallers = append(picky.AllowedCallers, "caller")
	if refusal := targetAcceptsDispatch("alice", caller, picky); refusal != "" {
		t.Errorf("an agent ON the caller list was refused: %s", refusal)
	}

	// A sub-agent's parent is exempt: ownership IS the link, and a rule that
	// locked a parent out of its own child would strand the child.
	child := AgentRecord{ID: "child", Name: "Child", Owner: "alice",
		OwnedBy: "caller", InboundMode: inboundNone}
	if refusal := targetAcceptsDispatch("alice", caller, child); refusal != "" {
		t.Errorf("a parent was locked out of its own sub-agent: %s", refusal)
	}

	// The user's Block is about the target, so it holds whatever route reached
	// it. This one governed only the surfaces that thought to ask.
	SetDelegationPolicy(RootDB, "alice", caller.ID, open.Name, PolicyBlock)
	refusal = targetAcceptsDispatch("alice", caller, open)
	if refusal == "" {
		t.Fatal("a BLOCKED target was reachable")
	}
	if !strings.Contains(refusal, "BLOCKED") || !strings.Contains(refusal, "only the user") {
		t.Errorf("the refusal does not say who can change it: %s", refusal)
	}
}

// A run with no calling agent is not gated. Inbound mode answers "which agents
// may call me"; a machine started from its own page, or a pipeline the owner
// launched, has a person on the other end, and a rule about agents must not
// quietly become a rule about the owner reaching their own fleet.
func TestTheOwnerIsNotAnAgentCaller(t *testing.T) {
	pinRootDB(t)
	shut := AgentRecord{ID: "shut", Name: "Shut", Owner: "alice", InboundMode: inboundNone}
	if refusal := targetAcceptsDispatch("alice", AgentRecord{}, shut); refusal != "" {
		t.Errorf("the owner was refused their own agent: %s", refusal)
	}
}

// The machine route reports the drop rather than swallowing it, and runs the
// phase inline - the safe direction, since the calling agent doing the work
// itself is strictly less reach than handing it to somebody else.
func TestTheRefusedMachineStepLeavesABreadcrumb(t *testing.T) {
	src := mustReadFile(t, "machine_host.go")
	if !strings.Contains(src, `h.diag("phase_delegate_refused"`) {
		t.Error("a refused delegation leaves no trace, so it cannot be told from a machine that never delegated")
	}
	if !strings.Contains(src, "return base(ctx, ph, prompt)") {
		t.Error("the refused step no longer falls back to running inline")
	}
}
