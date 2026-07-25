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
package orchestrate

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

//go:embed archetypes/*.md
var archetypeFS embed.FS

// archetype is one build recipe: a slug (the filename stem), a one-line summary
// pulled from the doc's first heading, and the full markdown body.
type archetype struct {
	Slug    string
	Summary string
	Body    string
}

// loadArchetypes reads every embedded archetype doc. Deterministic order (by
// slug) so the list tool and any log line are stable across runs.
func loadArchetypes() []archetype {
	entries, err := fs.ReadDir(archetypeFS, "archetypes")
	if err != nil {
		return nil
	}
	var out []archetype
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body, err := archetypeFS.ReadFile("archetypes/" + e.Name())
		if err != nil {
			continue
		}
		out = append(out, archetype{
			Slug:    strings.TrimSuffix(e.Name(), ".md"),
			Summary: archetypeSummary(string(body)),
			Body:    string(body),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// archetypeSummary extracts a one-line description: the paragraph after the
// first "# " heading, collapsed to a single line. Falls back to the heading.
func archetypeSummary(body string) string {
	lines := strings.Split(body, "\n")
	heading := ""
	for i, ln := range lines {
		if strings.HasPrefix(ln, "# ") {
			heading = strings.TrimSpace(strings.TrimPrefix(ln, "# "))
			// The first non-empty line after the heading is the summary.
			for _, next := range lines[i+1:] {
				if s := strings.TrimSpace(next); s != "" {
					return s
				}
			}
			break
		}
	}
	return heading
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

func normalizeArchetypeSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "-", "_")
	switch s {
	case "kb", "knowledgebase", "knowledge":
		return "knowledge_base"
	case "chat", "assistant", "general", "conversation":
		return "conversational"
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
