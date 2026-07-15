package core

import "testing"

func TestUnsourcedFigures(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		source string
		want   []string
	}{
		{
			name:   "fabricated price, empty source",
			answer: "A decent 24-port switch runs about $300 these days.",
			source: "",
			want:   []string{"$300"},
		},
		{
			name:   "price present in source is grounded",
			answer: "The switch is $1,299.",
			source: "Product page: Model X, price $1,299, in stock.",
			want:   nil,
		},
		{
			name:   "comma vs no-comma normalizes (answer $3,000 / source 3000)",
			answer: "It lists for $3,000.",
			source: "MSRP 3000 USD per unit.",
			want:   nil,
		},
		{
			name:   "user-provided figure is grounded",
			answer: "So your $450 budget covers it.",
			source: "I have a $450 budget.",
			want:   nil,
		},
		{
			name:   "bare integers, years, and counts are NOT flagged",
			answer: "Here are 3 options released in 2024, each with 48 ports.",
			source: "",
			want:   nil,
		},
		{
			name:   "lone single-digit money is skipped (too noisy)",
			answer: "It's just $5.",
			source: "",
			want:   nil,
		},
		{
			name:   "currency-word forms are caught",
			answer: "Expect around 300 dollars, or USD 1200 for the pro model.",
			source: "",
			want:   []string{"300 dollars", "USD 1200"},
		},
		{
			name:   "mixed: one grounded, one fabricated",
			answer: "The base is $99 (per the page) but the add-on is $250.",
			source: "Base plan: $99/month.",
			want:   []string{"$250"},
		},
		{
			name:   "duplicate figures reported once",
			answer: "$800 up front and another $800 later.",
			source: "",
			want:   []string{"$800"},
		},
		{
			name:   "fabricated percentage is flagged",
			answer: "40% of users churn in month one.",
			source: "",
			want:   []string{"40%"},
		},
		{
			name:   "hedged percentage is left alone (honest estimate)",
			answer: "Roughly 40% churn, and about 15% never onboard.",
			source: "",
			want:   nil,
		},
		{
			name:   "hedged money is still caught (a price is a price)",
			answer: "A 24-port switch runs about $300.",
			source: "",
			want:   []string{"$300"},
		},
		{
			name:   "sourced percentage is grounded",
			answer: "About 12% did not renew.",
			source: "Report: 12% non-renewal rate observed.",
			want:   nil,
		},
		{
			name:   "magnitude figure is flagged",
			answer: "The market is worth 2.3 billion.",
			source: "",
			want:   []string{"2.3 billion"},
		},
		{
			name:   "bare percent-less numbers and versions still ignored",
			answer: "Version 14 ships with 12 modules and 8 presets.",
			source: "",
			want:   nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := unsourcedFigures(c.answer, c.source)
			if !equalStrs(got, c.want) {
				t.Errorf("unsourcedFigures(%q, %q) = %v, want %v", c.answer, c.source, got, c.want)
			}
		})
	}
}

func TestGroundingCorpusExcludesAssistantAndGateMessages(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "what's a fair price?"},
		{Role: "assistant", Content: "I think it's $9,999."}, // must NOT ground
		{Role: "user", Content: groundingGatePrompt([]string{"$9,999"})},                        // gate's own msg, must NOT ground
		{Role: "assistant", ToolResults: []ToolResult{{Content: "listing: $1,250", IsError: false}}}, // grounds
		{Role: "assistant", ToolResults: []ToolResult{{Content: "$7,777", IsError: true}}},           // errored, must NOT ground
	}
	corpus := groundingCorpus(history)

	// The real tool result grounds $1,250.
	if got := unsourcedFigures("It's $1,250.", corpus); got != nil {
		t.Errorf("expected $1,250 grounded by tool result, got unsourced %v", got)
	}
	// Assistant prose ($9,999), the gate message, and the errored result
	// ($7,777) must all fail to ground.
	if got := unsourcedFigures("Maybe $9,999 or $7,777.", corpus); !equalStrs(got, []string{"$9,999", "$7,777"}) {
		t.Errorf("expected $9,999 and $7,777 to remain unsourced, got %v", got)
	}
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
