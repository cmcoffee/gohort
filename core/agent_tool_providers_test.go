// The seam an app uses to put tools in an agent's hands. Its whole value is
// that the runtime asks a question it cannot answer itself, so the tests are
// about what happens when a provider misbehaves and about the catalog staying
// identical between turns.
package core

import (
	"strings"
	"testing"
)

func providerReturning(names ...string) AgentToolProvider {
	return func(_ *ToolSession, _, _ string) []AgentToolDef {
		var out []AgentToolDef
		for _, n := range names {
			out = append(out, AgentToolDef{Tool: Tool{Name: n}})
		}
		return out
	}
}

func toolNames(defs []AgentToolDef) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Tool.Name)
	}
	return out
}

func resetProviders(t *testing.T) {
	t.Helper()
	agentToolProviderMu.Lock()
	agentToolProviders = map[string]AgentToolProvider{}
	agentToolProviderMu.Unlock()
}

// The catalog is part of the prompt. A map walk would reshuffle it between
// turns and cost a prefix-cache miss for nothing.
func TestProviderOrderIsStable(t *testing.T) {
	resetProviders(t)
	RegisterAgentToolProvider("zulu", providerReturning("z1", "z2"))
	RegisterAgentToolProvider("alpha", providerReturning("a1"))
	RegisterAgentToolProvider("mike", providerReturning("m1"))

	want := []string{"a1", "m1", "z1", "z2"}
	for i := 0; i < 8; i++ {
		got := toolNames(AgentProvidedTools(nil, "craig", "wren"))
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("catalog order changed between calls: got %v, want %v", got, want)
		}
	}
}

// A provider that panics costs its own tools and nothing else — a turn must not
// die because an app broke while assembling a catalog.
func TestPanickingProviderIsContained(t *testing.T) {
	resetProviders(t)
	RegisterAgentToolProvider("good", providerReturning("survivor"))
	RegisterAgentToolProvider("bad", func(_ *ToolSession, _, _ string) []AgentToolDef {
		panic("provider blew up")
	})
	got := toolNames(AgentProvidedTools(nil, "craig", "wren"))
	if strings.Join(got, ",") != "survivor" {
		t.Errorf("the healthy provider's tools should survive, got %v", got)
	}
}

// No agent, nothing to ask about: a bare call must not reach providers at all.
func TestNoAgentMeansNoProviders(t *testing.T) {
	resetProviders(t)
	RegisterAgentToolProvider("any", func(_ *ToolSession, _, _ string) []AgentToolDef {
		t.Error("a provider must not be consulted without an agent")
		return nil
	})
	if got := AgentProvidedTools(nil, "craig", ""); got != nil {
		t.Errorf("expected nil, got %v", toolNames(got))
	}
}

// Contributing nothing is the common case and must return nil, so callers can
// append unconditionally.
func TestNoProvidersReturnsNil(t *testing.T) {
	resetProviders(t)
	if got := AgentProvidedTools(nil, "craig", "wren"); got != nil {
		t.Errorf("expected nil with nothing registered, got %v", toolNames(got))
	}
}

// A repeat registration replaces rather than silently keeping one of the two —
// which one survived would otherwise depend on map iteration.
func TestRepeatRegistrationReplaces(t *testing.T) {
	resetProviders(t)
	RegisterAgentToolProvider("dup", providerReturning("first"))
	RegisterAgentToolProvider("dup", providerReturning("second"))
	got := toolNames(AgentProvidedTools(nil, "craig", "wren"))
	if strings.Join(got, ",") != "second" {
		t.Errorf("the later registration should win, got %v", got)
	}
}

// Nil registrations are ignored rather than stored and crashed on later.
func TestNilAndUnnamedRegistrationsAreIgnored(t *testing.T) {
	resetProviders(t)
	RegisterAgentToolProvider("", providerReturning("x"))
	RegisterAgentToolProvider("named", nil)
	if got := AgentProvidedTools(nil, "craig", "wren"); got != nil {
		t.Errorf("neither should register, got %v", toolNames(got))
	}
}

// Which app contributed a tool, for a surface that has to say what a call
// reaches. An app-provided tool arrives in the same catalog looking exactly
// like a framework one, and the app is there because the capability belongs to
// a system somebody connected.
func TestEachToolIsAttributedToTheAppThatContributedIt(t *testing.T) {
	resetProviders(t)
	RegisterAgentToolProvider("zulu", providerReturning("z1", "z2"))
	RegisterAgentToolProvider("alpha", providerReturning("a1"))

	got := AgentProvidedToolOrigins(nil, "alice", "agent-1")
	for name, want := range map[string]string{"z1": "zulu", "z2": "zulu", "a1": "alpha"} {
		if got[name] != want {
			t.Errorf("%s attributed to %q, want %q", name, got[name], want)
		}
	}
	if len(got) != 3 {
		t.Errorf("attributed %d tools, want 3: %v", len(got), got)
	}
	// A tool nobody contributed has no entry, so a caller reading the map gets
	// "" and classifies it as framework rather than as somebody's system.
	if p, ok := got["read_file"]; ok {
		t.Errorf("a framework tool was attributed to %q", p)
	}

	// Same containment as the flat call: a provider that panics costs its own
	// attribution and nothing else.
	RegisterAgentToolProvider("bad", func(_ *ToolSession, _, _ string) []AgentToolDef {
		panic("provider blew up")
	})
	if got = AgentProvidedToolOrigins(nil, "alice", "agent-1"); got["a1"] != "alpha" {
		t.Error("one broken provider took the other attributions with it")
	}

	// No agent, nothing to attribute.
	if got = AgentProvidedToolOrigins(nil, "alice", ""); len(got) != 0 {
		t.Errorf("attributed tools with no agent: %v", got)
	}
}
