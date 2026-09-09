package orchestrate

import (
	"crypto/sha256"
	"encoding/hex"
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

// TestAShapeShipsTheAgentItDescribes. There is one document per shape now, so
// nothing CAN disagree with anything: the record the wizard clones, the record
// a dispatch materializes, and the recipe Builder reads are the same file.
// This checks what one file can still get wrong.
//
// The two documents drifted exactly where it mattered, with the research
// recipe insisting the citation contract belongs in rules and the record
// carrying no rules at all, so the three-click path produced the agent the
// recipe warns about. rules_required is what is left of that check: a shape
// says the contract belongs in rules, and its own record has to carry them.
func TestAShapeShipsTheAgentItDescribes(t *testing.T) {
	shipped := 0
	for _, doc := range loadArchetypes() {
		if doc.Record == nil {
			if doc.Template != nil {
				t.Errorf("%s is offered as a wizard template and ships no record", doc.Slug)
			}
			continue
		}
		shipped++
		t.Run(doc.Slug, func(t *testing.T) {
			rec := *doc.Record
			if rec.ID == "" || rec.Name == "" {
				t.Fatalf("the shipped record has no id or name: %+v", rec)
			}
			// The record resolves by ID, which is how every by-id caller in
			// the tree reaches it: the console default, the MCP default, the
			// dispatches from collections and guides.
			live, ok := seedAgentByID(rec.ID)
			if !ok {
				t.Fatalf("%s ships %q and it does not resolve", doc.Slug, rec.ID)
			}
			if live.Owner != seedOwner {
				t.Errorf("owner = %q, want the framework marker", live.Owner)
			}
			if strings.TrimSpace(live.OrchestratorPrompt) == "" {
				t.Error("the shipped agent has no persona")
			}
			// The persona is the Persona SECTION, and the recipe above it is
			// what Builder reads. A recipe that leaks into the prompt would
			// tell the agent how to build itself.
			if strings.Contains(live.OrchestratorPrompt, "## Composition") {
				t.Error("the recipe leaked into the persona")
			}
			if strings.Contains(doc.Body, personaHeading) {
				t.Error("the persona leaked into the recipe Builder reads")
			}
			if doc.RulesRequired && strings.TrimSpace(live.Rules) == "" {
				t.Error("this shape says its contract belongs in rules, and ships a record carrying none")
			}
			// A shape's own instances resolve back to it.
			if slug, ok := shapeForSeed(rec.ID); !ok || slug != doc.Slug {
				t.Errorf("an agent built from %s traces back to %q (found=%v)", doc.Slug, slug, ok)
			}
		})
	}
	if shipped == 0 {
		t.Error("no shape ships an agent, so the framework has no agents but Builder")
	}
}

// TestArchetypeHeadersAreUsable pins what the header has to carry. The summary
// is the one line Builder reads when choosing between shapes, and it used to be
// scraped from the first paragraph: taking the first LINE cut every entry off
// at whatever margin its author wrapped on ("A deep-research agent that answers
// a factual question by searching the web,").
func TestArchetypeHeadersAreUsable(t *testing.T) {
	for _, a := range loadArchetypes() {
		if strings.HasSuffix(a.Summary, ",") {
			t.Errorf("%s summarises as %q, which is a wrapped line rather than a sentence", a.Slug, a.Summary)
		}
		if !strings.Contains(a.Summary, ".") {
			t.Errorf("%s has no sentence in its summary: %q", a.Slug, a.Summary)
		}
		for _, alias := range a.Aliases {
			if alias != normalizeArchetypeSlug(alias) && archetypeAliasOwner(alias) != a.Slug {
				t.Errorf("%s declares the alias %q, which resolves elsewhere", a.Slug, alias)
			}
			if alias == a.Slug {
				t.Errorf("%s declares its own slug as an alias", a.Slug)
			}
		}
	}
}

func archetypeAliasOwner(alias string) string {
	for _, a := range loadArchetypes() {
		for _, x := range a.Aliases {
			if x == alias {
				return a.Slug
			}
		}
	}
	return ""
}

// TestParseArchetypeRejectsBadDocs: a recipe that does not parse has to stop
// the build. Skipped quietly, it would present as a shape Builder was never
// given, and Builder would compose from scratch with nobody the wiser.
func TestParseArchetypeRejectsBadDocs(t *testing.T) {
	good := "---\n{\"summary\":\"Does a thing.\"}\n---\n# Thing\n\nbody\n"
	a, err := parseArchetype("thing.md", []byte(good))
	if err != nil {
		t.Fatalf("valid recipe rejected: %v", err)
	}
	if a.Slug != "thing" || a.Summary != "Does a thing." {
		t.Errorf("parsed as slug=%q summary=%q", a.Slug, a.Summary)
	}
	if strings.HasPrefix(a.Body, "---") {
		t.Error("the frontmatter reached the body Builder reads")
	}

	for name, doc := range map[string]string{
		"no fence":       "# Thing\n\nbody\n",
		"unclosed fence": "---\n{\"summary\":\"x.\"}\n# Thing\n",
		"bad json":       "---\n{\"summary\":,}\n---\n# Thing\n",
		"unknown key":    "---\n{\"summary\":\"x.\",\"alises\":[\"a\"]}\n---\n# Thing\n",
		"no summary":     "---\n{\"aliases\":[\"a\"]}\n---\n# Thing\n",
		"no body":        "---\n{\"summary\":\"x.\"}\n---\n\n",
		// Offered in the wizard with nothing to clone, and offered with
		// nothing to read on the row.
		"template without a seed":  "---\n{\"summary\":\"x.\",\"template\":{\"label\":\"A thing\"}}\n---\n# Thing\n",
		"template without a label": "---\n{\"summary\":\"x.\",\"seed\":\"seed-x\",\"template\":{\"order\":1}}\n---\n# Thing\n",
	} {
		if _, err := parseArchetype("thing.md", []byte(doc)); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}

// An empty slug must match nothing. The alias pass is a substring test and
// every slug contains the empty string, so an unset shape used to resolve to
// whichever recipe sorted first, which is how an agent that follows nothing
// came back following Chat.
func TestAnEmptySlugResolvesToNothing(t *testing.T) {
	for _, s := range []string{"", "   ", "\t"} {
		if a, ok := archetypeBySlug(s); ok {
			t.Errorf("archetypeBySlug(%q) resolved to %q", s, a.Slug)
		}
	}
}

// TestShippedPersonasUnchanged proves the personas moved out of seeds/ without
// changing. Each digest is of the Persona section exactly as written, and each
// value is the one the old seed document carried: research's is checked before
// snippet expansion, because the memory-tool name resolves differently
// depending on a deployment flag.
//
// A failure means an edit changed what one of these agents says. If that was
// the intent, update the digest in the same commit so it shows in the diff.
func TestShippedPersonasUnchanged(t *testing.T) {
	for _, tc := range []struct{ slug, sum string }{
		{"conversational", "02180e1ef22e3f234b5883aa2b9ca9e246f75327981abd2f134821df0ecf4702"},
		{"knowledge_base", "a8254110d50093eaa81ad73ad41a1c46f002f8066cb4d09af3466139b7bd590c"},
		{"research", "2a570183462b68e77a0837242ee79f83120c4bfc67f5d7757e3ac38afa636a30"},
	} {
		data, err := archetypeFS.ReadFile("archetypes/" + tc.slug + ".md")
		if err != nil {
			t.Errorf("%s: %v", tc.slug, err)
			continue
		}
		_, body, err := splitFrontmatter(data)
		if err != nil {
			t.Errorf("%s: %v", tc.slug, err)
			continue
		}
		_, persona := splitPersona(body)
		sum := sha256.Sum256([]byte(persona))
		if got := hex.EncodeToString(sum[:]); got != tc.sum {
			t.Errorf("%s: persona digest = %s (%d bytes), want %s", tc.slug, got, len(persona), tc.sum)
		}
	}
}

// splitPersona has to be unambiguous about which half is which, because one
// half is read by Builder and the other is sent to a model as its identity.
func TestSplitPersona(t *testing.T) {
	recipe, persona := splitPersona("# Shape\n\nBuild this when.\n\n## Persona\n\nYou are a thing.")
	if recipe != "# Shape\n\nBuild this when." {
		t.Errorf("recipe = %q", recipe)
	}
	if persona != "You are a thing." {
		t.Errorf("persona = %q", persona)
	}
	// A document with no persona is all recipe: that is a shape whose subject
	// is not known yet, and there is nothing to instantiate.
	if r, p := splitPersona("# Shape\n\nBuild this when."); r != "# Shape\n\nBuild this when." || p != "" {
		t.Errorf("a recipe-only doc split as recipe=%q persona=%q", r, p)
	}
	// A heading that merely mentions the word is not the marker.
	if _, p := splitPersona("# Shape\n\n## Persona notes\n\nnot the marker\n"); p != "" {
		t.Errorf("a lookalike heading was treated as the persona: %q", p)
	}
}
