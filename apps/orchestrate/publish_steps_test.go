package orchestrate

import (
	"context"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

// Each tool call of a publish run is a step the person watching can read:
// what was called with what (a long value said by its size), then how it
// came back, failures included.
func TestPublishRunToolCallsAreSteps(t *testing.T) {
	var mu sync.Mutex
	var steps []string
	ctx := docs.WithPublishSteps(context.Background(), func(s string) { mu.Lock(); steps = append(steps, s); mu.Unlock() })
	td := publishStepReported(ctx, AgentToolDef{
		Tool: Tool{Name: "atlassian_updatePage"},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			if args["fail"] == true {
				return "", errFake("page not found")
			}
			return "Updated page 42\nmore detail", nil
		},
	})
	body := strings.Repeat("x", 500)
	if _, err := td.Handler(context.Background(), map[string]any{"pageId": "42", "body": body}); err != nil {
		t.Fatal(err)
	}
	td.Handler(context.Background(), map[string]any{"fail": true})
	got := strings.Join(steps, "\n")
	for _, want := range []string{"atlassian_updatePage (body=(500 characters), pageId=42)", "returned: Updated page 42", "failed: page not found"} {
		if !strings.Contains(got, want) {
			t.Errorf("steps lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "more detail") || strings.Contains(got, body) {
		t.Errorf("a step carried more than one line, or the whole body:\n%s", got)
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }
