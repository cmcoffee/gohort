package core

import "testing"

func TestMentionedNoArgTool(t *testing.T) {
	defs := []Tool{
		{Name: "get_joke"},                                // no-arg, snake_case → eligible
		{Name: "get_meme", Required: []string{}},          // no-arg → eligible
		{Name: "web_search", Required: []string{"query"}}, // has a required arg → never
		{Name: "image"},                                   // no underscore → never (common-word guard)
	}
	handlers := map[string]ToolHandlerFunc{}
	for _, d := range defs {
		handlers[d.Name] = func(map[string]any) (string, error) { return "", nil }
	}

	cases := []struct{ content, want string }{
		{"Sure, let me get_joke for you.", "get_joke"}, // token, eligible
		{"I'll fire get_meme now", "get_meme"},         // token, eligible
		{"here is a joke i made up myself", ""},        // no tool token at all
		{"I could web_search that", ""},                // has required args → excluded
		{"look at these images", ""},                   // "image" substring + no underscore → excluded
		{"the getjokes endpoint", ""},                  // not token-bounded, no underscore
		{"maybe use get_joke instead", "get_joke"},     // bounded by space/edge
	}
	for _, c := range cases {
		if got := mentionedNoArgTool(c.content, handlers, defs); got != c.want {
			t.Errorf("mentionedNoArgTool(%q) = %q, want %q", c.content, got, c.want)
		}
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
