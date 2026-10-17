package orchestrate

// What Builder may run: only what it is building.
//
// Delegation is one way. Agents hand building to Builder; Builder does not hand
// work to the fleet. It used to, through the run its worker steps were given
// for smoke tests: a step asked to inspect a file store's commands queued a
// recon task with another agent instead of reading them, and the work went on
// in that agent's thread, under its tools and limits, out of the person's
// sight. Two agents that hand each other work can also cycle.
//
// Running what it built is still how Builder checks its work, so it keeps
// that: an agent, pipeline or machine it created or changed in this
// conversation, and the agent and pipeline of an app it created or changed.
// "Created or changed" is the conversation's own record of that, the change
// approvals (change_approval.go): what it made here, and what the owner let it
// change. The rest of the fleet stays readable (list, get), for wiring and
// copying, and is refused to run.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// builderRunRefusal is "" when this turn may run the target, or why not.
// kind is "agent", "pipeline" or "machine"; id and name identify it. Only
// Builder (and its worker steps, which run as Builder) is limited.
func (t *chatTurn) builderRunRefusal(kind, id, name string) string {
	if t == nil || !isBuilderAgent(t.agent.ID) {
		return ""
	}
	if t.builderBuilt(kind, id, name) {
		return ""
	}
	return fmt.Sprintf("NOT RUN: you run only what you are building in this conversation (an agent, pipeline or machine you created or changed here, or the agent and pipeline of an app you created or changed), to test it. The %s %q is not one of them, and handing work to the fleet is not your job: do it yourself with your own tools (research, mcp, the stores, your authoring tools). You can still read it with agents(action=\"get\").", kind, chFirst(name, id))
}

// builderBuilt reports whether this conversation created or changed the
// target, directly or as part of an app.
func (t *chatTurn) builderBuilt(kind, id, name string) bool {
	set := changeApprovalsFor(t.user, t.chatSessionID())
	refs := []string{name, id}
	if kind == "agent" {
		// An agent is recorded by id (changeKey): a name could be another
		// agent's.
		refs = []string{id}
	}
	for _, n := range refs {
		if strings.TrimSpace(n) != "" && set.covers(changeKey(kind, n)) {
			return true
		}
	}
	if kind != "agent" && kind != "pipeline" {
		return false
	}
	for _, slug := range set.names("app") {
		spec, ok := LoadAppSpec(t.user, slug)
		if !ok {
			continue
		}
		bound := spec.AgentID
		if kind == "pipeline" {
			bound = spec.PipelineID
		}
		if bound != "" && (bound == id || bound == name) {
			return true
		}
	}
	return false
}
