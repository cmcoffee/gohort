package core

import "testing"

func TestMentionedUncalledTool(t *testing.T) {
	defs := []Tool{
		{Name: "get_joke"},                       // zero params, snake_case
		{Name: "get_meme", Required: []string{}}, // zero params
		{Name: "web_search", // parameterized — still eligible since the widening
			Parameters: map[string]ToolParam{"query": {Type: "string"}},
			Required:   []string{"query"}},
		{Name: "read_support_bundles", // the reported case: a refusal that names a real tool
			Parameters: map[string]ToolParam{"file": {Type: "string"}},
			Required:   []string{"file"}},
		{Name: "image"}, // no underscore → never (common-word guard)
	}
	handlers := map[string]ToolHandlerFunc{}
	for _, d := range defs {
		handlers[d.Name] = func(map[string]any) (string, error) { return "", nil }
	}

	cases := []struct {
		content  string
		want     string
		wantArgs bool
	}{
		{"Sure, let me get_joke for you.", "get_joke", false},
		{"I'll fire get_meme now", "get_meme", false},
		{"here is a joke i made up myself", "", false},
		{"I could web_search that", "web_search", true},
		{"I don't have access to read_support_bundles.", "read_support_bundles", true},
		{"look at these images", "", false},  // "image" substring + no underscore
		{"the getjokes endpoint", "", false}, // not token-bounded, no underscore
		{"maybe use get_joke instead", "get_joke", false},
	}
	for _, c := range cases {
		got, gotArgs := mentionedUncalledTool(c.content, handlers, defs)
		if got != c.want || gotArgs != c.wantArgs {
			t.Errorf("mentionedUncalledTool(%q) = (%q, %v), want (%q, %v)", c.content, got, gotArgs, c.want, c.wantArgs)
		}
	}
}

// A tool the catalog no longer carries must not trigger the nudge: the
// handler map is the authority on what can actually be dispatched, so a
// name left over in prose from an earlier phase stays prose.
func TestMentionedUncalledToolIgnoresUnhandled(t *testing.T) {
	defs := []Tool{{Name: "read_support_bundles", Parameters: map[string]ToolParam{"file": {Type: "string"}}}}
	if got, _ := mentionedUncalledTool("try read_support_bundles", map[string]ToolHandlerFunc{}, defs); got != "" {
		t.Errorf("got %q, want \"\" for a tool with no handler", got)
	}
}

func TestMentionsToken(t *testing.T) {
	cases := []struct {
		hay, needle string
		want        bool
	}{
		{"let me get_joke now", "get_joke", true},
		{"get_joke", "get_joke", true},          // whole string
		{"call get_joke.", "get_joke", true},    // trailing punctuation
		{"get_jokes plural", "get_joke", false}, // 's' is an ident byte → not a token
		{"forget_joke", "get_joke", false},      // preceding letter → not a token
		{"no mention here", "get_joke", false},
	}
	for _, c := range cases {
		if got := mentionsToken(c.hay, c.needle); got != c.want {
			t.Errorf("mentionsToken(%q, %q) = %v, want %v", c.hay, c.needle, got, c.want)
		}
	}
}
