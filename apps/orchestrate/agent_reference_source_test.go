package orchestrate

import (
	"context"
	"strings"
	"testing"
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
