package core

import "testing"

// A grouped tool is one name over many operations, so the bare name in a log
// cannot answer the only question worth asking of a scheduled fire: did it
// actually post, or did it just read? "tool call: moltbook (args=46 bytes)" was
// the same line for both.
func TestToolCallLabelNamesTheAction(t *testing.T) {
	cases := []struct {
		name string
		tc   ToolCall
		want string
	}{
		{"grouped call names its action",
			ToolCall{Name: "moltbook", Args: map[string]any{"action": "create_post", "title": "x"}},
			"moltbook/create_post"},
		{"the read action is distinguishable from the write one",
			ToolCall{Name: "moltbook", Args: map[string]any{"action": "list_user_messages"}},
			"moltbook/list_user_messages"},
		{"a plain tool is unchanged",
			ToolCall{Name: "fetch_url", Args: map[string]any{"url": "https://example.com"}},
			"fetch_url"},
		{"no args at all",
			ToolCall{Name: "get_time"},
			"get_time"},
		{"a non-string action is not guessed at",
			ToolCall{Name: "weird", Args: map[string]any{"action": 42}},
			"weird"},
		{"an empty action adds nothing",
			ToolCall{Name: "moltbook", Args: map[string]any{"action": "   "}},
			"moltbook"},
	}
	for _, c := range cases {
		if got := toolCallLabel(c.tc); got != c.want {
			t.Errorf("%s: label = %q, want %q", c.name, got, c.want)
		}
	}
}
