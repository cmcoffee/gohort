package temptool

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// finalizeAuthoredTool routes a freshly-authored tool (currently a
// session draft written by the create path) to its durable home based
// on WHO authored it — no admin-approval queue in either path:
//
//   - Builder (sess.CanScopeGlobal): the user-wide persistent pool via
//     AdminPersistTempTool. Builder is the trusted authoring surface, so
//     re-approving its own output was pure ceremony; the tool persists
//     immediately and replace-by-name handles LLM iteration.
//
//   - every other agent: its OWN record (AgentRecord.Tools) via
//     sess.BundleTool, so a self-serve agent grows only its own kit and
//     can never write to the shared pool.
//
// The session draft stays registered in-memory so the tool is
// dispatchable THIS turn regardless of scope. Deliberate re-scoping
// (agent→global, or attaching a global tool onto an agent) is an admin
// action, not an authoring one — see the admin Tools surface.
// It returns a short, ACCURATE one-line description of where the tool landed,
// which the create path appends to its result so the LLM (and the user) get the
// truth instead of the stale "an admin must promote this" ceremony — Builder's
// tools are already the user's and persist with no approval.
func finalizeAuthoredTool(sess *ToolSession, toolName string) string {
	if sess == nil || sess.DB == nil || sess.Username == "" || sess.ChatSessionID == "" || toolName == "" {
		return ""
	}
	var draft *TempTool
	for _, d := range LoadSessionTempTools(sess.DB, sess.ChatSessionID) {
		if d.Name == toolName {
			tmp := d
			draft = &tmp
			break
		}
	}
	if draft == nil {
		return ""
	}
	// In-place edit redirect: an update of a tool that lives on ANOTHER of the
	// user's agents writes back to THAT agent's record, taking precedence over
	// both Builder-global pooling and self-bundle. This keeps a repair from
	// silently promoting an agent-private tool into the shared pool — Builder
	// fixes the tool where it lives, its scope unchanged. Clean up the pending
	// mirror the same way the agent-scope branch does.
	if target := strings.TrimSpace(sess.BundleAuthoredToolTo); target != "" && AttachToolToAgent != nil {
		if err := AttachToolToAgent(sess.DB, sess.Username, target, *draft); err != nil {
			Log("[temptool.scope] in-place write-back of %q to agent %s failed: %v", toolName, target, err)
			return "Available for this session; saving the edit back to the owning agent failed (see server logs)."
		}
		DequeuePendingTempTool(sess.DB, sess.Username, toolName)
		Log("[temptool.scope] wrote %q back in place to agent %s (in-place edit)", toolName, target)
		return "Saved back to the owning agent's tools — edited in place, scope unchanged."
	}
	// Global scope (Builder only): auto-persist to the user-wide pool,
	// skipping the pending-approval queue. AdminPersistTempTool replaces
	// by name (so LLM iteration updates in place), dedupes any stale
	// pending entry, and cleans redundant session drafts.
	if sess.CanScopeGlobal {
		if err := AdminPersistTempTool(sess.DB, sess.Username, *draft); err != nil {
			Log("[temptool.scope] global persist failed for %q: %v", toolName, err)
			return "Available for this session; saving it to your tools failed (see server logs)."
		}
		Log("[temptool.scope] persisted %q to the user-wide pool (Builder authoring; no approval)", toolName)
		return "Saved to your tools — available to all your agents and across sessions. No admin approval needed."
	}
	// Agent scope (every non-Builder agent): attach to the calling
	// agent's own record. On any failure (no bundle target, seed with no
	// per-user record, ownership mismatch) the tool simply stays
	// session-scoped — usable this turn, not escalated to the shared pool.
	if sess.BundleTool == nil {
		Log("[temptool.scope] %q kept session-scoped (no agent bundle target)", toolName)
		return "Available for THIS session. Promote it from the Tools modal to keep it past the session."
	}
	if err := sess.BundleTool(*draft); err != nil {
		Log("[temptool.scope] agent-scope attach failed for %q (kept session-scoped): %v", toolName, err)
		return "Available for THIS session. Promote it from the Tools modal to keep it past the session."
	}
	// Now owned by the agent record; clear any stale pending entry so the
	// same name can't linger in the admin review queue.
	DequeuePendingTempTool(sess.DB, sess.Username, toolName)
	Log("[temptool.scope] attached %q to authoring agent record (agent-scoped)", toolName)
	return "Saved to this agent's own tools (persists across sessions)."
}

// createGrouped dispatches between create_temp_tool (shell) and
// create_api_tool (api) based on the mode arg.
// persistentToolLocked reports whether a tool of this name is Locked in the
// user's persistent pool. Lock is a user-only control set from Extensions ›
// Extensions › Tools; the AI's create/update/delete honor it so a stable tool can't be
// silently rewritten or removed. Session-only drafts (never locked) don't count.
func persistentToolLocked(sess *ToolSession, name string) bool {
	if sess == nil || sess.DB == nil || sess.Username == "" || name == "" {
		return false
	}
	for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
		if p.Tool.Name == name {
			return p.Tool.Locked
		}
	}
	return false
}

const lockedToolMsg = "Tool %q is LOCKED — it can't be modified or deleted. If it genuinely must change, the user unlocks it first in Extensions › Tools, then it's editable. Do NOT recreate it under a different name."

func createGrouped(args map[string]any, sess *ToolSession) (string, error) {
	if name := strings.TrimSpace(StringArg(args, "name")); persistentToolLocked(sess, name) {
		return fmt.Sprintf(lockedToolMsg, name), nil
	}
	mode := strings.TrimSpace(StringArg(args, "mode"))
	switch mode {
	case "", TempToolModeShell:
		// Shell mode — call the existing CreateTempToolTool path by
		// reconstructing its expected args.
		shellArgs := map[string]any{
			"name":             args["name"],
			"description":      args["description"],
			"params":           args["params"],
			"command_template": args["command_template"],
			"category":         args["category"],
		}
		if r, ok := args["required"]; ok {
			shellArgs["required"] = r
		}
		if p, ok := args["persist"]; ok {
			shellArgs["persist"] = p
		}
		if v, ok := args["state_path"]; ok {
			shellArgs["state_path"] = v
		}
		if v, ok := args["script_body"]; ok {
			shellArgs["script_body"] = v
		}
		if v, ok := args["script_name"]; ok {
			shellArgs["script_name"] = v
		}
		if v, ok := args["cache"]; ok {
			shellArgs["cache"] = v
		}
		if v, ok := args["hook_capabilities"]; ok {
			shellArgs["hook_capabilities"] = v
		}
		if v, ok := args["raw_network"]; ok {
			shellArgs["raw_network"] = v
		}
		t := &CreateTempToolTool{}
		Debug("[tool_def] create(shell) %q: RunWithSession start", StringArg(args, "name"))
		res, err := t.RunWithSession(shellArgs, sess)
		Debug("[tool_def] create(shell) %q: RunWithSession done (err=%v)", StringArg(args, "name"), err)
		if err == nil {
			// finalizeAuthoredTool routes the write-back (agent attach vs
			// bundle vs pool), which is where the shared tool mutex is taken.
			Debug("[tool_def] create(shell) %q: finalize start", StringArg(args, "name"))
			if scope := finalizeAuthoredTool(sess, strings.TrimSpace(StringArg(args, "name"))); scope != "" {
				res = strings.TrimRight(res, " ") + " " + scope
			}
			Debug("[tool_def] create(shell) %q: finalize done", StringArg(args, "name"))
		}
		return res, err
	case TempToolModeAPI:
		apiArgs := map[string]any{
			"name":         args["name"],
			"description":  args["description"],
			"params":       args["params"],
			"credential":   args["credential"],
			"url_template": args["url_template"],
			"category":     args["category"],
		}
		if v, ok := args["method"]; ok {
			apiArgs["method"] = v
		}
		if v, ok := args["body_template"]; ok {
			apiArgs["body_template"] = v
		}
		if v, ok := args["content_type"]; ok {
			apiArgs["content_type"] = v
		}
		if v, ok := args["headers"]; ok {
			apiArgs["headers"] = v
		}
		if v, ok := args["response_pipe"]; ok {
			apiArgs["response_pipe"] = v
		}
		if v, ok := args["response_extract"]; ok {
			apiArgs["response_extract"] = v
		}
		if v, ok := args["required"]; ok {
			apiArgs["required"] = v
		}
		if v, ok := args["persist"]; ok {
			apiArgs["persist"] = v
		}
		t := &CreateAPIToolTool{}
		res, err := t.RunWithSession(apiArgs, sess)
		if err == nil {
			if scope := finalizeAuthoredTool(sess, strings.TrimSpace(StringArg(args, "name"))); scope != "" {
				res = strings.TrimRight(res, " ") + " " + scope
			}
		}
		return res, err
	case TempToolModePipeline:
		res, err := createPipelineGrouped(args, sess)
		if err == nil {
			if scope := finalizeAuthoredTool(sess, strings.TrimSpace(StringArg(args, "name"))); scope != "" {
				res = strings.TrimRight(res, " ") + " " + scope
			}
		}
		return res, err
	case TempToolModeToolbox:
		res, err := createToolboxGrouped(args, sess)
		if err == nil {
			if scope := finalizeAuthoredTool(sess, strings.TrimSpace(StringArg(args, "name"))); scope != "" {
				res = strings.TrimRight(res, " ") + " " + scope
			}
		}
		return res, err
	default:
		return "", fmt.Errorf("mode must be \"shell\", \"api\", \"pipeline\", or \"toolbox\" (got %q)", mode)
	}
}

// createToolboxGrouped builds a toolbox-mode TempTool from the
// grouped-action arg map. A toolbox bundles multiple api-mode
// endpoints under one tool name + one shared credential, surfacing
// in the catalog as a single GroupedTool with action="<sub>"
// dispatch (same UX as the framework's built-in grouped tools).
// Use when wrapping an API with several related endpoints (GitHub:
// get_user / get_repo / list_issues) so the catalog stays clean and
// the credential is declared once.
func createToolboxGrouped(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("requires a session")
	}
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if !validToolName(name) {
		return "", fmt.Errorf("name must be lowercase letters / digits / underscores only (got %q)", name)
	}
	for _, ct := range RegisteredChatTools() {
		if ct.Name() == name {
			return "", fmt.Errorf("name %q collides with a registered tool — pick another", name)
		}
	}
	desc := strings.TrimSpace(StringArg(args, "description"))
	if desc == "" {
		return "", fmt.Errorf("description is required")
	}
	credential := strings.TrimSpace(StringArg(args, "credential"))
	if credential == "" {
		// Default to no_auth for toolbox mode the same way api mode
		// does — public APIs are the common case. Admin scopes via
		// no_auth's AllowedURLPattern if needed.
		credential = "no_auth"
	}
	// Secured-credential binding is AUTO-RESOLVED: a toolbox that declares a
	// secured cred is bound to it (its actions dispatch server-side). Exception: an
	// admin's explicit REVOKE is a durable deny. See docs/secured-credential-tool-binding.md.
	if cr, ok := Secure().Load(credential); ok && cr.Secured {
		if Secure().ToolBindingRevoked(credential, name) {
			return "", fmt.Errorf("credential %q is SECURED and toolbox %q's binding was REVOKED by an admin — ask them to restore it in Admin > APIs", credential, name)
		}
		_ = Secure().ApproveToolBinding(credential, name)
	}
	rawActions, ok := args["actions"]
	if !ok || rawActions == nil {
		return "", fmt.Errorf("actions is required for toolbox mode — provide an array of {name, description, url_template, params, ...} sub-action objects")
	}
	actionsList, ok := rawActions.([]any)
	if !ok {
		return "", fmt.Errorf("actions must be an array (got %T)", rawActions)
	}
	if len(actionsList) == 0 {
		return "", fmt.Errorf("actions must contain at least one sub-action (a toolbox with no actions is just an unbuilt api tool — use mode=\"api\" instead)")
	}
	actions := make([]TempToolAction, 0, len(actionsList))
	seen := make(map[string]bool, len(actionsList))
	var scaffoldedActions []string // write actions we auto-gave a body_template
	for i, raw := range actionsList {
		m, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("actions[%d]: must be an object (got %T)", i, raw)
		}
		actName := strings.TrimSpace(StringArg(m, "name"))
		if actName == "" {
			return "", fmt.Errorf("actions[%d]: name is required", i)
		}
		if !validToolName(actName) {
			return "", fmt.Errorf("actions[%d].name %q: must be lowercase letters / digits / underscores only", i, actName)
		}
		if seen[actName] {
			return "", fmt.Errorf("actions[%d]: duplicate action name %q (each action must be uniquely named within the toolbox)", i, actName)
		}
		seen[actName] = true
		urlTpl := strings.TrimSpace(StringArg(m, "url_template"))
		if urlTpl == "" {
			return "", fmt.Errorf("actions[%d] (%q): url_template is required", i, actName)
		}
		actDesc := strings.TrimSpace(StringArg(m, "description"))
		actParams, err := parseParamsArg(m["params"])
		if err != nil {
			return "", fmt.Errorf("actions[%d] (%q): params: %w", i, actName, err)
		}
		actRequired := stringSliceArg(m["required"])
		// Distinguish "required omitted" (fall back to what the framework can
		// PROVE is required — see defaultRequiredParams) from an EXPLICIT empty
		// array (nothing required at all). Both yield
		// len==0 after stringSliceArg, so check presence: a non-nil value
		// under "required" means the author specified it — honor even [].
		// Without this, `required: []` silently became "all required", which
		// made optional params impossible (observed: a full 100-second thrash
		// trying to make feed's limit/sort optional).
		raw, present := m["required"]
		if !present || raw == nil {
			actRequired = defaultRequiredParams(urlTpl, actParams)
		} else {
			for _, r := range actRequired {
				if _, ok := actParams[r]; !ok {
					return "", fmt.Errorf("actions[%d] (%q): required lists %q which is not in params", i, actName, r)
				}
			}
		}
		method := strings.TrimSpace(StringArg(m, "method"))
		if method == "" {
			method = "GET"
		}
		bodyTpl := strings.TrimSpace(StringArg(m, "body_template"))
		// Write action whose required param lands in neither url_template nor
		// body_template would send it NOWHERE — the API 400s at RUN time. The
		// old behavior REJECTED this, but models (esp. small local workers) then
		// loop re-submitting the same POST without ever hand-writing a
		// body_template (observed: a whole conversation burned, then a false
		// "Done, I fixed it"). Instead:
		//   - no body_template at all → AUTO-SCAFFOLD one carrying the unsent
		//     params as a flat JSON body ({"p": {p}}). The obvious right shape;
		//     ends the loop and the write actually works.
		//   - a body_template exists but still misses them → a real key mismatch;
		//     keep the actionable error so the author fixes the keys.
		if missing := pathPlaceholderParams(urlTpl, actRequired); len(missing) > 0 {
			return "", fmt.Errorf("action %q: "+pathPlaceholderMsg, actName, missing, missing[0], missing[0])
		}
		// Scaffold from every non-URL param, not just the required ones: since
		// required now means "the URL can't be built without it", the body's
		// contents are precisely the params that are NOT required. See
		// writeBodyParams. The MISMATCH error below still keys on required — an
		// optional field the author left out of their own body_template is their
		// call to make, not a mistake to reject.
		scaffoldFrom := actRequired
		if bodyTpl == "" {
			scaffoldFrom = writeBodyParams(urlTpl, actParams)
		}
		if unsent := unsentWriteParams(method, urlTpl, bodyTpl, scaffoldFrom); len(unsent) > 0 {
			if bodyTpl != "" {
				return "", fmt.Errorf("actions[%d] (%q): required param(s) %v are sent NOWHERE — this %s action's body_template doesn't reference them, so the API never receives them (the cause of a 400 like \"content must be a string\"). Add them to the body_template, e.g. {\"content\": {content}}", i, actName, unsent, method)
			}
			bodyTpl = scaffoldBodyTemplate(unsent)
			scaffoldedActions = append(scaffoldedActions, actName)
		}
		// Every identifier-shaped {placeholder} in the templates must name a
		// declared param — the same gate the standalone api create has had all
		// along, previously MISSING here. Without it, dropping a param while a
		// template still references it passed authoring and died at DISPATCH
		// ("body template substitution produced invalid JSON: … {comment_id} …"
		// — the Moltbook reply_to_comment regression); worse, a raw
		// content_type body would send the unresolved placeholder to the API
		// verbatim. Validated AFTER scaffolding so the auto-built body is
		// covered too.
		if err := validateTemplate(urlTpl, actParams); err != nil {
			return "", fmt.Errorf("actions[%d] (%q): url_template: %w — every {placeholder} must name a declared param. If you removed a param, update the template in the same call", i, actName, err)
		}
		if bodyTpl != "" {
			if err := validateTemplate(bodyTpl, actParams); err != nil {
				return "", fmt.Errorf("actions[%d] (%q): body_template: %w — every {placeholder} must name a declared param. If you removed a param, update the body_template in the same call (otherwise dispatch dies substituting the template)", i, actName, err)
			}
		}
		actions = append(actions, TempToolAction{
			Name:            actName,
			Description:     actDesc,
			Params:          actParams,
			Required:        actRequired,
			URLTemplate:     urlTpl,
			Method:          method,
			BodyTemplate:    bodyTpl,
			ContentType:     strings.TrimSpace(StringArg(m, "content_type")),
			Headers:         stringMapArg(m, "headers"),
			ResponsePipe:    strings.TrimSpace(StringArg(m, "response_pipe")),
			ResponseExtract: ParseExtractSpec(m["response_extract"]),
			Disabled:        BoolArg(m, "disabled"),
		})
	}
	// Every action mints a "<toolbox>_<action>" catalog name, so the collision
	// check waits until the action list is final and tests all of them at once.
	if err := CheckCatalogNameCollision(sess, name, actions); err != nil {
		return "", err
	}
	tool := &TempTool{
		Name:        name,
		Description: desc,
		Mode:        TempToolModeToolbox,
		Credential:  credential,
		Actions:     actions,
		Expand:      BoolArg(args, "expand"),
		Category:    strings.TrimSpace(StringArg(args, "category")),
	}
	sess.RemoveTempTool(tool.Name)
	if err := sess.AppendTempTool(tool); err != nil {
		return "", err
	}
	// Durable home for the unapproved toolbox — see persistUnapprovedTool.
	persistUnapprovedTool(sess, tool)
	_ = BoolArg(args, "persist") // ignored — same as other modes
	msg := fmt.Sprintf("Created toolbox tool %q with %d action(s): %v. Call as %s(action=\"<sub-action>\", ...).",
		name, len(actions), actionNames(actions), name)
	if len(scaffoldedActions) > 0 {
		msg += fmt.Sprintf(" NOTE: for write action(s) %v I auto-added a body_template whose JSON keys are your PARAM NAMES — that is a GUESS at the API's body schema, not a verified fact. If the API expects different field names (a common case: it wants \"parent_id\" for a comment_id value), the live call will 4xx. Override with an explicit body_template via action=\"update\", mapping each value with its {param} placeholder — e.g. body_template={\"parent_id\": {comment_id}, \"content\": {content}}. Verify the field names against the API docs before relying on these actions.", scaffoldedActions)
	}
	return msg, nil
}

// scaffoldBodyTemplate builds a flat JSON body template carrying each param as
// {"name": {name}} — the obvious shape for a write action whose params are just
// a JSON body. Auto-generated when a POST/PUT/PATCH action declares required
// params but no body_template, so the fields reach the API instead of the author
// looping. Empty in → "" (nothing to carry).
// defaultRequiredParams answers "which of these params can this action not run
// without?" for an author who didn't say.
//
// The old answer was ALL of them, and it is the single largest source of tool
// errors in production: 371 in two days on one toolbox, ~360 of them a model
// bounced for omitting "cursor", "limit" or "sort" on a feed read. A cursor is
// the handle for the NEXT page — on a first call it cannot exist — so requiring
// it makes the action uncallable by construction, and the model has no way to
// learn that except by failing. It failed 260 times on one action.
//
// The new answer is the set the framework can PROVE: a param that fills a
// {placeholder} in the URL path. Without it there is no URL to request, so it
// is required whatever the endpoint thinks. Everything else is a query value or
// a body field that only the endpoint can rule on, and guessing on the author's
// behalf is what produced the loop. An author who knows better still says so
// explicitly, and an explicit list is still honoured exactly as written.
//
// Only DECLARED params count: a placeholder naming something else (a value
// spliced in from elsewhere) was never the caller's to supply.
func defaultRequiredParams(urlTpl string, params map[string]ToolParam) []string {
	var out []string
	for _, name := range pathPlaceholderParams(urlTpl, nil) {
		if _, declared := params[name]; declared {
			out = append(out, name)
		}
	}
	return out
}

// liveRequired is defaultRequiredParams applied to a toolbox action ALREADY IN
// STORAGE, at the moment it is registered.
//
// Authoring-time defaults only help tools authored from now on. The tools doing
// the damage are the ones already saved: every toolbox written before this
// carries required == all-its-params, because that was the default, and it
// keeps bouncing every call that omits a page cursor. Repairing here rather
// than migrating the records means nothing rewrites a user's tool behind their
// back, a restart is all it takes, and an author who later says what they mean
// still wins.
//
// Scoped to that exact fingerprint — required naming EVERY declared param, with
// something in there that the URL path does not need. An explicit list, a
// partial list, and a list that is all path placeholders anyway are all left
// alone, so this only touches definitions that could not have been deliberate:
// an author who wants a cursor mandatory can still say so, and gets it.
func liveRequired(act TempToolAction) []string {
	if len(act.Required) == 0 || len(act.Required) != len(act.Params) {
		return act.Required // explicit, partial, or nothing to do
	}
	for _, r := range act.Required {
		if _, ok := act.Params[r]; !ok {
			return act.Required // doesn't match the shape the old default produced
		}
	}
	repaired := defaultRequiredParams(act.URLTemplate, act.Params)
	if len(repaired) == len(act.Required) {
		return act.Required // every param really is a path placeholder
	}
	Debug("[temptool] action %q: required %v was every declared param — narrowed to the %d the URL actually needs (%v); the rest are now optional",
		act.Name, act.Required, len(repaired), repaired)
	return repaired
}

// writeBodyParams returns the params a write action has to carry in its BODY:
// every declared param that the URL template doesn't already spell. Sorted, so
// a scaffolded template is byte-identical between runs.
//
// Keyed on all params rather than the required ones, because "required" now
// means "the URL can't be built without it" — which is the exact complement of
// what belongs in a body. Scaffolding from required would produce a body
// holding the path ids and leave the content behind. Optional fields drop out
// cleanly when the caller omits them (see substituteJSON), so carrying them all
// costs nothing.
func writeBodyParams(urlTpl string, params map[string]ToolParam) []string {
	out := make([]string, 0, len(params))
	for name := range params {
		if !templateReferences(urlTpl, name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func scaffoldBodyTemplate(params []string) string {
	if len(params) == 0 {
		return ""
	}
	parts := make([]string, 0, len(params))
	for _, p := range params {
		parts = append(parts, fmt.Sprintf("%q: {%s}", p, p))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// actionNames returns the sub-action names of a toolbox for log /
// success-message formatting.
func actionNames(actions []TempToolAction) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, a.Name)
	}
	return out
}
