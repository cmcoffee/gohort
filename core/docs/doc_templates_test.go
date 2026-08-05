package docs

import (
	"strings"
	"testing"
)

// Templates land in the sections editor, so their bodies have to be real
// outlines. A body with no headings parses as one prose blob and the
// outline is useless for it — which defeats the point of offering it
// only for markdown.
func TestMarkdownTemplatesAreOutlines(t *testing.T) {
	if len(MarkdownDocTemplates) == 0 {
		t.Fatal("no templates declared")
	}
	seen := map[string]bool{}
	for _, tpl := range MarkdownDocTemplates {
		t.Run(tpl.Name, func(t *testing.T) {
			if strings.TrimSpace(tpl.Name) == "" {
				t.Fatal("template with no name — Name doubles as the snippet name")
			}
			if seen[strings.ToLower(tpl.Name)] {
				t.Errorf("duplicate template name %q", tpl.Name)
			}
			seen[strings.ToLower(tpl.Name)] = true
			if strings.TrimSpace(tpl.Description) == "" {
				t.Error("no description — it's the only thing distinguishing rows in the picker")
			}
			body := tpl.Body
			if strings.TrimSpace(body) == "" {
				t.Fatal("empty body")
			}
			if !strings.HasPrefix(body, "# ") {
				t.Error("body should open with a top-level title heading")
			}
			// Count "## " section headings — the blocks the outline shows.
			sections := 0
			for _, line := range strings.Split(body, "\n") {
				if strings.HasPrefix(line, "## ") {
					sections++
				}
			}
			if sections < 3 {
				t.Errorf("only %d sections — too thin to be worth a template", sections)
			}
		})
	}
}

// The serializer emits no trailing newline, so a body carrying one gets
// normalized away on the first edit. Harmless, but it means the stored
// snippet differs from the template it came from before the user has
// typed anything. Keep them already-normalized instead.
func TestMarkdownTemplateBodiesHaveNoLeadingBlank(t *testing.T) {
	for _, tpl := range MarkdownDocTemplates {
		if strings.HasPrefix(tpl.Body, "\n") {
			t.Errorf("%q: body starts with a blank line", tpl.Name)
		}
	}
}

// The Agent prompt template's headings mirror the agent editor's
// declared outline. codewriter can't import orchestrate, so the
// correspondence is by convention — this pins the headings so a drift
// shows up as a failing test rather than as a prompt that lands in the
// agent editor as one unsectioned blob.
func TestAgentPromptTemplateMatchesEditorOutline(t *testing.T) {
	var body string
	for _, tpl := range MarkdownDocTemplates {
		if tpl.Name == "Agent prompt" {
			body = tpl.Body
		}
	}
	if body == "" {
		t.Fatal("Agent prompt template missing")
	}
	// Keep in step with orchestratorPromptSections in apps/orchestrate.
	for _, heading := range []string{
		"## Role & voice",
		"## Approach",
		"## Rules",
		"## Failure modes",
		"## Output format",
	} {
		if !strings.Contains(body, heading) {
			t.Errorf("missing %q — the agent editor declares this section, so a prompt drafted here should fill it", heading)
		}
	}
}
