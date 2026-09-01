// tool_def — grouped management tool consolidating list_temp_tools,
// create_temp_tool, create_api_tool, and delete_temp_tool into one
// catalog entry with action="<list|create|delete|help>".
//
// Brief catalog description points the LLM at action="help" for the
// full usage spec. Reduces prompt budget consumed by 4 separate tool
// descriptions every round.

package temptool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"time"
)

// responseExtractDesc documents the response_extract spec for the tool_def
// schema. Shared across the create/action/update schemas so the shape stays
// consistent. Namespace-agnostic (local names) is the headline — it's what
// makes XML/CalDAV parseable without hand-written ElementTree/xpath.
const responseExtractDesc = "(api mode, optional) Parse an XML response into JSON declaratively — prefer it over a hand-written XML pipe or script. Shape {\"select\":\"<repeating element>\", \"where\":{…}, \"fields\":{\"<out>\":\"<selector>\"}}; matching is by local element name, namespaces ignored. Full grammar, selectors, filters, and a worked example: tool_def(action=\"help\")."

// BuildToolDef constructs the tool_def grouped tool. NOT globally
// registered — callers (Builder's catalog assembly) construct a
// fresh instance per session so the tool can't be reached except via
// explicit code import. Returns the ready-to-use *GroupedTool.
func BuildToolDef() *GroupedTool {
	gt := NewGroupedTool("tool_def",
		"Manage runtime-defined tools — wrappers around shell commands or registered API credentials. Use to list what's defined, create a new one, delete one you no longer need. Call action=\"help\" for the full usage spec including the workspace-first flow for wrapping scripts.")
	gt.SetHelpPreamble(helpText)
	// tool_def is serial-fire per batch: when the LLM bundles several
	// tool_def calls in one response (the classic [delete X, create Y]
	// replace, or two edits), they all run — but SEQUENTIALLY in submission
	// order, so the delete lands before the create and two writes can't race
	// the same record. Single-fire used to run only the first and SKIP the
	// rest, which fired the delete and dropped the recreate — leaving the
	// tool gone and costing a round to notice. Serial-fire keeps each
	// mutation ordered and visible while letting a legit multi-step edit
	// complete in one turn.
	gt.SetSerialFirePerBatch(true)
	// Decline the BLANKET fence and apply it per-action instead. Nearly every
	// action returns framework-authoring text (create/update/delete/list/get
	// confirmations) — not external content — and the union Caps() carry
	// CapNetwork only because "test" makes real calls, so fencing the whole tool
	// would wrap all that authoring output in an external-content warning.
	// "test" IS the exception and fences itself: it fires the authored tool at a
	// real endpoint and hands back the response. Any future action that returns
	// fetched content must do the same.
	gt.SetTrustedOutput(true)

	gt.AddAction("list", &GroupedToolAction{
		Description: "List all session-scoped + persistent tools currently available to you. Returns name, mode (shell|api), and a one-line description for each.",
		Params:      map[string]ToolParam{},
		Required:    nil,
		// Listing is read-only metadata. No caps required — gating is
		// done at runtime when a created tool is actually dispatched.
		Caps:         nil,
		NeedsConfirm: false,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil {
				return "", fmt.Errorf("requires a session")
			}
			return listGrouped(args, sess)
		},
	})

	gt.AddAction("create", &GroupedToolAction{
		Description: "Define a new runtime tool for THIS session. **THIS IS THE CREATION CALL — JUST CALL IT** — it IS the act of creation and persists automatically with NO approval step; never ask the user's permission first or say an admin must register it. After iterate-and-test (local(write) + local(run) to validate a script), the next step is ALWAYS tool_def(action=\"create\", ...) — without it you've written a script, not authored a tool. **COMPOSE BEFORE YOU BUILD**: if an existing tool already does part of the work (web_search for search, fetch_url for an HTTPS fetch, find_image / fetch_image / download_video for media), prefer chaining it via mode=\"pipeline\" (pipeline_steps) with a shell-mode tool for local processing — DON'T reimplement what the framework already gives you. CHOOSE MODE: (a) \"api\" — a single HTTPS endpoint the framework can't already reach (credential=\"no_auth\" for public APIs, or a registered credential name); (b) \"toolbox\" — MULTIPLE related endpoints under one tool name (a whole API surface: GitHub, Stripe, the moltbook social API), one catalog entry with action=\"<sub>\" dispatch sharing one credential. Toolboxes live ONLY here (`add_tool` can't build one); change a SINGLE action with action=\"update\" (actions=[{name, ...changed fields}]) rather than recreating; (c) \"shell\" — local computation/parsing/scripting on data the caller passes in, NOT network fetches; (d) \"pipeline\" — a deterministic chain of existing tools (e.g. fetch_url → your shell processor). For an adaptive multi-step LLM workflow author no tool at all — use the standalone pipeline tool. Do NOT wrap an HTTPS endpoint in a Python+urllib or curl script — that path is plagued by invented method names, homoglyph URL bugs, and JSON errors that don't exist in api/toolbox/pipeline mode. Required: name, description, mode, plus mode-specific fields — api: credential, url_template, method, params (optional body_template, response_pipe); toolbox: credential + actions[{name, description, url_template, params, ...}]; shell: command_template + params (script_body for non-trivial scripts); pipeline: pipeline_tools + pipeline_steps. Tools are immediately callable and persist across sessions — Builder's land in your user-wide pool (all your agents); every other agent's land on that agent's OWN record. Call action=\"help\" for the full spec + examples.",
		Params: map[string]ToolParam{
			"name":              {Type: "string", Description: "Tool name (snake_case, must not match an existing tool)."},
			"description":       {Type: "string", Description: "What the tool does and when to reach for it, in ONE or TWO sentences. This line is re-sent on every turn for the life of the tool — no worked examples, no restating the params, no failure modes. Hard cap 500 characters."},
			"mode":              {Type: "string", Description: "\"api\" (one HTTPS endpoint) · \"toolbox\" (several endpoints under one name, action=\"<sub>\" dispatch) · \"shell\" (local script) · \"pipeline\" (chain existing tools). See action=\"help\"."},
			"params":            {Type: "object", Description: "Object of {param: {type, description}}. Types: string|integer|number|boolean|array|object. Keep each description to one line — what the value is, plus the format only if it isn't obvious (cap 250 chars). Full rules + coercion in action=\"help\"."},
			"command_template":  {Type: "string", Description: "(shell) Shell command with {param} placeholders. Use script_body for anything non-trivial. See action=\"help\" for the sandbox fact sheet."},
			"script_body":       {Type: "string", Description: "(shell, optional) Full script source, written to the workspace and run. Python3 stdlib only — no pip. See action=\"help\"."},
			"script_name":       {Type: "string", Description: "(shell mode, optional) Filename for script_body. Defaults to \"script.py\". Match the script's language (e.g. \"run.sh\") — the extension drives interpreter selection when command_template is omitted."},
			"credential":        {Type: "string", Description: "(api/toolbox, optional) Name of a registered secure credential; auth is injected server-side and never reaches you. Use \"no_auth\" for public APIs. See action=\"help\"."},
			"url_template":      {Type: "string", Description: "(api mode) URL template with {param} placeholders, URL-encoded at dispatch. A path placeholder keeps its slashes as real separators, which is right for /repos/{owner_repo} or a CalDAV path. When the API wants a NESTED PATH as one segment — GitLab's files endpoint is the common case — write {param:encoded} and the whole value is percent-encoded, slashes included. Pass NATURAL values either way: pre-encoding a value yourself yields %25 where you meant %."},
			"method":            {Type: "string", Description: "(api mode) HTTP method. Default GET."},
			"body_template":     {Type: "string", Description: "(api) Request body with {param} placeholders, JSON-encoded and validated by default. See action=\"help\"."},
			"headers":           {Type: "object", Description: "(api, optional) Extra request headers as {name: value}. See action=\"help\"."},
			"content_type":      {Type: "string", Description: "(api, optional) Content-Type for the body. Empty = application/json; any other value switches to raw substitution."},
			"response_pipe":     {Type: "string", Description: "(api, optional) sh -c filter over the response body (jq/awk/sed) to keep noise out of your context. See action=\"help\" for the jq gotchas."},
			"response_extract":  {Type: "object", Description: responseExtractDesc},
			"category":          {Type: "string", Description: "Short grouping label for the tool catalog (e.g. \"Calendar\", \"Moltbook\")."},
			"required":          {Type: "array", Items: &ToolParam{Type: "string"}, Description: "Param names that must be supplied. Omit for none."},
			"state_path":        {Type: "string", Description: "(shell, optional) Workspace subdirectory this tool may persist state in."},
			"hook_capabilities": {Type: "array", Items: &ToolParam{Type: "string"}, Description: "(shell, optional) Extra sandbox capabilities the script needs. See action=\"help\" for the list and when each applies."},
			"raw_network":       {Type: "boolean", Description: "(shell, advanced) Allow direct outbound network from the script instead of the gohort fetch shims. See action=\"help\" before using."},
			// Pipeline-mode params. Either pipeline_prompt OR pipeline_steps is required.
			"pipeline_prompt": {Type: "string", Description: "(pipeline, ADAPTIVE) System prompt for a sub-agent that picks its own steps. Either this or pipeline_steps. See action=\"help\"."},
			"pipeline_steps": {Type: "array", Description: "(pipeline mode, DETERMINISTIC variant) Ordered list of step objects {tool, args, name?}, executed in sequence with no inner LLM. Args undergo template substitution: {param_name} → caller arg; $N → output of step N (1-indexed); $N.field.path → JSON field path. Mutually exclusive with pipeline_prompt.",
				Items: &ToolParam{
					Type: "object",
					Properties: map[string]ToolParam{
						"tool": {Type: "string", Description: "Name of the tool this step runs (must appear in pipeline_tools)."},
						"args": {Type: "object", Description: "Arguments passed to the tool; values may use {param} / $N templating."},
						"name": {Type: "string", Description: "Optional label to reference this step's output as $name in a later step."},
					},
					Required: []string{"tool"},
				}},
			"pipeline_tools":      {Type: "array", Items: &ToolParam{Type: "string"}, Description: "(pipeline mode) Names of tools the sub-agent (adaptive) or step executor (deterministic) may call. Must include every tool referenced in pipeline_steps."},
			"pipeline_max_rounds": {Type: "integer", Description: "(pipeline mode, adaptive only) Cap on sub-agent LLM rounds. Default 6. Ignored when pipeline_steps is set."},
			"actions": {Type: "array", Description: "(toolbox) Sub-actions, each either an HTTP endpoint (url_template) or a local command (command_template) — one or the other, never both. HTTP: {name, description, url_template, params, required?, method?, body_template?, content_type?, response_pipe?}, sharing the toolbox's credential. Local: {name, description, command_template, params, required?} — a command line with {placeholders}, shell-quoted and sandboxed exactly as a shell-mode tool's is. Use command_template for several verbs of ONE local binary (unpack / verify / list): they are one thing, and loose tools scattered across the catalog lose that. Names unique within the toolbox; called as <toolbox>(action=\"<sub>\", ...). See action=\"help\".",
				Items: &ToolParam{
					Type: "object",
					Properties: map[string]ToolParam{
						"name":             {Type: "string", Description: "Sub-action name, unique within the toolbox."},
						"description":      {Type: "string", Description: "What this sub-action does, in one sentence (cap 250 chars). The toolbox pays for this line once per action, on every turn."},
						"url_template":     {Type: "string", Description: "(HTTP action) Endpoint URL with {param} placeholders. Use {param:encoded} when a nested path must arrive as ONE percent-encoded segment (GitLab files, and any API that takes a path as an id). Give this OR command_template."},
						"command_template": {Type: "string", Description: "(local action) Command line with {param} placeholders, shell-quoted at dispatch and run in the sandbox exactly as a shell-mode tool is. Give this OR url_template. The HTTP-only fields (method, body_template, content_type, headers) do not apply."},
						"method":           {Type: "string", Description: "HTTP method. Default GET."},
						"params":           {Type: "object", Description: "Object of {param: {type, description}}. One line per description (cap 250 chars)."},
						"required":         {Type: "array", Items: &ToolParam{Type: "string"}, Description: "Param names that must be supplied. Omit for none."},
						"body_template":    {Type: "string", Description: "Request body with {param} placeholders."},
						"headers":          {Type: "object", Description: "Extra request headers as {name: value}."},
						"content_type":     {Type: "string", Description: "Content-Type for the body. Empty = application/json."},
						"response_pipe":    {Type: "string", Description: "sh -c filter over the response body (jq/awk)."},
						"response_extract": {Type: "object", Description: responseExtractDesc},
						"disabled":         {Type: "boolean", Description: "Quarantine this ONE action without touching the rest of the toolbox."},
					},
					// Not url_template: an action declares one template or the
					// other, and requiring the HTTP one would make every local
					// action fail schema validation before it was read.
					Required: []string{"name"},
				}},
			"expand": {Type: "boolean", Description: "(toolbox) Surface each action as its own top-level <toolbox>_<action> tool instead of one collapsed tool. See action=\"help\"."},
		},
		Required: []string{"name", "description", "mode"},
		// Creating a tool is registry CRUD — it does not execute anything.
		// The created tool, when invoked, carries its own caps (CapExecute
		// for shell mode, CapNetwork for api mode) and is filtered at
		// dispatch time. So this action itself needs no caps to be visible.
		Caps: nil,
		// Additive and reversible: a wrong tool is fixed with update or
		// removed with delete, and authoring is the whole point of the
		// agents that hold this. Gating every creation would put a prompt
		// in front of the most common authoring move in the system.
		NeedsConfirm: false,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil {
				return "", fmt.Errorf("requires a session")
			}
			// Enforced HERE rather than inside createGrouped: update
			// round-trips a stored tool back through that function, and a
			// legacy over-long description would then block edits that
			// aren't touching the description at all.
			if err := CheckAuthoredToolText(args); err != nil {
				return "", err
			}
			out, err := createGrouped(args, sess)
			if err == nil {
				// A freshly authored tool is UNVERIFIED until something proves
				// otherwise. Recorded so the build-plan done-gate can't sign off
				// on a tool nobody ever ran.
				RecordToolVerification(sess, strings.TrimSpace(StringArg(args, "name")), false, "authored but never tested — run tool_def(action=\"test\")")
			}
			return out, err
		},
	})

	gt.AddAction("get", &GroupedToolAction{
		Description: "Return the FULL definition of a tool by name — script_body, command_template, url_template, params, mode-specific fields, hook_capabilities. Read-only inspection: use this to COPY content from an existing tool (e.g. lift a known-good script_body, adapt a params shape) when authoring a new one, OR to inspect what's there before re-authoring with the same name (which overwrites the active entry). Returns a JSON-shaped block with every field set on the record. Pulls from the active pool first, then pending, then session drafts.",
		Params: map[string]ToolParam{
			"name": {Type: "string", Description: "Name of the tool to fetch."},
		},
		Required:     []string{"name"},
		Caps:         nil,
		NeedsConfirm: false,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil {
				return "", fmt.Errorf("requires a session")
			}
			return getGrouped(args, sess)
		},
	})

	gt.AddAction("update", &GroupedToolAction{
		Description: "THE way to fix a broken tool — ALWAYS reach for update before delete+recreate. Deleting loses the tool's working actions AND its credential wiring, and a from-scratch rebuild routinely fails on a detail you already had right. PARTIALLY edit an existing tool WITHOUT recreating it whole: pass name plus only the fields you're changing. For a TOOLBOX: pass actions=[{name, ...}] to upsert (an action name that already exists is replaced, a new one is added) — the OTHER actions are preserved untouched; pass remove_actions=[\"x\"] to drop actions. For an api/shell tool: pass any of description / params / required / url_template / command_template / method / body_template / response_pipe / script_body to change just those. (A POST action missing a body_template is auto-scaffolded — you don't have to hand-write it.)",
		Params: map[string]ToolParam{
			"name":             {Type: "string", Description: "The tool to update."},
			"description":      {Type: "string", Description: "(optional) New top-level description — one or two sentences, cap 500 chars. Omit to leave the current one alone."},
			"credential":       {Type: "string", Description: "(api/toolbox, optional) Name of a registered secure credential; auth is injected server-side and never reaches you. Use \"no_auth\" for public APIs. See action=\"help\"."},
			"actions":          {Type: "array", Description: "(toolbox) Action objects to UPSERT by name — same shape as create's actions (including optional `disabled` to quarantine/re-enable one action). Existing actions not listed here are kept as-is."},
			"remove_actions":   {Type: "array", Items: &ToolParam{Type: "string"}, Description: "(toolbox) Names of actions to remove."},
			"expand":           {Type: "boolean", Description: "(toolbox) Surface each action as its own top-level <toolbox>_<action> tool instead of one collapsed tool. See action=\"help\"."},
			"params":           {Type: "object", Description: "Object of {param: {type, description}}. Types: string|integer|number|boolean|array|object. Full rules + coercion in action=\"help\"."},
			"required":         {Type: "array", Items: &ToolParam{Type: "string"}, Description: "Param names that must be supplied. Omit for none."},
			"url_template":     {Type: "string", Description: "(api) New URL template."},
			"command_template": {Type: "string", Description: "(shell) Shell command with {param} placeholders. Use script_body for anything non-trivial. See action=\"help\" for the sandbox fact sheet."},
			"method":           {Type: "string", Description: "(api) New HTTP method."},
			"body_template":    {Type: "string", Description: "(api) Request body with {param} placeholders, JSON-encoded and validated by default. See action=\"help\"."},
			"headers":          {Type: "object", Description: "(api, optional) Extra request headers as {name: value}. See action=\"help\"."},
			"content_type":     {Type: "string", Description: "(api, optional) Content-Type for the body. Empty = application/json; any other value switches to raw substitution."},
			"response_pipe":    {Type: "string", Description: "(api, optional) sh -c filter over the response body (jq/awk/sed) to keep noise out of your context. See action=\"help\" for the jq gotchas."},
			"response_extract": {Type: "object", Description: "(api) New response_extract spec (XML→JSON). Same shape as create; see the create schema."},
			"category":         {Type: "string", Description: "Short grouping label for the tool catalog (e.g. \"Calendar\", \"Moltbook\")."},
			"script_body":      {Type: "string", Description: "(shell, optional) Full script source, written to the workspace and run. Python3 stdlib only — no pip. See action=\"help\"."},
		},
		Required:     []string{"name"},
		Caps:         nil,
		NeedsConfirm: false,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil {
				return "", fmt.Errorf("requires a session")
			}
			out, err := updateGrouped(args, sess)
			if err == nil {
				// An edit INVALIDATES any earlier pass: the tool that was tested
				// is not the tool that now exists. This is the exact hole that
				// let a failed verify get "fixed" by an update and then reported
				// as done without anyone re-running it.
				RecordToolVerification(sess, strings.TrimSpace(StringArg(args, "name")), false, "edited since it was last tested — re-run tool_def(action=\"test\")")
			}
			return out, err
		},
	})

	gt.AddAction("delete", &GroupedToolAction{
		Description: "Remove a tool by name — use ONLY when you truly want it gone. To FIX a tool that isn't working, use action=\"update\", NOT delete+recreate: deleting throws away the working actions and the credential wiring you'd have to rebuild from scratch (a common self-inflicted loop). Removes it from this session AND from your persistent pool if applicable. For an [agent-bundled] tool (one attached to the running agent's record — these reload every turn, so deleting just the session copy leaves it firing), this also unbundles it from the agent record so it does NOT come back next turn.",
		Params: map[string]ToolParam{
			"name": {Type: "string", Description: "Name of the tool to remove."},
		},
		Required: []string{"name"},
		// Deletion is registry CRUD — no caps required.
		Caps: nil,
		// The one action here that destroys work. Deleting takes the
		// tool's actions, its credential wiring, and its admin-approved
		// status with it, and a rebuild routinely misses a detail the
		// original had right. Observed live: three rejected updates, then
		// a delete that threw away a working admin-approved toolbox.
		// (create was marked confirm and delete was not — exactly
		// backwards.)
		NeedsConfirm: true,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil {
				return "", fmt.Errorf("requires a session")
			}
			return deleteGrouped(args, sess)
		},
	})

	gt.AddAction("test", &GroupedToolAction{
		Description: "VERIFY a tool actually works BEFORE you call it done or hand it to a user. SHELL tools: syntax-checks the script (an unterminated string or bad indent means every call dies before doing any work — this catches it without touching the live service), reports how each required param reaches the script, and — when you pass `cases` — RUNS the tool for real with those args and reports the exit status. A shell tool with no case stays UNVERIFIED: executing it is the only proof. API/TOOLBOX tools: for every endpoint it: (1) renders the URL + body template with your sample args and checks the body is valid JSON — catches a body field that never lands (the #1 cause of a live 400 like \"content must be a string\"); (2) compile-checks the response_pipe — catches a broken jq/awk filter before it fails live; (3) for READ endpoints (GET/HEAD and the read-only WebDAV queries REPORT/PROPFIND/SEARCH) it makes a real call and asserts a 2xx, then runs the response_pipe against the REAL response body — catches shape mismatches a syntax check can't. WRITE endpoints (POST/PUT/PATCH/DELETE) are NOT auto-fired (that would spam the live service): their body is render-validated only, and the report tells you to make one manual call and confirm a 2xx yourself. Pass `cases` with representative inputs per action so read probes and body renders have real values to work with (e.g. a real post_id for get_post). Returns a per-endpoint PASS/FAIL table. Run this, fix every FAIL by action=\"update\", and re-run until green — an unexercised toolbox action is a live grenade.",
		Params: map[string]ToolParam{
			"name":  {Type: "string", Description: "Name of the shell, api or toolbox tool to verify."},
			"cases": {Type: "array", Description: "Sample inputs to exercise. Array of objects: {action?: \"<sub-action>\" (toolbox only — omit for a single api tool), args: {param: value, ...}}. Provide one per endpoint you want live-probed or body-validated; give real values (a genuine id, a valid query) so read probes hit 2xx. Endpoints with no case still get offline checks (pipe compile-check, and body render when they need no required args).", Items: &ToolParam{Type: "object"}},
		},
		Required: []string{"name"},
		// Live read-probes reach the network; response_pipe compile-checks
		// run in the exec sandbox. Same caps an api/toolbox dispatch needs.
		Caps:         []Capability{CapNetwork, CapExecute},
		NeedsConfirm: false,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil {
				return "", fmt.Errorf("requires a session")
			}
			out, err := testGrouped(args, sess)
			if err != nil || strings.TrimSpace(out) == "" {
				return out, err
			}
			// Fence THIS action, since the tool as a whole opted out
			// (SetTrustedOutput above). Every other action returns authoring
			// text we generated; "test" fires the authored tool at a REAL
			// endpoint and reports the response body back, so its output is
			// third-party content wearing a PASS/FAIL table. Author a tool
			// against a hostile URL, hit test, and without this the response
			// lands in the authoring model's context as trusted text.
			return UntrustedToolResultFence + out, nil
		},
	})

	return gt
}

// listGrouped reuses the existing ListTempToolsTool logic via a
// shim. We can't call its RunWithSession directly without an instance,
// so reproduce the formatting inline.
func listGrouped(args map[string]any, sess *ToolSession) (string, error) {
	persistentByName := map[string]bool{}
	persistentDescByName := map[string]string{}
	persistentModeByName := map[string]string{}
	pendingByName := map[string]bool{}
	pendingDescByName := map[string]string{}
	pendingModeByName := map[string]string{}
	var orphanedPool []OrphanedTempTool
	if sess.DB != nil && sess.Username != "" {
		orphanedPool = LoadOrphanedTempTools(sess.DB, sess.Username)
		for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
			persistentByName[p.Tool.Name] = true
			persistentDescByName[p.Tool.Name] = p.Tool.Description
			persistentModeByName[p.Tool.Name] = p.Tool.Mode
		}
		for _, p := range LoadPendingTempTools(sess.DB, sess.Username) {
			pendingByName[p.Tool.Name] = true
			pendingDescByName[p.Tool.Name] = p.Tool.Description
			pendingModeByName[p.Tool.Name] = p.Tool.Mode
		}
	}
	// Deployment-wide shared tools live in their OWNER's pool, so none of
	// the maps above contain one you don't own. Without this, such a tool
	// listed as "[session-only]" — the exact opposite of the truth (it is
	// permanent and global), and with no hint that someone else owns it.
	sharedOwners := SharedToolOwners(sess.DB)
	tools := sess.CopyTempTools()
	if len(tools) == 0 && len(pendingByName) == 0 && len(persistentByName) == 0 && len(orphanedPool) == 0 {
		return "No temp tools defined in this session.", nil
	}
	var b strings.Builder
	inSession := make(map[string]bool, len(tools))
	for i, t := range tools {
		inSession[t.Name] = true
		var tag string
		switch {
		// Agent-bundled wins the tag: a tool can be BOTH bundled and in
		// the persistent pool, and "bundled" is the fact that explains
		// why deleting the session copy doesn't stick (the record
		// reloads it each turn). Say so, and say how to remove it.
		case sess.BundledToolNames[t.Name]:
			tag = " [agent-bundled — attached to this agent's record; delete removes it from the record too]"
		case persistentByName[t.Name]:
			tag = " [persistent]"
		case pendingByName[t.Name]:
			tag = " [pending approval]"
		case sharedOwners[t.Name] != "" && sharedOwners[t.Name] != sess.Username:
			tag = " [shared deployment-wide — owned by " + sharedOwners[t.Name] + "; read-only to you, copy under a new name to change it]"
		case sharedOwners[t.Name] == sess.Username:
			tag = " [persistent, shared deployment-wide — you own it; edits affect every user]"
		default:
			tag = " [session-only]"
		}
		fmt.Fprintf(&b, "%d. %s%s [%s] — %s\n", i+1, t.Name, tag, modeLabel(t.Mode), t.Description)
	}
	// Orphan pending: tools queued for approval that aren't currently in
	// sess.TempTools (e.g. requested in a prior session, still waiting).
	// Surface them as a footer so the LLM understands why the catalog
	// doesn't have a tool it remembers requesting. Approved-and-loaded
	// orphans (persistent but not in this session) shouldn't happen in
	// normal flow but list them too for completeness.
	var orphanPending []string
	for name := range pendingByName {
		if !inSession[name] {
			orphanPending = append(orphanPending, name)
		}
	}
	if len(orphanPending) > 0 {
		b.WriteString("\nPending approval (queued but not yet usable in this session — admin must approve):\n")
		for _, name := range orphanPending {
			mode := pendingModeByName[name]
			if mode == "" {
				mode = "shell"
			}
			fmt.Fprintf(&b, "  - %s [%s] — %s\n", name, modeLabel(mode), pendingDescByName[name])
		}
	}
	// Approved-but-not-loaded: tools in the user's persistent pool that
	// aren't in THIS session's executable catalog. The common case is
	// Builder, which deliberately doesn't load user-authored persistent
	// tools ("authors fresh") — so without this footer an APPROVED tool
	// the user is asking about (e.g. "the moltbook toolbox") is invisible
	// to Builder, which then insists the only such tool is a pending
	// draft of a different name. Surface them read-only so the model can
	// SEE (and tool_def get / delete) them, and knows they already exist.
	var orphanPersistent []string
	for name := range persistentByName {
		if !inSession[name] && !pendingByName[name] {
			orphanPersistent = append(orphanPersistent, name)
		}
	}
	sort.Strings(orphanPersistent)
	if len(orphanPersistent) > 0 {
		b.WriteString("\nApproved & in your tool pool, but NOT loaded in this session (exists already — inspect with tool_def get, or load_tool it to call/test it; don't re-author a duplicate):\n")
		for _, name := range orphanPersistent {
			mode := persistentModeByName[name]
			if mode == "" {
				mode = "shell"
			}
			fmt.Fprintf(&b, "  - %s [%s] — %s\n", name, modeLabel(mode), persistentDescByName[name])
		}
	}
	// Orphaned: the last agent carrying the tool was deleted, so the record
	// left every catalog. Listing them is what makes the disappearance
	// legible — otherwise a tool the model used last week is simply absent,
	// and "absent" reads as "I must reach it some other way."
	{
		var orphaned []OrphanedTempTool
		for _, o := range orphanedPool {
			if !persistentByName[o.Tool.Name] && !inSession[o.Tool.Name] {
				orphaned = append(orphaned, o)
			}
		}
		sort.Slice(orphaned, func(i, j int) bool { return orphaned[i].Tool.Name < orphaned[j].Tool.Name })
		if len(orphaned) > 0 {
			b.WriteString("\nORPHANED — definition survives but the tool is NOT callable by anyone (its last carrying agent was deleted). Re-home in Admin › Orphaned Tools, or tool_def get then re-create it. Do NOT try to reach these another way:\n")
			for _, o := range orphaned {
				mode := o.Tool.Mode
				if mode == "" {
					mode = "shell"
				}
				former := o.FormerAgentName
				if former == "" {
					former = "deleted agent"
				}
				fmt.Fprintf(&b, "  - %s [%s] (was on %s) — %s\n", o.Tool.Name, modeLabel(mode), former, o.Tool.Description)
			}
		}
	}
	return b.String(), nil
}

func modeLabel(mode string) string {
	switch mode {
	case TempToolModeAPI:
		return "api"
	case TempToolModeToolbox:
		return "toolbox"
	case TempToolModePipeline:
		return "pipeline"
	default:
		return "shell"
	}
}

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

// createPipelineGrouped builds a pipeline-mode TempTool from the
// grouped-action arg map and registers it on the session. Wraps a
// multi-step sub-agent flow as a single LLM-callable tool. Two
// shapes: adaptive (pipeline_prompt drives an LLM sub-agent over
// pipeline_tools) or deterministic (pipeline_steps runs in order
// with no inner LLM). One of the two is required.
func createPipelineGrouped(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("requires a session")
	}
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	desc := strings.TrimSpace(StringArg(args, "description"))
	if desc == "" {
		return "", fmt.Errorf("description is required")
	}
	params, err := parseParamsArg(args["params"])
	if err != nil {
		return "", fmt.Errorf("params: %w", err)
	}
	required := stringSliceArg(args["required"])
	if len(required) == 0 {
		for k := range params {
			required = append(required, k)
		}
	}
	prompt := strings.TrimSpace(StringArg(args, "pipeline_prompt"))
	steps := pipelineStepsFromArg(args["pipeline_steps"])
	if prompt == "" && len(steps) == 0 {
		return "", fmt.Errorf("either pipeline_prompt (adaptive) or pipeline_steps (deterministic) is required for mode=\"pipeline\"")
	}
	if prompt != "" && len(steps) > 0 {
		return "", fmt.Errorf("pipeline_prompt and pipeline_steps are mutually exclusive — pick one")
	}
	inner := stringSliceArg(args["pipeline_tools"])
	if len(inner) == 0 {
		return "", fmt.Errorf("pipeline_tools must list at least one inner tool name")
	}
	if len(steps) > 0 {
		allowed := map[string]bool{}
		for _, n := range inner {
			allowed[n] = true
		}
		for i, s := range steps {
			if !allowed[s.Tool] {
				return "", fmt.Errorf("pipeline_steps[%d].tool %q is not in pipeline_tools %v — add it or pick a different tool", i, s.Tool, inner)
			}
		}
	}
	maxRounds := 0
	if v, ok := args["pipeline_max_rounds"]; ok {
		switch n := v.(type) {
		case float64:
			maxRounds = int(n)
		case int:
			maxRounds = n
		}
	}

	tool := &TempTool{
		Name:              name,
		Description:       desc,
		Params:            params,
		Required:          required,
		Mode:              TempToolModePipeline,
		PipelinePrompt:    prompt,
		PipelineSteps:     steps,
		PipelineTools:     inner,
		PipelineMaxRounds: maxRounds,
	}
	sess.RemoveTempTool(tool.Name)
	if err := sess.AppendTempTool(tool); err != nil {
		return "", err
	}
	// Durable home for the unapproved pipeline tool — see persistUnapprovedTool.
	persistUnapprovedTool(sess, tool)
	shape := "adaptive (pipeline_prompt + pipeline_tools)"
	if len(steps) > 0 {
		shape = fmt.Sprintf("deterministic (%d steps)", len(steps))
	}
	return fmt.Sprintf("Pipeline tool %q registered (%s) for this session. Inner tools: %v. Dispatch by name with the declared params to verify the flow.", name, shape, inner), nil
}

// pipelineStepsFromArg coerces the LLM-supplied pipeline_steps value
// into []PipelineStep. Accepts the native []any of objects shape;
// silently drops malformed entries instead of erroring so the LLM
// gets a clear "missing pipeline_steps" or step-mismatch message
// from the caller instead of a parse complaint.
func pipelineStepsFromArg(raw any) []PipelineStep {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]PipelineStep, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tool, _ := m["tool"].(string)
		tool = strings.TrimSpace(tool)
		if tool == "" {
			continue
		}
		step := PipelineStep{Tool: tool}
		if name, ok := m["name"].(string); ok {
			step.Name = strings.TrimSpace(name)
		}
		if a, ok := m["args"].(map[string]any); ok {
			step.Args = a
		}
		out = append(out, step)
	}
	return out
}

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

// updateGrouped applies a PARTIAL edit to an existing tool without recreating
// it whole — the fix for the recreate-everything pain (a one-action change
// meant resupplying all N actions, inviting copy-paste errors). It loads the
// record, patches the provided fields (for a toolbox: upserts the given
// actions by name and applies remove_actions), then routes the merged result
// through createGrouped so persistence is identical to create.
func updateGrouped(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("requires a session")
	}
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	// Only the fields this call actually passes — a patch to url_template
	// must not be refused over a description it inherited.
	if err := CheckAuthoredToolText(args); err != nil {
		return "", err
	}
	// Stage breadcrumbs. An update that never returns leaves the UI showing
	// "running" forever with nothing in the log to say WHERE it stopped —
	// resolve, script syntax-check (which shells out), or persist. The gap
	// between the last stage logged here and the next line in the log is the
	// answer. Cheap, and it turns the next hang into a five-second diagnosis
	// instead of a code read.
	t0 := time.Now()
	stage := "resolve"
	Debug("[tool_def] update %q: begin", name)
	defer func() {
		Debug("[tool_def] update %q: returned after %s (last stage: %s)", name, time.Since(t0), stage)
	}()
	_ = stage
	if persistentToolLocked(sess, name) {
		return fmt.Sprintf(lockedToolMsg, name), nil
	}
	existing, ok := loadExistingToolRecord(sess, name)
	if !ok {
		// Not in the user-wide pool, pending queue, this session's drafts, or
		// this agent's own live tools — but it may be bundled to ANOTHER of the
		// user's agents (e.g. Builder repairing a tool that lives on a channel
		// agent's record). Resolve it there and redirect the write-back to that
		// agent so the edit lands IN PLACE, not promoted to the shared pool.
		if FindUserAgentTool != nil {
			if tt, ownerAgent, found := FindUserAgentTool(sess.DB, sess.Username, name); found {
				existing = tt
				ok = true
				sess.BundleAuthoredToolTo = ownerAgent
				defer func() { sess.BundleAuthoredToolTo = "" }()
			}
		}
	}
	if !ok {
		// Before declaring it missing: it may be a DEPLOYMENT-WIDE shared
		// tool. Those are callable by everyone but live in their owner's
		// pool, so the searches above never see one you don't own. Saying
		// "no tool named X" there is actively misleading — the model just
		// called it.
		if _, owner, shared := FindSharedToolWithOwner(sess.DB, name); shared {
			return fmt.Sprintf("Tool %q is a DEPLOYMENT-WIDE SHARED tool owned by %s, so you cannot edit it from here — an edit would change it for every user. Nothing is broken and there is nothing to report. Options: ask %s to make the change; or copy it into your own pool with action=\"create\" under a NEW name (use action=\"get\" to read its current definition first) and edit that. Do NOT re-create it under the SAME name.",
				name, owner, owner), nil
		}
		return "", fmt.Errorf("no tool named %q to update — use action=\"create\" to make a new one, or action=\"list\" to see what exists", name)
	}
	// Scope-preserving write-back (flattened namespace): when the resolved
	// record is AGENT-scoped, pin the write-back to a carrying agent so
	// finalize routes through AttachToolToAgent — which updates the ONE store
	// row in place with its ScopeAgents intact. Without this, a non-Builder
	// session's finalize falls to sess.BundleTool, which would extend the
	// tool's scope to the EDITING agent — an edit must never be a grant.
	// (Builder's AdminPersistTempTool path also preserves scope; the redirect
	// just makes every session take the same explicit route.)
	if sess.BundleAuthoredToolTo == "" {
		if row, found := UserToolByName(sess.DB, sess.Username, name); found && len(row.ScopeAgents) > 0 {
			sess.BundleAuthoredToolTo = row.ScopeAgents[0]
			defer func() { sess.BundleAuthoredToolTo = "" }()
		}
	}
	stage = "merge"
	merged := tempToolToCreateArgs(existing)

	// Secured-credential bindings are AUTO-RESOLVED on the re-run create below:
	// the tool already declares the cred, so the authoring guard just re-approves
	// it (unless an admin revoked it, which the guard respects). No pre-approval
	// or edit re-review needed — access follows the tool's own scope.

	// A toolbox has NO top-level url/body/method/etc — those are per-ACTION.
	// Passing one at the top level used to be silently ignored (the "I set
	// body_template on the toolbox and nothing changed, so I tried again five
	// times" trap). Reject with a redirect that shows the correct shape.
	if existing.Mode == TempToolModeToolbox {
		for _, f := range []string{"url_template", "command_template", "method", "body_template", "response_pipe", "script_body", "script_name", "params", "required"} {
			if _, present := args[f]; present {
				return "", fmt.Errorf("%q is a PER-ACTION field on a toolbox, not a top-level one — setting it at the top level does nothing. Put it INSIDE the action: actions=[{name:\"<action>\", %s:...}]. Example fixing a reply body: actions=[{name:\"reply_to_comment\", body_template:{\"parent_id\": {comment_id}, \"content\": {content}}}] (unspecified fields on that action are preserved)", f, f)
			}
		}
	}

	// Patch top-level scalar fields when provided (present = intent to change).
	// "expand" (toolbox presentation toggle) rides the same present-means-change
	// path — BoolArg in createToolboxGrouped reads whatever value lands here.
	for _, f := range []string{"description", "credential", "url_template", "command_template", "method", "body_template", "content_type", "headers", "response_pipe", "response_extract", "category", "script_body", "script_name", "expand"} {
		if v, present := args[f]; present {
			merged[f] = v
		}
	}
	if v, present := args["params"]; present {
		merged["params"] = v
	}
	if v, present := args["required"]; present {
		merged["required"] = v
	}
	// url_template and command_template are ONE stored field
	// (TempTool.CommandTemplate) under two names, and
	// tempToolToCreateArgs seeds BOTH from it so either spelling works on
	// the way in. That is exactly what made a patch silently revert:
	// passing command_template alone left the STALE url_template sitting
	// beside it, and api-mode create reads url_template — so the update
	// reported success, echoed the new value, and persisted the old one.
	// get and live dispatch then both showed the original template, which
	// reads as a broken write path rather than a merge that preferred the
	// wrong twin.
	//
	// Whichever the caller supplied now wins for both. When both are
	// supplied and differ, the mode decides which is meant.
	reconcileTemplateAliases(merged, existing, args)

	// Toolbox: upsert the given actions by name, then apply remove_actions.
	if existing.Mode == TempToolModeToolbox {
		cur, _ := merged["actions"].([]any)
		byName := map[string]int{}
		for i, a := range cur {
			if am, ok := a.(map[string]any); ok {
				byName[strings.TrimSpace(StringArg(am, "name"))] = i
			}
		}
		if inc, present := args["actions"]; present {
			incList, ok := inc.([]any)
			if !ok {
				return "", fmt.Errorf("actions must be an array of action objects to upsert")
			}
			for _, a := range incList {
				am, ok := a.(map[string]any)
				if !ok {
					return "", fmt.Errorf("each entry in actions must be an object")
				}
				an := strings.TrimSpace(StringArg(am, "name"))
				if an == "" {
					return "", fmt.Errorf("each action to upsert needs a name")
				}
				if idx, found := byName[an]; found {
					// Field-level MERGE, not whole-object replace. The docs
					// promise "just the changed fields"; a wholesale replace
					// silently dropped every field the caller didn't re-supply.
					// That was the "I set body_template but it reverted to the
					// param-name guess" bug: updating an action's params without
					// re-passing body_template lost the explicit body, and the
					// write-action scaffold regenerated the wrong one. Merge so
					// unspecified fields (body_template, response_pipe, method,
					// description, disabled, …) survive.
					if existingAM, ok := cur[idx].(map[string]any); ok {
						for k, v := range am {
							existingAM[k] = v
						}
						// Dropping a param has to drop its required entry too.
						// params is REPLACED by the merge above, required is only
						// replaced if the caller re-sent it — so removing a param
						// while leaving required alone strands a name that no
						// longer exists. Dispatch then demands a param the schema
						// doesn't declare ("requires param phone_number_id" for a
						// tool whose help lists no such param), which is
						// unfixable from the model's side: every subsequent
						// update reproduces it, and the tool reads as haunted.
						// The intent is unambiguous — the param is gone — so
						// prune rather than erroring.
						if _, sentParams := am["params"]; sentParams {
							if _, sentRequired := am["required"]; !sentRequired {
								existingAM["required"] = pruneRequired(existingAM["required"], existingAM["params"])
							}
						}
						cur[idx] = existingAM
					} else {
						cur[idx] = am
					}
				} else {
					cur = append(cur, am) // add new
					byName[an] = len(cur) - 1
				}
			}
		}
		if rem := stringSliceArg(args["remove_actions"]); len(rem) > 0 {
			remSet := map[string]bool{}
			for _, r := range rem {
				remSet[strings.TrimSpace(r)] = true
			}
			kept := make([]any, 0, len(cur))
			for _, a := range cur {
				am, _ := a.(map[string]any)
				if am != nil && remSet[strings.TrimSpace(StringArg(am, "name"))] {
					continue
				}
				kept = append(kept, a)
			}
			cur = kept
		}
		if len(cur) == 0 {
			return "", fmt.Errorf("that would leave the toolbox with no actions — delete the tool instead if you mean to remove it")
		}
		merged["actions"] = cur
	}

	// createGrouped re-runs the full create path, which for a shell tool
	// SHELLS OUT to syntax-check the script (py_compile / bash -n). That
	// subprocess is the most likely place for a long stall — pair this line
	// with the [sandbox] spawn/exit breadcrumbs to tell them apart.
	stage = "create/persist (may syntax-check the script in a subprocess)"
	Debug("[tool_def] update %q: entering create path after %s", name, time.Since(t0))
	res, err := createGrouped(merged, sess)
	if err == nil {
		// An update that reports success while the record is unchanged is
		// the single most expensive failure this path can have: the caller
		// believes the fix landed, re-tests, sees the old behaviour, and
		// goes looking for the bug somewhere else entirely. It cost an
		// investigation most of a session.
		//
		// So the claim is checked rather than assumed, against the same
		// lookup action="get" uses.
		if warn := unlandedUpdateWarning(sess, name, args); warn != "" {
			return "Updated " + name + ", BUT THE CHANGE DID NOT LAND. " + warn + " " + res, nil
		}
	}
	if err != nil {
		return "", err
	}
	return "Updated " + name + " in place. " + res, nil
}

// securedBindingCreds returns the set of SECURED credentials a tool binds — its
// api/toolbox Credential plus every fetch_via:<cred> in its hook_capabilities.
// Used to re-pend (on material edit) or clear (on delete) exactly those bindings.
func securedBindingCreds(tt TempTool) map[string]bool {
	out := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if cr, ok := Secure().Load(name); ok && cr.Secured {
			out[name] = true
		}
	}
	add(tt.Credential)
	for _, c := range tt.HookCapabilities {
		if i := strings.IndexByte(c, ':'); i >= 0 && strings.EqualFold(strings.TrimSpace(c[:i]), "fetch_via") {
			add(c[i+1:])
		}
	}
	return out
}

func deleteGrouped(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(StringArg(args, "name"))
	if persistentToolLocked(sess, name) {
		return fmt.Sprintf(lockedToolMsg, name), nil
	}
	// Forget this tool's secured-credential bindings (approved/pending) on delete —
	// tidy-up under auto-resolve. A revoke tombstone is deliberately KEPT so an
	// admin's deny survives a delete + same-name recreate.
	if rec, ok := loadExistingToolRecord(sess, name); ok {
		for cred := range securedBindingCreds(rec) {
			_ = Secure().ForgetToolBinding(cred, name)
		}
	}
	// Agent-bundled tool: the durable copy lives on the agent RECORD and
	// is reconstituted every turn, so removing only the session/pool copy
	// leaves a "zombie" that keeps firing. Route through the app's
	// unbundle callback to remove it from the record, THEN evict the live
	// session copy so it can't dispatch again this turn either.
	if sess != nil && sess.BundledToolNames[name] {
		if sess.UnbundleTool == nil {
			return "", fmt.Errorf("tool %q is bundled onto this agent's record; this surface can't unbundle it — remove it from the agent in its editor (Tools modal → Remove), or ask Builder to update the agent", name)
		}
		if err := sess.UnbundleTool(name); err != nil {
			return "", fmt.Errorf("unbundle %q from the agent record: %w", name, err)
		}
		sess.RemoveTempTool(name)
		// Also clear any lingering session-draft / pending shadows of the
		// same name so it doesn't reappear from a different pool.
		if sess.DB != nil && sess.ChatSessionID != "" {
			RemoveSessionTempTool(sess.DB, sess.ChatSessionID, name)
		}
		if sess.DB != nil && sess.Username != "" {
			DequeuePendingTempTool(sess.DB, sess.Username, name)
		}
		return fmt.Sprintf("Unbundled %q from this agent's record and dropped it from the session — it will not reload next turn.", name), nil
	}
	// Bundled to ANOTHER of the user's agents: the durable copy lives on that
	// agent's record. update already resolves + writes back there in place
	// (FindUserAgentTool → BundleAuthoredToolTo); without the same reach here,
	// delete either reported "no temp tool named X" or removed only a session
	// shadow while the record copy reloaded next turn. Mirror the in-place
	// semantics: remove from the owning agent's record, then clear any
	// session/pending shadows so nothing resurrects the name. Gated on the
	// name having NO higher-precedence durable home (pool / pending / session
	// draft) — when a duplicate exists in both the pool and an agent record,
	// delete peels the copy that actually WINS resolution first; a second
	// delete then reaches the record copy.
	if sess != nil && sess.DB != nil && sess.Username != "" && FindUserAgentTool != nil &&
		!toolInUserPools(sess, name) && !toolInSessionDrafts(sess, name) {
		if rec, ownerAgent, found := FindUserAgentTool(sess.DB, sess.Username, name); found {
			if DetachToolFromAgent == nil {
				return "", fmt.Errorf("tool %q lives on agent %s's record; this surface can't remove it — remove it from that agent in its editor (Tools modal → Remove)", name, ownerAgent)
			}
			for cred := range securedBindingCreds(rec) {
				_ = Secure().ForgetToolBinding(cred, name)
			}
			if err := DetachToolFromAgent(sess.DB, sess.Username, ownerAgent, name); err != nil {
				return "", fmt.Errorf("remove %q from agent %s's record: %w", name, ownerAgent, err)
			}
			sess.RemoveTempTool(name)
			if sess.ChatSessionID != "" {
				RemoveSessionTempTool(sess.DB, sess.ChatSessionID, name)
			}
			DequeuePendingTempTool(sess.DB, sess.Username, name)
			Log("[temptool.scope] removed %q from agent %s's record (in-place delete)", name, ownerAgent)
			return fmt.Sprintf("Removed %q from the owning agent's record (it lived on agent %s, not this session) — it will not reload next turn.", name, ownerAgent), nil
		}
	}
	t := &DeleteTempToolTool{}
	res, err := t.RunWithSession(args, sess)
	if err == nil && sess != nil && sess.DB != nil && sess.Username != "" {
		// Dequeue from pending-review pool too. If the LLM cancels a
		// tool it just authored, the admin shouldn't still see it in
		// their review queue — that'd be stale work that won't fire.
		DequeuePendingTempTool(sess.DB, sess.Username, name)
	}
	return res, err
}

// testGrouped verifies an api/toolbox tool end-to-end BEFORE it ships:
// it render- and JSON-validates every endpoint's body, checks that every
// required param is actually SENT somewhere (the #1 authoring bug —
// a POST param that lands in neither url_template nor body_template, so
// the API 400s with "field must be a string"), compile-checks every
// response_pipe, live-probes READ endpoints for a 2xx (running the pipe
// against the REAL body), and render-validates WRITE endpoints without
// firing them. Returns a per-endpoint PASS/FAIL report the author acts on.
func testGrouped(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required — the api/toolbox tool to verify")
	}
	tt, ok := loadExistingToolRecord(sess, name)
	if !ok {
		return "", fmt.Errorf("no tool named %q — use action=\"list\" to see what exists", name)
	}

	// Flatten to a uniform endpoint list. A single api tool becomes one
	// synthetic endpoint; a toolbox contributes each of its actions.
	//
	// Mode is resolved through effectiveTempToolMode, NOT read raw: shell
	// tools are stored with Mode=="" (the legacy spelling — see
	// createGrouped and add_tool), so a raw `case TempToolModeAPI, ""`
	// swept every shell tool into the api path and "verified" it by
	// HTTP-GETting its command_template. That produced the nonsense
	// `unsupported protocol scheme ""` verdict on a working script and
	// sent the authoring model editing command_template in a loop trying
	// to make a python3 invocation look like a URL.
	var endpoints []TempToolAction
	switch effectiveTempToolMode(tt) {
	case TempToolModeToolbox:
		endpoints = tt.Actions
	case TempToolModeShell:
		return testShellTool(tt, args, sess)
	case TempToolModeAPI:
		if strings.TrimSpace(tt.CommandTemplate) == "" {
			return "", fmt.Errorf("tool %q has no url_template — nothing to probe", name)
		}
		endpoints = []TempToolAction{{
			Name: name, Params: tt.Params, Required: tt.Required,
			URLTemplate: tt.CommandTemplate, Method: tt.Method,
			BodyTemplate: tt.BodyTemplate, ContentType: tt.ContentType,
			Headers:      tt.Headers,
			ResponsePipe: tt.ResponsePipe, ResponseExtract: tt.ResponseExtract,
		}}
	default:
		return "", fmt.Errorf("tool %q is mode=%q — test verifies shell, api and toolbox tools. For a %s tool, exercise it by calling it directly with real args", name, tt.Mode, tt.Mode)
	}
	if len(endpoints) == 0 {
		return "", fmt.Errorf("tool %q has no endpoints to test", name)
	}

	cases := parseTestCases(args["cases"])

	// Private mode / a blocked network connector means the offline checks
	// (param wiring, body render, pipe compile) still run, but live read
	// probes can't — degrade gracefully to offline-only rather than
	// reporting a spurious "live probe errored" on every read endpoint.
	netOK := sess.NetworkAllowed()

	var b strings.Builder
	fmt.Fprintf(&b, "Verification report for %q (%d endpoint(s)):\n\n", name, len(endpoints))
	if !netOK {
		b.WriteString("(network is blocked this turn — running OFFLINE checks only; read endpoints are not live-probed.)\n\n")
	}
	failCount, writeManual, emptyRead := 0, 0, 0

	for _, ep := range endpoints {
		method := strings.ToUpper(strings.TrimSpace(ep.Method))
		if method == "" {
			method = "GET"
		}
		// GET/HEAD and the read-only WebDAV query methods (REPORT, PROPFIND,
		// SEARCH — RFC 3253/4918) are safe to live-fire: they QUERY, never
		// mutate. A CalDAV list_events is a REPORT; without this it was
		// misclassified as a write, so verify refused to fire it and the model
		// punted a manual call to the user for a plain read.
		isRead := method == "GET" || method == "HEAD" || method == "REPORT" || method == "PROPFIND" || method == "SEARCH"
		sample := cases[strings.ToLower(ep.Name)]
		if sample == nil {
			sample = cases[""] // single-api-tool convenience: unlabeled case
		}

		var lines []string
		epFail := false
		fail := func(f string, a ...any) { lines = append(lines, "FAIL  "+fmt.Sprintf(f, a...)); epFail = true }
		pass := func(f string, a ...any) { lines = append(lines, "ok    "+fmt.Sprintf(f, a...)) }
		note := func(f string, a ...any) { lines = append(lines, "note  "+fmt.Sprintf(f, a...)) }

		// A. Every required param must be SENT somewhere. This is the
		//    deterministic, offline catch for the "content must be a
		//    string" class: a required param referenced in neither the
		//    url_template nor the body_template never reaches the API.
		var unref []string
		for _, r := range ep.Required {
			if !templateReferences(ep.URLTemplate, r) && !templateReferences(ep.BodyTemplate, r) {
				unref = append(unref, r)
			}
		}
		if len(unref) > 0 {
			if ep.BodyTemplate == "" && !isRead {
				fail("required param(s) %v are sent NOWHERE — this %s action has no body_template, so the API never receives them (the exact cause of a 400 like \"content must be a string\"). Add a body_template, e.g. {\"content\": {content}}.", unref, method)
			} else {
				fail("required param(s) %v appear in neither url_template nor body_template — the API will never receive them.", unref)
			}
		} else {
			pass("all required params are wired into the url/body templates")
		}

		// B. Body template renders with the sample args. A non-JSON
		// content_type (application/xml for CalDAV/SOAP) switches to RAW
		// substitution + no JSON validation — mirroring the dispatch path,
		// so an XML PROPFIND/REPORT body doesn't fail verify as "invalid
		// JSON". content_type is a tool-level field (toolbox actions are
		// JSON-only today), so it applies to the single-endpoint api case.
		if ep.BodyTemplate != "" {
			// Per-action content_type wins (toolbox actions each carry their own);
			// fall back to the tool-level one for a single api tool.
			epCT := ep.ContentType
			if epCT == "" {
				epCT = tt.ContentType
			}
			rawBody := epCT != "" && !isJSONContentType(epCT)
			if coversRequired(sample, ep.Required) {
				if rawBody {
					if _, err := substituteRaw(ep.BodyTemplate, ep.Params, ep.Required, sample); err != nil {
						fail("body_template render failed: %v", err)
					} else {
						pass("body_template renders (raw, %s — no JSON validation)", epCT)
					}
				} else if body, err := substituteJSON(ep.BodyTemplate, ep.Params, ep.Required, sample); err != nil {
					fail("body_template render failed: %v", err)
				} else if jerr := json.Unmarshal([]byte(body), new(any)); jerr != nil {
					fail("body_template produced INVALID JSON: %v — rendered body: %s. (For an XML/non-JSON API set content_type, e.g. \"application/xml\", so the body is sent RAW.)", jerr, oneLine(body, 200))
				} else {
					pass("body_template renders valid JSON")
				}
			} else {
				note("body_template not render-checked — no sample args covering required %v (pass a case)", ep.Required)
			}
		}

		// C. response_pipe compiles (catches a broken jq/awk filter).
		if ep.ResponsePipe != "" {
			if serr := pipeCompileError(ep.ResponsePipe, sess); serr != "" {
				fail("response_pipe has a syntax/compile error: %s", serr)
			} else {
				pass("response_pipe compiles")
			}
		}

		// D. READ endpoints: real call + assert 2xx + run pipe on the
		//    real body. WRITE endpoints are never auto-fired.
		if isRead {
			switch {
			case !netOK:
				note("read endpoint NOT live-probed — network is blocked this turn (private mode); offline checks only")
			case coversRequired(sample, ep.Required):
				status, body, derr := liveProbe(sess, tt.Credential, ep, sample)
				switch {
				case derr != nil:
					fail("live probe errored: %v", derr)
				case !isStatus2xx(status):
					fail("live call returned %q (want 2xx) — body: %s", status, oneLine(body, 200))
				default:
					pass("live %s returned %q", method, status)
					if ep.ResponsePipe != "" {
						if perr := runPipeAgainst(ep.ResponsePipe, body, sess); perr != "" {
							fail("response_pipe failed on the REAL response body (shape mismatch — e.g. the filter expects .posts[] but the body is a bare array): %s", perr)
						} else {
							pass("response_pipe runs clean on the real response")
						}
					}
					// A 2xx that carried NO records is the single most
					// misleading result this action can produce. It proves the
					// request was well-formed and says NOTHING about whether the
					// query is right — and it is exactly what a CalDAV REPORT
					// missing its Depth header returns (an empty 207
					// multistatus). Reported live: "all endpoints passed, tool
					// verified" on a list tool that could never return an event,
					// while the user was saying it didn't work.
					//
					// Not a FAIL — an empty collection is a legitimate state for
					// a fresh account — but it must never read as proof the tool
					// returns data.
					if emptyResultBody(body, ep) {
						note("live call returned 2xx but ZERO records — this proves the request is well-formed, NOT that the query is right. If you expected data: check the filter/date-range, and for WebDAV/CalDAV check headers (a REPORT/PROPFIND without \"Depth\": \"1\" matches nothing and returns exactly this). Confirm against data you know exists before calling it done.")
						emptyRead++
					}
				}
			default:
				note("read endpoint NOT live-probed — no sample args for required %v (pass a case with real values to hit the live API)", ep.Required)
			}
		} else {
			note("write endpoint NOT auto-fired — make ONE manual %s call and confirm a 2xx before calling this done", method)
			writeManual++
		}

		verdict := "PASS"
		if epFail {
			verdict = "FAIL"
			failCount++
		}
		fmt.Fprintf(&b, "[%s] %s (%s)\n", verdict, ep.Name, method)
		for _, l := range lines {
			fmt.Fprintf(&b, "   %s\n", l)
		}
		b.WriteByte('\n')
	}

	// Record the verdict where it's actually known, rather than leaving
	// downstream to parse this prose. Only a clean sweep counts as verified:
	// a FAIL obviously doesn't, and neither does "all automated checks passed
	// but N write endpoints still need a manual call" — an unfired write
	// endpoint is exactly the untested grenade this action exists to catch.
	switch {
	case failCount > 0:
		RecordToolVerification(sess, name, false, fmt.Sprintf("%d of %d endpoint(s) FAILED verification", failCount, len(endpoints)))
		fmt.Fprintf(&b, "RESULT: %d of %d endpoint(s) FAILED. Fix each with tool_def(action=\"update\", actions=[{name, ...}]) and re-run test until green. Do NOT call this tool done or hand it to a user while any endpoint is FAIL.", failCount, len(endpoints))
	case writeManual > 0:
		RecordToolVerification(sess, name, false, fmt.Sprintf("%d write endpoint(s) never fired — needs one manual live call each to confirm a 2xx", writeManual))
		fmt.Fprintf(&b, "RESULT: all automated checks passed. %d write endpoint(s) still need ONE manual live call each — fire one, confirm a 2xx, then it's done.", writeManual)
	case emptyRead > 0:
		// Checks passed, but every read came back empty — the tool is
		// UNPROVEN, not verified. Signing it off here is what let a list tool
		// that could never return a row ship as "verified".
		RecordToolVerification(sess, name, false, fmt.Sprintf("%d read endpoint(s) returned 2xx with zero records — not proven to return data", emptyRead))
		fmt.Fprintf(&b, "RESULT: the request shape is valid, but %d read endpoint(s) came back EMPTY — nothing here proves the tool returns data. Point a case at a record you KNOW exists and re-run; if it is still empty, the query (filter, date range, headers) is wrong, not the plumbing.", emptyRead)
	default:
		RecordToolVerification(sess, name, true, "")
		b.WriteString("RESULT: all endpoints passed. Tool verified.")
	}
	return b.String(), nil
}

// effectiveTempToolMode resolves a stored record's mode. Mode=="" is the
// legacy spelling of shell (createGrouped, add_tool and the shell dispatch
// path all treat it that way), so an empty mode resolves to shell — EXCEPT
// for a record whose command_template is plainly an http(s) URL, which is an
// api tool written before Mode was populated. Never returns "".
func effectiveTempToolMode(tt TempTool) string {
	if m := strings.TrimSpace(tt.Mode); m != "" {
		return m
	}
	cmd := strings.TrimSpace(tt.CommandTemplate)
	if strings.HasPrefix(cmd, "http://") || strings.HasPrefix(cmd, "https://") {
		return TempToolModeAPI
	}
	return TempToolModeShell
}

// testShellTool verifies a shell-mode tool. There are no endpoints to probe,
// so the checks are the ones that actually catch shell-tool bugs:
//
//	(1) the script PARSES — an unterminated string or a bad indent means every
//	    dispatch dies before doing any work, and nothing else in the report
//	    matters until it's fixed;
//	(2) each required param has a delivery route the script can read;
//	(3) the tool actually RUNS, for real, with the author's sample args.
//
// (3) is the only thing that verifies a shell tool — there is no offline
// substitute — so a tool tested without `cases` stays UNVERIFIED and the
// report says why. The run is a genuine dispatch: the tool's side effects
// happen. Pass sample args you're willing to have executed.
func testShellTool(tt TempTool, args map[string]any, sess *ToolSession) (string, error) {
	cases := parseTestCases(args["cases"])
	sample := cases[strings.ToLower(tt.Name)]
	if sample == nil {
		sample = cases[""] // single-tool convenience: unlabeled case
	}

	var lines []string
	failed := false
	fail := func(f string, a ...any) { lines = append(lines, "FAIL  "+fmt.Sprintf(f, a...)); failed = true }
	pass := func(f string, a ...any) { lines = append(lines, "ok    "+fmt.Sprintf(f, a...)) }
	note := func(f string, a ...any) { lines = append(lines, "note  "+fmt.Sprintf(f, a...)) }

	// A. Does the script parse? Runs the interpreter's own syntax checker —
	//    no args, no network, no side effects. This is the deterministic
	//    catch for the class where a tool was authored with a broken
	//    f-string / quote and every single call returns a SyntaxError.
	if strings.TrimSpace(tt.ScriptBody) != "" {
		lang, problem, checked := scriptSyntaxCheck(tt, sess)
		switch {
		case !checked:
			note("script_body not syntax-checked (no checker available for this language) — the live run is the only proof")
		case problem != "":
			fail("script_body has a SYNTAX ERROR — every dispatch dies before the tool does any work: %s", problem)
		default:
			pass("script_body parses clean (%s)", lang)
		}
	} else if strings.Contains(tt.CommandTemplate, "{workspace_dir}") {
		note("no script_body on the record, but command_template references a workspace file — the tool breaks the moment that workspace is wiped. Re-author with script_body so the script travels with the tool record.")
	}

	// B. Param delivery. A shell tool receives every arg BOTH as a {param}
	//    substitution in command_template AND as a lowercase env var, so a
	//    param missing from the template is not a bug the way it is for an
	//    api tool — it just means the script must read it from the
	//    environment. Report the route rather than failing on it.
	var envOnly []string
	for _, r := range tt.Required {
		if !templateReferences(tt.CommandTemplate, r) {
			envOnly = append(envOnly, r)
		}
	}
	switch {
	case len(envOnly) > 0:
		note("required param(s) %v are not in command_template — they reach the script ONLY as lowercase env vars (os.environ[%q] / $%s). Confirm the script reads them there, not from argv.", envOnly, envOnly[0], envOnly[0])
	case len(tt.Required) > 0:
		pass("every required param is substituted into command_template")
	}

	// B2. Does the script call a hook the tool never declared?
	//
	// The hook methods are capability-gated at dispatch: an undeclared one
	// comes back "method %q not granted", which surfaces inside the script as
	// whatever that failure does to the code after it. An agent repairing a
	// tool swapped fetch_url for browse_page and left hook_capabilities alone;
	// the run died downstream and it concluded the SITE was blocking it. This
	// is a static, deterministic check — the same class as the syntax check —
	// so it fails rather than notes.
	if body := tt.ScriptBody; strings.TrimSpace(body) != "" {
		for _, hc := range []struct{ call, capability string }{
			{"fetch_url", "fetch"},
			{"browse_page", "browse_page"},
			{"fetch_via", "fetch_via"},
			{"secret", "secret"},
		} {
			if !scriptCallsHook(body, hc.call) || hookCapabilityDeclared(tt.HookCapabilities, hc.capability) {
				continue
			}
			fail("script_body calls %s() but hook_capabilities does not include %q — that call is refused at dispatch (\"method not granted\"), and the script fails on whatever it does with the result. Add %q to hook_capabilities.",
				hc.call, hc.capability, hc.capability)
		}
	}

	// C. The real run.
	ran := false
	switch {
	case sample == nil:
		note("tool NOT run — pass cases=[{args:{...}}] with real values. Running it is the ONLY thing that verifies a shell tool; the checks above can't.")
	case !coversRequired(sample, tt.Required):
		note("tool NOT run — the supplied case doesn't cover required %v. Pass a value for each.", tt.Required)
	default:
		ran = true
		out, derr := DispatchTempToolDirect(sess, &tt, sample)
		switch {
		case derr != nil:
			fail("live run FAILED: %v", derr)
		case shellRunFailed(out):
			fail("live run returned a non-zero exit / timeout: %s", oneLine(out, 300))
		default:
			pass("live run succeeded — output: %s", oneLine(out, 200))
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Verification report for %q (shell tool):\n\n", tt.Name)
	verdict := "PASS"
	if failed {
		verdict = "FAIL"
	} else if !ran {
		verdict = "UNVERIFIED"
	}
	fmt.Fprintf(&b, "[%s] %s\n", verdict, tt.Name)
	for _, l := range lines {
		fmt.Fprintf(&b, "   %s\n", l)
	}
	b.WriteByte('\n')

	switch {
	case failed:
		RecordToolVerification(sess, tt.Name, false, "shell tool failed verification")
		b.WriteString("RESULT: FAILED. Fix with tool_def(action=\"update\", script_body=\"...\") and re-run test until it's green. Do NOT call this tool done or hand it to a user while it FAILs.")
	case !ran:
		RecordToolVerification(sess, tt.Name, false, "never run — test was called without cases")
		b.WriteString("RESULT: NOT VERIFIED. The static checks passed, but the tool was never executed. Re-run: tool_def(action=\"test\", name=\"" + tt.Name + "\", cases=[{args:{...}}]) with real values.")
	default:
		RecordToolVerification(sess, tt.Name, true, "")
		b.WriteString("RESULT: ran clean with the supplied args. Tool verified.")
	}
	return b.String(), nil
}

// shellRunFailed reports whether a shell dispatch result is the framework's
// rendering of a non-zero exit or a killed command. dispatchTempTool returns
// those as OUTPUT with a trailing marker rather than as an error, so a report
// that only checks err would call a script that died on line 1 a success.
func shellRunFailed(out string) bool {
	return strings.Contains(out, "[exit: ") || strings.Contains(out, "[TIMED OUT")
}

// scriptSyntaxCheck parses tt.ScriptBody with the interpreter's own syntax
// checker inside the sandbox. Returns the language, a one-line problem
// description (empty when it parses), and whether a verdict could be reached
// at all — an unknown extension or a missing interpreter yields checked=false
// rather than a false accusation of a syntax error.
func scriptSyntaxCheck(tt TempTool, sess *ToolSession) (lang, problem string, checked bool) {
	name := tt.CanonicalScriptName
	if name == "" {
		name = tt.ScriptName
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		// No filename on the record — infer from the interpreter the
		// command_template invokes.
		switch {
		case strings.Contains(tt.CommandTemplate, "python"):
			ext = ".py"
		case strings.Contains(tt.CommandTemplate, "bash"), strings.Contains(tt.CommandTemplate, "sh "):
			ext = ".sh"
		case strings.Contains(tt.CommandTemplate, "node"):
			ext = ".js"
		}
	}
	var checker string
	switch ext {
	case ".py":
		lang, checker = "python3", "python3 -m py_compile %s"
	case ".sh", ".bash":
		lang, checker = "bash", "bash -n %s"
	case ".js":
		lang, checker = "node", "node --check %s"
	case ".rb":
		lang, checker = "ruby", "ruby -c %s"
	default:
		return "", "", false
	}

	dir, err := MintToolDispatchDir("toolsyntax-")
	if err != nil {
		return lang, "", false
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "script"+ext)
	if werr := os.WriteFile(path, []byte(tt.ScriptBody), 0700); werr != nil {
		return lang, "", false
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	// Authoring-time checks are still sandboxed runs, so they carry the same
	// caller stamp as a dispatch — otherwise an admin on a host that cannot
	// confine gets their tool refused by the checker that was supposed to
	// help them write it, with "sandbox refused" as the only verdict.
	ctx = sess.ContextWithSandboxCaller(ctx)
	res := RunSandboxedShell(ctx, fmt.Sprintf(checker, shellQuote(path)), dir)
	if res.Err == nil && !res.TimedOut {
		return lang, "", true
	}
	// A non-zero exit is only evidence of a SYNTAX problem when the output
	// says so. Anything else (interpreter not installed, sandbox refused,
	// timeout) is a checker failure, not the author's bug — say nothing
	// rather than send them rewriting a script that's fine.
	out := strings.TrimSpace(res.Output)
	low := strings.ToLower(out)
	for _, marker := range []string{"syntaxerror", "syntax error", "unexpected", "indentationerror", "parse error", "unterminated"} {
		if strings.Contains(low, marker) {
			return lang, oneLine(out, 300), true
		}
	}
	return lang, "", false
}

// stringMapArg reads a {name: value} object arg into a string map, tolerating
// the shapes an LLM actually emits: a JSON object (the normal path) or a JSON
// STRING containing one (models quote objects surprisingly often). Non-string
// values are stringified rather than dropped, so headers={"Depth": 1} still
// sends "1" instead of silently sending nothing. Returns nil when empty, so an
// absent field stays absent through the create/update round-trip.
func stringMapArg(args map[string]any, key string) map[string]string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	// Already a string map — the shape tempToolToCreateArgs emits when an
	// update round-trips a stored tool. Missing this case is how a field
	// survives create and vanishes on the next unrelated edit.
	if m, ok := raw.(map[string]string); ok {
		if len(m) == 0 {
			return nil
		}
		out := make(map[string]string, len(m))
		for k, v := range m {
			if k = strings.TrimSpace(k); k != "" {
				out[k] = v
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	if str, isStr := raw.(string); isStr {
		str = strings.TrimSpace(str)
		if str == "" {
			return nil
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(str), &m); err != nil {
			return nil
		}
		raw = m
	}
	m, isMap := raw.(map[string]any)
	if !isMap || len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		k = strings.TrimSpace(k)
		if k == "" || v == nil {
			continue
		}
		if str, isStr := v.(string); isStr {
			out[k] = str
			continue
		}
		out[k] = strings.TrimSpace(fmt.Sprint(v))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseTestCases normalizes the `cases` arg into action-name → args.
// Each case is {action?: "<sub>", args: {...}}; a case with no action
// is stored under "" for the single-api-tool convenience path.
func parseTestCases(v any) map[string]map[string]any {
	out := map[string]map[string]any{}
	list, ok := v.([]any)
	if !ok {
		return out
	}
	for _, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(StringArg(m, "action")))
		a, _ := m["args"].(map[string]any)
		if a == nil {
			a = map[string]any{}
		}
		out[key] = a
	}
	return out
}

// coversRequired reports whether sample supplies a non-empty value for
// every required param — the precondition for a body render or a live
// probe that would otherwise error on a missing arg (which is not an
// authoring bug, just an absent sample).
func coversRequired(sample map[string]any, required []string) bool {
	for _, r := range required {
		v, ok := lookupArgCI(sample, r)
		if !ok || v == nil {
			return false
		}
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			return false
		}
	}
	return true
}

// liveProbe dispatches an endpoint for real with its response_pipe
// CLEARED, so the raw "HTTP <code>\n<body>" comes back for status
// classification and for running the pipe separately against the true
// body. Reuses the production api dispatch path end-to-end.
func liveProbe(sess *ToolSession, cred string, ep TempToolAction, sample map[string]any) (status, body string, err error) {
	syn := TempTool{
		Name: "test." + ep.Name, Params: ep.Params, Required: ep.Required,
		Mode: TempToolModeAPI, CommandTemplate: ep.URLTemplate, Credential: cred,
		Method: ep.Method, BodyTemplate: ep.BodyTemplate,
		// Carry content_type so a raw XML/CalDAV/iCalendar body is sent as-is
		// (not JSON-validated). ResponsePipe + ResponseExtract are suppressed:
		// the probe only confirms a live 2xx; projection/extraction correctness
		// is checked separately, and running them here could turn a healthy 2xx
		// into a spurious FAIL.
		// Headers MUST ride along: probing without the Depth header a CalDAV
		// REPORT requires would test a different request than dispatch sends,
		// and report a 2xx for a call the real tool can't make work.
		ContentType: ep.ContentType, Headers: ep.Headers, ResponsePipe: "",
	}
	inner := canonicalizeArgKeys(cloneArgs(sample), ep.Required, ep.Params)
	raw, derr := dispatchAPIModeTempTool(sess, &syn, inner)
	if derr != nil {
		return "", raw, derr
	}
	status, body = splitStatusLine(raw)
	return status, body, nil
}

// emptyResultBody reports whether a 2xx response carried no records. It is
// deliberately conservative — only shapes that unambiguously mean "nothing
// came back" count, because a false positive would nag about a healthy tool.
//
// Recognized: an empty body; a bare empty JSON array/object; and the WebDAV
// signature that motivated this check — a multistatus element with no
// <response> children, which is what a CalDAV REPORT returns when it matched
// nothing (classically, a missing Depth header). When the endpoint declares a
// response_extract, the extraction is run and its RESULT is what's judged: an
// extractor yielding [] over a body full of XML is still zero records.
func emptyResultBody(body string, ep TempToolAction) bool {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return true
	}
	if ep.ResponseExtract != nil {
		out, err := ExtractXML([]byte(trimmed), *ep.ResponseExtract)
		if err != nil {
			return false // extraction problems are reported elsewhere
		}
		trimmed = strings.TrimSpace(string(out))
	}
	switch trimmed {
	case "[]", "{}", "null":
		return true
	}
	// WebDAV: <multistatus/> or <multistatus ...></multistatus> with no
	// <response> child. Namespace prefixes vary (D:, d:, none), so match on
	// the local name rather than a fixed spelling.
	low := strings.ToLower(trimmed)
	if strings.Contains(low, "multistatus") && !strings.Contains(low, "<response") &&
		!strings.Contains(low, ":response") {
		return true
	}
	return false
}

// pipeCompileError runs a response_pipe against a trivial JSON doc and
// returns a non-empty message ONLY for a syntax/compile error — those
// fire regardless of input shape and are true authoring bugs. A runtime
// error against the dummy input (null iteration, missing field) is not a
// compile bug and yields "" (the real shape is checked live for reads).
func pipeCompileError(pipe string, sess *ToolSession) string {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	// Authoring-time checks are still sandboxed runs, so they carry the same
	// caller stamp as a dispatch — otherwise an admin on a host that cannot
	// confine gets their tool refused by the checker that was supposed to
	// help them write it, with "sandbox refused" as the only verdict.
	ctx = sess.ContextWithSandboxCaller(ctx)
	res := RunSandboxedShellPipe(ctx, pipe, "{}")
	if res.Err == nil {
		return ""
	}
	msg := strings.ToLower(fmt.Sprint(res.Err) + " " + res.Output)
	if strings.Contains(msg, "syntax error") || strings.Contains(msg, "compile error") || strings.Contains(msg, "unexpected") {
		return oneLine(res.Output, 200)
	}
	return ""
}

// runPipeAgainst runs a response_pipe against a real response body and
// returns a non-empty message if it failed (bad filter, shape mismatch).
func runPipeAgainst(pipe, body string, sess *ToolSession) string {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	// Authoring-time checks are still sandboxed runs, so they carry the same
	// caller stamp as a dispatch — otherwise an admin on a host that cannot
	// confine gets their tool refused by the checker that was supposed to
	// help them write it, with "sandbox refused" as the only verdict.
	ctx = sess.ContextWithSandboxCaller(ctx)
	res := RunSandboxedShellPipe(ctx, pipe, body)
	if res.TimedOut {
		return "timed out"
	}
	if res.Err != nil {
		return oneLine(res.Output, 200)
	}
	return ""
}

// pathPlaceholderMsg is the authoring error for an optional PATH placeholder.
// Shared by the single-api-tool and toolbox-action create paths so both refuse
// the same shape with the same guidance.
const pathPlaceholderMsg = "param(s) %v are interpolated into the url_template's PATH but are not required — url substitution has nothing to put there when they're omitted, so the call dies at dispatch with `url template: missing arg \"%s\"`. Either add them to required, or move them to the query string (\"?key={%s}\"), where an omitted placeholder legitimately drops out of the URL"

// placeholderRE matches a {param} interpolation in a URL or body template.
// The optional ":modifier" tail is part of the placeholder syntax (see
// urlEncodeModifiers), so it must be part of every pattern that looks for
// one. Group 1 stays the bare name.
var placeholderRE = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)(?::[A-Za-z_]+)?\}`)

// templateReferences reports whether tmpl interpolates param — with or
// without an encoding modifier.
//
// Every "is this param wired in?" check was an exact strings.Contains for
// "{name}", so adding {name:encoded} made three of them believe the
// param was referenced nowhere. The tool worked; the VALIDATOR failed it,
// printing "live GET returned 200" and "required param appears in neither
// template" in the same report. A checker that contradicts itself is
// worse than one that says nothing, because the author has to work out
// which half to trust.
func templateReferences(tmpl, param string) bool {
	if param == "" {
		return false
	}
	return strings.Contains(tmpl, "{"+param+"}") || strings.Contains(tmpl, "{"+param+":")
}

// pathPlaceholderParams returns the params a url_template interpolates into its
// PATH — the segment before any "?" — that are not in required.
//
// A path placeholder cannot be optional: url-template substitution has nothing
// to put there when the arg is absent, so the call dies with `url template:
// missing arg "uid"` at DISPATCH time, long after authoring, with an error that
// reads like a framework bug rather than a spec mistake. (Seen live on an
// iCloud CalDAV create tool whose "{uid}.ics" filename param was declared
// optional — the model then invented a uid to get past it.)
//
// QUERY placeholders are deliberately excluded: "?since={cursor}" legitimately
// drops out of the URL when omitted, which is the documented optional-query
// behavior.
func pathPlaceholderParams(urlTpl string, required []string) []string {
	path := urlTpl
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	req := map[string]bool{}
	for _, r := range required {
		req[r] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range placeholderRE.FindAllStringSubmatch(path, -1) {
		name := m[1]
		if name == "" || req[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// unsentWriteParams returns the required params of a WRITE action (POST/PUT/
// PATCH/DELETE) that appear in neither the url_template nor the body_template
// — meaning the API never receives them. Scoped to write methods so it never
// blocks the legitimate GET "_"-placeholder pattern (a dummy required param
// that satisfies an API demanding some query arg). Reads rarely carry a
// required body field, so the risk there isn't worth the false-positive.
func unsentWriteParams(method, urlTpl, bodyTpl string, required []string) []string {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "POST", "PUT", "PATCH", "DELETE":
	default:
		return nil
	}
	var out []string
	for _, r := range required {
		if !templateReferences(urlTpl, r) && !templateReferences(bodyTpl, r) {
			out = append(out, r)
		}
	}
	return out
}

// cloneArgs returns a shallow copy so dispatch-side canonicalization can
// rewrite keys without mutating the caller's sample map.
func cloneArgs(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// oneLine collapses whitespace/newlines and truncates for a compact,
// single-line report cell.
func oneLine(s string, max int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// suppress unused import — json is used by future expansions; keep
// the import handy.
var _ = json.Marshal

// helpText is the full usage guide returned by action="help". Kept
// inline (not loaded from disk) so it ships with the binary and
// can't drift from the action descriptions.
const helpText = `tool_def — runtime tool builder

Use this to define a wrapper around a shell command or an HTTP API
call. Three modes: "shell", "api", and "pipeline". Pick by what you
need to do, not by what's easier to write.

================================================================
SANDBOX FACT SHEET — read this BEFORE authoring shell-mode tools
================================================================

The shell-mode sandbox is restrictive. The most common authoring
failures come from assuming things that aren't true. Memorize this:

PYTHON
  * python3 is available
  * STDLIB ONLY. There is NO pip, NO requests, NO pillow (PIL),
    NO numpy, NO pandas, NO beautifulsoup4, NO lxml, NO opencv.
  * Safe imports: json, re, csv, sqlite3, urllib.parse, hashlib,
    hmac, datetime, collections, itertools, functools, os, sys,
    subprocess, pathlib, base64, html, xml.etree.ElementTree,
    statistics, math, random. (urllib.request is NOT on this list —
    it is a network call and tool_def REFUSES scripts that use it;
    see NETWORK.)
  * Need a third-party package? PIVOT — jq/awk for parsing,
    gohort.fetch_url for HTTP, or api mode usually reaches the
    same outcome.

SHELL
  * Interpreter is sh (POSIX), not bash. No arrays, no [[ ]],
    no <(...). Use plain sh-compatible syntax.
  * Reliably available binaries: jq, awk, sed, grep, head,
    tail, sort, uniq, tr, cut, wc, basename, dirname, date, cat,
    echo, printf, tee, xargs, find.
  * NOT available: bash-only features. curl/wget are NOT usable —
    the sandbox has no network (see NETWORK), and tool_def
    REFUSES scripts that call them.

NETWORK
  * The shell sandbox is NETWORK-ISOLATED (bwrap --unshare-net).
    curl, wget, urllib.request, socket — they ALL FAIL inside a
    shell-mode tool, and tool_def refuses a script_body that uses
    any of them at authoring time.
  * HTTP from a script goes through the gohort bridge instead:
    "from gohort import fetch_url" then fetch_url(url) — granted
    by default, no declaration needed. Authenticated or scoped
    endpoints: hook_capabilities=["fetch_via:<credential>"].
  * api mode is usually the better fit for HTTPS work anyway. It
    handles credentials, allow-listed URLs, audit logs, and rate
    limits — none of which a script gets on its own. Pick api
    mode for any work that just hits an HTTPS endpoint.

FILESYSTEM
  * Writable paths:
      {workspace_dir}  — your tool's bound sandbox. PERSISTS
                         across invocations of THIS tool when
                         StatePath is set; otherwise contents
                         survive while the tool exists but you
                         shouldn't rely on persistence across
                         deletes.
      /tmp             — tmpfs, ephemeral. WIPED every invocation.
                         Fine for scratch files within a single
                         dispatch; do NOT use for state.
  * Read-only paths: /usr, /bin, /sbin, /lib, /etc/{resolv.conf,
    hosts, ssl, alternatives} — bound from the host so binaries
    + DNS + TLS just work.
  * NOT VISIBLE: /home, /root, /var, anywhere outside the binds
    above. Don't reference user home paths or arbitrary system
    paths.
  * For state across invocations: write inside {workspace_dir}
    and declare StatePath on the tool.

THE script_body / script_name PATTERN
  * script_body = the source of a script shipped INTO the sandbox.
  * script_name = the filename it's written as (default "script.py").
  * command_template references {workspace_dir}/<script_name>.
  * One ship at registration; reused on every dispatch. You do not
    re-ship the script per call.
  * MULTI-FILE: helper files your entry script pulls in (a Python
    module it imports, a bash file it sources) are bundled into the
    tool AUTOMATICALLY — write them to the workspace with local(write)
    under the name the script imports (helper.py for "import helper"),
    and they travel with the tool and survive workspace wipes. No
    extra param; just author them beside the entry script.

THE local() TOOL IS A DIFFERENT SANDBOX
  * local() lets you iterate on a script BEFORE wrapping it as a
    tool. Its sandbox is per-user, not per-tool.
  * After local-testing, when you call tool_def, the script_body
    you pass gets shipped into the TOOL's fresh sandbox. They're
    separate environments.

PROBING FOR BINARIES (workspace probe action)
  * Before authoring a tool that depends on a non-POSIX binary
    (convert, ffmpeg, yt-dlp, etc.), call workspace(action="probe")
    to verify it's present:
        workspace(action="probe", name="ffmpeg")
        → "available at /usr/bin/ffmpeg" or "NOT available"
  * No user confirmation required (the probe is scope-limited to
    a "command -v" lookup with a validated identifier — zero
    injection surface). Call it freely during design.
  * If the probe says NOT available, pivot — don't author a tool
    that will fail at dispatch.

EMITTING ATTACHMENTS (images, video, audio)
  * Shell-mode tools CAN attach binary content to the reply by
    writing a marker block to stdout:

        <<<ATTACH:image/png
        <base64 data, can span multiple lines>
        ATTACH_END>>>

  * Supported mimes: image/* (PNG, JPEG, GIF, WEBP), video/*
    (MP4, WEBM, MOV), audio/* (MP3, WAV, M4A, OGG).
  * Multiple markers per stdout = multiple attachments.
  * The dispatcher strips the marker from the LLM-visible output
    and routes the base64 to the session's attachment channel.
  * Use this when the tool PROCESSES binaries (fetch+convert,
    transcode, crop). For plain fetch-and-attach, prefer the
    built-ins: find_image, fetch_image, generate_image,
    download_video. They're more efficient (no base64 round-trip
    through stdout) and don't need authoring.

If you're tempted to author a tool that imports requests, runs
under bash, or writes to /tmp expecting persistence — STOP and
pivot. The shell sandbox will reject those at dispatch time.

================================================================
WHEN TO USE WHICH MODE — decide by the work, not by what's familiar
================================================================

**COMPOSE BEFORE YOU BUILD.** Before authoring anything that touches
the network, check whether an existing framework tool already does
the fetch step:

  web_search       — search the web, returns ranked results
  fetch_url        — GET a URL, returns body
  find_image       — search for an image and save best match to workspace
  fetch_image      — download a specific image URL to workspace
  download_video   — download a video from a supported site to workspace

If one of these covers the fetch, your authoring job is the LOCAL
PROCESSING ON TOP — write that as a shell-mode tool and chain the two
via pipeline_steps. Don't reimplement the fetch. The framework's
versions handle credentials, retries, redirects, content-type sniffing,
size caps, caching, and observability — none of which a curl-in-shell
script gets.

Decision tree:

  Network involved?
    └─ YES — does an existing tool already fetch what you need?
        ├─ YES → pipeline mode: chain that tool + a shell-mode
        │        processor you author for the transformation.
        └─ NO  → api mode (HTTPS endpoint the framework can't
                  already reach).
    └─ NO  — purely local computation? → shell mode.

That's the rule. The two most common mistakes:
  (1) Reaching for shell mode + a Python urllib (or curl) script
      when the task is "call this HTTPS endpoint and pass the
      response back." Use api mode. Invented method names, JSON
      parse bugs, URL-encoding mistakes, even invisible homoglyphs
      in URLs are all eliminated by api mode.
  (2) Re-authoring a fetch when fetch_url or web_search already
      does it. The right shape is pipeline_steps that chains the
      existing fetch tool with your custom processor — you only
      author the part that doesn't already exist.

api mode — for HTTPS endpoints the framework doesn't already reach.
  Use when:
    - The task is to hit an authenticated HTTPS URL with a
      registered credential (Bearer, header, query, basic_auth) —
      pass credential="<name>".
    - The task is an unauthenticated public API (Open-Meteo,
      wttr.in, exchange rates, geocoders, etc.) — pass
      credential="no_auth". Same machinery (allow-list, audit log,
      rate limit) without an auth header.
  Do NOT write a Python urllib or curl-in-shell client around an
  HTTPS endpoint. There is no situation where a hand-rolled HTTP
  client in shell mode is the right answer.

shell mode — for local computation in a sandbox.
  Use when:
    - You need to parse, transform, or aggregate data with a
      script (Python, Bash, jq, awk, sed) — and the data is
      passed in as an arg, NOT fetched by the script itself.
    - You need persistent state across invocations (StatePath).
    - You need a multi-step computation that operates on
      caller-supplied input only.
  Sandbox: bubblewrap, network technically reachable but using it
  for HTTP work is the anti-pattern called out above. Constraints
  documented in the SANDBOX FACT SHEET at the top of this help —
  read that before authoring shell-mode tools.

pipeline mode — for composition (THIS is how "use existing tools").
  Two variants:
    pipeline_steps (DETERMINISTIC): each step is one tool call,
      args templated with {param} (caller args) and $N / $N.field
      (prior step output). No inner LLM. Cheap, fast, predictable.
      The right choice for "fetch X then process X" — pair an
      existing fetch tool with a shell-mode processor you author.
    pipeline_prompt (ADAPTIVE): a sub-agent LLM runs the chain
      with reasoning between steps. Use when the chain needs
      branching ("if the search returns a paper PDF, fetch and
      summarize; if it returns a webpage, scrape and summarize").

Worked example — fetch a JSON endpoint and project just the fields
you want, composing fetch_url + a shell processor:

  Step 1 — author the shell processor (works on caller-supplied data):
    tool_def(action="create", mode="shell",
             name="project_user_summary",
             description="Project name + repo count from a GitHub user JSON.",
             params={"json": {"type": "string", "description": "raw JSON body"}},
             command_template="echo {json} | jq -c '{login, public_repos, followers}'")

  Step 2 — author the pipeline that chains fetch_url + the processor:
    tool_def(action="create", mode="pipeline",
             name="gh_user_summary",
             description="Get a GitHub user's summary by username.",
             params={"user": {"type": "string", "description": "GitHub username"}},
             pipeline_tools=["fetch_url", "project_user_summary"],
             pipeline_steps=[
               {tool: "fetch_url", args: {url: "https://api.github.com/users/{user}"}, name: "page"},
               {tool: "project_user_summary", args: {json: "$page"}}
             ])

What you DIDN'T have to write: the HTTPS fetch, retry handling,
content-type sniffing, error formatting, size caps. fetch_url
already gives you all of that.

When NOT to use pipeline mode:
  - The whole flow is one HTTPS call: just use api mode directly.
  - The processing is so trivial it fits in api mode's
    response_pipe (which is jq / awk on the response body —
    cheaper than a pipeline_steps chain when nothing else is in
    the chain).

================================================================
WRAPPING A SCRIPT — fast path
================================================================

The single-call shortcut (this example uses jq, but Python, Bash,
awk, sed all work the same way — pick the smallest tool for the
job, not a Python script by default):

  tool_def(action=create, mode="shell",
           name="extract_titles",
           description="Extract titles from a JSON list of items.",
           params={"input": {"type": "string", "description": "JSON array on stdin."}},
           script_body="jq -r '.[] | .title'",
           script_name="run.jq",
           command_template="echo {input} | jq -r '.[] | .title'",
           persist=true)

What happens: the sandbox is auto-minted, script_body is written to
{workspace_dir}/script.py (or whatever script_name you set), and
the tool is registered. One call, no setup.

Why script_body beats inlining the script in command_template:
shell-quoting Python (or any non-trivial script) inside a template
is a footgun. Embedded quotes break, line breaks vanish, dollar
signs get expanded. Pass the source verbatim through script_body
and the file system handles it correctly. The template only sees
filenames and {arg} placeholders, which are safe.

================================================================
command_template — placeholders are pre-quoted; DON'T wrap them
================================================================

Every {param} placeholder in command_template is SHELL-QUOTED by
the framework at dispatch time. Wrapping a placeholder in quotes
yourself creates nested quoting and breaks the command.

WRONG (nested quotes — the framework's quote is INSIDE your quote):
  command_template:  curl '{url}' -H "X-Auth: {token}"
  → renders to:     curl ''https://...'' -H "X-Auth: 'abc123'"
  → shell sees doubled and nested quotes, command parses wrong

RIGHT (bare placeholders — let the framework do the quoting):
  command_template:  curl {url} -H X-Auth:\ {token}
  → renders to:     curl 'https://...' -H X-Auth:\ 'abc123'
  → values arrive as separate argv entries, correctly quoted

When script_body does the heavy lifting (typical case), pass the
values as bare placeholders and read them positionally in the script:
  command_template:  python3 {workspace_dir}/run.py {url} {token}
  Then in run.py:    url, token = sys.argv[1], sys.argv[2]

The rule: NEVER put a quote character around a {placeholder}, in
either single or double form. Literal quotes ELSEWHERE in the
template are fine — only the placeholders are auto-quoted.

================================================================
url_template / body_template — placeholders are URL-encoded; DON'T
wrap them either
================================================================

Same rule applies for api-mode and toolbox-mode url_template: the
framework URL-encodes each {placeholder} value at substitution and
splices it into the template. Literal quote characters in the
template (single OR double) survive into the final URL and the
upstream service sees them in the value.

WRONG (literal quotes survive into the URL):
  url_template:  https://api.example.com/search?q='{query}'
  with {query}="Seattle WA":
  → renders to: https://api.example.com/search?q='Seattle%20WA'
  → upstream sees q=%27Seattle%20WA%27 (encoded single quotes
    around the value — usually a 400 / "no results" / wrong match)

RIGHT (bare placeholder):
  url_template:  https://api.example.com/search?q={query}
  with {query}="Seattle WA":
  → renders to: https://api.example.com/search?q=Seattle%20WA

Path segments work the same way:
  url_template:  https://api.example.com/users/{username}/repos
  with {username}="cmcoffee":
  → renders to: https://api.example.com/users/cmcoffee/repos

A path placeholder KEEPS its slashes, so a value that spans
segments substitutes as real separators:
  url_template:  https://api.example.com/dav/{calendar_path}
  with {calendar_path}="/195178399/calendars/home/":
  → renders to: .../dav/195178399/calendars/home/

When the API wants a NESTED PATH as ONE segment, add ":encoded"
("segment" is a synonym) and the whole value is percent-encoded,
slashes included. GitLab's files endpoint is the case this exists
for — it takes the file path as an id:
  url_template:  /projects/{id}/repository/files/{path:encoded}/raw?ref={ref}
  with {path}="src/handlers/webhook_retry.py":
  → renders to: .../files/src%2Fhandlers%2Fwebhook_retry.py/raw?ref=dev

PASS NATURAL VALUES either way. Do NOT pre-encode: "%2F" arrives
as "%252F", because the escaper encodes "%" as it must — a literal
percent in a value is indistinguishable from an encoding you did
by hand. Before this modifier existed there was no third thing to
try: a raw "/" and a hand-written "%2F" both 404 on that endpoint.
A modifier that is not "encoded" / "segment" is REFUSED, at
authoring time and at dispatch, rather than left in the URL as a
literal.

Same rule for body_template — bare {placeholders}, no wrapping
quotes. The framework JSON-encodes string values for you (the
encoder adds its own surrounding quotes), so writing
  body_template:  {"key":"{value}"}
double-quotes the value. Write
  body_template:  {"key":{value}}
instead and let the encoder handle the JSON quoting.

================================================================
AUTHORING A SHELL-MODE TOOL — script_body inline is the path
================================================================

The canonical pattern is ONE call that ships the script content with
the tool record:

  tool_def(action=create, mode="shell",
           name="get_weather_by_city",
           description="Current weather for a US city via wttr.in.",
           script_name="weather.py",
           script_body="""
             import sys
             from gohort import fetch_url
             city = sys.argv[1]; state = sys.argv[2]
             url = f"https://wttr.in/{city},{state}?format=j1"
             print(fetch_url(url)["body"])
           """,
           command_template="python3 {workspace_dir}/weather.py {city} {state}",
           params={
             "city": {"type": "string", "description": "City name"},
             "state": {"type": "string", "description": "Two-letter state"}
           },
           test_args={"city": "Santa Cruz", "state": "CA"})

Why script_body inline:
  - The script content lives ON the tool record. Survives workspace
    wipes (e.g. a new chat session) because the framework redeploys
    it on every dispatch.
  - One call, not three. Workers don't waste rounds on a
    write-then-run-then-wrap dance.
  - test_args runs the freshly-authored tool with concrete inputs
    and folds the result (or error) into your response — if it
    errors, fix it inline and re-call tool_def; if it works,
    you're done.

CRITICAL: command_template must reference the same filename you
passed as script_name. If script_name="weather.py", command_template
must say {workspace_dir}/weather.py — NOT {workspace_dir}/script.py
or any other name. Mismatch → dispatch fails with "no such file."

Iterating-and-testing via local(write) + local(run) BEFORE the
tool_def call is OPTIONAL — useful when you're debugging a non-
trivial algorithm interactively. Once it works, copy the verified
content into script_body and call tool_def ONCE. Do NOT skip
script_body and hope the workspace file survives — it won't, across
sessions or after workspace pruning.

================================================================
NETWORK POLICY — shell sandbox is network-isolated by default
================================================================

Shell-mode tools run in a bwrap sandbox with --unshare-net. That
means: urllib.request, socket.connect, curl, wget — ALL FAIL from
inside the sandbox, and tool_def REFUSES a script_body that uses
any of them at authoring time.

HTTP goes through the gohort bridge instead. The bare hooks —
fetch_url, browse_page, log — are granted BY DEFAULT for any
shell-mode tool with script_body; no declaration needed:

  tool_def(action=create, mode="shell",
           name="get_weather_by_city",
           script_body="""
             from gohort import fetch_url
             import sys, json
             city, state = sys.argv[1], sys.argv[2]
             data = fetch_url(f"https://wttr.in/{city},{state}?format=j1")
             print(data["body"])
           """,
           command_template="python3 {workspace_dir}/weather.py {city} {state}",
           params={...},
           test_args={"city": "Santa Cruz", "state": "CA"})

Why this shape (vs raw network):
  - Every outbound call is logged in gohort's audit trail
  - Secrets stay in the credential store, out of the script's hands
  - Same posture across sessions — no surprises on a fresh workspace

For authenticated endpoints, declare the credential and route the
request THROUGH it (allow-list enforced, auth injected server-side,
the script never sees the secret):

  hook_capabilities=["fetch_via:openweather"]

Then in the script:

  from gohort import fetch_via
  data = fetch_via("openweather",
                   "https://api.openweathermap.org/data/2.5/weather?q=Seattle")
  print(data["body"])

  # fetch_via also takes method, body, and extra request headers —
  # e.g. a CalDAV PROPFIND that needs a Depth header and an XML body:
  #   fetch_via("apple_caldav", url, method="PROPFIND", body=xml,
  #             headers={"Depth": "1", "Content-Type": "application/xml"})
  # The credential's auth header always wins over anything you pass.
  # Returns {status, status_line, body}; status is the NUMERIC code, same
  # as fetch_url, so a plain  if r["status"] != 200:  works unchanged.
  # Do NOT write  int(r["status"].split()[1])  — that was a workaround
  # for an older shape and now raises on an int.

(secret:<name> exists for the rare API that can't be reached that
way — the script gets the decrypted value and injects it itself.
Prefer fetch_via.)

The escape hatch (raw_network=true) is RESERVED for narrow cases:
  - persistent-mode REPLs over non-HTTP (psql, redis-cli, ssh-like)
  - shell tools that NEED raw TCP/UDP and can't use the hook

For ordinary HTTP-shaped work, the default fetch_url hook is the
right answer. raw_network=true should be a deliberate exception
flagged in the description, not a default.

================================================================
state_path — for tools that need to remember
================================================================

The sandbox itself persists across dispatches of the same tool —
your script can write a file in dispatch #1 and read it back in
dispatch #2. That's the default behavior.

state_path is only needed when you want one specific subdir to be
treated as durable state separate from the rest of the sandbox.
Most tools don't need this; leave it unset.

  command_template="python3 {workspace_dir}/run.py --db {workspace_dir}/state/counts.db"
  state_path="state"

================================================================
api mode and response_pipe
================================================================

api-mode tool shape (authenticated — credential registered in admin):

  tool_def(action=create, mode="api",
           name="get_issue",
           description="Get a GitHub issue by number.",
           credential="github_api",
           url_template="https://api.github.com/repos/{owner}/{repo}/issues/{number}",
           method="GET",
           params={
             "owner": {"type": "string", "description": "..."},
             "repo": {"type": "string", "description": "..."},
             "number": {"type": "string", "description": "..."}
           },
           response_pipe="jq -c '{title, state, body, user: .user.login}'")

Public API (no auth) — same shape, credential="none":

  tool_def(action=create, mode="api",
           name="get_weather_forecast",
           description="Forecast for a lat/lon via Open-Meteo.",
           credential="none",
           url_template="https://api.open-meteo.com/v1/forecast?latitude={lat}&longitude={lon}&current=temperature_2m,weather_code&forecast_days={days}",
           method="GET",
           params={
             "lat": {"type": "string", "description": "..."},
             "lon": {"type": "string", "description": "..."},
             "days": {"type": "string", "description": "1-16"}
           },
           response_pipe="jq -c '{current: .current, daily: .daily}'")

response_pipe is optional but powerful. The API response BODY is
piped to your sh -c command on stdin. Whatever lands on stdout is
what reaches your context. Use it to project only the fields you
care about, drop noise, cap list lengths.

  Examples:
    response_pipe="jq -c '[.items[] | {id, name, status}]'"
    response_pipe="jq -c '.[:20]'"
    response_pipe="jq -r '.message'"

Notes:
  - The HTTP status line is stripped before piping and re-prepended
    to your output. You don't need "tail -n +2".
  - The pipe is skipped on non-2xx responses; you'll see the raw
    error in that case.
  - The pipe runs in the same sandbox as shell mode (no network,
    no writable fs, /tmp tmpfs).
  - Available binaries: jq, awk, sed, grep, head, tail, tr, cut.

URL placeholders are URL-encoded at dispatch. Body placeholders are
JSON-encoded. Both are safe against injection.

================================================================
WRITING THE DESCRIPTION — one or two sentences, then stop
================================================================

A tool's description and its param descriptions are re-sent on EVERY
turn the tool sits in a catalog, for the whole life of the tool. You
write them once; every future conversation pays for them. Treat the
length as a budget you are spending on someone else's behalf.

CAPS (enforced — create and update are refused over them):
  tool description          500 characters
  toolbox action            250 characters
  each param description    250 characters

The description answers exactly two questions: WHAT does this do, and
WHEN do I reach for it instead of something else. That is one or two
sentences.

  RIGHT: "Get a GitHub issue by number, including title, state, body
          and author."
  RIGHT: "Search Moltbook posts by keyword. Use get_post for the full
          body of a single hit."

Do NOT put these in the description:
  * worked examples or sample calls (the params already show the shape)
  * a restatement of the params (they are right there, with their own
    descriptions)
  * failure modes and troubleshooting ("if you get a 404, check the
    id") — that belongs in the ERROR the tool returns, where it is
    read only when it actually happens, instead of on every turn
  * setup or authoring history ("built against v2 of the API, uses the
    acme_api credential") — the caller cannot act on it
  * emphasis markup and repetition. Saying it once is saying it.

Param descriptions are one line: what the value is, plus the format
only when it is not obvious from the name and type.

  RIGHT: "Issue number, e.g. 1421."
  RIGHT: "Sort order: newest | oldest | top."
  WRONG: "The number of the issue you want to fetch. You can find this
          in the URL of the issue page, after /issues/. It must be a
          number, not the issue title..."

If a rule genuinely has to reach the caller before they call, it goes
in the ONE param it constrains, not in the tool description.

================================================================
WHAT THE TOOL RETURNS — anchor list items, never omit a field
================================================================

The output shape is part of the tool's contract, same as its params.
These two rules apply in every mode; a tool can pass action="test"
clean and still produce wrong answers in use by breaking them.

ANCHOR EVERY ITEM IN A LIST RESULT.

A tool that returns many similar-shaped items — search hits,
headlines, rows, files, messages — must give each item an id. Without
one the caller can read every field correctly and still attach it to
the wrong item: the attributes survive, the binding to their item
doesn't. It looks like a hallucination and isn't. It's two neighbors
in an undifferentiated wall of text.

WRONG (nothing to point at):
  ### Cracker Barrel CEO steps down after rebrand chaos
  *BBC Business* — Mon, 27 Jul 2026
  Julie Masino will exit after backlash over the logo redesign.

RIGHT (each item carries a handle):
  [id: bbc-cr49z0r54nko] Cracker Barrel CEO steps down
  source: BBC Business | date: 2026-07-27

Prefer a stable opaque id (the upstream's own id, a URL slug) over a
position number: ordinals shift between calls, so [3] names a
different item an hour later. If another tool consumes the selection,
have it take the id as a param rather than a retyped description of
the item. The retyping is where the drift happens.

In api mode this is a response_pipe job:

  response_pipe="jq -c '[.items[] | {id, title, source, published}]'"

STATE ABSENT FIELDS, DON'T DROP THEM.

An omitted key reads as a gap to fill from whatever else is in
context. An explicit null reads as a fact. The caller can't tell
"this tool doesn't report that" from "this item happens to lack it"
unless you say which.

  WRONG:  {"id": "a1", "title": "...", "source": "BBC"}
  RIGHT:  {"id": "a1", "title": "...", "source": "BBC",
           "summary": null, "author": null}

This matters most for the field the caller needs in order to act. If
your tool hands back a headline with no body and the caller's job is
to summarize the body, the missing body gets invented from the
nearest plausible text in context. Emitting "summary": null makes the
caller go fetch it or report that it can't.

Keep the field set identical across items and use a delimiter the
caller can't mistake for content. Ragged records let values slide
across item boundaries.

================================================================
WebDAV / CalDAV — the Depth header is not optional
================================================================

A calendar-query REPORT (or a PROPFIND) applies at the DEPTH the
request asks for. Depth 0 means "the collection resource itself" —
which is never a VEVENT — so the server answers 207 Multi-Status
with an EMPTY multistatus and no error anywhere:

  <?xml version="1.0" encoding="UTF-8"?><multistatus xmlns="DAV:"/>

That is indistinguishable from "the calendar has no events." It is
the single most common reason a CalDAV read tool looks correct,
verifies clean, and returns nothing forever. Set the header:

  tool_def(action=create, mode="api",
           name="list_calendar_events",
           credential="apple_caldav",
           url_template="/{principal}/calendars/{calendar_id}/",
           method="REPORT",
           headers={"Depth": "1"},
           content_type="application/xml",
           body_template="""<?xml version="1.0" encoding="utf-8" ?>
             <c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
               <d:prop><d:getetag/><c:calendar-data/></d:prop>
               <c:filter>
                 <c:comp-filter name="VCALENDAR">
                   <c:comp-filter name="VEVENT">
                     <c:time-range start="{start}" end="{end}"/>
                   </c:comp-filter>
                 </c:comp-filter>
               </c:filter>
             </c:calendar-query>""",
           response_extract={"select": "response",
                             "where": {"has": "calendar-data"},
                             "fields": {"href": "href", "ical": "calendar-data"}},
           params={...})

Rules that make the difference between working and silently empty:
  * headers={"Depth": "1"} on REPORT and PROPFIND. Always.
  * The filter MUST nest comp-filter VCALENDAR > VEVENT > time-range.
    A time-range at the VCALENDAR level matches nothing — same empty
    207, no error.
  * <c:calendar-data/> is a SELF-CLOSING prop. Putting <d:prop>
    children inside it (<d:summary/> etc.) is not a partial-retrieval
    spec and returns nothing useful — those are iCalendar properties,
    not DAV ones.
  * content_type="application/xml" so the body substitutes RAW.
  * Parse with response_extract, not a hand-written XML pipe.
  * A WRITE is a plain PUT of one .ics to <calendar>/<uid>.ics with
    content_type="text/calendar" — no Depth, no filter. A create tool
    working proves NOTHING about a read tool: they exercise different
    verbs, different headers, and different server-side logic.

If a read returns 2xx with zero records, check in this order:
Depth header, filter nesting, the time range, and only THEN the
calendar path — a path that accepts a PUT is a path that exists.

================================================================
toolbox mode — wrap a whole API surface
================================================================

When the work is "expose several endpoints of one API as tools"
(GitHub: users + repos + issues; Stripe: charges + invoices +
customers; an internal service with 5 read endpoints), use mode=
"toolbox" instead of authoring N separate api-mode tools. A toolbox
surfaces as ONE catalog entry with action="<sub>" dispatch — the
same UX as the framework's built-in grouped tools (tool_def itself
is one). Cleaner for the catalog, one credential shared across
actions, one approval.

Shape:

    tool_def(action="create", mode="toolbox",
             name="github",
             description="Query GitHub: users, repos, issues.",
             credential="github_api",     # shared across all actions
             actions=[
               {name: "get_user",
                description: "Get a user's public profile.",
                url_template: "https://api.github.com/users/{username}",
                method: "GET",
                params: {"username": {"type": "string",
                                      "description": "GitHub username"}},
                response_pipe: "jq -c '{login, name, bio, public_repos, followers}'"},
               {name: "get_repo",
                description: "Get a repository's metadata.",
                url_template: "https://api.github.com/repos/{owner}/{repo}",
                method: "GET",
                params: {"owner": {"type": "string"}, "repo": {"type": "string"}},
                response_pipe: "jq -c '{full_name, description, stars: .stargazers_count, language}'"},
               {name: "list_issues",
                description: "List issues on a repo by state.",
                url_template: "https://api.github.com/repos/{owner}/{repo}/issues?state={state}",
                method: "GET",
                params: {"owner": {"type": "string"}, "repo": {"type": "string"},
                         "state": {"type": "string",
                                   "description": "open | closed | all"}},
                response_pipe: "jq -c '[.[] | {number, title, state, user: .user.login}]'"}
             ])

Called as:

    github(action="get_user", username="octocat")
    github(action="get_repo", owner="cmcoffee", repo="gohort")
    github(action="list_issues", owner="cmcoffee", repo="gohort", state="open")

Each action is structurally a single api-mode endpoint — same URL
template substitution, same method/body_template/response_pipe
semantics. The toolbox is a packaging primitive on top.

Why toolbox over N api-mode tools:
  * One catalog entry (the toolbox name) vs N (gh_get_user,
    gh_get_repo, ...). Much cleaner when the catalog is already
    busy.
  * One credential declared at toolbox level vs repeated per tool.
  * One pending-approval entry vs N — admin reviews "the github
    toolbox" as one unit.
  * Adding a new endpoint = adding one entry to actions[], not
    minting a new tool_def call.

When NOT to use toolbox:
  * The work is one HTTPS call — mode="api" is leaner.
  * The endpoints share NOTHING (different APIs, different
    credentials) — author separate api-mode tools per endpoint.
  * The "actions" would have wildly different params with no
    semantic relation — that's usually a sign the work isn't really
    a wrapper around one API.

================================================================
verify — action="test" (DO THIS BEFORE YOU CALL A TOOL DONE)
================================================================

Authoring a tool and NOT exercising it is how a broken
tool reaches a user: a POST action with no body_template (so a required
field is never sent → live 400 "content must be a string"), a jq
response_pipe with a syntax error, a URL that 404s. action="test"
catches these BEFORE the tool ships.

    tool_def(action="test",
             name="moltbook",
             cases=[
               {action: "feed",     args: {limit: 5, sort: "new"}},
               {action: "get_post", args: {post_id: "<a real id>"}},
               {action: "comment",  args: {post_id: "<real id>", content: "test"}}
             ])

What it does per endpoint:
  * Checks every REQUIRED param is actually sent — referenced in the
    url_template or the body_template. An unreferenced required param
    is the #1 bug (the "must be a string" 400). Fails offline, no
    network needed.
  * Renders the body_template with your sample args and confirms it is
    valid JSON.
  * Compile-checks the response_pipe (a broken jq filter fails here,
    not live).
  * READ endpoints (GET): makes a REAL call, asserts a 2xx, and runs
    the response_pipe against the real body (catches shape mismatches).
  * WRITE endpoints (POST/PUT/PATCH/DELETE): body-validated but NOT
    auto-fired — the report tells you to make one manual call and
    confirm a 2xx yourself (so test never spams the live service).

Pass a cases entry per endpoint with REAL values so reads hit 2xx.
Returns a PASS/FAIL table. Fix every FAIL with action="update" and
re-run until green. Treat a tool as done only when test is clean and
each write endpoint has had one confirmed live call.

SHELL tools go through the same action, with checks that fit a script:

    tool_def(action="test",
             name="create_calendar_entry",
             cases=[{args: {summary: "test", start_time: "...",
                            end_time: "..."}}])

  * Syntax-checks script_body with the real interpreter (python3 -m
    py_compile, bash -n, node --check). An unterminated string or a
    bad indent fails HERE instead of on every future call.
  * Reports how each required param reaches the script: substituted
    into command_template, or ONLY as a lowercase env var (params are
    always exported as env vars — os.environ["summary"], $summary).
  * RUNS the tool with your case args and checks the exit status.
    This is a genuine dispatch — its side effects really happen, so
    pass args you're willing to have executed.

Without a cases entry a shell tool reports UNVERIFIED, not PASS:
running it is the only thing that proves a script works.

================================================================
persist
================================================================

**Your tool_def call is the creation. Stop second-guessing it.**
There is no separate "register with admin" step you need to ask
about. The moment tool_def(action="create", ...) returns success,
your tool is callable in this session AND auto-queued for admin
review in the background. The admin decides whether to keep it
past the session; you don't ask, you author. Saying "want me to
register it now?" after writing a script means you skipped the
tool_def call — go make it.

persist=false (default): the tool exists only for the current
session. Disappears at session end. No approval required.

persist=true: the tool is queued for operator approval. Once
approved it survives across sessions and shows up in your tool
catalog every time. Use this for tools you'll reuse; don't use it
for one-off transformations.

================================================================
cache (optional)
================================================================

cache opts a tool into persistent result memoization — the same
call returns the prior result instead of re-executing. Use for
tools whose output is expensive AND deterministic given the same
args:

  - api tools hitting paid or rate-limited endpoints
  - shell tools that download / convert / process external content
  - anything where re-running on a follow-up turn would waste
    bandwidth, money, or wall-time

Shape (all fields optional inside the cache object):

  key             {param}-template that produces the cache key.
                  Default = hash of all args. Set this when one
                  arg uniquely identifies the result (a URL, a
                  document ID) and other args don't affect output.
  ttl             Duration string: "30d", "12h", "30m", "45s".
                  Empty = no expiry.
  scope           "user" (default; dedup per-user across sessions),
                  "session" (per-conversation), or "global" (shared
                  across all users — only when the result is
                  content-addressable AND privacy-safe).
  invalidate_when Array of post-hit checks. Each entry has the form
                  "kind:expression". Today one kind:
                    file_exists:<path-template>
                  The rendered path must exist on disk or the entry
                  is dropped and the tool re-runs.

Example — api tool with TTL (current-weather lookup, ~10min fresh):

    create(mode="api",
           name="current_weather",
           description="Get current weather for lat/lon.",
           credential="none",
           url_template="https://api.open-meteo.com/v1/forecast?latitude={lat}&longitude={lon}&current_weather=true",
           method="GET",
           params={"lat": {"type": "number", "description": "latitude"},
                   "lon": {"type": "number", "description": "longitude"}},
           cache={"key": "{lat},{lon}", "ttl": "10m", "scope": "user"})

The same (lat, lon) within 10 minutes returns the prior response
without re-hitting Open-Meteo.

Example — shell tool with file_exists invalidation (download once):

    create(mode="shell",
           name="download_url_to_workspace",
           description="Download a URL into the workspace as out.bin.",
           command_template="curl -sSL -o {workspace_dir}/out.bin {url}",
           params={"url": {"type": "string", "description": "source URL"}},
           cache={"key": "{url}",
                  "scope": "user",
                  "invalidate_when": ["file_exists:{workspace_dir}/out.bin"]})

Same URL on a later turn: if the workspace file is still present,
the cached result string is returned instantly and the file is NOT
re-fetched. If the workspace was reaped between runs, file_exists
fails and the tool downloads again.

DO NOT set cache on tools whose output legitimately differs across
calls (status checks, "fetch latest news", anything time-sensitive
beyond your TTL). Cache is for input → output determinism, not for
"make it generally faster."

================================================================
common pitfalls
================================================================

- Wrapping an HTTPS endpoint with a Python+urllib (or curl-in-shell)
  script. This is the most expensive mistake in this system. Use
  api mode. For unauthenticated public APIs pass credential="none".
  Symptoms when you don't: invented method names (.UpperCase()),
  hand-written URL strings with invisible homoglyphs (Cyrillic 'о'
  for Latin 'o'), JSON parsing errors, retry loops blaming your
  own syntax. None of those exist in api mode.

- Trying to fetch a script over api mode and run it. Don't. Pass
  the script source via script_body.

- Embedding a multi-line script inside command_template. Shell
  quoting will fight you. Use script_body — the file system handles
  the source verbatim and the template only sees filenames.

- Wrapping a script you haven't tested. Use the local(write/run)
  iterate loop first; only wrap once it actually works.

- Using api mode for arithmetic or text munging. Use shell mode
  with a small Python or jq command — no credential needed.

- Defining response_pipe that produces empty output. The LLM-
  visible result is what comes off stdout; if your jq filter
  doesn't match, you get nothing. Test the filter against a real
  response first.

- Returning a list of items with no per-item id, or dropping keys
  the item doesn't have. Both invite the caller to bind a field to
  the wrong item or invent one outright. See "WHAT THE TOOL RETURNS"
  above.
`

// pruneRequired drops entries from a required list that no longer name a param.
// Used when an action update replaces params without re-sending required: the
// caller removed a param, so the required entry naming it is dead weight that
// would otherwise block every dispatch.
//
// Conservative about shapes it doesn't recognize — a nil or non-list required,
// or a non-map params, is returned untouched. This runs inside an edit path; a
// helper that discards data it merely failed to parse would be worse than the
// bug it fixes.
func pruneRequired(required, params any) any {
	// The merge base comes from actionToArgs, which emits required as a native
	// []string — normalize so a round-tripped list still prunes.
	if rs, ok := required.([]string); ok {
		conv := make([]any, len(rs))
		for i, s := range rs {
			conv[i] = s
		}
		required = conv
	}
	reqList, ok := required.([]any)
	if !ok || len(reqList) == 0 {
		return required
	}
	paramMap, ok := params.(map[string]any)
	if !ok {
		return required
	}
	kept := make([]any, 0, len(reqList))
	for _, r := range reqList {
		name, ok := r.(string)
		if !ok {
			kept = append(kept, r) // unrecognized entry: leave it alone
			continue
		}
		if _, exists := paramMap[strings.TrimSpace(name)]; exists {
			kept = append(kept, r)
		} else {
			Debug("[temptool] update: dropped required %q — the action no longer declares that param", name)
		}
	}
	return kept
}


// scriptCallsHook reports whether a script body invokes one of the gohort hook
// helpers. Matches the forms that actually appear — a call, a qualified call,
// or membership in an import list (where the name may be first, middle, or
// last, so substring matching on "import <name>" misses two of the three).
// Scoped to syntactic positions rather than any mention, because this check
// FAILS a verification and a comment shouldn't be able to do that.
func scriptCallsHook(body, name string) bool {
	if strings.Contains(body, name+"(") || strings.Contains(body, "gohort."+name) {
		return true
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "from ") && !strings.HasPrefix(line, "import ") {
			continue
		}
		i := strings.Index(line, "import ")
		if i < 0 {
			continue
		}
		for _, part := range strings.Split(line[i+len("import "):], ",") {
			// "x as y" imports under an alias; the import still grants the call.
			if f := strings.Fields(strings.TrimSpace(part)); len(f) > 0 && f[0] == name {
				return true
			}
		}
	}
	return false
}

// hookCapabilityDeclared matches the server's own gate: a bare capability, or
// any qualified form of it ("fetch_via:openweather", "secret:apikey").
func hookCapabilityDeclared(caps []string, want string) bool {
	prefix := want + ":"
	for _, c := range caps {
		c = strings.TrimSpace(c)
		if c == want || strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}
