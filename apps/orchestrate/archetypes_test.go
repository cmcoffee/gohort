package orchestrate

import (
	"os"
	"regexp"
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

// TestResearchTemplateAndArchetypeAgree. A new user can reach a research agent
// two ways — the wizard's "Start from a template" row clones the seed-research
// RECORD, and asking Builder for one has it follow the research ARCHETYPE. Two
// separately-maintained descriptions of the same agent, and they had already
// drifted on the load-bearing one: the archetype argues that the citation
// contract belongs in `rules` because rules outrank memory and the persona and
// this is exactly the constraint a persona loses when a plausible answer is
// already in the model's head — and the seed carried no rules at all. So the
// three-click path produced the agent the archetype warns about.
//
// This reads the archetype doc rather than restating it: whichever artifact
// changes, the other has to keep up.
func TestResearchTemplateAndArchetypeAgree(t *testing.T) {
	// Every wizard template has an archetype describing the same agent. Both
	// pairs had the same omission, so both are checked.
	for _, pair := range []struct{ seedID, slug string }{
		{"seed-research", "research"},
		{"seed-kb", "knowledge_base"},
	} {
		t.Run(pair.slug, func(t *testing.T) { checkTemplateAgainstArchetype(t, pair.seedID, pair.slug) })
	}
}

func checkTemplateAgainstArchetype(t *testing.T, seedID, slug string) {
	t.Helper()
	doc, ok := archetypeBySlug(slug)
	if !ok {
		t.Fatalf("the %s archetype is gone — the wizard template now has no counterpart", slug)
	}
	var seed AgentRecord
	for _, a := range seedAgents() {
		if a.ID == seedID {
			seed = a
		}
	}
	if seed.ID == "" {
		t.Fatalf("%s is gone, but the wizard still offers it as a template", seedID)
	}

	// The tool set is stated in the doc as an inline-code list on the
	// allowed_tools bullet; the seed must allow exactly those.
	line := ""
	for _, l := range strings.Split(doc.Body, "\n") {
		if strings.Contains(l, "**allowed_tools**") {
			line = l
		}
	}
	// Only an archetype that names its tools inline can be compared on them;
	// the knowledge-base doc describes its allowlist in prose instead.
	want := regexp.MustCompile("`([a-z_]+)`").FindAllStringSubmatch(line, -1)
	if len(want) == 0 {
		want = nil
	}
	named := map[string]bool{}
	for _, m := range want {
		named[m[1]] = true
	}
	allowed := map[string]bool{}
	for _, tool := range seed.AllowedTools {
		allowed[tool] = true
	}
	for tool := range named {
		if !allowed[tool] {
			t.Errorf("the archetype builds with %q and the template does not allow it", tool)
		}
	}
	if len(named) > 0 {
		for tool := range allowed {
			if !named[tool] {
				t.Errorf("the template allows %q and the archetype does not name it — an agent's reach should not depend on which path you took", tool)
			}
		}
	}

	// And the rule the archetype exists to hold.
	if strings.Contains(doc.Body, "`rules`") && strings.TrimSpace(seed.Rules) == "" {
		t.Error("the archetype puts the citation contract in rules; the cloned template carries none, " +
			"so the wizard path produces the agent the archetype warns about")
	}
	if !strings.Contains(strings.ToLower(seed.Rules), "training") {
		t.Error("the template's rules no longer refuse to fill a gap from training — the one thing both archetypes put in rules")
	}
}

// TestArchetypeSummaryIsTheWholeParagraph. The summary is the one line Builder
// reads when choosing between archetypes, and it was the first LINE of the
// paragraph — so every entry in that list stopped mid-sentence at the margin
// the doc happened to wrap on ("A deep-research agent that answers a factual
// question by searching the web,"). The function's own comment said paragraph;
// only the code said line.
func TestArchetypeSummaryIsTheWholeParagraph(t *testing.T) {
	got := archetypeSummary("# Archetype: Thing\n\nFirst line of the summary,\nsecond line of it.\n\nA later paragraph.\n\n## Section\n")
	if want := "First line of the summary, second line of it."; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}

	// No prose before the first section, and a doc with nothing after the
	// heading, both fall back to the heading rather than to a section title
	// or an empty string.
	if got := archetypeSummary("# Archetype: Thing\n\n## Composition\n\n- a bullet\n"); got != "Archetype: Thing" {
		t.Errorf("a doc that starts with a section summarised as %q", got)
	}
	if got := archetypeSummary("# Archetype: Thing\n"); got != "Archetype: Thing" {
		t.Errorf("a heading-only doc summarised as %q", got)
	}

	// And the real library: no summary trails off. A comma at the end is the
	// signature of the old behaviour — a line cut at the margin its author
	// happened to wrap on.
	for _, a := range loadArchetypes() {
		if strings.HasSuffix(a.Summary, ",") {
			t.Errorf("%s summarises as %q — that is a wrapped line, not a sentence", a.Slug, a.Summary)
		}
		if !strings.Contains(a.Summary, ".") {
			t.Errorf("%s has no sentence in its summary: %q", a.Slug, a.Summary)
		}
	}
}
