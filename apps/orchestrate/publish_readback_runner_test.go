package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

func fakeIntegrationTool(name string, out string, calls *[]string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{Name: name},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			*calls = append(*calls, name)
			return out, nil
		},
	}
}

// A publish run that updates a page others may have edited reads it first:
// its writes are refused until the page has been read and checked, a clear
// check lets them through, and a stopped check lets nothing more through.
func TestAGuardedPublishRunReadsBeforeItWrites(t *testing.T) {
	page := `{"id":"1","title":"Guide","body":"<h2>Setup</h2><p>Run the installer, then reboot the host.</p>"}`
	for _, tc := range []struct {
		name  string
		stop  error
		write bool
	}{
		{"unchanged", nil, true},
		{"edited there", errors.New("somebody changed it"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen []string
			ctx := docs.WithPublishSteps(context.Background(), func(string) {})
			ctx = docs.WithPublishGuard(ctx, func(cur docs.PublishedCopy) error {
				seen = append(seen, cur.Body)
				return tc.stop
			})
			var calls []string
			res, gate := &publishResults{}, &publishWriteGate{}
			get := gate.wrap(res.wrap(fakeIntegrationTool("atlassian_getconfluencepage", page, &calls)))
			put := gate.wrap(res.wrap(fakeIntegrationTool("atlassian_updateconfluencepage", "{}", &calls)))

			if _, err := put.Handler(ctx, nil); err == nil {
				t.Fatal("a write went through before the page was read")
			}
			out, err := get.Handler(ctx, nil)
			if err != nil || !strings.HasPrefix(out, "(result 1)\n") {
				t.Fatalf("the read should be numbered: %q %v", out, err)
			}
			reply, err := gate.check(ctx, res, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(seen) != 1 || !strings.HasPrefix(seen[0], "<h2>Setup</h2>") {
				t.Fatalf("the check should see the page body: %q", seen)
			}
			_, err = put.Handler(ctx, nil)
			if wrote := err == nil; wrote != tc.write {
				t.Fatalf("write went through = %v, want %v (check said %q)", wrote, tc.write, reply)
			}
			if tc.stop != nil && gate.stoppedErr() != tc.stop {
				t.Errorf("the run should end with the producer's reason, got %v", gate.stoppedErr())
			}
		})
	}
}

// What counts as a read: an API call by its method, an MCP tool by a name
// that gets something and changes nothing.
func TestPublishToolReads(t *testing.T) {
	api := AgentToolDef{Tool: Tool{Name: "fetch_url_wiki"}}
	for _, tc := range []struct {
		td   AgentToolDef
		args map[string]any
		want bool
	}{
		{api, map[string]any{}, true},
		{api, map[string]any{"method": "get"}, true},
		{api, map[string]any{"method": "PUT"}, false},
		{AgentToolDef{Tool: Tool{Name: "atlassian_getconfluencepage"}}, nil, true},
		{AgentToolDef{Tool: Tool{Name: "atlassian_searchconfluenceusingcql"}}, nil, true},
		{AgentToolDef{Tool: Tool{Name: "atlassian_updateconfluencepage"}}, nil, false},
		{AgentToolDef{Tool: Tool{Name: "atlassian_createconfluencepage"}}, nil, false},
		{AgentToolDef{Tool: Tool{Name: "atlassian_addcommenttopage"}}, nil, false},
	} {
		if got := publishToolReads(tc.td, tc.args); got != tc.want {
			t.Errorf("%s %v: reads = %v, want %v", tc.td.Tool.Name, tc.args, got, tc.want)
		}
	}
}
