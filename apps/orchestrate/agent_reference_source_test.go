package orchestrate

import (
	"context"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An agent asked NOTHING has nothing to say, and asked "the article topic"
// answers about the topic. So an empty query returns empty rather than
// dispatching a full agent run that can only produce something generic.
func TestAgentSourceIgnoresAnEmptyQuery(t *testing.T) {
	s := agentReferenceSource{app: nil}
	for _, q := range []string{"", "   ", "\n"} {
		if got := s.Fetch(context.Background(), "craig", "a1", q); got != "" {
			t.Errorf("query %q should not dispatch, got %q", q, got)
		}
	}
}

// The registry routes a Fetch back by kind, and a consumer groups the picker by
// label — both are part of the wire contract with apps that never import this
// package, so neither may drift casually.
func TestAgentSourceIdentity(t *testing.T) {
	s := agentReferenceSource{}
	if s.Kind() != "agent" {
		t.Errorf("kind = %q, want \"agent\" — stored selections name it", s.Kind())
	}
	if s.Label() == "" {
		t.Error("a source needs a group label for the picker")
	}
}

// Nothing is offered for a caller with no user or a source with no app, so a
// misconfigured registration degrades to an empty group rather than a panic in
// every consumer's picker.
func TestAgentSourceDegradesWithoutAnApp(t *testing.T) {
	s := agentReferenceSource{app: nil}
	if got := s.List("craig"); got != nil {
		t.Errorf("want nil, got %v", got)
	}
	if got := s.ItemTools("craig", "a1"); got != nil {
		t.Errorf("want nil tools, got %v", got)
	}
}

// The consultation is framed as a QUESTION, not a task: without that an agent
// reads a bare question as a request to go do something, and a drafting surface
// waiting on a sentence of schema does not want a run that files a deliverable.
func TestConsultationIsFramedAsAQuestion(t *testing.T) {
	src := readSourceFile(t, "agent_reference_source.go")
	for _, want := range []string{"take no action", "no deliverable", "say which part you don't have"} {
		if !strings.Contains(src, want) {
			t.Errorf("the consultation prompt should say %q", want)
		}
	}
}

// A consultation declines every confirmation: a consulted agent that reaches
// for a tool needing approval does not get it run, and still answers. The
// path it used approved everything, on the strength of a question.
func TestAConsultationApprovesNoActions(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	fake := &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "drop_table", Args: map[string]any{}}}},
		{Content: "The region is in invoices.region_code.", Repeat: true},
	}}
	app.LLM, app.LeadLLM = fake, fake
	saveAgent(udb, AgentRecord{ID: "dba", Owner: user, Name: "DBA", OrchestratorPrompt: "You know the schema."})
	ran := false
	dangerous := AgentToolDef{
		Tool:         Tool{Name: "drop_table", Description: "Drop a table.", Caps: []Capability{CapWrite}},
		NeedsConfirm: true,
		Handler: func(context.Context, map[string]any) (string, error) {
			ran = true
			return "dropped", nil
		},
	}
	res, err := app.runAgentSyncAppTools(context.Background(), user, user, "dba", "Which column holds the region?",
		declineConsultConfirm, []AgentToolDef{dangerous}, nil, "reference")
	if err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Error("a consultation ran a tool that needs approval")
	}
	if !strings.Contains(res.Text, "region_code") {
		t.Errorf("the consulted agent did not still answer: %q", res.Text)
	}
}

// Fetch is the consultation, and it runs with confirmations declined rather
// than through RunAgentSync, which approves them all.
func TestFetchConsultsWithConfirmationsDeclined(t *testing.T) {
	src, err := os.ReadFile("agent_reference_source.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "func (s agentReferenceSource) Fetch(")
	j := strings.Index(body[i:], "\n}\n")
	fetch := body[i : i+j]
	if !strings.Contains(fetch, "runAgentSyncConfirm(ctx, user, user, itemID, ask, declineConsultConfirm,") || strings.Contains(fetch, "RunAgentSync(") {
		t.Error("Fetch must consult with confirmations declined, not through RunAgentSync")
	}
}
