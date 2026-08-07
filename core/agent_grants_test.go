// The visibility half of per-agent privileges. Its value is that a row reading
// "none" is as informative as one listing three machines — so the tests are
// about what appears when nothing was granted, and about never inventing a
// grant that was not.
package core

import (
	"strings"
	"testing"
)

func resetGrantors(t *testing.T) {
	t.Helper()
	agentGrantorMu.Lock()
	agentGrantors = map[string]AgentGrantor{}
	agentGrantorMu.Unlock()
}

func grantorOf(name string, grants ...AgentGrant) AgentGrantor {
	return AgentGrantor{Name: name, Label: name + " things", ManageURL: "/" + name,
		Granted: func(_, _ string) []AgentGrant { return grants }}
}

// An app granting nothing still gets a row. Omitting empties would make a
// capability invisible until the moment it mattered.
func TestEveryGrantorAppearsIncludingEmptyOnes(t *testing.T) {
	resetGrantors(t)
	RegisterAgentGrantor(grantorOf("servitor", AgentGrant{Label: "Lab Box", Detail: "nothing without asking"}))
	RegisterAgentGrantor(grantorOf("calendars"))

	got := AgentGrantSummaries("craig", "wren")
	if len(got) != 2 {
		t.Fatalf("both apps should report, got %d", len(got))
	}
	// Stable order by name, so the editor does not reshuffle between renders.
	if got[0].Name != "calendars" || got[1].Name != "servitor" {
		t.Errorf("summaries should be name-ordered, got %s then %s", got[0].Name, got[1].Name)
	}
	if got[0].Text != "none" {
		t.Errorf("an app granting nothing should say so, got %q", got[0].Text)
	}
	if got[1].Text != "Lab Box" {
		t.Errorf("a single grant should read as itself, got %q", got[1].Text)
	}
}

// The row's sentence is built once here so every caller renders the empty and
// the many cases identically.
func TestGrantTextReadsAsASentence(t *testing.T) {
	g := func(n int) []AgentGrant {
		out := make([]AgentGrant, n)
		for i := range out {
			out[i] = AgentGrant{Label: string(rune('A' + i))}
		}
		return out
	}
	for n, want := range map[int]string{0: "none", 1: "A", 2: "A and B", 4: "A, B and 2 more"} {
		if got := grantText(g(n)); got != want {
			t.Errorf("%d grant(s) → %q, want %q", n, got, want)
		}
	}
}

// A grantor that panics reports NOTHING, never everything. A permissions
// display that guesses is worse than one that fails.
func TestPanickingGrantorReportsNone(t *testing.T) {
	resetGrantors(t)
	RegisterAgentGrantor(AgentGrantor{Name: "broken", Granted: func(_, _ string) []AgentGrant {
		panic("listing blew up")
	}})
	RegisterAgentGrantor(grantorOf("healthy", AgentGrant{Label: "Fine"}))

	got := AgentGrantSummaries("craig", "wren")
	if len(got) != 2 {
		t.Fatalf("both should still render, got %d", len(got))
	}
	for _, s := range got {
		if s.Name == "broken" && s.Text != "none" {
			t.Errorf("a broken grantor must report none, got %q", s.Text)
		}
		if s.Name == "healthy" && s.Text != "Fine" {
			t.Errorf("the healthy grantor should be unaffected, got %q", s.Text)
		}
	}
}

// No agent, nothing to ask about.
func TestNoAgentMeansNoSummaries(t *testing.T) {
	resetGrantors(t)
	RegisterAgentGrantor(AgentGrantor{Name: "any", Granted: func(_, _ string) []AgentGrant {
		t.Error("a grantor must not be asked about an empty agent")
		return nil
	}})
	if got := AgentGrantSummaries("craig", ""); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

// A grantor with no Granted function cannot report and must not register — it
// would render an empty row forever with nothing behind it.
func TestIncompleteGrantorsAreIgnored(t *testing.T) {
	resetGrantors(t)
	RegisterAgentGrantor(AgentGrantor{Name: "no_func"})
	RegisterAgentGrantor(AgentGrantor{Granted: func(_, _ string) []AgentGrant { return nil }})
	if got := AgentGrantSummaries("craig", "wren"); len(got) != 0 {
		t.Errorf("neither should register, got %v", got)
	}
}

// The label names the RESOURCE, and falls back to the app id rather than
// rendering blank.
func TestLabelFallsBackToTheName(t *testing.T) {
	resetGrantors(t)
	RegisterAgentGrantor(AgentGrantor{Name: "servitor", Granted: func(_, _ string) []AgentGrant { return nil }})
	got := AgentGrantSummaries("craig", "wren")
	if len(got) != 1 || got[0].Label != "servitor" {
		t.Errorf("an unlabelled grantor should fall back to its name, got %+v", got)
	}
	if !strings.Contains(got[0].Text, "none") {
		t.Errorf("and still report its state, got %q", got[0].Text)
	}
}
