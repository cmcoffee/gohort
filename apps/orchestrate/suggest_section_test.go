package orchestrate

import (
	"strings"
	"testing"
)

// Section-scoped suggestion: the prompt must name the section, carry the
// declared Help as guidance, and demand a bare body back.
func TestBuildSuggestPromptSectionScoped(t *testing.T) {
	rec := map[string]any{
		"name":                "Research helper",
		"orchestrator_prompt": "## Role\nYou are a helper.",
	}
	p := buildSuggestPrompt("orchestrator_prompt", "Failure modes", "be blunt", rec)

	for _, want := range []string{
		`"Failure modes" section`,
		"What this section is for",
		"What commonly goes wrong", // the declared Help
		"be blunt",                 // the user's hint
		"Return ONLY the body",     // bare-body instruction
		"You are a helper.",        // current value as context
	} {
		if !strings.Contains(p, want) {
			t.Errorf("section prompt missing %q\n---\n%s", want, p)
		}
	}
	// The whole-field framing must not leak into a section request.
	if strings.Contains(p, "## Field to suggest") {
		t.Error("section prompt used the whole-field framing")
	}
}

// A section the user added themselves has no declared Help. It should
// still produce a usable prompt rather than an empty guidance block.
func TestBuildSuggestPromptUndeclaredSection(t *testing.T) {
	p := buildSuggestPrompt("orchestrator_prompt", "Escalation", "", nil)
	if !strings.Contains(p, `"Escalation" section`) {
		t.Errorf("undeclared section not named:\n%s", p)
	}
	if strings.Contains(p, "What this section is for") {
		t.Error("emitted an empty guidance heading for an undeclared section")
	}
}

// Empty Section keeps the original whole-field behavior.
func TestBuildSuggestPromptWholeFieldUnchanged(t *testing.T) {
	p := buildSuggestPrompt("rules", "", "", nil)
	if !strings.Contains(p, "## Field to suggest") {
		t.Errorf("whole-field prompt lost its framing:\n%s", p)
	}
	if strings.Contains(p, "Section to write") {
		t.Error("whole-field prompt leaked section framing")
	}
}

func TestSectionGuidance(t *testing.T) {
	if g := sectionGuidance("orchestrator_prompt", "Rules"); !strings.Contains(g, "Hard constraints") {
		t.Errorf("Rules guidance = %q", g)
	}
	// Title matching is case- and space-insensitive: it comes from a
	// round-tripped markdown heading, not from a picker.
	if g := sectionGuidance("orchestrator_prompt", "  rules  "); g == "" {
		t.Error("guidance lookup should tolerate case and surrounding space")
	}
	if g := sectionGuidance("orchestrator_prompt", "Nonexistent"); g != "" {
		t.Errorf("unknown section returned guidance %q", g)
	}
	if g := sectionGuidance("rules", "Rules"); g != "" {
		t.Errorf("field without an outline returned guidance %q", g)
	}
}

// Models restate the heading they were asked to write under. Letting it
// through would nest a second "## Rules" inside the Rules section.
func TestStripSectionHeading(t *testing.T) {
	cases := []struct {
		name, in, section, want string
	}{
		{"own heading dropped", "## Rules\n- always cite", "Rules", "- always cite"},
		{"any level", "#### Rules\n- x", "Rules", "- x"},
		{"leading blank lines", "\n\n## Rules\n- x", "Rules", "- x"},
		{"case insensitive", "## rules\n- x", "Rules", "- x"},
		{"no heading untouched", "- always cite", "Rules", "- always cite"},
		{"other heading kept", "## Hard\n- x", "Rules", "## Hard\n- x"},
		{"heading only", "## Rules", "Rules", ""},
		{"empty", "", "Rules", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripSectionHeading(c.in, c.section); got != c.want {
				t.Errorf("stripSectionHeading(%q, %q) = %q, want %q", c.in, c.section, got, c.want)
			}
		})
	}
}

// The gate and the context dump read one list, so a suggestable field can
// never be invisible as context for the others.
func TestSuggestableFieldsSingleSource(t *testing.T) {
	if len(fieldsSuggestable) != len(suggestableFields) {
		t.Fatalf("gate has %d fields, list has %d", len(fieldsSuggestable), len(suggestableFields))
	}
	rec := map[string]any{}
	for _, f := range suggestableFields {
		if !fieldsSuggestable[f] {
			t.Errorf("%q listed but not gated", f)
		}
		rec[f] = "sentinel-" + f
	}
	p := buildSuggestPrompt("name", "", "", rec)
	for _, f := range suggestableFields {
		if !strings.Contains(p, "sentinel-"+f) {
			t.Errorf("suggestable field %q never reaches the prompt as context", f)
		}
	}
}

func TestSplitDraftReply(t *testing.T) {
	cases := []struct {
		name, in, reply, value string
	}{
		{
			name:  "reply plus draft",
			in:    "Tightened the second sentence.\n\n```draft\nYou are a helper.\nBe brief.\n```",
			reply: "Tightened the second sentence.",
			value: "You are a helper.\nBe brief.",
		},
		{
			name:  "no fence is a plain answer",
			in:    "That section is for hard constraints only.",
			reply: "That section is for hard constraints only.",
			value: "",
		},
		{
			name:  "draft with no preamble",
			in:    "```draft\n- always cite\n```",
			reply: "",
			value: "- always cite",
		},
		{
			// A prompt that teaches by example legitimately contains its
			// own fences. Closing on the FIRST ``` would truncate it.
			name:  "draft containing nested fences",
			in:    "Added an example.\n\n```draft\nReturn code like:\n\n```go\nfmt.Println(1)\n```\n\nThat is the shape.\n```",
			reply: "Added an example.",
			value: "Return code like:\n\n```go\nfmt.Println(1)\n```\n\nThat is the shape.",
		},
		{
			name:  "unterminated fence keeps the body",
			in:    "Here you go.\n\n```draft\nYou are a helper.",
			reply: "Here you go.",
			value: "You are a helper.",
		},
		{
			name:  "empty",
			in:    "",
			reply: "",
			value: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reply, value := splitDraftReply(c.in)
			if reply != c.reply {
				t.Errorf("reply = %q, want %q", reply, c.reply)
			}
			if value != c.value {
				t.Errorf("value = %q, want %q", value, c.value)
			}
		})
	}
}

// A field-declared AssistPrompt must land inside the framing, ABOVE the
// response contract — it shapes the advice, it doesn't replace the rules.
func TestBuildAssistSystemPromptFieldFraming(t *testing.T) {
	p := buildAssistSystemPrompt("orchestrator_prompt", "", "", "Write in the second person.", nil)
	at := strings.Index(p, "Write in the second person.")
	contract := strings.Index(p, draftFence)
	if at < 0 {
		t.Fatalf("declared assist prompt missing:\n%s", p)
	}
	if contract < 0 || at > contract {
		t.Error("declared framing must precede the response contract, not displace it")
	}
	if !strings.Contains(p, "How to reply") {
		t.Error("response contract lost when a field declared its own framing")
	}
}

// Bounded so a rewritten payload can't turn the endpoint into a
// general-purpose LLM proxy.
func TestBuildAssistSystemPromptBoundsDeclaredFraming(t *testing.T) {
	huge := strings.Repeat("x", assistPromptMax*3)
	p := buildAssistSystemPrompt("orchestrator_prompt", "", "", huge, nil)
	// Assert on the run itself: other framing text contains stray x's
	// ("context"), so a global count would be measuring the wrong thing.
	if strings.Contains(p, strings.Repeat("x", assistPromptMax+1)) {
		t.Errorf("declared framing not truncated to %d", assistPromptMax)
	}
	if !strings.Contains(p, strings.Repeat("x", assistPromptMax)) {
		t.Error("declared framing truncated below the cap")
	}
	if !strings.Contains(p, "How to reply") {
		t.Error("truncation ate the response contract")
	}
}

func TestBuildAssistSystemPrompt(t *testing.T) {
	rec := map[string]any{"name": "Research helper"}
	p := buildAssistSystemPrompt("orchestrator_prompt", "Rules", "- cite a URL", "", rec)
	for _, want := range []string{
		`"Rules" section`,
		"Hard constraints", // declared Help for that section
		"- cite a URL",     // the live draft
		"Research helper",  // surrounding record
		draftFence,         // the response contract
		"replaces the draft outright",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("assist prompt missing %q\n---\n%s", want, p)
		}
	}
}

// An empty draft must be stated, not silently omitted — otherwise the
// model has no signal that it is writing from scratch.
func TestBuildAssistSystemPromptEmptyDraft(t *testing.T) {
	p := buildAssistSystemPrompt("orchestrator_prompt", "", "", "", nil)
	if !strings.Contains(p, "nothing written yet") {
		t.Errorf("empty draft not called out:\n%s", p)
	}
	if !strings.Contains(p, "`orchestrator_prompt` field") {
		t.Errorf("whole-field assist prompt didn't name the field:\n%s", p)
	}
}

// Every declared section needs Help: it is both the author's hint under
// the slot and the model's guidance when suggesting that section.
func TestOrchestratorPromptSectionsDeclareHelp(t *testing.T) {
	if len(orchestratorPromptSections) == 0 {
		t.Fatal("no sections declared")
	}
	seen := map[string]bool{}
	for _, s := range orchestratorPromptSections {
		if strings.TrimSpace(s.Title) == "" {
			t.Error("section with an empty title")
		}
		if strings.TrimSpace(s.Help) == "" {
			t.Errorf("section %q has no Help", s.Title)
		}
		key := strings.ToLower(strings.TrimSpace(s.Title))
		if seen[key] {
			t.Errorf("duplicate section title %q — titles key both the markdown heading and the guidance lookup", s.Title)
		}
		seen[key] = true
		switch s.Mode {
		case "", "prose", "list", "steps":
		default:
			t.Errorf("section %q declares unsupported mode %q", s.Title, s.Mode)
		}
	}
}

// TestAWholeFieldDraftIsToldTheOutline. The prompt editor offers the persona
// as five named sections, and the per-section drafts read their guidance from
// that same declaration. A WHOLE-field draft was told none of it, so the
// wizard produced one undifferentiated block: it opened in the editor's
// free-form area with every slot empty, which is not what the author is shown
// the field is for.
func TestAWholeFieldDraftIsToldTheOutline(t *testing.T) {
	p := buildSuggestPrompt("orchestrator_prompt", "", "", map[string]any{"name": "Ada"})
	for _, sec := range orchestratorPromptSections {
		if !strings.Contains(p, "## "+sec.Title) {
			t.Errorf("the draft prompt never mentions the %q section", sec.Title)
		}
	}
	if !strings.Contains(p, "using exactly the `## ` headings") {
		t.Error("nothing tells the model to actually use the headings, so it may return prose anyway")
	}

	// A section-scoped draft is unchanged: it writes one body, with no
	// heading, and must not be handed the whole outline.
	one := buildSuggestPrompt("orchestrator_prompt", "Rules", "", map[string]any{})
	if strings.Contains(one, "Structure to write it in") {
		t.Error("a single-section draft was handed the whole outline")
	}
	if !strings.Contains(one, "no heading") {
		t.Error("the single-section contract changed")
	}

	// Every other field keeps the plain one-value contract.
	if strings.Contains(buildSuggestPrompt("description", "", "", map[string]any{}), "Structure to write it in") {
		t.Error("an unsectioned field was given an outline")
	}
}

// TestTheFallbackPromptIsSectionedToo. The fallback is what an agent gets when
// the draft call fails, so it is exactly the case where someone will open the
// editor to fix it. It should hand them the same structure a successful draft
// would have.
func TestTheFallbackPromptIsSectionedToo(t *testing.T) {
	got := wizardFallbackPrompt("Ada", "keep track of my projects", "warm and plain-spoken")
	for _, want := range []string{"## Role & voice", "## Approach", "You are Ada.", "warm and plain-spoken"} {
		if !strings.Contains(got, want) {
			t.Errorf("the fallback prompt is missing %q:\n%s", want, got)
		}
	}
}
