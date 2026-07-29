package core

// The prose tool-call scan reads ordinary conversation, so a tool whose name
// is also an English word puts that word through the matcher every time it
// comes up. From a live session: an agent with an "image" tool DESCRIBING a
// photo to the user tripped the scan on the word itself.
//
// Two guards keep English from reading as a call — token-bounded matching,
// and the adjacent-paren requirement for names that aren't snake_case.

import "testing"

func commonWordTools() (map[string]ToolHandlerFunc, []Tool) {
	handlers := map[string]ToolHandlerFunc{
		"image":      func(map[string]any) (string, error) { return "", nil },
		"web_search": func(map[string]any) (string, error) { return "", nil },
	}
	defs := []Tool{
		{
			Name:       "image",
			Parameters: map[string]ToolParam{"prompt": {Type: "string"}},
			Required:   []string{"prompt"},
		},
		{
			Name:       "web_search",
			Parameters: map[string]ToolParam{"query": {Type: "string"}},
			Required:   []string{"query"},
		},
	}
	return handlers, defs
}

func TestParseTextToolCall_CommonWordNameStaysProse(t *testing.T) {
	handlers, defs := commonWordTools()

	prose := []struct{ name, content string }{
		{
			"describing a photo",
			"The image shows a golden retriever asleep on a porch, with " +
				"afternoon light coming in from the left.",
		},
		{
			"parenthetical after the word",
			"I cropped the image (a 4x6 print, width=1200) before sending it.",
		},
		{
			"substring inside a longer word",
			"Those images are imagery from the 1970s survey.",
		},
		{
			"markdown rule after the mention",
			"Here is the image you asked about.\n\n---\n\nLet me know if you " +
				"want a different crop.",
		},
	}
	for _, c := range prose {
		if got := ParseTextToolCall(c.content, handlers, defs, true); got != nil {
			t.Errorf("%s: must stay prose, got call %s(%v)", c.name, got.Name, got.Args)
		}
	}
}

func TestParseTextToolCall_CommonWordCallFormStillFires(t *testing.T) {
	handlers, defs := commonWordTools()
	// The narrated-call shape prose does not produce: paren immediately
	// after the name. A model that writes its call instead of emitting it
	// still gets rescued.
	content := `Generating that now: image(prompt="a golden retriever on a porch")`
	got := ParseTextToolCall(content, handlers, defs, true)
	if got == nil {
		t.Fatal("adjacent-paren call form must still parse")
	}
	if got.Name != "image" || got.Args["prompt"] != "a golden retriever on a porch" {
		t.Errorf("got %s(%v)", got.Name, got.Args)
	}
}

func TestParseTextToolCall_MarkdownRuleIsNotAFlag(t *testing.T) {
	handlers, defs := commonWordTools()
	// Snake_case names skip the common-word guard, so the flag scan is the
	// only thing standing between a section break and a bogus call that
	// carries the rest of the message as its argument.
	content := "I ran web_search on the filings already.\n\n---\n\n" +
		"The short version is that the numbers hold up."
	if got := ParseTextToolCall(content, handlers, defs, true); got != nil {
		t.Errorf("a markdown rule must not read as --flags, got %s(%v)", got.Name, got.Args)
	}

	// A real flag still extracts. Asserted against parseNaturalToolCall
	// rather than ParseTextToolCall because the flag branch only ever fills
	// args["args"], which hasRequired then rejects for any tool with named
	// required params — so at the outer level this shape is unreachable
	// whatever the scan does. Pinning it here keeps the guard honest
	// without pinning that separate limitation.
	flagged := "Next I'll run web_search --query filings"
	got := parseNaturalToolCall(flagged, handlers)
	if got == nil {
		t.Fatal("a genuine --flag must still extract")
	}
	if got.Name != "web_search" || got.Args["args"] != "--query filings" {
		t.Errorf("got %s(%v)", got.Name, got.Args)
	}

	// And the rule alone yields nothing to extract.
	if got := parseNaturalToolCall(content, handlers); got != nil {
		t.Errorf("markdown rule produced args %v", got.Args)
	}
}

func TestLastTokenIndex(t *testing.T) {
	cases := []struct {
		hay, needle string
		want        int
	}{
		{"image", "image", 0},
		{"the image here", "image", 4},
		{"image then image again", "image", 11}, // last occurrence
		{"images imagery", "image", -1},         // never token-bounded
		{"an image, cropped", "image", 3},       // punctuation is a boundary
		{"nothing here", "image", -1},
		{"", "image", -1},
		{"image", "", -1},
	}
	for _, c := range cases {
		if got := lastTokenIndex(c.hay, c.needle); got != c.want {
			t.Errorf("lastTokenIndex(%q, %q) = %d, want %d", c.hay, c.needle, got, c.want)
		}
	}
}

func TestIsFlagToken(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"--verbose", true},
		{"--To", true}, // case-folded
		{"---", false}, // markdown rule
		{"--", false},
		{"----", false},
		{"-v", false},
		{"--1", false}, // digits aren't flag names
		{"plain", false},
	}
	for _, c := range cases {
		if got := isFlagToken(c.s); got != c.want {
			t.Errorf("isFlagToken(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}
