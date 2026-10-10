package orchestrate

import (
	"context"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/core/docs"
)

// Each tool call of a publish run is a step the person watching can read:
// what it does (its description's first sentence) and what to (a title, a
// page, a space), and why it failed if it did. Not the raw trace it used to
// be: a cloud id, "returned: {", and short lines of the document itself.
func TestPublishRunToolCallsAreSteps(t *testing.T) {
	var mu sync.Mutex
	var steps []string
	ctx := docs.WithPublishSteps(context.Background(), func(s string) { mu.Lock(); steps = append(steps, s); mu.Unlock() })
	td := publishStepReported(ctx, AgentToolDef{
		Tool: Tool{Name: "atlassian_getconfluencepage",
			Description: "Get a Confluence page. Returns its body in the requested format."},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			if args["fail"] == true {
				return "", errFake("page not found")
			}
			return "{\n  \"id\": \"2044428289\"\n}", nil
		},
	})
	cmd := "llama-bench -m model.gguf -p 32768 -n 0 -ub 2048 -fa 1"
	if _, err := td.Handler(context.Background(), map[string]any{
		"cloudId": "cebc598e-8c35-4780-abf5-1cb3efb6f390", "pageId": "2044428289", "body": cmd}); err != nil {
		t.Fatal(err)
	}
	td.Handler(context.Background(), map[string]any{"fail": true, "title": "Getting started"})
	got := strings.Join(steps, "\n")
	for _, want := range []string{"Get a Confluence page \u00b7 page 2044428289", "Get a Confluence page \u00b7 \u201cGetting started\u201d", "failed: page not found"} {
		if !strings.Contains(got, want) {
			t.Errorf("steps lack %q:\n%s", want, got)
		}
	}
	for _, noise := range []string{"cebc598e", "returned:", "llama-bench", "atlassian_getconfluencepage"} {
		if strings.Contains(got, noise) {
			t.Errorf("a step carries %q, which is plumbing or the document itself:\n%s", noise, got)
		}
	}
}

// With no description, the tool's name stands in; a method and a URL read as
// the method and the path.
func TestPublishStepFallbacks(t *testing.T) {
	if got := publishStepLabel(Tool{Name: "fetch_url_blog"}); got != "fetch_url_blog" {
		t.Errorf("label fallback = %q", got)
	}
	if got := publishStepTarget(map[string]any{"method": "PUT", "url": "https://wiki.example/api/v2/pages/42?x=1"}); got != "PUT /api/v2/pages/42" {
		t.Errorf("target = %q", got)
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }
