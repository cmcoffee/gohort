// Co-author wiring for workbench apps: the bound agent gets an add_section tool
// that writes a markdown section DIRECTLY into the open document's record (the
// same store the viewer reads), so "add a section about hooks" appears in the
// guide with no button. The tool is a customapps closure over this app's record
// store, handed to orchestrate via PublicHandleSendWithAppTools — orchestrate
// runs it but stays ignorant of where the data lives.
//
// "Which document is open" is tracked server-side: the workbench POSTs the
// selected record id to chat/active on every selection; the tool reads it fresh
// on each call. Stored per (user, slug) in this app's own store.

package customapps

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// activeTable holds the open-record id per app slug (within a user's store).
const activeTable = "wb_active"

// setActiveRecord records which record the workbench has open, so the
// co-author tool knows where to write. No body clears it.
func (T *CustomApps) setActiveRecord(a *appCall, body *activeRecordBody) (any, error) {
	if strings.TrimSpace(a.spec.AgentID) == "" {
		return nil, jsonapi.NotFound("this app has no chat agent bound")
	}
	id := ""
	if body != nil {
		id = body.ID
	}
	a.appdb.Set(activeTable, a.spec.Slug, strings.TrimSpace(id))
	return jsonapi.Empty(http.StatusNoContent), nil
}

// activeRecordBody is what the workbench posts on each selection.
type activeRecordBody struct {
	ID string `json:"id"`
}

func activeRecordID(udb Database, slug string) string {
	var id string
	udb.Get(activeTable, slug, &id)
	return id
}

// coauthorTools builds the per-run tools the workbench agent gets: an add_section
// tool that appends a markdown section to the OPEN document's record. The closure
// captures this app's record store (udb) + spec, so it writes exactly where the
// viewer reads. Returns nil when the app isn't a workbench (no BodyField).
func (T *CustomApps) coauthorTools(udb Database, spec AppSpec) []AgentToolDef {
	if strings.TrimSpace(spec.BodyField) == "" {
		return nil
	}
	bodyField := spec.BodyField
	return []AgentToolDef{{
		Tool: Tool{
			Name:        "add_section",
			Description: "Append a new section to the document the user currently has OPEN in the workbench. Use this to add content the user asked for (e.g. \"add a section about hooks\"): it writes the section directly into the open document and the viewer updates. Provide the section heading and its body as MARKDOWN (sub-sections as ### …, plus lists/code as needed). Do NOT restate the whole document; pass only the new section. If no document is open you'll get an error: ask the user to select or create one first.",
			Parameters: map[string]ToolParam{
				"section_title": {Type: "string", Description: "Heading for the new section (rendered as a ## heading)."},
				"markdown":      {Type: "string", Description: "The section body as markdown. Sub-sections as ### headings; lists, code fences, etc. allowed. Just the new section, not the whole document."},
			},
			Required: []string{"section_title", "markdown"},
		},
		// Authoring action — only one append per batch (don't let a single reply
		// fan out into many duplicate writes).
		SingleFirePerBatch: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			title := strings.TrimSpace(fmt.Sprint(args["section_title"]))
			md := strings.TrimSpace(fmt.Sprint(args["markdown"]))
			if md == "" {
				return "", fmt.Errorf("markdown is required: pass the section body")
			}
			id := activeRecordID(udb, spec.Slug)
			if id == "" {
				return "", fmt.Errorf("no document is open: ask the user to select or create one in the list first")
			}
			tbl := recTable(spec.Slug)
			var rec map[string]any
			if !udb.Get(tbl, id, &rec) || rec == nil {
				return "", fmt.Errorf("the open document could not be loaded (it may have been deleted)")
			}
			section := md
			if title != "" {
				section = "## " + title + "\n\n" + md
			}
			existing, _ := rec[bodyField].(string)
			rec[bodyField] = strings.TrimSpace(strings.TrimSpace(existing) + "\n\n" + section)
			udb.Set(tbl, id, rec)
			docName, _ := rec["title"].(string)
			if strings.TrimSpace(docName) == "" {
				docName = "the document"
			}
			return fmt.Sprintf("Added the %q section to %q. It now shows in the document viewer.", title, docName), nil
		},
	}}
}
