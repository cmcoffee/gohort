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

// archetype is one build recipe: a slug (the filename stem), the recipe body
// Builder reads, the persona the shipped agent wears, and the header.
type archetype struct {
	Slug string
	Body string
	archetypeHeader
}

// Seed is the ID of the agent this shape ships, or empty when it ships none.
// Callers use it to go the other way, from a running agent back to the shape
// it follows.
func (a archetype) Seed() string {
	if a.Record == nil {
		return ""
	}
	return a.Record.ID
}

// archetypeHeader is a recipe's frontmatter.
type archetypeHeader struct {
	// Summary is the one line Builder reads when choosing between shapes.
	Summary string `json:"summary"`

	// Aliases are the words a model actually types for this shape ("kb",
	// "probe", "watcher"). They live in the doc rather than in a switch here
	// so adding a shape is adding a file.
	Aliases []string `json:"aliases,omitempty"`

	// Template, when set, offers this shape in the New Agent wizard's "Start
	// from a template" row. What it clones is Record, so a template without a
	// record has nothing to copy and is refused at parse.
	//
	// It lives here because this file is where the shape is described. It
	// used to be a two-entry list of {seed id, label} pairs in
	// page_agent_wizard.go, which meant a shape's name for users sat a
	// package away from the shape, and adding one was a code change.
	Template *archetypeTemplate `json:"template,omitempty"`

	// Record is the agent this shape ships, when it ships one. Its fields are
	// AgentRecord's own json keys, and its prompt is the Persona section of
	// this document rather than a field, because a persona is prose.
	//
	// A shape with a record can be INSTANTIATED: cloned by the wizard,
	// materialized on a dispatch, followed by the agents built from it. A
	// shape without one describes an agent whose subject is not known yet (a
	// watcher, an investigator), so there is nothing to copy and Builder
	// composes from the recipe instead.
	//
	// The record and the recipe used to be two documents, one in seeds/ and
	// one here, saying the same thing to two readers: the five numbered beats
	// of the research recipe were the five numbered beats of the research
	// persona. They drifted exactly where it mattered, with the recipe
	// insisting the citation contract belongs in rules and the record
	// carrying no rules at all, so the three-click path produced the agent the
	// recipe warns about.
	Record *AgentRecord `json:"record,omitempty"`

	// Notes is free text for whoever reads the file: JSON has no comments,
	// and the reason a setting on the record is the way it is belongs beside
	// the setting. The loader reads it and discards it.
	Notes map[string]string `json:"notes,omitempty"`

	// RulesRequired marks a shape whose contract has to live in rules rather
	// than in the persona, because rules outrank memory and the persona and
	// win on the turn a plausible answer is already in the model's head. A
	// record that ships without them is refused at parse.
	RulesRequired bool `json:"rules_required,omitempty"`
}

// archetypeTemplate is a shape's offer in the wizard's template row.
type archetypeTemplate struct {
	// Label is what the user reads on the row.
	Label string `json:"label"`

	// Order places it. The row is a short list of starting points where the
	// first one is the most prominent, and that is an editorial decision
	// about which shape a new user most often wants, not something to be
	// derived from a label. Equal orders fall back to the label, so a shape
	// that does not care lands alphabetically among its peers.
	Order int `json:"order,omitempty"`
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
	recipe, persona := splitPersona(body)
	if strings.TrimSpace(recipe) == "" {
		return archetype{}, fmt.Errorf("archetype %s: no recipe below the frontmatter", name)
	}
	if hdr.Record != nil {
		if strings.TrimSpace(hdr.Record.ID) == "" {
			return archetype{}, fmt.Errorf("archetype %s: the record it ships has no id", name)
		}
		if strings.TrimSpace(hdr.Record.Name) == "" {
			return archetype{}, fmt.Errorf("archetype %s: the record it ships has no name", name)
		}
		if strings.TrimSpace(hdr.Record.OrchestratorPrompt) != "" {
			return archetype{}, fmt.Errorf("archetype %s: put the persona in a %q section, not in orchestrator_prompt", name, personaHeading)
		}
		if strings.TrimSpace(persona) == "" {
			return archetype{}, fmt.Errorf("archetype %s: ships a record with no %q section, so there is no agent to instantiate", name, personaHeading)
		}
		if err := checkSeedPlaceholders(persona); err != nil {
			return archetype{}, fmt.Errorf("archetype %s: %v", name, err)
		}
		hdr.Record.OrchestratorPrompt = persona
		hdr.Record.Owner = seedOwner
	} else if strings.TrimSpace(persona) != "" {
		return archetype{}, fmt.Errorf("archetype %s: has a %q section but ships no record to wear it", name, personaHeading)
	}
	if hdr.RulesRequired {
		if hdr.Record == nil {
			return archetype{}, fmt.Errorf("archetype %s: requires rules but ships no record", name)
		}
		if strings.TrimSpace(hdr.Record.Rules) == "" {
			return archetype{}, fmt.Errorf("archetype %s: says its contract belongs in rules, and ships a record carrying none", name)
		}
	}
	if hdr.Template != nil {
		if strings.TrimSpace(hdr.Template.Label) == "" {
			return archetype{}, fmt.Errorf("archetype %s: offered as a wizard template with no label", name)
		}
		if hdr.Record == nil {
			return archetype{}, fmt.Errorf("archetype %s: offered as a wizard template with no record to clone", name)
		}
	}
	return archetype{
		Slug:            strings.TrimSuffix(name, ".md"),
		Body:            recipe,
		archetypeHeader: hdr,
	}, nil
}

// personaHeading opens the section a shipped agent wears. Everything above it
// is the recipe, written to Builder about construction; everything below is
// the prompt itself, written to the model in second person.
//
// Two sections rather than two files because they are the same instructions at
// two levels of detail, and keeping them apart is what let them disagree.
// Builder is handed the recipe alone: it composes agents, and a persona it can
// copy verbatim is one it will copy instead of composing.
const personaHeading = "## Persona"

// splitPersona divides a document at its persona heading. A document with no
// such heading is all recipe.
func splitPersona(body string) (recipe, persona string) {
	marker := "\n" + personaHeading + "\n"
	i := strings.Index(body, marker)
	if i < 0 {
		if strings.HasPrefix(body, personaHeading+"\n") {
			return "", strings.TrimSpace(body[len(personaHeading)+1:])
		}
		return body, ""
	}
	return strings.TrimRight(body[:i], "\n"), strings.TrimSpace(body[i+len(marker):])
}

// archetypeRecords returns the agents the shapes ship, ready to run: the
// framework's copy of each, with its runtime snippets resolved.
func archetypeRecords() []AgentRecord {
	var out []AgentRecord
	for _, a := range loadArchetypes() {
		if a.Record == nil {
			continue
		}
		out = append(out, copySeedRecord(*a.Record))
	}
	return out
}

// archetypeBySlug returns one archetype's body, tolerating a name the model
// might use ("knowledge base" → knowledge_base, "kb" → knowledge_base).
func archetypeBySlug(slug string) (archetype, bool) {
	want := normalizeArchetypeSlug(slug)
	// An empty name matches NOTHING. The contained-word pass below is a
	// substring test, and every slug contains the empty string, so without
	// this an unset shape resolves to whichever recipe sorts first.
	if want == "" {
		return archetype{}, false
	}
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
