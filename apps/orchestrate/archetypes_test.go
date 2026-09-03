package orchestrate

import (
	"os"
	"strings"
	"testing"
)

// TestArchetypeLibrary pins the embedded archetype docs: every shape loads,
// each has a non-empty summary, and slug resolution tolerates the aliases the
// model is likely to use.
func TestArchetypeLibrary(t *testing.T) {
	got := loadArchetypes()
	want := map[string]bool{"research": false, "knowledge_base": false, "conversational": false, "investigator": false}
	for _, a := range got {
		if _, ok := want[a.Slug]; ok {
			want[a.Slug] = true
		}
		if strings.TrimSpace(a.Summary) == "" {
			t.Errorf("archetype %q has no summary", a.Slug)
		}
		if strings.TrimSpace(a.Body) == "" {
			t.Errorf("archetype %q has no body", a.Slug)
		}
	}
	for slug, seen := range want {
		if !seen {
			t.Errorf("archetype %q missing from the library", slug)
		}
	}

	// Alias resolution: the names a model actually types.
	for _, alias := range []string{"kb", "Knowledge Base", "knowledge-base"} {
		if a, ok := archetypeBySlug(alias); !ok || a.Slug != "knowledge_base" {
			t.Errorf("alias %q should resolve to knowledge_base; got %q ok=%v", alias, a.Slug, ok)
		}
	}
	for _, alias := range []string{"chat", "assistant", "conversational"} {
		if a, ok := archetypeBySlug(alias); !ok || a.Slug != "conversational" {
			t.Errorf("alias %q should resolve to conversational; got %q ok=%v", alias, a.Slug, ok)
		}
	}
	// The words someone actually types when they want one of these: they ask
	// for an agent that can "investigate", rarely for "an investigator".
	for _, alias := range []string{"investigate", "investigation", "probe", "scout", "investigator agent"} {
		if a, ok := archetypeBySlug(alias); !ok || a.Slug != "investigator" {
			t.Errorf("alias %q should resolve to investigator; got %q ok=%v", alias, a.Slug, ok)
		}
	}
	if a, ok := archetypeBySlug("research agent"); !ok || a.Slug != "research" {
		t.Errorf("'research agent' should resolve to research; got %q ok=%v", a.Slug, ok)
	}
	if _, ok := archetypeBySlug("nonexistent-shape-xyz"); ok {
		t.Error("unknown slug must not resolve")
	}
}

// TestArchetypeToolListAndRead exercises the Builder-facing tool surface.
func TestArchetypeToolListAndRead(t *testing.T) {
	gt := archetypeTool()
	list, err := gt.Run(map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, slug := range []string{"research", "knowledge_base", "conversational"} {
		if !strings.Contains(list, slug) {
			t.Errorf("list should mention %q; got:\n%s", slug, list)
		}
	}
	read, err := gt.Run(map[string]any{"action": "read", "slug": "research"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(read, "web_search") {
		t.Errorf("research recipe should name its toolset; got:\n%s", read)
	}
	if _, err := gt.Run(map[string]any{"action": "read", "slug": "bogus"}); err == nil {
		t.Error("read of an unknown slug should error")
	}
}

// The investigator recipe turns on four things being right, and each of them is
// the kind of detail a rewrite quietly drops. They are asserted here rather than
// left to a reader because the archetype is what Builder composes from — a beat
// missing from the doc is a beat missing from every agent built to it.
func TestInvestigatorRecipeKeepsItsLoadBearingParts(t *testing.T) {
	a, ok := archetypeBySlug("investigator")
	if !ok {
		t.Fatal("the investigator archetype is missing")
	}
	for what, want := range map[string]string{
		// A sub-agent, so the probe transcript never lands in the parent's
		// persisted history and replay into its prompt forever.
		"ownership makes it a sub-agent": "owned_by",
		// The read-only gate is a capability class, not a promise in a persona
		// and not a list of tool names that describes one deployment.
		"the reach gate": `reach: "read"`,
		// Memory is the whole reason a second question is better than the
		// first; it is a default worth stating, not one worth inheriting.
		"memory is on": "Memory ON",
		// The parent has to be told to hand over, or it uses the closer tools
		// and the transcript lands in its thread anyway.
		"wiring the parent": "dispatch to",
	} {
		if !strings.Contains(a.Body, want) {
			t.Errorf("%s: the recipe must say %q", what, want)
		}
	}
	// And it must say what an investigation owes the reader when it comes up
	// short. An investigation that reports only findings reads as complete.
	if !strings.Contains(a.Body, "Not determined") {
		t.Error("the recipe must require the answer to name what it could not determine")
	}
}

// The built-in variant is a two-phase machine, and nothing in the product said
// so — which is why it kept being asked for as a setting. A checkbox cannot
// carry it (what "look" means differs per subject), so the answer is a pointer
// where the question arises: the agent editor's machine field, and the drafter.
func TestTheLookBeforeAnsweringShapeIsSignposted(t *testing.T) {
	for _, f := range []struct{ file, want string }{
		{"page_agent.go", "INVESTIGATES before it answers"},
		{"machine_page.go", "a first step that goes and LOOKS"},
	} {
		raw, err := os.ReadFile(f.file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), f.want) {
			t.Errorf("%s must point at the look-before-answering shape (%q)", f.file, f.want)
		}
	}
	// And the pointer has to name the gate, or somebody builds an agent that
	// can act while it is meant to be looking.
	raw, _ := os.ReadFile("page_agent.go")
	if !strings.Contains(string(raw), "read-only") {
		t.Error("the signpost must say the looking phase is read-only")
	}
}

// `rules` renders above memory and above the persona and wins every conflict —
// the strongest slot in the prompt — and the word appeared in no recipe and
// nowhere in Builder's own prompt. A parameter nothing teaches is a parameter
// nothing uses: Builder wrote constraints into the persona, where they are one
// voice among many on exactly the turn something else pulls the other way.
//
// Every recipe now says which of its beats are constraints and which are craft.
func TestEveryRecipeSaysWhatBelongsInRules(t *testing.T) {
	got := loadArchetypes()
	if len(got) < 4 {
		t.Fatalf("expected the library, got %d", len(got))
	}
	for _, a := range got {
		if !strings.Contains(a.Body, "rules vs. persona") {
			t.Errorf("%s: no guidance on rules vs persona", a.Slug)
		}
		// The reason, not just the pointer. "Put it in rules" without the
		// precedence is advice a reader cannot apply to their own case.
		if !strings.Contains(a.Body, "above the persona") {
			t.Errorf("%s: must say WHY rules is the stronger slot", a.Slug)
		}
		// And a worked example: each shape has a different constraint worth
		// promoting, which is the whole reason this is not one shared line.
		if !strings.Contains(a.Body, "persona") || !strings.Contains(a.Body, "\"") {
			t.Errorf("%s: needs a concrete rule to copy", a.Slug)
		}
	}
}
