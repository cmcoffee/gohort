// Archetype library — Builder's build recipes for the common agent shapes.
//
// These are the successors to the in-code Chat / Research / Knowledge-Base
// seeds: instead of shipping fixed framework PERSONAS that live inside every
// user's fleet (and need frozen-shadow handling, retired-seed dispatch guards,
// and seed-vs-user gating throughout), the archetypes are markdown SPECS
// describing each shape — toolset, memory config, prompt beats, caps — that
// Builder reads and composes a USER-OWNED agent from. A spec is versionable,
// diffable, and customizable per user; a seed persona is none of those.
//
// Docs live in archetypes/*.md, embedded at build so there's no runtime file
// dependency. Builder reaches them via the `archetype` tool (list + read).
//
// Each doc opens with a JSON frontmatter header (see frontmatter.go) carrying
// the facts a machine needs: the one-line summary, the aliases a model is
// likely to type, the seed this shape ships as when it ships as one, and the
// settings the recipe prescribes. Everything else stays prose, because the
// rest of a recipe is judgement and a reader is the only thing that can apply
// it. The header exists because the prose was being SCRAPED: the summary came
// out of the first paragraph, the aliases were a switch statement in this
// file, and the test that stops a shape's two descriptions from drifting
// regex-matched a bullet for backticked tool names, so a recipe that phrased
// its allowlist any other way was silently exempt from the check.
package orchestrate

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

//go:embed archetypes/*.md
var archetypeFS embed.FS

// archetype is one build recipe: a slug (the filename stem), the header the
// doc declares, and the markdown body below it.
type archetype struct {
	Slug string
	Body string
	archetypeHeader
}

// archetypeHeader is a recipe's frontmatter.
type archetypeHeader struct {
	// Summary is the one line Builder reads when choosing between shapes.
	Summary string `json:"summary"`

	// Aliases are the words a model actually types for this shape ("kb",
	// "probe", "watcher"). They live in the doc rather than in a switch here
	// so adding a shape is adding a file.
	Aliases []string `json:"aliases,omitempty"`

	// Seed names the seed agent that ships THIS shape, when one does. It is
	// the link between a recipe and the record a user can reach without
	// Builder, and TestArchetypesAgreeWithTheirSeeds walks it: a user who
	// clones the template and a user who asks Builder for one should not end
	// up with agents of different reach.
	Seed string `json:"seed,omitempty"`

	// Template, when set, is this shape's label in the New Agent wizard's
	// "Start from a template" row. Present means the wizard offers it; the
	// record it clones is Seed, so a template without a seed has nothing to
	// copy and is refused at parse.
	//
	// The label lives here because this file is where the shape is described.
	// It used to be a two-entry list of {seed id, label} pairs in
	// page_agent_wizard.go, which meant a shape's name for users sat a
	// package away from the shape, and adding one was a code change.
	Template string `json:"template,omitempty"`

	// Settings are the parts of the recipe a test can check. Optional, and
	// deliberately narrow: what the agent may reach, how far it may go, and
	// whether the shape's contract belongs in rules. Everything else about a
	// recipe is prose because it is judgement.
	Settings *archetypeSettings `json:"settings,omitempty"`
}

// archetypeSettings are the machine-checkable prescriptions of a recipe.
// Pointers so "the recipe does not say" stays distinguishable from "the recipe
// says zero", since an unstated budget is not a budget of nothing.
type archetypeSettings struct {
	// AllowedTools is the exact allowlist the shape prescribes. An empty
	// slice is meaningful (the conversational shape prescribes the default
	// pool), so nil means unstated and [] means "grant nothing extra".
	AllowedTools    *[]string `json:"allowed_tools,omitempty"`
	MaxPlanSteps    *int      `json:"max_plan_steps,omitempty"`
	MaxWorkerRounds *int      `json:"max_worker_rounds,omitempty"`
	GapCheck        *bool     `json:"gap_check,omitempty"`

	// RulesRequired marks a shape whose contract has to live in rules rather
	// than in the persona, because rules outrank memory and the persona and
	// win on the turn a plausible answer is already in the model's head. Two
	// recipes argued exactly this while the records they describe carried no
	// rules at all.
	RulesRequired bool `json:"rules_required,omitempty"`
}

// loadArchetypes returns every embedded archetype doc, ordered by slug so the
// list tool and any log line are stable across runs.
//
// Read and parsed once. The docs are embedded, so nothing about them changes
// while the process runs, and the list tool used to re-read the directory
// three times to answer one call.
func loadArchetypes() []archetype {
	archetypesOnce.Do(func() { archetypes = parseArchetypes() })
	return archetypes
}

var (
	archetypesOnce sync.Once
	archetypes     []archetype
)

// parseArchetypes reads the library. Every failure is fatal rather than
// skipped: this used to swallow a read error and a directory error alike, so a
// broken doc presented as a shape Builder had never been given, and Builder
// would compose the agent from scratch and nobody would learn why it came out
// different.
func parseArchetypes() []archetype {
	entries, err := fs.ReadDir(archetypeFS, "archetypes")
	if err != nil {
		Fatal("archetypes: %v", err)
	}
	var out []archetype
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == libraryReadmeName {
			continue
		}
		data, err := archetypeFS.ReadFile("archetypes/" + e.Name())
		if err != nil {
			Fatal("archetypes: %s: %v", e.Name(), err)
		}
		a, err := parseArchetype(e.Name(), data)
		if err != nil {
			Fatal("%v", err)
		}
		if seen[a.Slug] {
			Fatal("archetypes: two docs claim the slug %q", a.Slug)
		}
		seen[a.Slug] = true
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// parseArchetype turns one doc into a recipe. The slug is the filename stem,
// so a doc cannot disagree with its own name.
func parseArchetype(name string, data []byte) (archetype, error) {
	front, body, err := splitFrontmatter(data)
	if err != nil {
		return archetype{}, fmt.Errorf("archetype %s: %v", name, err)
	}
	var hdr archetypeHeader
	if err := decodeFrontmatter(front, &hdr); err != nil {
		return archetype{}, fmt.Errorf("archetype %s: %v", name, err)
	}
	if strings.TrimSpace(hdr.Summary) == "" {
		return archetype{}, fmt.Errorf("archetype %s: no summary, which is the one line Builder reads to choose between shapes", name)
	}
	if strings.TrimSpace(body) == "" {
		return archetype{}, fmt.Errorf("archetype %s: no recipe below the frontmatter", name)
	}
	if hdr.Template != "" && hdr.Seed == "" {
		return archetype{}, fmt.Errorf("archetype %s: offered as a wizard template with no seed to clone", name)
	}
	return archetype{
		Slug:            strings.TrimSuffix(name, ".md"),
		Body:            body,
		archetypeHeader: hdr,
	}, nil
}

// archetypeBySlug returns one archetype's body, tolerating a name the model
// might use ("knowledge base" → knowledge_base, "kb" → knowledge_base).
func archetypeBySlug(slug string) (archetype, bool) {
	want := normalizeArchetypeSlug(slug)
	for _, a := range loadArchetypes() {
		if a.Slug == want {
			return a, true
		}
	}
	// Alias pass — match on a contained word so "research agent" finds research.
	for _, a := range loadArchetypes() {
		if strings.Contains(want, a.Slug) || strings.Contains(a.Slug, want) {
			return a, true
		}
	}
	return archetype{}, false
}

// normalizeArchetypeSlug folds a typed name toward a slug, then resolves the
// aliases each doc declares for itself.
func normalizeArchetypeSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "-", "_")
	for _, a := range loadArchetypes() {
		for _, alias := range a.Aliases {
			if s == alias {
				return a.Slug
			}
		}
	}
	return s
}

// archetypeTool is Builder's read access to the archetype library. list shows
// the available shapes; read returns one shape's full build recipe. Builder
// consults it when a request matches a known shape ("build me a research
// agent") so the composed agent inherits the vetted toolset/prompt/memory
// configuration instead of Builder reinventing it each time.
func archetypeTool() *GroupedTool {
	gt := NewGroupedTool("archetype",
		"Build recipes for the common agent SHAPES (research, knowledge-base, conversational). When a build request matches a known shape, read its recipe FIRST and compose the new agent from it — the recipe carries the vetted toolset, memory config, prompt beats, and caps for that shape. Actions: list, read.")
	gt.AddAction("list", &GroupedToolAction{
		Description: "List the available agent archetypes with a one-line summary of each.",
		Params:      map[string]ToolParam{},
		Caps:        []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			var b strings.Builder
			b.WriteString("Agent archetypes (read one with archetype(action=\"read\", slug=\"<slug>\")):\n\n")
			for _, a := range loadArchetypes() {
				fmt.Fprintf(&b, "- %s — %s\n", a.Slug, a.Summary)
			}
			b.WriteString("\nNo match? Build from scratch with create_agent as usual.")
			return b.String(), nil
		},
	})
	gt.AddAction("read", &GroupedToolAction{
		Description: "Read one archetype's full build recipe (toolset, memory config, prompt beats, caps) so you can compose an agent of that shape.",
		Params: map[string]ToolParam{
			"slug": {Type: "string", Description: "Archetype slug from list (e.g. \"research\", \"knowledge_base\", \"conversational\"). Common aliases (kb, chat) resolve."},
		},
		Required: []string{"slug"},
		Caps:     []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			a, ok := archetypeBySlug(stringArg(args, "slug"))
			if !ok {
				var slugs []string
				for _, x := range loadArchetypes() {
					slugs = append(slugs, x.Slug)
				}
				return "", fmt.Errorf("no archetype %q — available: %s. Or build from scratch with create_agent", stringArg(args, "slug"), strings.Join(slugs, ", "))
			}
			return a.Body, nil
		},
	})
	return gt
}
