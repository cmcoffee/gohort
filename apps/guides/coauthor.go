// Co-author tools for the Guide Author agent: add_section / edit_section write
// directly into the OPEN guide's section list (the store the viewer renders), so
// "add an introduction" appears in the document. Built as closures over this
// app's guide store and injected into the agent's run via
// PublicHandleSendWithAppTools — orchestrate runs them, ignorant of guide storage.
package guides

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func (T *Guides) coauthorTools(udb Database) []AgentToolDef {
	// openGuide resolves the active guide for this turn, fresh each call.
	openGuide := func() (Guide, bool) {
		id := activeGuideID(udb)
		if id == "" {
			return Guide{}, false
		}
		return loadGuide(udb, id)
	}
	// findIdx locates a section by case-insensitive title, returning its index in
	// the guide's slice (-1 if absent).
	findIdx := func(g Guide, title string) int {
		title = strings.TrimSpace(title)
		for i := range g.Sections {
			if strings.EqualFold(strings.TrimSpace(g.Sections[i].Title), title) {
				return i
			}
		}
		return -1
	}
	sectionTitles := func(g Guide) string {
		var ts []string
		for _, s := range g.sorted() {
			ts = append(ts, s.Title)
		}
		return strings.Join(ts, ", ")
	}

	addSection := AgentToolDef{
		Tool: Tool{
			Name:        "add_section",
			Description: "Append a new section to the guide the user has OPEN. Provide the section title and its BODY as markdown (sub-headings as ###, lists, fenced code — do NOT repeat the title inside the body). Use this to add content the user asks for; it lands in the document and the viewer updates. Errors if no guide is open — ask the user to select or create one.",
			Parameters: map[string]ToolParam{
				"section_title": {Type: "string", Description: "Title of the new section (shown as a numbered heading + in the table of contents)."},
				"markdown":      {Type: "string", Description: "The section body as markdown. Substantive content, not a placeholder. No top-level heading — the title is separate."},
			},
			Required: []string{"section_title", "markdown"},
		},
		SingleFirePerBatch: true,
		Handler: func(args map[string]any) (string, error) {
			title := strings.TrimSpace(fmt.Sprint(args["section_title"]))
			md := strings.TrimSpace(fmt.Sprint(args["markdown"]))
			if md == "" {
				return "", fmt.Errorf("markdown is required — pass the section body")
			}
			g, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no guide is open — ask the user to select or create one first")
			}
			g.Sections = append(g.Sections, Section{ID: newID(), Title: title, Markdown: md, Order: g.nextOrder()})
			saveGuide(udb, g)
			return fmt.Sprintf("Added the %q section to %q (now %d section%s).", title, g.Title, len(g.Sections), plural(len(g.Sections))), nil
		},
	}

	editSection := AgentToolDef{
		Tool: Tool{
			Name:        "edit_section",
			Description: "Replace the body of an EXISTING section in the open guide, matched by its title (case-insensitive). Provide the new markdown body. Use for revisions the user asks for (\"expand the install section\", \"fix the example in Setup\"). Errors if no guide is open or no section matches.",
			Parameters: map[string]ToolParam{
				"section_title": {Type: "string", Description: "Title of the section to edit (must match an existing section)."},
				"markdown":      {Type: "string", Description: "The new section body as markdown (replaces the old body). No top-level heading."},
			},
			Required: []string{"section_title", "markdown"},
		},
		SingleFirePerBatch: true,
		Handler: func(args map[string]any) (string, error) {
			title := strings.TrimSpace(fmt.Sprint(args["section_title"]))
			md := strings.TrimSpace(fmt.Sprint(args["markdown"]))
			g, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no guide is open — ask the user to select or create one first")
			}
			idx := findIdx(g, title)
			if idx < 0 {
				return "", fmt.Errorf("no section titled %q — existing sections: %s", title, sectionTitles(g))
			}
			g.Sections[idx].Markdown = md
			saveGuide(udb, g)
			return fmt.Sprintf("Updated the %q section in %q.", title, g.Title), nil
		},
	}

	listSections := AgentToolDef{
		Tool: Tool{
			Name:        "list_sections",
			Description: "List the sections of the OPEN guide, in order, with their titles. Call this to see the guide's current structure before renaming, deleting, moving, or editing a section — so you use the exact existing titles and correct positions. No arguments.",
		},
		Handler: func(args map[string]any) (string, error) {
			g, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no guide is open — ask the user to select or create one first")
			}
			secs := g.sorted()
			if len(secs) == 0 {
				return fmt.Sprintf("%q has no sections yet.", g.Title), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%q has %d section%s:\n", g.Title, len(secs), plural(len(secs)))
			for i, s := range secs {
				fmt.Fprintf(&b, "%d. %s\n", i+1, s.Title)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}

	deleteSection := AgentToolDef{
		Tool: Tool{
			Name:        "delete_section",
			Description: "Remove a section from the open guide, matched by its title (case-insensitive). Use when the user asks to drop or remove a section. This can't be undone from here, so only delete when the user clearly asked.",
			Parameters: map[string]ToolParam{
				"section_title": {Type: "string", Description: "Title of the section to remove (must match an existing section)."},
			},
			Required: []string{"section_title"},
		},
		SingleFirePerBatch: true,
		Handler: func(args map[string]any) (string, error) {
			title := strings.TrimSpace(fmt.Sprint(args["section_title"]))
			g, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no guide is open — ask the user to select or create one first")
			}
			idx := findIdx(g, title)
			if idx < 0 {
				return "", fmt.Errorf("no section titled %q — existing sections: %s", title, sectionTitles(g))
			}
			removed := g.Sections[idx].Title
			g.Sections = append(g.Sections[:idx], g.Sections[idx+1:]...)
			normalizeOrder(&g)
			saveGuide(udb, g)
			return fmt.Sprintf("Removed the %q section from %q (%d left).", removed, g.Title, len(g.Sections)), nil
		},
	}

	renameSection := AgentToolDef{
		Tool: Tool{
			Name:        "rename_section",
			Description: "Rename a section in the open guide (changes its heading + table-of-contents entry; the body is untouched). Match the existing section by its current title.",
			Parameters: map[string]ToolParam{
				"section_title": {Type: "string", Description: "Current title of the section."},
				"new_title":     {Type: "string", Description: "New title for the section."},
			},
			Required: []string{"section_title", "new_title"},
		},
		SingleFirePerBatch: true,
		Handler: func(args map[string]any) (string, error) {
			title := strings.TrimSpace(fmt.Sprint(args["section_title"]))
			newTitle := strings.TrimSpace(fmt.Sprint(args["new_title"]))
			if newTitle == "" {
				return "", fmt.Errorf("new_title is required")
			}
			g, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no guide is open — ask the user to select or create one first")
			}
			idx := findIdx(g, title)
			if idx < 0 {
				return "", fmt.Errorf("no section titled %q — existing sections: %s", title, sectionTitles(g))
			}
			g.Sections[idx].Title = newTitle
			saveGuide(udb, g)
			return fmt.Sprintf("Renamed %q to %q.", title, newTitle), nil
		},
	}

	moveSection := AgentToolDef{
		Tool: Tool{
			Name:        "move_section",
			Description: "Reorder a section: move it to a 1-based position in the open guide (1 = first). Use to rearrange the document — e.g. move \"Troubleshooting\" to the end, or move \"Overview\" to position 1. Call list_sections first to see current positions.",
			Parameters: map[string]ToolParam{
				"section_title": {Type: "string", Description: "Title of the section to move."},
				"position":      {Type: "integer", Description: "Target 1-based position (1 = first). Values past the end move it last."},
			},
			Required: []string{"section_title", "position"},
		},
		SingleFirePerBatch: true,
		Handler: func(args map[string]any) (string, error) {
			title := strings.TrimSpace(fmt.Sprint(args["section_title"]))
			pos := coerceIntArg(args["position"])
			g, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no guide is open — ask the user to select or create one first")
			}
			secs := g.sorted()
			idx := -1
			for i := range secs {
				if strings.EqualFold(strings.TrimSpace(secs[i].Title), title) {
					idx = i
					break
				}
			}
			if idx < 0 {
				return "", fmt.Errorf("no section titled %q — existing sections: %s", title, sectionTitles(g))
			}
			reordered, target := reorderSections(secs, idx, pos-1)
			g.Sections = reordered
			saveGuide(udb, g)
			return fmt.Sprintf("Moved %q to position %d in %q.", title, target+1, g.Title), nil
		},
	}

	return []AgentToolDef{addSection, editSection, listSections, renameSection, deleteSection, moveSection}
}

// reorderSections moves the section at idx to clampedTarget (0-based), clamping
// the target into range, then reassigns 1..N Order values. Returns the new slice
// and the resolved 0-based target. secs is treated as the current display order;
// it is not mutated (a copy is taken).
func reorderSections(secs []Section, idx, target int) ([]Section, int) {
	out := append([]Section(nil), secs...)
	if idx < 0 || idx >= len(out) {
		return out, idx
	}
	if target < 0 {
		target = 0
	}
	if target > len(out)-1 {
		target = len(out) - 1
	}
	moved := out[idx]
	out = append(out[:idx], out[idx+1:]...)
	out = append(out[:target], append([]Section{moved}, out[target:]...)...)
	for i := range out {
		out[i].Order = i + 1
	}
	return out, target
}

// normalizeOrder reassigns 1..N Order values in current sorted order, closing any
// gaps left by a deletion.
func normalizeOrder(g *Guide) {
	secs := g.sorted()
	for i := range secs {
		secs[i].Order = i + 1
	}
	g.Sections = secs
}

// coerceIntArg pulls an int from an LLM-supplied arg (float64 / int / numeric
// string).
func coerceIntArg(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		out := 0
		for _, c := range strings.TrimSpace(n) {
			if c < '0' || c > '9' {
				break
			}
			out = out*10 + int(c-'0')
		}
		return out
	}
	return 0
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
