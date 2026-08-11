package core

import (
	"strings"
	"testing"
)

// TestDropInvalidlyNamedTools — the backstop for a whole-catalog outage. A
// provider handed one malformed tool name rejects the ENTIRE request, so
// without this guard a single bad tool silently costs the agent every other
// tool it had.
func TestDropInvalidlyNamedTools(t *testing.T) {
	tools := []Tool{
		{Name: "fetch_url"},
		{Name: "atlassian.search"}, // the real one: a dot
		{Name: "read-file"},
		{Name: "has space"},
		{Name: ""},
		{Name: strings.Repeat("x", maxLLMToolNameBytes+1)},
		{Name: "OK_Mixed-123"},
	}
	got := dropInvalidlyNamedTools(tools, "test")
	want := []string{"fetch_url", "read-file", "OK_Mixed-123"}
	if len(got) != len(want) {
		t.Fatalf("kept %d tools, want %d: %v", len(got), len(want), plainToolNames(got))
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("kept[%d] = %q, want %q", i, got[i].Name, want[i])
		}
	}
}

// TestDropInvalidlyNamedToolsIsFreeOnTheCommonPath — this runs over the whole
// catalog on every LLM call, so an all-valid list must not be copied.
func TestDropInvalidlyNamedToolsIsFreeOnTheCommonPath(t *testing.T) {
	tools := []Tool{{Name: "a"}, {Name: "b_c"}, {Name: "d-e"}}
	got := dropInvalidlyNamedTools(tools, "test")
	if len(got) != len(tools) || &got[0] != &tools[0] {
		t.Error("an all-valid tool list was reallocated instead of passed through")
	}
	if dropInvalidlyNamedTools(nil, "test") != nil {
		t.Error("a nil tool list should stay nil")
	}
}

// TestApplyOptsDropsInvalidToolNames pins the guard to the chokepoint every
// provider path goes through, rather than to any one provider's builder.
func TestApplyOptsDropsInvalidToolNames(t *testing.T) {
	cfg := applyOpts("m", 100, []ChatOption{
		WithTools([]Tool{{Name: "good_tool"}, {Name: "bad.tool"}}),
	})
	if len(cfg.Tools) != 1 || cfg.Tools[0].Name != "good_tool" {
		t.Errorf("applyOpts left %v in the catalog", plainToolNames(cfg.Tools))
	}
}

func TestValidLLMToolName(t *testing.T) {
	ok := []string{"a", "A", "0", "a_b-C9", strings.Repeat("x", maxLLMToolNameBytes)}
	for _, s := range ok {
		if !validLLMToolName(s) {
			t.Errorf("validLLMToolName(%q) = false, want true", s)
		}
	}
	bad := []string{"", "a.b", "a b", "a/b", "a:b", "ключ", strings.Repeat("x", maxLLMToolNameBytes+1)}
	for _, s := range bad {
		if validLLMToolName(s) {
			t.Errorf("validLLMToolName(%q) = true, want false", s)
		}
	}
}

func plainToolNames(tools []Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}
