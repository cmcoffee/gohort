package orchestrate

// An app's memory (docs/design/app-history.md, stage 2, piece E). What an
// app's agents learn about the people using it belongs to the app, not to
// one agent: a memory of the app's own, one per person, kept in that
// person's store, shared by every agent of the app, deleted with the app and
// never exported. Copying or replacing one of its agents loses nothing.
//
// The app decides whether it keeps one (AppSpec.Memory, part of its
// blueprint); each person using it may turn it off for themselves, which
// stops new memories of them and leaves what is kept until they forget it,
// all from the app's Settings page (the ⚙ in its bar). What the app's agents
// know from the start (AppSpec.StartingFacts) is given to each of them every
// turn, apart from what they learn.
//
// An app not saved since this existed (Memory "") leaves its agents the
// memory of their own they always had, until its first change.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// agentApp is the app t's agent is part of, when it is one of an app's.
func (t *chatTurn) agentApp() (AppSpec, bool) {
	slug := strings.TrimSpace(t.agent.OwningApp)
	if slug == "" {
		return AppSpec{}, false
	}
	owner := firstNonEmptyStr(t.agent.Owner, t.user)
	return LoadAppSpec(owner, slug)
}

// factsNS is the memory namespace this turn's facts, recall and graph read
// and write: the agent's own, or for an agent of an app the app's (for the
// person in the conversation); "" when its app keeps no memory.
func (t *chatTurn) factsNS() string {
	if t.memNSSet {
		return t.memNS
	}
	t.memNS, t.memNSSet = factsNamespace(t.agent.ID), true
	if spec, ok := t.agentApp(); ok {
		switch spec.Memory {
		case "on":
			t.memNS = AppMemoryNamespace(spec.Owner, spec.Slug)
		case "off":
			t.memNS = ""
		}
	}
	return t.memNS
}

// appMemoryWriteRefusal is why this turn may not add to what is remembered,
// "" when it may: the agent's app keeps no memory, or the person in the
// conversation asked it not to remember them.
func (t *chatTurn) appMemoryWriteRefusal() string {
	spec, ok := t.agentApp()
	if !ok {
		return ""
	}
	switch {
	case spec.Memory == "off":
		return fmt.Sprintf("the app %q keeps no memory of the people using it: say what you would have remembered instead of saving it", chFirst(spec.Name, spec.Slug))
	case spec.Memory == "on" && AppMemoryOptedOut(t.udb, spec.Owner, spec.Slug):
		return fmt.Sprintf("this person asked the app %q not to remember them: nothing new is saved about them, so do not save it, and do not ask them to undo their choice", chFirst(spec.Name, spec.Slug))
	}
	return ""
}

// startingFacts are what the agent's app says its agents know from the
// start, as facts given before the ones learned.
func (t *chatTurn) startingFacts() []MemoryFact {
	spec, ok := t.agentApp()
	if !ok || len(spec.StartingFacts) == 0 {
		return nil
	}
	out := make([]MemoryFact, 0, len(spec.StartingFacts))
	for i, f := range spec.StartingFacts {
		mf := MemoryFact{Namespace: "app-start:" + spec.Owner + "/" + spec.Slug, ID: fmt.Sprintf("start-%d", i+1), Note: f}
		mf.Source = MemSourceUserStated
		out = append(out, mf)
	}
	return out
}
