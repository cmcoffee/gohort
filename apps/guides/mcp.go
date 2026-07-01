// MCP controls for Guides: tools exposed on gohort's inbound MCP server (/mcp/)
// so an external MCP client (e.g. Claude Desktop) can drive a user's guides —
// list, read, create, and add sections — over the bridge-key auth. Each handler
// is scoped to the bridge-key owner and operates on that user's guide store via
// the same loadGuide/saveGuide/etc. functions the web UI and co-author use, so
// there is exactly one storage path. This is the canonical example of an app
// registering against the core.RegisterMCPTool seam — the MCP server itself
// knows nothing about guides.
package guides

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func registerGuidesMCPTools() {
	RegisterMCPTool(MCPToolSpec{
		Name:        "guides_list",
		Description: "List the user's guides (id, title, subtitle, section count), newest first. Use the returned id with guides_read / guides_add_section.",
		InputSchema: map[string]any{"type": "object"},
		Handler:     guidesMCPList,
	})
	RegisterMCPTool(MCPToolSpec{
		Name:        "guides_read",
		Description: "Read a guide as Markdown (title, subtitle, and every section in order). Pass the guide id from guides_list.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "The guide id (from guides_list)."},
			},
			"required": []string{"id"},
		},
		Handler: guidesMCPRead,
	})
	RegisterMCPTool(MCPToolSpec{
		Name:        "guides_create",
		Description: "Create a new, empty guide and return its id. Add content afterward with guides_add_section.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":    map[string]any{"type": "string", "description": "The guide title."},
				"subtitle": map[string]any{"type": "string", "description": "Optional one-line description."},
			},
			"required": []string{"title"},
		},
		Handler: guidesMCPCreate,
	})
	RegisterMCPTool(MCPToolSpec{
		Name:        "guides_add_section",
		Description: "Append a section to a guide. The markdown is the section BODY (don't repeat the title in it); use sub-headings (### …), lists, and fenced code blocks for structure.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"guide_id": map[string]any{"type": "string", "description": "The guide id (from guides_list)."},
				"title":    map[string]any{"type": "string", "description": "The section title."},
				"markdown": map[string]any{"type": "string", "description": "The section body as Markdown."},
			},
			"required": []string{"guide_id", "title", "markdown"},
		},
		Handler: guidesMCPAddSection,
	})
}

// guidesUserDB resolves the calling owner's guide store — the SAME store the
// guides web UI reads/writes. Guides live in the guides app's own bucket
// (RootDB.Bucket("guides"), which is what the framework assigns as the app's
// T.DB via get_agentstore(Name())), NOT RootDB itself — reading RootDB directly
// finds no guides. Empty owner / unwired DB ⇒ nil (guarded by callers).
func guidesUserDB(owner string) Database {
	if RootDB == nil || owner == "" {
		return nil
	}
	return UserDB(RootDB.Bucket("guides"), owner)
}

func guidesMCPList(_ context.Context, owner string, _ map[string]any) (string, error) {
	udb := guidesUserDB(owner)
	if udb == nil {
		return "", fmt.Errorf("no store for user")
	}
	guides := listGuides(udb)
	if len(guides) == 0 {
		return "You have no guides yet. Create one with guides_create.", nil
	}
	var b strings.Builder
	for _, g := range guides {
		fmt.Fprintf(&b, "- %s — %q", g.ID, g.Title)
		if g.Subtitle != "" {
			fmt.Fprintf(&b, " (%s)", g.Subtitle)
		}
		fmt.Fprintf(&b, " — %d section(s)\n", len(g.Sections))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func guidesMCPRead(_ context.Context, owner string, args map[string]any) (string, error) {
	udb := guidesUserDB(owner)
	if udb == nil {
		return "", fmt.Errorf("no store for user")
	}
	id := strings.TrimSpace(mcpStr(args, "id"))
	if id == "" {
		return "", fmt.Errorf("id is required")
	}
	g, ok := loadGuide(udb, id)
	if !ok {
		return "", fmt.Errorf("no guide with id %q (use guides_list)", id)
	}
	return renderGuideMarkdown(g), nil
}

func guidesMCPCreate(_ context.Context, owner string, args map[string]any) (string, error) {
	udb := guidesUserDB(owner)
	if udb == nil {
		return "", fmt.Errorf("no store for user")
	}
	title := strings.TrimSpace(mcpStr(args, "title"))
	if title == "" {
		return "", fmt.Errorf("title is required")
	}
	g := saveGuide(udb, Guide{ID: newID(), Title: title, Subtitle: strings.TrimSpace(mcpStr(args, "subtitle"))})
	return fmt.Sprintf("Created guide %q (id %s). Add content with guides_add_section.", g.Title, g.ID), nil
}

func guidesMCPAddSection(_ context.Context, owner string, args map[string]any) (string, error) {
	udb := guidesUserDB(owner)
	if udb == nil {
		return "", fmt.Errorf("no store for user")
	}
	gid := strings.TrimSpace(mcpStr(args, "guide_id"))
	g, ok := loadGuide(udb, gid)
	if !ok {
		return "", fmt.Errorf("no guide with id %q (use guides_list)", gid)
	}
	title := strings.TrimSpace(mcpStr(args, "title"))
	if title == "" {
		title = "New section"
	}
	g.Sections = append(g.Sections, Section{ID: newID(), Title: title, Markdown: strings.TrimSpace(mcpStr(args, "markdown")), Order: g.nextOrder()})
	saveGuideRev(udb, g, "Added section (via MCP): "+title)
	return fmt.Sprintf("Added section %q to guide %q.", title, g.Title), nil
}

// mcpStr reads a string argument, tolerating a nil map / non-string value.
func mcpStr(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	s, _ := args[key].(string)
	return s
}
