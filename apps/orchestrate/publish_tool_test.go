package orchestrate

import (
	"context"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/docs"
)

type toolTestDest struct{ got docs.PublishRequest }

func (d *toolTestDest) Kind() string                    { return "tooltest:" }
func (d *toolTestDest) Label() string                   { return "Tool test" }
func (d *toolTestDest) Available(string) (bool, string) { return true, "" }
func (d *toolTestDest) Targets(context.Context, string) ([]docs.PublishTarget, error) {
	return []docs.PublishTarget{{ID: "blog", Title: "Team blog"}, {ID: "wiki", Title: "Wiki"}}, nil
}
func (d *toolTestDest) Publish(_ context.Context, _ string, req docs.PublishRequest) (docs.PublishResult, error) {
	d.got = req
	return docs.PublishResult{URL: "https://blog.example/p/1"}, nil
}
func (d *toolTestDest) TargetSpecs(context.Context, string) []docs.PublishTargetSpec {
	return []docs.PublishTargetSpec{
		{Kind: "tooltest:blog", Target: docs.PublishTarget{ID: "blog", Title: "Team blog"}, Agents: []string{"writer"},
			Fields: []docs.PublishField{{Name: "category", Options: []string{"News", "Guides"}, Required: true}}},
		{Kind: "tooltest:wiki", Target: docs.PublishTarget{ID: "wiki", Title: "Wiki"}, Agents: []string{"someone-else"}},
	}
}

// An agent switched on for a target gets a publish tool naming only its
// targets and their questions; one switched on for none gets no tool; the
// call publishes the text with its answers, and a missing required answer
// is named.
func TestThePublishToolIsOnlyForAllowedAgents(t *testing.T) {
	d := &toolTestDest{}
	docs.RegisterPublishDestination(d)
	if _, ok := (&chatTurn{user: "u", agent: AgentRecord{ID: "nobody"}}).publishToolDef(); ok {
		t.Error("an agent switched on for no target gets no tool")
	}
	def, ok := (&chatTurn{user: "u", agent: AgentRecord{ID: "writer"}}).publishToolDef()
	if !ok || !strings.Contains(def.Tool.Description, "Team blog") || strings.Contains(def.Tool.Description, "Wiki") || !strings.Contains(def.Tool.Description, "category [News | Guides] required") {
		t.Fatalf("the tool names only the agent's targets and their questions:\n%s", def.Tool.Description)
	}
	if _, err := def.Handler(context.Background(), map[string]any{"target": "blog", "title": "T", "content": "Body"}); err == nil || !strings.Contains(err.Error(), "category") {
		t.Errorf("a required answer left out is named: %v", err)
	}
	out, err := def.Handler(context.Background(), map[string]any{"target": "blog", "title": "Launch", "content": "# Launch", "answers": map[string]any{"category": "News"}})
	if err != nil || !strings.Contains(out, "https://blog.example/p/1") || d.got.Answers["category"] != "News" || d.got.Doc.Markdown != "# Launch" {
		t.Errorf("publishes the text with its answers: %v %q %+v", err, out, d.got)
	}
}
