package temptool

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// getGrouped returns the full definition of a tool by name as a JSON
// blob the LLM can read directly. Lookup order: active pool first
// (admin-approved tools), then pending review queue, then session
// drafts authored this turn. Used by Builder to inspect existing tools
// when iterating or composing — Builder's executable catalog hides
// persistent tools by design, so this is its read-access channel.
func getGrouped(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if sess == nil || sess.DB == nil || sess.Username == "" {
		return "", fmt.Errorf("requires a session bound to a user")
	}
	// Active pool — most common case for "tool already exists."
	for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
		if p.Tool.Name == name {
			body, err := json.MarshalIndent(p.Tool, "", "  ")
			if err != nil {
				return "", fmt.Errorf("marshal tool %q: %w", name, err)
			}
			src := "active (admin-approved)"
			if p.Shared {
				src = "active (admin-approved), PUBLISHED deployment-wide — you own it, so an update changes it for every user"
			}
			return fmt.Sprintf("source: %s\n%s", src, string(body)), nil
		}
	}
	// Pending review queue — recently authored by an LLM, awaiting admin.
	for _, p := range LoadPendingTempTools(sess.DB, sess.Username) {
		if p.Tool.Name == name {
			body, err := json.MarshalIndent(p.Tool, "", "  ")
			if err != nil {
				return "", fmt.Errorf("marshal tool %q: %w", name, err)
			}
			return fmt.Sprintf("source: pending (awaiting admin review)\n%s", string(body)), nil
		}
	}
	// Session draft — authored THIS turn but not yet propagated.
	if sess.ChatSessionID != "" {
		for _, t := range LoadSessionTempTools(sess.DB, sess.ChatSessionID) {
			if t.Name == name {
				body, err := json.MarshalIndent(t, "", "  ")
				if err != nil {
					return "", fmt.Errorf("marshal tool %q: %w", name, err)
				}
				return fmt.Sprintf("source: session draft (this turn)\n%s", string(body)), nil
			}
		}
	}
	// Deployment-wide SHARED pool. A shared tool is callable from every
	// user's catalog, so "get" erroring on one the model just invoked was
	// pure confusion — it reads as the tool having vanished.
	if p, owner, ok := FindSharedToolWithOwner(sess.DB, name); ok {
		body, err := json.MarshalIndent(p.Tool, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal tool %q: %w", name, err)
		}
		if owner == sess.Username {
			return fmt.Sprintf("source: active (shared deployment-wide; you own it, so update edits it in place)\n%s", string(body)), nil
		}
		return fmt.Sprintf("source: shared deployment-wide, owned by %s — FULL definition below including its script; reading is always allowed, only editing is not. To change its behavior, copy it under a NEW name with action=\"create\" and edit that.\n%s", owner, string(body)), nil
	}
	// Live session tools — the only place an agent-bundled tool appears
	// (it's reconstituted from the agent record each turn, never written
	// to the draft/pending/persistent pools). Without this branch, get
	// erroring on a tool that's plainly firing was itself a source of the
	// "zombie" confusion.
	for _, t := range sess.CopyTempTools() {
		if t.Name == name {
			body, err := json.MarshalIndent(t, "", "  ")
			if err != nil {
				return "", fmt.Errorf("marshal tool %q: %w", name, err)
			}
			src := "session (live)"
			if sess.BundledToolNames[name] {
				src = "agent-bundled (attached to this agent's record — delete removes it from the record)"
			}
			return fmt.Sprintf("source: %s\n%s", src, string(body)), nil
		}
	}
	// Bundled to another of the user's agents — the case Builder hits when
	// inspecting a tool it doesn't hold itself (e.g. before an in-place edit).
	if FindUserAgentTool != nil {
		if tt, ownerAgent, found := FindUserAgentTool(sess.DB, sess.Username, name); found {
			body, err := json.MarshalIndent(tt, "", "  ")
			if err != nil {
				return "", fmt.Errorf("marshal tool %q: %w", name, err)
			}
			return fmt.Sprintf("source: agent-bundled (on agent %s — action=\"update\" edits it there in place)\n%s", ownerAgent, string(body)), nil
		}
	}
	// Orphan pool — the tool's last carrying agent was deleted, so the record
	// left every catalog at once. Checked LAST (a live copy always wins) but
	// checked, because "no tool found" is a lie here: the definition still
	// exists and the model's memory of having used it is correct. Erroring
	// instead sent it looking for other ways to reach a tool it could no
	// longer call.
	for _, o := range LoadOrphanedTempTools(sess.DB, sess.Username) {
		if o.Tool.Name != name {
			continue
		}
		body, err := json.MarshalIndent(o.Tool, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal tool %q: %w", name, err)
		}
		former := o.FormerAgentName
		if former == "" {
			former = "a deleted agent"
		}
		return fmt.Sprintf("source: ORPHANED — this tool is NOT callable right now. Its last carrying agent (%s) was deleted, which removed the tool from every agent's catalog; the definition below survived. To make it callable again, re-home it (Admin › Orphaned Tools) or re-create it with action=\"create\" using the definition below. Tell the user it needs re-homing rather than working around it.\n%s",
			former, string(body)), nil
	}
	return "", fmt.Errorf("no tool found with name %q (checked active pool, pending queue, session drafts, live session tools, your other agents' bundled tools, and the orphan pool)", name)
}

// loadExistingToolRecord resolves a tool by name to its full TempTool record,
// checking the same layers get does (active pool → pending → session draft →
// live session). Used by update to load-merge-save.
func loadExistingToolRecord(sess *ToolSession, name string) (TempTool, bool) {
	if sess == nil {
		return TempTool{}, false
	}
	if sess.DB != nil && sess.Username != "" {
		for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
			if p.Tool.Name == name {
				return p.Tool, true
			}
		}
		for _, p := range LoadPendingTempTools(sess.DB, sess.Username) {
			if p.Tool.Name == name {
				return p.Tool, true
			}
		}
	}
	if sess.DB != nil && sess.ChatSessionID != "" {
		for _, t := range LoadSessionTempTools(sess.DB, sess.ChatSessionID) {
			if t.Name == name {
				return t, true
			}
		}
	}
	for _, t := range sess.CopyTempTools() {
		if t.Name == name {
			return *t, true
		}
	}
	return TempTool{}, false
}

// toolInUserPools reports whether the name has a durable home in the user's
// persistent pool or pending-approval queue. Used to pick the write-back /
// delete target when the same name also lives on an agent record: the pool
// outranks the record (matching loadExistingToolRecord's resolution order).
func toolInUserPools(sess *ToolSession, name string) bool {
	if sess == nil || sess.DB == nil || sess.Username == "" {
		return false
	}
	for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
		if p.Tool.Name == name {
			return true
		}
	}
	for _, p := range LoadPendingTempTools(sess.DB, sess.Username) {
		if p.Tool.Name == name {
			return true
		}
	}
	return false
}

// toolInSessionDrafts reports whether the name exists as a per-session draft.
func toolInSessionDrafts(sess *ToolSession, name string) bool {
	if sess == nil || sess.DB == nil || sess.ChatSessionID == "" {
		return false
	}
	for _, t := range LoadSessionTempTools(sess.DB, sess.ChatSessionID) {
		if t.Name == name {
			return true
		}
	}
	return false
}

// actionToArgs serializes one toolbox action back to the create-arg map shape
// (the same keys createToolboxGrouped reads), so update can rebuild the actions
// array. required is emitted explicitly (present) so the presence-honoring
// parse keeps the action's exact optional/required split instead of defaulting.
func actionToArgs(a TempToolAction) map[string]any {
	m := map[string]any{
		"name":         a.Name,
		"description":  a.Description,
		"url_template": a.URLTemplate,
		"params":       a.Params,
		"required":     append([]string{}, a.Required...), // present even when empty
		"method":       a.Method,
	}
	if a.BodyTemplate != "" {
		m["body_template"] = a.BodyTemplate
	}
	if a.ContentType != "" {
		m["content_type"] = a.ContentType
	}
	if len(a.Headers) > 0 {
		m["headers"] = a.Headers
	}
	if a.ResponsePipe != "" {
		m["response_pipe"] = a.ResponsePipe
	}
	if a.ResponseExtract != nil {
		m["response_extract"] = a.ResponseExtract
	}
	if a.Disabled {
		m["disabled"] = true
	}
	return m
}

// tempToolToCreateArgs serializes a stored tool back into the create-arg shape
// so update can patch it and re-run it through createGrouped (reusing all of
// create's validation + persistence + active-overwrite semantics).
// reconcileTemplateAliases makes the supplied spelling win for both keys.
//
// Whichever the caller passed is what they meant. When both are passed
// and differ, the mode decides: an api or toolbox tool is addressed by
// url_template, everything else by command_template.
func reconcileTemplateAliases(merged map[string]any, existing TempTool, args map[string]any) {
	newURL, hasURL := args["url_template"]
	newCmd, hasCmd := args["command_template"]
	win, any := newURL, hasURL || hasCmd
	switch {
	case hasURL && hasCmd:
		if existing.Mode != TempToolModeAPI && existing.Mode != TempToolModeToolbox {
			win = newCmd
		}
	case hasCmd:
		win = newCmd
	}
	if !any {
		return
	}
	merged["url_template"], merged["command_template"] = win, win
}

// unlandedUpdateWarning re-reads the tool and reports any explicitly
// supplied scalar field whose value did not actually change. Empty when
// everything landed.
//
// Deliberately narrow: only fields the caller PASSED, only string-valued
// ones, and only when the record is still findable. A tool routed into an
// approval queue reads back as its pending copy through the same lookup,
// so this does not cry wolf over review — it fires when get would show
// the caller something other than what they just wrote.
func unlandedUpdateWarning(sess *ToolSession, name string, args map[string]any) string {
	after, ok := loadExistingToolRecord(sess, name)
	if !ok {
		return ""
	}
	stored := tempToolToCreateArgs(after)
	var stale []string
	for _, f := range []string{"description", "url_template", "command_template", "method", "body_template", "content_type", "response_pipe", "response_extract", "category", "credential"} {
		want, passed := args[f]
		if !passed {
			continue
		}
		wantStr, isStr := want.(string)
		if !isStr || strings.TrimSpace(wantStr) == "" {
			continue
		}
		if got, _ := stored[f].(string); got != wantStr {
			stale = append(stale, fmt.Sprintf("%s is still %q", f, got))
		}
	}
	if len(stale) == 0 {
		return ""
	}
	return "Re-reading the tool shows " + strings.Join(stale, "; ") +
		". Do NOT re-issue the same update expecting a different result, and do not report the tool as fixed — say what you tried and that the write did not take."
}

func tempToolToCreateArgs(tt TempTool) map[string]any {
	mode := tt.Mode
	if mode == "" {
		mode = TempToolModeShell
	}
	out := map[string]any{
		"name":        tt.Name,
		"description": tt.Description,
		"mode":        mode,
	}
	if tt.Category != "" {
		out["category"] = tt.Category
	}
	if tt.Credential != "" {
		out["credential"] = tt.Credential
	}
	switch mode {
	case TempToolModeToolbox:
		acts := make([]any, 0, len(tt.Actions))
		for _, a := range tt.Actions {
			acts = append(acts, actionToArgs(a))
		}
		out["actions"] = acts
		if tt.Expand {
			out["expand"] = true
		}
	default:
		// api / shell share the same scalar fields; empty ones are simply
		// absent, which the create path tolerates per mode.
		if len(tt.Params) > 0 {
			out["params"] = tt.Params
			out["required"] = append([]string{}, tt.Required...)
		}
		if tt.CommandTemplate != "" {
			out["command_template"] = tt.CommandTemplate
			out["url_template"] = tt.CommandTemplate // api mode reads url_template
		}
		if tt.Method != "" {
			out["method"] = tt.Method
		}
		if tt.BodyTemplate != "" {
			out["body_template"] = tt.BodyTemplate
		}
		// content_type drives raw (non-JSON) body substitution for api tools.
		// The update schema round-trips through here, so dropping it silently
		// turned an XML/CalDAV tool back into a JSON-validated one on ANY edit —
		// the body then failed as "invalid character '<'". Preserve it.
		if tt.ContentType != "" {
			out["content_type"] = tt.ContentType
		}
		if len(tt.Headers) > 0 {
			out["headers"] = tt.Headers
		}
		if tt.ResponsePipe != "" {
			out["response_pipe"] = tt.ResponsePipe
		}
		if tt.ResponseExtract != nil {
			out["response_extract"] = tt.ResponseExtract
		}
		if tt.ScriptBody != "" {
			out["script_body"] = tt.ScriptBody
		}
		if tt.ScriptName != "" {
			out["script_name"] = tt.ScriptName
		}
	}
	// Sandbox capability + state fields the create path consumes from args.
	// The update schema has NO parameter for any of these (there's no
	// hook_capabilities / raw_network / state_path / cache update field), so
	// if we don't round-trip them here they're the caller's to lose: updating
	// a shell tool reconstructs its create-args WITHOUT them, and the create
	// path then default-ons only [fetch log browse_page]. A tool that declared
	// hook_capabilities=["fetch_via:<cred>"] on create silently reverts to the
	// defaults on the next edit, and its next dispatch fails with
	// `method "fetch_via" not granted`. Emit them so update is non-lossy.
	if len(tt.HookCapabilities) > 0 {
		caps := make([]any, len(tt.HookCapabilities))
		for i, c := range tt.HookCapabilities {
			caps[i] = c
		}
		out["hook_capabilities"] = caps
	}
	if tt.RawNetwork {
		out["raw_network"] = true
	}
	if tt.StatePath != "" {
		out["state_path"] = tt.StatePath
	}
	if tt.Cache != nil {
		out["cache"] = cacheToArgs(tt.Cache)
	}
	return out
}

// cacheToArgs reverses parseCacheArg: it renders a stored TempToolCache back
// into the map[string]any the create path expects under args["cache"], so an
// update round-trips memoization instead of silently dropping it.
func cacheToArgs(c *TempToolCache) map[string]any {
	m := map[string]any{}
	if c.Key != "" {
		m["key"] = c.Key
	}
	if c.TTL != "" {
		m["ttl"] = c.TTL
	}
	if c.Scope != "" {
		m["scope"] = c.Scope
	}
	if len(c.InvalidateWhen) > 0 {
		inv := make([]any, len(c.InvalidateWhen))
		for i, s := range c.InvalidateWhen {
			inv[i] = s
		}
		m["invalidate_when"] = inv
	}
	return m
}
