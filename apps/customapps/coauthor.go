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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/docs"
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

// --- a co-authored document (workbench editable) ---------------------------
//
// A workbench whose section says editable is a document written with the
// agent beside it (ui.WorkbenchPanel.ProposeEdits): it opens in an editor,
// the person selects a passage and asks about it, and the agent proposes a
// change reviewed as a diff of that passage. Every send carries the document
// as it is on screen, so the agent quotes what the person sees, and the agent
// changes it only by proposing: a direct write (add_section, "Add to") would
// land under the person's unsaved edits and be lost to their next Save.

// workbenchEditable reports whether the app's workbench section is editable,
// read from the sections as authored. An app written before the option, or
// with no sections kept, is not. A section may leave its kind to be inferred
// from its fields, so one with no kind counts too: BodyField being set is
// what says the app has a workbench.
//
// A section that declares selection_actions and says nothing about editable
// is editable: quick actions for a highlighted passage mean a document the
// person writes. Builder's rebuilt song app declared the actions and left
// editable out, and got a viewer with nothing to highlight. editable: false
// still says no. orchestrate's buildWorkbench reads a section the same way.
func workbenchEditable(spec AppSpec) bool {
	for _, s := range workbenchSections(spec) {
		switch v := s["editable"].(type) {
		case bool:
			return v
		case float64:
			return v != 0
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true", "yes", "1":
				return true
			case "false", "no", "0":
				return false
			}
		}
		if acts, _ := s["selection_actions"].([]any); len(acts) > 0 {
			return true
		}
	}
	return false
}

// workbenchSections are the app's sections as authored that may be its
// workbench: kind workbench, or no kind given. None for an app with no
// sections kept or no BodyField, which is what says it has a workbench.
func workbenchSections(spec AppSpec) []map[string]any {
	if len(spec.Sections) == 0 || strings.TrimSpace(spec.BodyField) == "" {
		return nil
	}
	var secs, out []map[string]any
	if json.Unmarshal(spec.Sections, &secs) != nil {
		return nil
	}
	for _, s := range secs {
		if kind, _ := s["kind"].(string); strings.TrimSpace(kind) != "" && !strings.EqualFold(strings.TrimSpace(kind), "workbench") {
			continue
		}
		out = append(out, s)
	}
	return out
}

// coauthorSend is what a co-authored document's send carries beside the
// message (the workbench's send context and uiCoauthor's pinned passage).
type coauthorSend struct {
	Document struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"document"`
	Selection agenthost.Selection `json:"selection"`
}

// maxCoauthorDoc bounds the document a turn is shown.
const maxCoauthorDoc = 60000

// coauthorTurnContext is what the turn is told about the open document: the
// text as the person has it, how to change it, and the passage they selected
// when they pinned one. "" when no document is open.
func coauthorTurnContext(send coauthorSend) string {
	if strings.TrimSpace(send.Document.ID) == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("The document open beside this conversation, as it is now (edits not yet saved included):\n")
	text := send.Document.Text
	if strings.TrimSpace(text) == "" {
		b.WriteString("(The document is empty.)\n")
	} else {
		if r := []rune(text); len(r) > maxCoauthorDoc {
			text = string(r[:maxCoauthorDoc]) + "\n[... the rest is not shown]"
		}
		f := docs.DocFence(text)
		b.WriteString(f + "\n" + text + "\n" + f + "\n")
	}
	b.WriteString("\nTo change it, call propose_edit with find set to the text you are replacing, copied exactly from the document, " +
		"and replace set to its new text; to add to it, replace the passage the addition follows with that passage plus the addition. " +
		"An empty document is written with an empty find. The person sees a diff and applies it or not: nothing changes until they do.\n")
	if sel := agenthost.SelectionContext(send.Selection); sel != "" {
		b.WriteString("\n" + sel + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// handleCoauthorSend runs a send for a co-authored document: the document and
// the pinned passage go into the turn, and the agent's only way to change the
// document is to propose. others are the app's action tools, kept. open, when
// given, is set to the id of the document the send carries.
func (T *CustomApps) handleCoauthorSend(w http.ResponseWriter, r *http.Request, orch agenthost.Host, agent agenthost.Agent, others []AgentToolDef, open *string) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var send coauthorSend
	_ = json.Unmarshal(raw, &send)
	if open != nil {
		*open = strings.TrimSpace(send.Document.ID)
	}
	pub := &agenthost.Publisher{}
	tools := append([]AgentToolDef{agenthost.ProposeEditTool(pub)}, others...)
	if turn := coauthorTurnContext(send); turn != "" {
		r = agenthost.WithTurnContext(r, turn)
	}
	orch.PublicHandleSendWithAppToolsPublishing(w, r, agent, tools, pub)
}

// patchRecord merges what it is sent into the record ?id= names: the fields
// given replace theirs, every other field stays. A workbench editor saves
// through it, sending the id and the edited field only, which the records
// endpoint (a whole-record save) would take as the whole record and drop the
// rest. The key and created stamp are the record's own.
func (T *CustomApps) patchRecord(a *appCall, in map[string]any) (any, error) {
	id, err := recordID(a)
	if err != nil {
		return nil, err
	}
	recordWriteMu.Lock()
	defer recordWriteMu.Unlock()
	tbl := recTable(a.spec.Slug)
	var rec map[string]any
	if !a.appdb.Get(tbl, id, &rec) || rec == nil {
		return nil, jsonapi.NotFound("no record " + id)
	}
	files := recordFilesField(a.spec)
	for k, v := range in {
		// The kept files are the server's list (record_files.go).
		if k == a.spec.RecordKey || k == "created" || k == "id" || k == files {
			continue
		}
		rec[k] = v
	}
	a.appdb.Set(tbl, id, rec)
	noteChange(recordsChangeKey(a.owner, a.spec.Slug, a.User))
	return rec, nil
}
