// Linked-repo tools: the shape contract that keeps the worker allow-list and
// the worker's habits stable — same tool NAMES whether a system links one repo
// or several, with the multi-repo set requiring a `repo` argument.
package servitor

import (
	"strings"
	"testing"
)

func TestLinkedRepoToolsKeepCanonicalNames(t *testing.T) {
	if got := linkedRepoTools(nil); got != nil {
		t.Errorf("no linked repos should contribute no tools, got %d", len(got))
	}
	for _, repos := range [][]linkedRepo{
		{{Owner: "u", ID: "r1", Name: "api"}},
		{{Owner: "u", ID: "r1", Name: "api"}, {Owner: "u", ID: "r2", Name: "web"}},
	} {
		tools := linkedRepoTools(repos)
		names := map[string]bool{}
		for _, td := range tools {
			names[td.Tool.Name] = true
			if !servitorWorkerToolAllowList[td.Tool.Name] {
				t.Errorf("%d repos: tool %q is not on the worker allow-list", len(repos), td.Tool.Name)
			}
		}
		for _, want := range []string{"search_code", "read_file", "list_dir"} {
			if !names[want] {
				t.Errorf("%d repos: missing tool %q", len(repos), want)
			}
		}
	}
}

func TestLinkedRepoToolsMultiRequireRepoArg(t *testing.T) {
	tools := linkedRepoTools([]linkedRepo{
		{Owner: "u", ID: "r1", Name: "api"}, {Owner: "u", ID: "r2", Name: "web"},
	})
	for _, td := range tools {
		if td.Tool.Name == "search_code" {
			continue // searches every linked repo; no disambiguation needed
		}
		found := false
		for _, req := range td.Tool.Required {
			if req == "repo" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s must require `repo` when several are linked", td.Tool.Name)
		}
		// An unknown repo must fail naming the valid choices, not guess.
		_, err := td.Handler(map[string]any{"repo": "nope", "path": "x"})
		if err == nil || !strings.Contains(err.Error(), "api") {
			t.Errorf("%s with unknown repo: want error naming the linked repos, got %v", td.Tool.Name, err)
		}
	}
}
