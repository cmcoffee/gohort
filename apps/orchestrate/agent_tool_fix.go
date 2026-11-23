package orchestrate

// A maintainer fixes a broken tool while the owner is away.
//
// An agent's tools are its owner's, and a maintainer cannot edit them. When
// one breaks (an endpoint moved, a parameter was renamed), a maintainer can
// fix it for this agent: the fix is kept on the agent (AgentRecord.ToolFixes)
// UNDER THE TOOL'S OWN NAME, and runs in place of the tool for everybody who
// runs the agent EXCEPT its owner.
//
// Why that is safe. It runs only for people whose calls cannot write as
// anybody but themselves: reads held to reads on the owner's key, writes on
// their own account (fixMayRunFor). Somebody the owner lent the key to WITH
// writes keeps the original until the owner keeps the fix. So a fix cannot
// make anything write as the owner. The owner's own runs (their
// chats, schedules, channels) are the only ones with the owner's full key,
// and they keep the original until the owner keeps the fix. And because the
// fix keeps the tool's name, every per-agent setting keyed by it still holds:
// switched-off actions, ask first, never unattended, the scan lists.
//
// What a fix may change is its request's shape and nothing else: the URL, the
// method, the body, headers, parameters, the description, and a toolbox's
// actions. The credential, response pipes, scripts, quarantine and every
// other field stay the original's. An action whose address or method changed
// loses its "reads" mark, so a read still means a read.
//
// The owner is told. Keep applies the fix to the tool itself, in place, for
// every agent of theirs (a version the tool's history keeps); Discard drops
// it. Keep applies exactly the version the owner looked at, and is refused if
// the tool changed since the fix was made, which also stops the fix running.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/ui"
)

// ToolFix is a maintainer's fix of one of the owner's tools on one agent.
type ToolFix struct {
	Tool string    `json:"tool"`
	By   string    `json:"by"`
	At   time.Time `json:"at"`
	// Base fingerprints the tool as it was when the fix was made: the fix
	// runs, and can be kept, only while the tool is still that.
	Base string   `json:"base"`
	Def  TempTool `json:"def"`
}

// toolPrint fingerprints a tool's definition.
func toolPrint(t TempTool) string {
	b, _ := json.Marshal(t)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// fixVersion is the token the owner's Keep carries: the fix they looked at.
func fixVersion(f ToolFix) string { return strconv.FormatInt(f.At.UnixNano(), 10) }

// fixableTool reports whether a maintainer may fix t: an HTTP tool or toolbox,
// no script anywhere in it, and not locked by its owner. An API tool's URL is
// its CommandTemplate, which is a command only in shell mode.
func fixableTool(t TempTool) bool {
	if strings.TrimSpace(t.ScriptBody) != "" || t.Locked {
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

// fixedShape is base with the request shape of sent laid over it, and nothing
// else: the credential, pipes, quarantine and every other field stay base's.
// An action is matched by name: one sent under a new name is a new action,
// with no pipe, no mark and not quarantined. An action whose address or
// method changed loses its reads mark.
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
			old, existed := was[s.Name]
			a := old
			a.Name, a.Description = s.Name, s.Description
			a.URLTemplate, a.Method, a.BodyTemplate = s.URLTemplate, s.Method, s.BodyTemplate
			a.Headers, a.ContentType = s.Headers, s.ContentType
			a.Params, a.Required = s.Params, s.Required
			// A read is the call the owner marked: any change to what it
			// sends (a GraphQL mutation in the body, an override header)
			// is not that call any more.
			if !existed || a.URLTemplate != old.URLTemplate || !strings.EqualFold(a.Method, old.Method) ||
				a.BodyTemplate != old.BodyTemplate || !sameShape(a.Headers, old.Headers) || a.ContentType != old.ContentType {
				a.Reads = false
			}
			out.Actions = append(out.Actions, a)
		}
	}
	return out
}

// toolWithFix is the tool a run of a loads under name: the maintainer's fix
// for anybody but the agent's owner, while the tool is still what it was when
// the fix was made; the stored tool otherwise.
//
// Only the actions the tool already has run before the owner keeps the fix:
// an action the fix adds (or renames one to) is the owner's to let in. The
// per-agent settings on a toolbox's actions are keyed by action name, so an
// action under a new name would run past one the owner switched off.
func toolWithFix(a AgentRecord, runner string, stored TempTool) TempTool {
	if strings.TrimSpace(runner) == "" || runner == a.Owner || !fixMayRunFor(a, runner, stored) {
		return stored
	}
	for _, f := range a.ToolFixes {
		if f.Tool != stored.Name || f.Base != toolPrint(stored) {
			continue
		}
		fixed := f.Def
		fixed.Name, fixed.FixedBy = stored.Name, f.By
		if stored.Mode == TempToolModeToolbox {
			had := map[string]bool{}
			for _, x := range stored.Actions {
				had[x.Name] = true
			}
			fixed.Actions = nil
			for _, x := range f.Def.Actions {
				if had[x.Name] {
					fixed.Actions = append(fixed.Actions, x)
				}
			}
		}
		return fixed
	}
	return stored
}

// fixMayRunFor reports whether an unkept fix may shape runner's calls: only
// where they cannot write as anybody but runner. Somebody the owner lent the
// key to WITH writes keeps the original until the owner keeps the fix, as
// does anybody whose call would go out on a deployment credential. A tool
// with no credential but headers of its own may carry the owner's key in
// them, so its fix waits too.
func fixMayRunFor(a AgentRecord, runner string, stored TempTool) bool {
	if strings.TrimSpace(stored.Credential) == "" {
		if len(stored.Headers) > 0 {
			return false
		}
		for _, x := range stored.Actions {
			if len(x.Headers) > 0 {
				return false
			}
		}
		return true
	}
	return !Secure().WritesWithAnotherKey(stored.Credential, runner, a.ID)
}

// fixDiff is what a fix changes, in lines the owner reads, the risky ones
// first.
func fixDiff(orig, fix TempTool) []string {
	var risky, plain []string
	host := func(u string) string {
		if p, err := url.Parse(strings.TrimSpace(u)); err == nil && p.Host != "" {
			return p.Host
		}
		return ""
	}
	if orig.Mode == TempToolModeAPI && orig.CommandTemplate != fix.CommandTemplate {
		if host(orig.CommandTemplate) != host(fix.CommandTemplate) {
			risky = append(risky, "Now calls a different address: "+fix.CommandTemplate)
		} else {
			plain = append(plain, "Address: "+orig.CommandTemplate+" → "+fix.CommandTemplate)
		}
	}
	if !strings.EqualFold(orig.Method, fix.Method) {
		risky = append(risky, "Method: "+orig.Method+" → "+fix.Method)
	}
	r, p := shapeOf(orig.Headers, orig.ContentType, orig.BodyTemplate, orig.Params, orig.Required).
		diff("", shapeOf(fix.Headers, fix.ContentType, fix.BodyTemplate, fix.Params, fix.Required))
	risky, plain = append(risky, r...), append(plain, p...)
	if orig.Description != fix.Description {
		plain = append(plain, "Description changed")
	}
	was := map[string]TempToolAction{}
	for _, a := range orig.Actions {
		was[a.Name] = a
	}
	now := map[string]bool{}
	for _, a := range fix.Actions {
		now[a.Name] = true
		old, ok := was[a.Name]
		switch {
		case !ok:
			risky = append(risky, fmt.Sprintf("Action %s added: %s %s (runs only once you keep the fix)", a.Name, a.Method, a.URLTemplate))
		default:
			if host(old.URLTemplate) != host(a.URLTemplate) && host(a.URLTemplate) != "" {
				risky = append(risky, "Action "+a.Name+" now calls a different address: "+a.URLTemplate)
			} else if old.URLTemplate != a.URLTemplate {
				plain = append(plain, "Action "+a.Name+": "+old.URLTemplate+" → "+a.URLTemplate)
			}
			if !strings.EqualFold(old.Method, a.Method) {
				risky = append(risky, "Action "+a.Name+": method "+old.Method+" → "+a.Method)
			}
			if old.Reads && !a.Reads {
				risky = append(risky, "Action "+a.Name+" is no longer marked as one that only reads")
			}
			r, p := shapeOf(old.Headers, old.ContentType, old.BodyTemplate, old.Params, old.Required).
				diff("Action "+a.Name+": ", shapeOf(a.Headers, a.ContentType, a.BodyTemplate, a.Params, a.Required))
			risky, plain = append(risky, r...), append(plain, p...)
		}
	}
	for _, a := range orig.Actions {
		if !now[a.Name] {
			msg := "Action " + a.Name + " removed"
			if a.ResponsePipe != "" {
				msg += " (its response filter goes with it)"
			}
			risky = append(risky, msg)
		}
	}
	return append(risky, plain...)
}

// requestShape is the part of a request a fix may change besides its address
// and method, so the owner's diff names every one of them.
type requestShape struct {
	headers     map[string]string
	contentType string
	body        string
	params      map[string]ToolParam
	required    []string
}

func shapeOf(h map[string]string, ct, body string, p map[string]ToolParam, req []string) requestShape {
	return requestShape{headers: h, contentType: ct, body: body, params: p, required: req}
}

// diff lists what changes from rs to next. Headers are always risky (one can
// override the method or the account); the body is shown, so the owner reads
// what will be sent.
func (rs requestShape) diff(prefix string, next requestShape) (risky, plain []string) {
	for _, k := range fixKeys(rs.headers, next.headers) {
		was, had := rs.headers[k]
		now, has := next.headers[k]
		switch {
		case !had:
			risky = append(risky, prefix+"header "+k+" added: "+now)
		case !has:
			risky = append(risky, prefix+"header "+k+" removed")
		case was != now:
			risky = append(risky, prefix+"header "+k+": "+was+" → "+now)
		}
	}
	if rs.contentType != next.contentType {
		risky = append(risky, prefix+"content type: "+rs.contentType+" → "+next.contentType)
	}
	if rs.body != next.body {
		body := next.body
		if len(body) > 400 {
			body = body[:400] + "..."
		}
		if body == "" {
			body = "(empty)"
		}
		plain = append(plain, prefix+"body is now: "+body)
	}
	for _, k := range fixKeys(rs.params, next.params) {
		was, had := rs.params[k]
		now, has := next.params[k]
		switch {
		case !had:
			plain = append(plain, prefix+"parameter "+k+" added")
		case !has:
			plain = append(plain, prefix+"parameter "+k+" removed")
		case !jsonEqual(was, now):
			plain = append(plain, prefix+"parameter "+k+" changed")
		}
	}
	if !sameShape(rs.required, next.required) {
		plain = append(plain, prefix+"required parameters: "+strings.Join(next.required, ", "))
	}
	return risky, plain
}

func fixKeys[V any](a, b map[string]V) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range []map[string]V{a, b} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// sameShape compares two maps or slices of strings by their JSON form.
func sameShape(a, b any) bool { return jsonEqual(a, b) }

// keptValue stands in for a header value in the fix form: a maintainer sees
// which headers there are, never the owner's values, and a header left at
// keptValue keeps the value it had.
const keptValue = "(kept: not shown)"

func maskHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return h
	}
	out := map[string]string{}
	for k := range h {
		out[k] = keptValue
	}
	return out
}

// unmaskHeaders puts back the values the form hid: a header sent as keptValue
// takes the value it had in shown, and is dropped if it had none.
func unmaskHeaders(sent, shown map[string]string) map[string]string {
	if len(sent) == 0 {
		return sent
	}
	out := map[string]string{}
	for k, v := range sent {
		if v != keptValue {
			out[k] = v
		} else if was, ok := shown[k]; ok {
			out[k] = was
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

func toolFixOf(a AgentRecord, tool string) (ToolFix, bool) {
	for _, f := range a.ToolFixes {
		if f.Tool == tool {
			return f, true
		}
	}
	return ToolFix{}, false
}

// toolFixOriginal is the tool a fix call names, if this agent uses it and it
// can be fixed. A tool of the owner's this agent does not use is not found:
// the form must not read the rest of the owner's pool.
func toolFixOriginal(c *jsonapi.Call, a AgentRecord) (string, PersistentTempTool, error) {
	tool := strings.TrimSpace(c.R.URL.Query().Get("tool"))
	orig, ok := agentPoolTools(a.Owner, a)[tool]
	if !ok || !fixableTool(orig.Tool) {
		return tool, orig, jsonapi.NotFound("no tool of this agent's that can be fixed is called " + tool)
	}
	return tool, orig, nil
}

// agentFixableTools lists the tools a maintainer may fix on this agent, and
// whether each has a fix in use.
func (T *OrchestrateApp) agentFixableTools(c *jsonapi.Call) (any, error) {
	a, _, _, err := T.toolFixTarget(c)
	if err != nil {
		return nil, err
	}
	type row struct {
		Name        string `json:"name"`
		Mode        string `json:"mode"`
		Description string `json:"description"`
		Fixed       bool   `json:"fixed"`
	}
	out := []row{}
	for name, p := range agentPoolTools(a.Owner, a) {
		if !fixableTool(p.Tool) {
			continue
		}
		_, fixed := toolFixOf(a, name)
		out = append(out, row{Name: name, Mode: p.Tool.Mode, Description: firstLine(p.Tool.Description), Fixed: fixed})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// agentToolFixGet is one tool's request shape for the fix form: the fix in
// use when there is one, else the tool's.
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
	if f, ok := toolFixOf(a, tool); ok {
		shown = f.Def
	}
	form := fixedShape(TempTool{Mode: shown.Mode}, shown)
	form.Headers = maskHeaders(form.Headers)
	for i := range form.Actions {
		form.Actions[i].Headers = maskHeaders(form.Actions[i].Headers)
	}
	b, _ := json.MarshalIndent(form, "", "  ")
	return map[string]string{"tool": tool, "definition": string(b)}, nil
}

// agentToolFixSave saves a maintainer's fix on the agent.
func (T *OrchestrateApp) agentToolFixSave(c *jsonapi.Call, body struct {
	Definition string `json:"definition"`
}) (any, error) {
	a, db, maintainer, err := T.toolFixTarget(c)
	if err != nil {
		return nil, err
	}
	if !maintainer {
		return nil, jsonapi.BadRequest("it is your tool: change it under Extensions > Tools, and every agent of yours that uses it gets the change")
	}
	tool, orig, err := toolFixOriginal(c, a)
	if err != nil {
		return nil, err
	}
	var sent TempTool
	if err := json.Unmarshal([]byte(body.Definition), &sent); err != nil {
		return nil, jsonapi.BadRequest("the definition is not valid JSON: " + err.Error())
	}
	// Header values the form hid come back from the version it showed.
	shown := orig.Tool
	if f, ok := toolFixOf(a, tool); ok {
		shown = f.Def
	}
	sent.Headers = unmaskHeaders(sent.Headers, shown.Headers)
	shownActs := map[string]TempToolAction{}
	for _, x := range shown.Actions {
		shownActs[x.Name] = x
	}
	for i := range sent.Actions {
		sent.Actions[i].Headers = unmaskHeaders(sent.Actions[i].Headers, shownActs[sent.Actions[i].Name].Headers)
	}
	fix := fixedShape(orig.Tool, sent)
	fix.Name = tool
	kept := []ToolFix{}
	for _, f := range a.ToolFixes {
		if f.Tool != tool {
			kept = append(kept, f)
		}
	}
	a.ToolFixes = append(kept, ToolFix{Tool: tool, By: c.User, At: time.Now(), Base: toolPrint(orig.Tool), Def: fix})
	if _, err := saveAgentAs(db, a, "tool "+tool+" fixed by "+c.User); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	notices.Record(RootDB, notices.Notice{
		Owner: a.Owner, Agent: a.ID, Kind: notices.KindBlocked,
		Title: c.User + " fixed " + tool + " on " + chFirst(a.Name, a.ID),
		Body: "People using this agent now run their fix of your " + tool + ", except anyone you lent its key to with writes; your own runs keep your tool as it is. " +
			"Keep it to put it into your " + tool + " for every agent of yours, or discard it: both on the agent's page, under Fixes from maintainers.",
	})
	Log("[orchestrate.agents] %s fixed %s's tool %q on agent %q", c.User, a.Owner, tool, a.Name)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// agentToolFixes lists the fixes on this agent, each with what it changes and
// the version token Keep carries.
func (T *OrchestrateApp) agentToolFixes(c *jsonapi.Call) (any, error) {
	a, _, _, err := T.toolFixTarget(c)
	if err != nil {
		return nil, err
	}
	type row struct {
		Tool    string    `json:"tool"`
		By      string    `json:"by"`
		At      time.Time `json:"at"`
		Version string    `json:"version"`
		Changes string    `json:"changes"`
		Stale   bool      `json:"stale"`
	}
	out := []row{}
	for _, f := range a.ToolFixes {
		r := row{Tool: f.Tool, By: f.By, At: f.At, Version: fixVersion(f)}
		if p, ok := UserToolByName(AuthDB(), a.Owner, f.Tool); ok {
			r.Stale = toolPrint(p.Tool) != f.Base
			r.Changes = strings.Join(fixDiff(p.Tool, f.Def), "\n")
		}
		if r.Stale {
			r.Changes = "Your tool changed since this fix was made, so it is not running and cannot be kept. Discard it, or ask for a new fix.\n" + r.Changes
		}
		out = append(out, r)
	}
	return out, nil
}

// agentToolFixResolve is the owner's answer to a fix: keep puts the version
// they looked at into the tool itself; either way the fix goes.
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
	f, ok := toolFixOf(a, tool)
	if !ok {
		return nil, jsonapi.NotFound("no fix of " + tool + " on this agent")
	}
	reason := "fix of " + tool + " by " + f.By + " discarded"
	if q.Get("keep") == "1" {
		if q.Get("version") != fixVersion(f) {
			return nil, jsonapi.Status(http.StatusConflict, "the fix was changed after you looked at it: look again before keeping it")
		}
		orig, ok := UserToolByName(AuthDB(), a.Owner, tool)
		if !ok {
			return nil, jsonapi.NotFound("your tool " + tool + " is no longer there")
		}
		if toolPrint(orig.Tool) != f.Base {
			return nil, jsonapi.Status(http.StatusConflict, "your tool changed since this fix was made: discard it, or ask for a new fix")
		}
		next := fixedShape(orig.Tool, f.Def)
		next.Name = tool
		if !UpdatePersistentTempTool(AuthDB(), a.Owner, next, "fix by "+f.By+" kept") {
			return nil, jsonapi.BadRequest("your tool " + tool + " could not be changed")
		}
		reason = "fix of " + tool + " by " + f.By + " kept"
	}
	kept := []ToolFix{}
	for _, x := range a.ToolFixes {
		if x.Tool != tool {
			kept = append(kept, x)
		}
	}
	a.ToolFixes = kept
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
		Subtitle: "When one of its tools breaks and " + owner + " is away: a fix everybody but " + owner + " runs at once, until " + owner + " keeps or discards it.",
		Detail: "Your fix runs under the tool's own name, so every setting " + owner + " made for it on this agent still holds. It runs only where a call cannot write as anybody but the person running it: reads on " + owner + "'s key, writes on their own account. Anyone " + owner + " lent the key to with writes, and " + owner + "'s own runs, keep the tool as it is. " +
			"You can change what the request looks like: the URL, method, body, headers, parameters and a toolbox's actions. Its credential, response filters, quarantined actions and anything that runs a script stay " + owner + "'s; an action whose request you change is no longer counted as a read.",
		Body: ui.Table{
			Source: src + "/fixable-tools",
			RowKey: "name",
			Columns: []ui.Col{
				{Field: "name", Flex: 1},
				{Field: "mode", Label: "Kind", Mute: true},
				{Field: "fixed", Type: "badge", Badges: []ui.BadgeMapping{{Value: true, Label: "Fix in use", Color: "warning"}}},
				{Field: "description", Mute: true, Flex: 3},
			},
			EmptyText: "Nothing here can be fixed this way: it uses no HTTP tools of " + owner + "'s.",
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
		Subtitle: "Fixes of your tools a maintainer made: people using this agent run them until you keep or discard them, except you and anyone you lent the key to with writes.",
		Detail:   "Keep puts the fix into your own tool, for every agent of yours that uses it; the version it replaces stays in the tool's history. Discard drops it. Keep applies exactly the version listed here, and is refused if your tool changed since.",
		Body: ui.Table{
			Source: src + "/tool-fixes",
			RowKey: "tool",
			Columns: []ui.Col{
				{Field: "tool", Flex: 1},
				{Field: "by", Label: "By", Flex: 1},
				{Field: "at", Label: "When", Format: "reltime", Flex: 1},
				{Field: "changes", Label: "What it changes", Flex: 4},
			},
			EmptyText: "No maintainer has fixed a tool of yours on this agent.",
			RowActions: []ui.RowAction{
				{Type: "button", Label: "Keep it", PostTo: src + "/tool-fix/resolve?tool={tool}&keep=1&version={version}", Method: "POST",
					Confirm: "Put this fix into your own tool? Every agent of yours that uses it gets the change, and the version it replaces stays in the tool's history."},
				{Type: "button", Label: "Discard", PostTo: src + "/tool-fix/resolve?tool={tool}", Method: "POST", Variant: "danger",
					Confirm: "Discard the fix? Everybody goes back to your tool as it is."},
			},
		},
	}
}
