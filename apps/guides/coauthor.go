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
			idx := -1
			for i := range g.Sections {
				if strings.EqualFold(strings.TrimSpace(g.Sections[i].Title), title) {
					idx = i
					break
				}
			}
			if idx < 0 {
				titles := make([]string, 0, len(g.Sections))
				for _, s := range g.Sections {
					titles = append(titles, s.Title)
				}
				return "", fmt.Errorf("no section titled %q — existing sections: %s", title, strings.Join(titles, ", "))
			}
			g.Sections[idx].Markdown = md
			saveGuide(udb, g)
			return fmt.Sprintf("Updated the %q section in %q.", title, g.Title), nil
		},
	}

	return []AgentToolDef{addSection, editSection}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
