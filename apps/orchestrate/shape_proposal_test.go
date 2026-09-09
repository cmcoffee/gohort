package orchestrate

import (
	"strings"
	"testing"
)

// The opening proposal is the first thing a person sees when they ask for an
// agent, so it has to say what the thing IS before asking them anything.
func TestProposalOpensWithTheAgentItWouldBuild(t *testing.T) {
	p, ok := proposeAgent("an agent that answers questions from our handbook")
	if !ok {
		t.Fatal("no proposal for a request that plainly describes a knowledge base")
	}
	if p.Shape != "knowledge_base" || p.Name == "" || p.Summary == "" {
		t.Fatalf("proposal = %+v", p)
	}
	if p.NeedsComposing {
		t.Error("a shape that ships an agent was reported as needing composing")
	}
	if p.Draft.ShapeID != "knowledge_base" {
		t.Errorf("the draft follows %q, so improvements would never reach it", p.Draft.ShapeID)
	}
	// Unsaved, and not the framework's own record: a proposal is something to
	// react to, and creating on sight leaves a fleet full of agents nobody
	// agreed to.
	if p.Draft.ID != "" || p.Draft.Owner != "" {
		t.Errorf("the draft is already somebody's record: id=%q owner=%q", p.Draft.ID, p.Draft.Owner)
	}
	if p.Draft.Hidden || p.Draft.Exposed || p.Draft.Fleet {
		t.Errorf("the draft inherited a seed's posture: hidden=%v exposed=%v fleet=%v",
			p.Draft.Hidden, p.Draft.Exposed, p.Draft.Fleet)
	}

	// The points are what a person checks before agreeing: what it refuses,
	// what it can reach, what it remembers.
	joined := strings.Join(p.Points, " ")
	if !strings.Contains(joined, "Stays offline") {
		t.Errorf("the offline contract, the whole point of this shape, is not in the proposal: %v", p.Points)
	}
	if !strings.Contains(joined, "Answer only from the attached corpus") {
		t.Errorf("the shape's rules are not shown: %v", p.Points)
	}
	if strings.Contains(joined, "ask_user") {
		t.Errorf("conversation mechanics were listed as reach: %v", p.Points)
	}
	if len(p.Asks) == 0 {
		t.Error("nothing to ask, so the dialog would stop at the proposal")
	}
}

// A shape whose subject is not known yet has no draft, and dropping the match
// would throw away the useful half: which kind of agent this is, and what it
// needs to be told.
func TestAShapeWithNoAgentStillOpensTheConversation(t *testing.T) {
	p, ok := proposeAgent("watch our status page every 5 minutes")
	if !ok {
		t.Fatal("a watcher request produced nothing at all")
	}
	if !p.NeedsComposing {
		t.Error("a shape that ships no agent was presented as a draft")
	}
	if p.Name == "" || p.Summary == "" {
		t.Errorf("nothing to show the user: name=%q summary=%q", p.Name, p.Summary)
	}
	if len(p.Points) != 0 {
		t.Errorf("points were invented for an agent that does not exist yet: %v", p.Points)
	}
	if len(p.Asks) == 0 {
		t.Error("no questions, so there is no way to compose one either")
	}
}

// Declining is a real answer, and it has to survive all the way out: opening
// with the wrong agent costs more than opening with a question.
func TestProposalDeclinesWhenNothingFits(t *testing.T) {
	for _, req := range []string{"build me an app for tracking invoices", "an agent", ""} {
		if p, ok := proposeAgent(req); ok {
			t.Errorf("%q proposed %q", req, p.Shape)
		}
	}
}

// describeAgentPlainly is the one description of an agent shown to people, so
// it is derived rather than written per shape: a hand-written blurb is a
// second description of the same agent, and it goes stale the first time a
// tool is added.
func TestDescribeAgentPlainly(t *testing.T) {
	got := describeAgentPlainly(AgentRecord{
		AllowedTools: []string{"web_search", "ask_user", "fetch_url"},
		Rules:        "Never guess a price.\nAlways name the source.",
		GapCheck:     true,
		Cortex:       true,
	})
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "Reaches web_search and fetch_url.") {
		t.Errorf("reach line = %q", joined)
	}
	if !strings.Contains(joined, "Never guess a price.") || !strings.Contains(joined, "Always name the source.") {
		t.Errorf("both rules should appear, one per line: %q", joined)
	}
	if !strings.Contains(joined, "covers the question") || !strings.Contains(joined, "ongoing thread") {
		t.Errorf("gap check and cortex should be stated: %q", joined)
	}

	// The empty allowlist is the default pool, not "no tools". Getting these
	// two backwards would tell somebody their agent is offline when it is not.
	if got := describeAgentPlainly(AgentRecord{}); !strings.Contains(strings.Join(got, " "), "standard set of tools") {
		t.Errorf("an empty allowlist described as %v", got)
	}
	if got := describeAgentPlainly(AgentRecord{AllowedTools: []string{noToolsSentinel}}); !strings.Contains(strings.Join(got, " "), "no tools") {
		t.Errorf("the no-tools sentinel described as %v", got)
	}
	// And an offline agent says so once, not twice.
	priv := describeAgentPlainly(AgentRecord{ForcePrivate: true, AllowedTools: []string{"ask_user"}})
	if n := strings.Count(strings.Join(priv, " "), "Reaches"); n != 0 {
		t.Errorf("an offline agent also reported its reach: %v", priv)
	}
}

func TestShapeTitle(t *testing.T) {
	if got := shapeTitle("scheduled_watcher"); got != "Scheduled watcher" {
		t.Errorf("shapeTitle = %q", got)
	}
}
