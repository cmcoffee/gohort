package orchestrate

// A maintainer fixes a broken tool while the owner is away.
//
// An agent's tools are its owner's, and a maintainer cannot edit them. When
// one breaks (an endpoint moved, a parameter was renamed), the agent stays
// broken until the owner is back. So a maintainer can save a corrected copy
// that only this agent uses: a new tool, "<tool>_fix", scoped to this agent
// in the owner's pool, with the original switched off for this agent alone.
// The owner is told, and keeps it (the fix goes into the original, which is
// switched back on) or discards it (the copy goes, the original comes back).
//
// What a fix may change is its HTTP shape and nothing else: descriptions, the
// URL, the method, the body, headers, parameters, and the actions of a
// toolbox. Everything else is the original's, copied over whatever was sent:
// the credential (so the owner's key's allowed hosts and its read-only lend
// still hold the copy), any response pipe, and every other field. Scripts are
// not offered at all: a maintainer's code would run in the owner's agent.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/ui"
)

// ToolFix is a maintainer's corrected copy of one of the owner's tools, in
// use by this agent in place of the original (AgentRecord.ToolFixes).
type ToolFix struct {
	Tool string    `json:"tool"` // the original
	Fix  string    `json:"fix"`  // the copy, scoped to this agent
	By   string    `json:"by"`
	At   time.Time `json:"at"`
}

// toolFixName is the copy's name for an original.
func toolFixName(tool string) string { return tool + "_fix" }

// fixableTool reports whether a maintainer may fix t: an HTTP tool or toolbox,
// no script anywhere in it.
//
// An API tool's URL is its CommandTemplate, which is a shell command only in
// shell mode, and shell mode is not offered.
func fixableTool(t TempTool) bool {
	if strings.TrimSpace(t.ScriptBody) != "" {
		return false
	}
	switch t.Mode {
	case TempToolModeAPI:
		return true
	case TempToolModeToolbox:
		for _, a := range t.Actions {
			if strings.TrimSpace(a.CommandTemplate) != "" {
				return false
			}
		}
		return true
	}
	return false
}

// agentPoolTools is the owner's pool tools this agent loads, by name.
func agentPoolTools(owner string, a AgentRecord) map[string]PersistentTempTool {
	out := map[string]PersistentTempTool{}
	if AuthDB == nil {
		return out
	}
	g := newPoolToolGate(AuthDB(), a)
	for _, p := range LoadPersistentTempTools(AuthDB(), owner) {
		if g.reaches(p) && g.admits(p) {
			out[p.Tool.Name] = p
		}
	}
	return out
}

// fixedShape is base with the HTTP shape of sent laid over it, and nothing
// else: the credential, pipes and every other field stay base's.
func fixedShape(base, sent TempTool) TempTool {
	out := base
	out.Actions = nil
	out.Description = sent.Description
	if base.Mode == TempToolModeAPI {
		out.CommandTemplate = sent.CommandTemplate // the URL, in api mode
	}
	out.Method, out.BodyTemplate = sent.Method, sent.BodyTemplate
	out.Headers, out.ContentType = sent.Headers, sent.ContentType
	out.Params, out.Required = sent.Params, sent.Required
	if base.Mode == TempToolModeToolbox {
		was := map[string]TempToolAction{}
		for _, a := range base.Actions {
			was[a.Name] = a
		}
		for _, s := range sent.Actions {
			// A new action starts from nothing: no pipe, no mark, and no command
			// (an original with one is not offered to fix, fixableTool).
			a := was[s.Name]
			a.Name, a.Description = s.Name, s.Description
			a.URLTemplate, a.Method, a.BodyTemplate = s.URLTemplate, s.Method, s.BodyTemplate
			a.Headers, a.ContentType = s.Headers, s.ContentType
			a.Params, a.Required, a.Disabled = s.Params, s.Required, s.Disabled
			out.Actions = append(out.Actions, a)
		}
	}
	return out
}

// toolFixTarget is the agent a tool-fix call names, and whether the caller is
// its owner or one of its maintainers (with the owner's store).
func (T *OrchestrateApp) toolFixTarget(c *jsonapi.Call) (a AgentRecord, db Database, maintainer bool, err error) {
	id := c.R.PathValue("id")
	if own, ok := loadAgent(c.UDB, id); ok && own.Owner == c.User {
		return own, c.UDB, false, nil
	}
	if m, odb, ok := T.maintainedAgentRecord(c.User, id); ok {
		return m, odb, true, nil
	}
	return AgentRecord{}, nil, false, errNotFound
}

// agentFixableTools lists the tools a maintainer may fix on this agent, and
// whether each is running as a fix now.
func (T *OrchestrateApp) agentFixableTools(c *jsonapi.Call) (any, error) {
	a, _, _, err := T.toolFixTarget(c)
	if err != nil {
		return nil, err
	}
	fixed := map[string]bool{}
	isFix := map[string]bool{}
	for _, f := range a.ToolFixes {
		fixed[f.Tool], isFix[f.Fix] = true, true
	}
	type row struct {
		Name        string `json:"name"`
		Mode        string `json:"mode"`
		Description string `json:"description"`
		Fixed       bool   `json:"fixed"`
	}
	out := []row{}
	pool := agentPoolTools(a.Owner, a)
	// An original running as a fix is switched off for this agent, so it is
	// not in pool: add it back from the owner's whole pool.
	if AuthDB != nil {
		for _, f := range a.ToolFixes {
			if p, ok := UserToolByName(AuthDB(), a.Owner, f.Tool); ok {
				pool[f.Tool] = p
			}
		}
	}
	for name, p := range pool {
		if isFix[name] || !fixableTool(p.Tool) {
			continue
		}
		out = append(out, row{Name: name, Mode: p.Tool.Mode, Description: firstLine(p.Tool.Description), Fixed: fixed[name]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// toolFixOriginal is the tool a fix call names, if it is one that can be fixed.
func toolFixOriginal(c *jsonapi.Call, a AgentRecord) (string, PersistentTempTool, error) {
	tool := strings.TrimSpace(c.R.URL.Query().Get("tool"))
	orig, ok := UserToolByName(AuthDB(), a.Owner, tool)
	if !ok || !fixableTool(orig.Tool) {
		return tool, orig, jsonapi.NotFound("no tool of this agent's that can be fixed is called " + tool)
	}
	return tool, orig, nil
}

// agentToolFixGet is one tool's definition for the fix form: the running fix
// when there is one, else the original, as only the shape a fix may change.
func (T *OrchestrateApp) agentToolFixGet(c *jsonapi.Call) (any, error) {
	a, _, _, err := T.toolFixTarget(c)
	if err != nil {
		return nil, err
	}
	tool, orig, err := toolFixOriginal(c, a)
	if err != nil {
		return nil, err
	}
	shown := orig.Tool
	for _, f := range a.ToolFixes {
		if f.Tool == tool {
			if p, ok := UserToolByName(AuthDB(), a.Owner, f.Fix); ok {
				shown = p.Tool
			}
		}
	}
	b, _ := json.MarshalIndent(fixedShape(TempTool{Mode: shown.Mode}, shown), "", "  ")
	return map[string]string{"tool": tool, "definition": string(b)}, nil
}

// agentToolFixSave saves a maintainer's fix.
func (T *OrchestrateApp) agentToolFixSave(c *jsonapi.Call, body struct {
	Definition string `json:"definition"`
}) (any, error) {
	a, db, maintainer, err := T.toolFixTarget(c)
	if err != nil {
		return nil, err
	}
	tool, orig, err := toolFixOriginal(c, a)
	if err != nil {
		return nil, err
	}
	if !maintainer {
		return nil, jsonapi.BadRequest("it is your tool: change it under Extensions > Tools, and every agent of yours that uses it gets the change")
	}
	if _, uses := agentPoolTools(a.Owner, a)[tool]; !uses && !hasToolFix(a, tool) {
		return nil, jsonapi.BadRequest(tool + " is not one of this agent's tools")
	}
	var sent TempTool
	if err := json.Unmarshal([]byte(body.Definition), &sent); err != nil {
		return nil, jsonapi.BadRequest("the definition is not valid JSON: " + err.Error())
	}
	fixName := toolFixName(tool)
	if _, taken := UserToolByName(AuthDB(), a.Owner, fixName); taken && !hasToolFix(a, tool) {
		return nil, jsonapi.BadRequest(a.Owner + " already has a tool called " + fixName + ": it cannot be used for a fix")
	}
	fix := fixedShape(orig.Tool, sent)
	fix.Name = fixName
	if hasToolFix(a, tool) {
		UpdatePersistentTempTool(AuthDB(), a.Owner, fix)
	} else if err := AdminPersistTempTool(AuthDB(), a.Owner, fix); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	SetUserToolScopeAgents(AuthDB(), a.Owner, fixName, []string{a.ID})
	if !namedIn(a.DisabledPersistentTools, tool) {
		a.DisabledPersistentTools = append(a.DisabledPersistentTools, tool)
	}
	kept := a.ToolFixes[:0]
	for _, f := range a.ToolFixes {
		if f.Tool != tool {
			kept = append(kept, f)
		}
	}
	a.ToolFixes = append(kept, ToolFix{Tool: tool, Fix: fixName, By: c.User, At: time.Now()})
	if _, err := saveAgentAs(db, a, "tool "+tool+" fixed by "+c.User); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	notices.Record(RootDB, notices.Notice{
		Owner: a.Owner, Agent: a.ID, Kind: notices.KindBlocked,
		Title: c.User + " fixed " + tool + " on " + chFirst(a.Name, a.ID),
		Body: "Their corrected copy, " + fixName + ", is in use by this agent in place of your " + tool + ", which is switched off for this agent only. " +
			"Keep it to put the fix into your " + tool + ", or discard it to go back: both on the agent's page, under Fixes from maintainers.",
	})
	Log("[orchestrate.agents] %s fixed %s's tool %q on agent %q as %q", c.User, a.Owner, tool, a.Name, fixName)
	return jsonapi.Empty(http.StatusNoContent), nil
}

func hasToolFix(a AgentRecord, tool string) bool {
	for _, f := range a.ToolFixes {
		if f.Tool == tool {
			return true
		}
	}
	return false
}

// agentToolFixes lists the fixes in use on this agent, for its owner.
func (T *OrchestrateApp) agentToolFixes(c *jsonapi.Call) (any, error) {
	a, _, _, err := T.toolFixTarget(c)
	if err != nil {
		return nil, err
	}
	out := a.ToolFixes
	if out == nil {
		out = []ToolFix{}
	}
	return out, nil
}

// agentToolFixResolve is the owner's answer to a fix: keep puts its shape into
// the original; either way the copy goes and the original is switched back on
// for this agent.
func (T *OrchestrateApp) agentToolFixResolve(c *jsonapi.Call, _ struct{}) (any, error) {
	a, db, maintainer, err := T.toolFixTarget(c)
	if err != nil {
		return nil, err
	}
	if maintainer {
		return nil, jsonapi.Forbidden("keeping or discarding a fix is " + a.Owner + "'s to decide")
	}
	q := c.R.URL.Query()
	tool := strings.TrimSpace(q.Get("tool"))
	if !hasToolFix(a, tool) {
		return nil, jsonapi.NotFound("no fix of " + tool + " on this agent")
	}
	fixName := toolFixName(tool)
	if q.Get("keep") == "1" {
		orig, ok := UserToolByName(AuthDB(), a.Owner, tool)
		fix, fok := UserToolByName(AuthDB(), a.Owner, fixName)
		if !ok || !fok {
			return nil, jsonapi.NotFound("the tool or its fix is no longer there")
		}
		UpdatePersistentTempTool(AuthDB(), a.Owner, fixedShape(orig.Tool, fix.Tool))
	}
	DeletePersistentTempTool(AuthDB(), a.Owner, fixName)
	a.DisabledPersistentTools = withoutOne(a.DisabledPersistentTools, tool)
	kept := []ToolFix{}
	for _, f := range a.ToolFixes {
		if f.Tool != tool {
			kept = append(kept, f)
		}
	}
	a.ToolFixes = kept
	reason := "fix of " + tool + " discarded"
	if q.Get("keep") == "1" {
		reason = "fix of " + tool + " kept"
	}
	if _, err := saveAgentAs(db, a, reason); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// toolFixMaintainerSection is the maintainer editor's "Fix a tool".
func toolFixMaintainerSection(src, owner string) ui.Section {
	return ui.Section{
		Title:    "Fix a tool",
		Wide:     true,
		Subtitle: "When one of its tools breaks and " + owner + " is away: a corrected copy that only this agent uses.",
		Detail: "Saving makes <tool>_fix, used by this agent in place of " + owner + "'s tool, which is switched off for this agent only; " + owner + " is told, and keeps the fix or discards it. " +
			"You can change what the request looks like: the URL, method, body, headers, parameters and a toolbox's actions. Its credential, and anything that runs a script, stay " + owner + "'s, so " + owner + "'s key still goes only where it went and is still lent only for what it was.",
		Body: ui.Table{
			Source: src + "/fixable-tools",
			RowKey: "name",
			Columns: []ui.Col{
				{Field: "name", Flex: 1},
				{Field: "mode", Label: "Kind", Mute: true},
				{Field: "fixed", Type: "badge", Badges: []ui.BadgeMapping{{Value: true, Label: "Running as a fix", Color: "warning"}}},
				{Field: "description", Mute: true, Flex: 3},
			},
			EmptyText: "Nothing here can be fixed this way: it has no HTTP tools of " + owner + "'s.",
			RowActions: []ui.RowAction{
				ui.Expand("Fix for this agent", ui.FormPanel{
					Source:      src + "/tool-fix?tool={name}",
					PostURL:     src + "/tool-fix?tool={name}",
					SubmitLabel: "Save the fix",
					Fields: []ui.FormField{{Field: "definition", Type: "textarea", Rows: 18, Label: "Definition",
						Help: "The request this tool makes, as JSON: for an API tool its URL is command_template, for a toolbox each action's is url_template. Change what broke and save."}},
					Invalidate: []string{src + "/fixable-tools"},
				}),
			},
		},
	}
}

// toolFixOwnerSection is the owner's "Fixes from maintainers".
func toolFixOwnerSection(src string) ui.Section {
	return ui.Section{
		Title:    "Fixes from maintainers",
		Indent:   1,
		Wide:     true,
		Subtitle: "Corrected copies of your tools a maintainer made, in use by this agent in place of yours.",
		Detail:   "Keep it to put the fix into your own tool (every agent of yours that uses it gets it) and switch your tool back on here. Discard it to go back to your tool as it was.",
		Body: ui.Table{
			Source: src + "/tool-fixes",
			RowKey: "tool",
			Columns: []ui.Col{
				{Field: "tool", Flex: 1},
				{Field: "fix", Label: "Running as", Flex: 1},
				{Field: "by", Label: "By", Flex: 1},
				{Field: "at", Label: "When", Format: "reltime", Flex: 1},
			},
			EmptyText: "No maintainer has fixed a tool of yours on this agent.",
			RowActions: []ui.RowAction{
				{Type: "button", Label: "Keep it", PostTo: src + "/tool-fix/resolve?tool={tool}&keep=1", Method: "POST",
					Confirm: "Put this fix into your own tool? Every agent of yours that uses it gets the change."},
				{Type: "button", Label: "Discard", PostTo: src + "/tool-fix/resolve?tool={tool}", Method: "POST", Variant: "danger",
					Confirm: "Discard the fix and go back to your tool as it was?"},
			},
		},
	}
}
