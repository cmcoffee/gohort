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
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// BuildToolDef constructs the tool_def grouped tool. NOT globally
// registered — callers (Builder's catalog assembly) construct a
// fresh instance per session so the tool can't be reached except via
// explicit code import. Returns the ready-to-use *GroupedTool.
func BuildToolDef() *GroupedTool {
	gt := NewGroupedTool("tool_def",
		"Manage runtime-defined tools — wrappers around shell commands or registered API credentials. Use to list what's defined, create a new one, delete one you no longer need. Call action=\"help\" for the full usage spec including the workspace-first flow for wrapping scripts.")
	gt.SetHelpPreamble(helpText)
	// tool_def is single-fire per batch — authoring one tool at a
	// time keeps each create+verify cycle visible to the operator
	// before the LLM bundle-creates three tools that might overlap.
	gt.SetSingleFirePerBatch(true)

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
		Description: "Define a new runtime tool for THIS session. **THIS IS THE CREATION CALL — JUST CALL IT.** You do not need to ask the user for permission to call tool_def; it IS the act of creation. Admin approval for cross-session persistence is a SEPARATE downstream gate the framework queues automatically in the background — never tell the user 'an admin needs to register this' or 'want me to do that now?' as a confirmation question. After iterate-and-test (local(write) + local(run) to validate a script), the next step is ALWAYS tool_def(action=\"create\", ...) — without it you've written a script, not authored a tool. **COMPOSE BEFORE YOU BUILD**: if an existing tool already does part of the work (web_search for search, fetch_url for an HTTPS fetch, find_image / fetch_image / download_video for media), prefer chaining it via mode=\"pipeline\" (pipeline_steps) with a shell-mode tool authored alongside for the local processing — DON'T reimplement what the framework already gives you. CHOOSE MODE: (a) mode=\"api\" for a single HTTPS endpoint the framework can't already reach (with credential=\"no_auth\" for public APIs like Open-Meteo / wttr.in, or credential=\"<registered name>\" for authenticated ones); (b) mode=\"toolbox\" when wrapping MULTIPLE related endpoints under one tool name (a whole API surface: GitHub, Stripe, the moltbook social API, etc.) — surfaces as one catalog entry with action=\"<sub>\" dispatch, shares one credential across actions. Toolboxes live ONLY here — `add_tool` cannot build one, and to change a SINGLE action of an existing toolbox you use action=\"update\" (actions=[{name, ...just the changed fields}]) rather than recreating the whole thing; (c) mode=\"shell\" for pure local computation/parsing/scripting against data the caller passes in — NOT for fetching content from the network; (d) mode=\"pipeline\" with pipeline_steps for deterministic chains (e.g. fetch_url → your shell processor). For an adaptive multi-step LLM workflow, do NOT author a tool at all — use the standalone pipeline tool (action=\"create\") and attach it to the agent. Do NOT wrap an HTTPS endpoint with a Python+urllib or curl-in-shell script — that path is plagued by invented method names, homoglyph URL bugs, and JSON parse errors that don't exist in api / toolbox / pipeline mode. Required: name, description, mode, plus mode-specific fields. mode=\"api\" needs credential, url_template, method, params, optional body_template, and optional response_pipe. mode=\"toolbox\" needs credential and actions (an array of {name, description, url_template, params, ...}). mode=\"shell\" needs command_template + params; for non-trivial scripts pass script_body. mode=\"pipeline\" needs pipeline_tools plus pipeline_steps (deterministic chain). Tools you create here are immediately callable in this session. The framework auto-queues them for admin review in the background — admin approval governs cross-session persistence ONLY, not whether you can create or call the tool. Call action=\"help\" for the full spec including examples.",
		Params: map[string]ToolParam{
			"name":              {Type: "string", Description: "Tool name (snake_case, must not match an existing tool)."},
			"description":       {Type: "string", Description: "What the tool does. Shown to you in the catalog."},
			"mode":              {Type: "string", Description: "\"api\" for a single HTTPS endpoint. \"toolbox\" for multiple related endpoints bundled under one tool name (e.g. wrapping a whole API surface — GitHub, Stripe — with several actions sharing one credential). \"shell\" for local computation, parsing, or stateful scripts. \"pipeline\" for a deterministic chain of existing tools (pipeline_steps); adaptive multi-step LLM workflows are NOT tools — use the standalone pipeline tool. Pick by the work, not by familiarity — a Python urllib wrapper around an HTTPS endpoint is the wrong answer."},
			"params":            {Type: "object", Description: "Object describing the tool's parameters. Each key is a param name, value is {type, description}. OPTIONAL — omit entirely for a tool that takes no params (a GET with no query string, or a fixed shell command); don't invent a dummy placeholder. **Use the correct type:** \"integer\" for whole-number args (counts, indexes, ports), \"number\" for floats (rates, percentages), \"boolean\" for flags, \"string\" for text and identifiers. The dispatcher uses type to decide whether to shell-quote the value — a `count` typed as \"string\" gets passed to the script as `'1'` (with quotes) and any downstream int()/atoi() call fails. Default to \"string\" only when the value is genuinely free-form text."},
			"command_template":  {Type: "string", Description: "(shell mode) Shell command with {param} placeholders, shell-quoted at dispatch. {workspace_dir} resolves to the tool's sandbox path. **SHORTCUT**: when you pass script_body with a recognized extension (.py / .sh / .bash / .js / .jq / .rb / .pl) OR a shebang on line 1, you may OMIT command_template entirely — the framework auto-infers `python3 {workspace_dir}/script.py` (no positional args). **Declared params reach the script as ENVIRONMENT VARIABLES, NOT positional argv.** Read them in your script with `os.environ['name']` (Python), `$name` (bash), `process.env.name` (node). This means ordering is a non-question — params are looked up by NAME on both sides. You only need positional args if you're shelling out to a third-party tool that strictly expects argv; in that case supply your own command_template with explicit {placeholders}."},
			"script_body":       {Type: "string", Description: "(shell mode, optional) Full source of a script to ship with the tool (Python, Bash, awk, jq, etc.). Written into the sandbox at registration as `script_name` (default \"script.py\"). Read declared params with `os.environ['name']` (Python) / `$name` (bash) — they're injected as env vars, not positional argv. Auto-mints a sandbox; no setup required."},
			"script_name":       {Type: "string", Description: "(shell mode, optional) Filename for script_body. Defaults to \"script.py\". Match the script's language (e.g. \"run.sh\") — the extension drives interpreter selection when command_template is omitted."},
			"credential":        {Type: "string", Description: "(api / toolbox mode, optional) Name of the registered secure-API credential to dispatch through. For public no-auth APIs, OMIT it — it defaults to \"no_auth\", the bootstrapped open-pattern credential that applies gohort's allow-list/audit/rate-limit but injects no auth header. If you name the no-auth case explicitly, \"no_auth\" is the ONLY accepted spelling — never placeholders like \"none\" / \"public\" / \"n/a\". For authenticated APIs, name the credential the admin registered (\"github\", \"openweather\", etc.); the secret stays server-side and gohort injects the header (Bearer / custom / etc.) at dispatch."},
			"url_template":      {Type: "string", Description: "(api mode) URL template with {param} placeholders, URL-encoded at dispatch."},
			"method":            {Type: "string", Description: "(api mode) HTTP method. Default GET."},
			"body_template":     {Type: "string", Description: "(api mode) JSON body template with {param} placeholders (JSON-encoded at dispatch). Optional for GET; usually required for POST/PUT/PATCH."},
			"response_pipe":     {Type: "string", Description: "(api mode, optional) Shell command (sh -c) that receives the API response BODY on stdin and emits the LLM-visible result on stdout. The HTTP status line is stripped before piping and re-prepended to your output, so just write `jq` against the JSON body — no need for `tail -n +2`. Pipe is skipped on non-2xx responses (you'll see the raw error). Use to keep noisy responses out of your context — e.g. \"jq -c '[.items[] | {id, name, status}]'\" to project only the fields you care about, or \"jq -c '.[:20]'\" to cap a list. **jq gotcha:** the `//` alternative operator MUST be parenthesized inside object construction — write `{k: (.a // .b)}`, NOT `{k: .a // .b}` (the bare form is a jq syntax error). Runs in a tight sandbox (no network, no filesystem, /tmp tmpfs only) — jq, awk, sed, grep, head, tr available. Leave empty to see the raw response."},
			"required":          {Type: "array", Items: &ToolParam{Type: "string"}, Description: "List of param names callers MUST provide. OMIT this field entirely to default ALL params to required; pass an explicit empty array [] to make ALL params optional; or list a subset. An optional param that appears in url_template as a query segment (\"?key={name}\") is dropped from the URL when the caller omits it."},
			"state_path":        {Type: "string", Description: "Optional. Relative subdirectory inside the workspace whose contents persist between invocations. Use ONLY for tools that legitimately need runtime state (counters, accumulating logs, lookup DBs) — most tools don't and should leave this unset. Example: state_path=\"state\" with command_template=\"python3 {workspace_dir}/run.py --db {workspace_dir}/state/log.db\"."},
			"hook_capabilities": {Type: "array", Items: &ToolParam{Type: "string"}, Description: "(shell mode, OPTIONAL for HTTP; REQUIRED for credentialed access) Grant the script extra callbacks back into gohort. **The bare capabilities — \"fetch\", \"log\", \"browse_page\" — are GRANTED BY DEFAULT for any shell-mode tool with script_body.** You don't need to declare them. Just `from gohort import fetch_url, browse_page, log` and call them — works out of the box. Declare ONLY when you need credentialed access: \"secret:<credential_name>\" (return the decrypted value of a credential registered via the admin UI — script then injects it itself); \"fetch_via:<credential_name>\" (PREFERRED for credentialed or scoped endpoints — gohort routes the request through that credential's Secure.Dispatch: URL allowlist enforced, auth injected server-side, audit logged, script NEVER sees the secret). Example: hook_capabilities=[\"fetch_via:openweather\"]. Usage in script: `from gohort import fetch_via; data = fetch_via(\"openweather\", \"https://api.openweathermap.org/data/2.5/weather?q=Seattle\"); body = data[\"body\"]`. For unauth public endpoints, register a no_auth credential (with the URL pattern scoping reachable endpoints) and use fetch_via:no_auth — same audit + allowlist benefits, no auth header injected. **Prefer fetch_via:<name> over fetch_url + secret:<name>** — the credential machinery does the right thing automatically and the secret stays out of the script's hands."},
			"raw_network":       {Type: "boolean", Description: "(shell / persistent mode, advanced) When true, the sandbox keeps the host network namespace — raw TCP / UDP / any protocol works from inside. Default false (sandbox runs with --unshare-net). RESERVED for persistent-mode REPLs that connect to non-HTTP protocols (psql, redis-cli, ssh-like sessions) and the small set of legacy tools that haven't been migrated to hook_capabilities. For anything doing HTTP, use hook_capabilities=[\"fetch\"] instead — same outcome (your script can reach the web) but every call goes through gohort's audit log. Setting raw_network=true should be a deliberate exception, not a default."},
			// Pipeline-mode params. Either pipeline_prompt OR pipeline_steps is required.
			"pipeline_prompt": {Type: "string", Description: "(pipeline mode, ADAPTIVE variant) System prompt for an LLM sub-agent that runs the chain with reasoning between steps. Reference inner tools by name; be directive about sequencing. {param_name} placeholders get filled from caller args via plain string substitution — write them BARE (e.g. `for the topic {query}`), NEVER wrap them in quotes (`'{query}'` becomes `'AI 2026'` and the sub-agent will pass the literal quoted string to web_search). Mutually exclusive with pipeline_steps."},
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
			"actions": {Type: "array", Description: "(toolbox mode) Sub-action endpoints. Array of objects, each shape: {name, description, url_template, params, required?, method?, body_template?, response_pipe?}. Every action is one api-mode endpoint with its own params + URL. Names must be unique within the toolbox; the LLM calls the toolbox as <toolbox_name>(action=\"<sub>\", ...). The toolbox's top-level credential is shared across all actions — APIs almost always have one credential per service. NOT for shell-mode sub-actions (toolbox is api-only today); use mode=\"shell\" for those.",
				Items: &ToolParam{
					Type: "object",
					Properties: map[string]ToolParam{
						"name":          {Type: "string", Description: "Sub-action name, unique within the toolbox; called as <toolbox>(action=\"<name>\")."},
						"description":   {Type: "string", Description: "What this sub-action does."},
						"url_template":  {Type: "string", Description: "Endpoint URL with {param} placeholders."},
						"method":        {Type: "string", Description: "HTTP method. Default GET."},
						"params":        {Type: "object", Description: "Param definitions for this sub-action, shape {name: {type, description}}. OPTIONAL — omit entirely for a no-param sub-action (a plain GET like list_submolts or home); do NOT invent a dummy placeholder param, the sub-action is callable with just action=\"<name>\"."},
						"required":      {Type: "array", Items: &ToolParam{Type: "string"}, Description: "Names of this action's params callers MUST supply. OMIT to default all required; pass [] to make all optional; or list a subset. An optional query-param placeholder (\"?key={name}\") drops from the URL when omitted."},
						"body_template": {Type: "string", Description: "Optional request body template; {param} placeholders are JSON-encoded."},
						"response_pipe": {Type: "string", Description: "Optional shell post-processor (jq, awk, ...) for the raw response."},
					},
					Required: []string{"name", "url_template"},
				}},
		},
		Required: []string{"name", "description", "mode"},
		// Creating a tool is registry CRUD — it does not execute anything.
		// The created tool, when invoked, carries its own caps (CapExecute
		// for shell mode, CapNetwork for api mode) and is filtered at
		// dispatch time. So this action itself needs no caps to be visible.
		Caps:         nil,
		NeedsConfirm: true,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil {
				return "", fmt.Errorf("requires a session")
			}
			return createGrouped(args, sess)
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
		Description: "PARTIALLY edit an existing tool WITHOUT recreating it whole. Pass name plus only the fields you're changing. For a TOOLBOX: pass actions=[{name, ...}] to upsert (an action name that already exists is replaced, a new one is added) — the OTHER actions are preserved untouched; pass remove_actions=[\"x\"] to drop actions. For an api/shell tool: pass any of description / params / required / url_template / command_template / method / body_template / response_pipe / script_body to change just those. Use this instead of re-sending an entire toolbox to tweak one action.",
		Params: map[string]ToolParam{
			"name":             {Type: "string", Description: "The tool to update."},
			"description":      {Type: "string", Description: "(optional) New top-level description."},
			"credential":       {Type: "string", Description: "(optional) New credential name (api/toolbox)."},
			"actions":          {Type: "array", Description: "(toolbox) Action objects to UPSERT by name — same shape as create's actions. Existing actions not listed here are kept as-is."},
			"remove_actions":   {Type: "array", Items: &ToolParam{Type: "string"}, Description: "(toolbox) Names of actions to remove."},
			"params":           {Type: "object", Description: "(api/shell) Replacement params object."},
			"required":         {Type: "array", Items: &ToolParam{Type: "string"}, Description: "(api/shell) Replacement required list. [] = all optional; omit to leave unchanged."},
			"url_template":     {Type: "string", Description: "(api) New URL template."},
			"command_template": {Type: "string", Description: "(shell) New command template."},
			"method":           {Type: "string", Description: "(api) New HTTP method."},
			"body_template":    {Type: "string", Description: "(api) New request body template."},
			"response_pipe":    {Type: "string", Description: "(api) New response_pipe (jq/awk post-processor)."},
			"script_body":      {Type: "string", Description: "(shell) New script body."},
		},
		Required:     []string{"name"},
		Caps:         nil,
		NeedsConfirm: false,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil {
				return "", fmt.Errorf("requires a session")
			}
			return updateGrouped(args, sess)
		},
	})

	gt.AddAction("delete", &GroupedToolAction{
		Description: "Remove a tool by name. Removes it from this session AND from your persistent pool if applicable. For an [agent-bundled] tool (one attached to the running agent's record — these reload every turn, so deleting just the session copy leaves it firing), this also unbundles it from the agent record so it does NOT come back next turn.",
		Params: map[string]ToolParam{
			"name": {Type: "string", Description: "Name of the tool to remove."},
		},
		Required: []string{"name"},
		// Deletion is registry CRUD — no caps required.
		Caps:         nil,
		NeedsConfirm: false,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil {
				return "", fmt.Errorf("requires a session")
			}
			return deleteGrouped(args, sess)
		},
	})

	gt.AddAction("test", &GroupedToolAction{
		Description: "VERIFY an api/toolbox tool actually works BEFORE you call it done or hand it to a user. For every endpoint it: (1) renders the URL + body template with your sample args and checks the body is valid JSON — catches a body field that never lands (the #1 cause of a live 400 like \"content must be a string\"); (2) compile-checks the response_pipe — catches a broken jq/awk filter before it fails live; (3) for READ endpoints (GET) it makes a real call and asserts a 2xx, then runs the response_pipe against the REAL response body — catches shape mismatches a syntax check can't. WRITE endpoints (POST/PUT/PATCH/DELETE) are NOT auto-fired (that would spam the live service): their body is render-validated only, and the report tells you to make one manual call and confirm a 2xx yourself. Pass `cases` with representative inputs per action so read probes and body renders have real values to work with (e.g. a real post_id for get_post). Returns a per-endpoint PASS/FAIL table. Run this, fix every FAIL by action=\"update\", and re-run until green — an unexercised toolbox action is a live grenade.",
		Params: map[string]ToolParam{
			"name": {Type: "string", Description: "Name of the api or toolbox tool to verify."},
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
			return testGrouped(args, sess)
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
	if sess.DB != nil && sess.Username != "" {
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
	tools := sess.CopyTempTools()
	if len(tools) == 0 && len(pendingByName) == 0 && len(persistentByName) == 0 {
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

// queueForReview routes the named session draft to the right pool
// after a successful tool_def create:
//
//   - If the name is already in the user's ACTIVE pool, the LLM is
//     iterating on a tool that was approved at some point. Overwrite
//     the active entry in place (preserving the original ApprovedAt
//     for the audit trail) so the new version is immediately live
//     for every other session/agent. NO admin re-approval needed.
//
//   - Otherwise, the name is brand-new (or only in pending) — queue
//     the session draft into the pending-review pool. Admin decides
//     whether to promote it.
//
// Agent-bundled tools (created during a create_agent / add_tool call)
// get dequeued downstream by the auto-copy hook. The brief window of
// "pending until claimed" is acceptable since the admin UI isn't
// poll-refreshed.
//
// The in-place update on iteration is the fix for the "Builder
// updated the tool but the global one didn't change" bug — previously
// a re-author was a no-op for the persistent pool, so other agents
// kept seeing the stale version until the admin manually re-approved.
func queueForReview(sess *ToolSession, toolName string) {
	if sess == nil || sess.DB == nil || sess.Username == "" || sess.ChatSessionID == "" || toolName == "" {
		return
	}
	// LLM-iteration path: name already lives in the active pool. Find
	// the fresh session-draft content and overwrite the active entry
	// in place; skip the queue.
	for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
		if p.Tool.Name != toolName {
			continue
		}
		for _, draft := range LoadSessionTempTools(sess.DB, sess.ChatSessionID) {
			if draft.Name != toolName {
				continue
			}
			if UpdatePersistentTempTool(sess.DB, sess.Username, draft) {
				Log("[temptool.pending] in-place update of active tool %q (LLM iteration; admin re-approval skipped)", toolName)
				// Session draft is now redundant — the active pool
				// has the same content canonically. Drop the per-
				// session record so it doesn't show up as a
				// duplicate session draft in any UI that walks the
				// session_temp_tools table.
				RemoveSessionTempTool(sess.DB, sess.ChatSessionID, toolName)
			}
			break
		}
		return
	}
	for _, t := range LoadSessionTempTools(sess.DB, sess.ChatSessionID) {
		if t.Name != toolName {
			continue
		}
		if err := QueuePendingTempTool(sess.DB, sess.Username, t, sess.ChatSessionID); err != nil {
			Log("[temptool.pending] queue failed for %q (session=%s): %v", toolName, sess.ChatSessionID, err)
		} else {
			Log("[temptool.pending] queued %q for admin review (session=%s)", toolName, sess.ChatSessionID)
		}
		return
	}
}

// createGrouped dispatches between create_temp_tool (shell) and
// create_api_tool (api) based on the mode arg.
func createGrouped(args map[string]any, sess *ToolSession) (string, error) {
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
		res, err := t.RunWithSession(shellArgs, sess)
		if err == nil {
			queueForReview(sess, strings.TrimSpace(StringArg(args, "name")))
		}
		return res, err
	case TempToolModeAPI:
		apiArgs := map[string]any{
			"name":         args["name"],
			"description":  args["description"],
			"params":       args["params"],
			"credential":   args["credential"],
			"url_template": args["url_template"],
		}
		if v, ok := args["method"]; ok {
			apiArgs["method"] = v
		}
		if v, ok := args["body_template"]; ok {
			apiArgs["body_template"] = v
		}
		if v, ok := args["response_pipe"]; ok {
			apiArgs["response_pipe"] = v
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
			queueForReview(sess, strings.TrimSpace(StringArg(args, "name")))
		}
		return res, err
	case TempToolModePipeline:
		res, err := createPipelineGrouped(args, sess)
		if err == nil {
			queueForReview(sess, strings.TrimSpace(StringArg(args, "name")))
		}
		return res, err
	case TempToolModeToolbox:
		res, err := createToolboxGrouped(args, sess)
		if err == nil {
			queueForReview(sess, strings.TrimSpace(StringArg(args, "name")))
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
		// Distinguish "required omitted" (default all params required) from
		// an EXPLICIT empty array (make ALL params optional). Both yield
		// len==0 after stringSliceArg, so check presence: a non-nil value
		// under "required" means the author specified it — honor even [].
		// Without this, `required: []` silently became "all required", which
		// made optional params impossible (observed: a full 100-second thrash
		// trying to make feed's limit/sort optional).
		raw, present := m["required"]
		if !present || raw == nil {
			for k := range actParams {
				actRequired = append(actRequired, k)
			}
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
		if unsent := unsentWriteParams(method, urlTpl, bodyTpl, actRequired); len(unsent) > 0 {
			if bodyTpl != "" {
				return "", fmt.Errorf("actions[%d] (%q): required param(s) %v are sent NOWHERE — this %s action's body_template doesn't reference them, so the API never receives them (the cause of a 400 like \"content must be a string\"). Add them to the body_template, e.g. {\"content\": {content}}", i, actName, unsent, method)
			}
			bodyTpl = scaffoldBodyTemplate(unsent)
			scaffoldedActions = append(scaffoldedActions, actName)
		}
		actions = append(actions, TempToolAction{
			Name:         actName,
			Description:  actDesc,
			Params:       actParams,
			Required:     actRequired,
			URLTemplate:  urlTpl,
			Method:       method,
			BodyTemplate: bodyTpl,
			ResponsePipe: strings.TrimSpace(StringArg(m, "response_pipe")),
		})
	}
	tool := &TempTool{
		Name:        name,
		Description: desc,
		Mode:        TempToolModeToolbox,
		Credential:  credential,
		Actions:     actions,
	}
	sess.RemoveTempTool(tool.Name)
	if err := sess.AppendTempTool(tool); err != nil {
		return "", err
	}
	if sess.DB != nil && sess.ChatSessionID != "" {
		if err := SaveSessionTempTool(sess.DB, sess.ChatSessionID, *tool); err != nil {
			Debug("[temptool] session-scoped save failed for %s/%s: %v", sess.ChatSessionID, name, err)
		}
	}
	_ = BoolArg(args, "persist") // ignored — same as other modes
	msg := fmt.Sprintf("Created toolbox tool %q with %d action(s): %v. Call as %s(action=\"<sub-action>\", ...). Available in this session; admin promotes to permanent via the Tools modal.",
		name, len(actions), actionNames(actions), name)
	if len(scaffoldedActions) > 0 {
		msg += fmt.Sprintf(" NOTE: auto-added a body_template for write action(s) %v so their required params POST as a JSON body — no need to hand-write it. If the API expects different field names, refine via action=\"update\".", scaffoldedActions)
	}
	return msg, nil
}

// scaffoldBodyTemplate builds a flat JSON body template carrying each param as
// {"name": {name}} — the obvious shape for a write action whose params are just
// a JSON body. Auto-generated when a POST/PUT/PATCH action declares required
// params but no body_template, so the fields reach the API instead of the author
// looping. Empty in → "" (nothing to carry).
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
	if sess.DB != nil && sess.ChatSessionID != "" {
		if err := SaveSessionTempTool(sess.DB, sess.ChatSessionID, *tool); err != nil {
			Log("[temptool.pipeline.create] session-scoped save FAILED for session=%s tool=%q: %v", sess.ChatSessionID, name, err)
		} else {
			Log("[temptool.pipeline.create] saved session tool %q to session=%s (mode=pipeline)", name, sess.ChatSessionID)
		}
	} else {
		Log("[temptool.pipeline.create] pipeline tool %q NOT session-saved: db=%v chat_session_id=%q — modal Session-tools section will be empty", name, sess.DB != nil, sess.ChatSessionID)
	}
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
			return fmt.Sprintf("source: active (admin-approved)\n%s", string(body)), nil
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
	return "", fmt.Errorf("no tool found with name %q (checked active pool, pending queue, session drafts, and live session tools)", name)
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
	if a.ResponsePipe != "" {
		m["response_pipe"] = a.ResponsePipe
	}
	return m
}

// tempToolToCreateArgs serializes a stored tool back into the create-arg shape
// so update can patch it and re-run it through createGrouped (reusing all of
// create's validation + persistence + active-overwrite semantics).
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
		if tt.ResponsePipe != "" {
			out["response_pipe"] = tt.ResponsePipe
		}
		if tt.ScriptBody != "" {
			out["script_body"] = tt.ScriptBody
		}
		if tt.ScriptName != "" {
			out["script_name"] = tt.ScriptName
		}
	}
	return out
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
	existing, ok := loadExistingToolRecord(sess, name)
	if !ok {
		return "", fmt.Errorf("no tool named %q to update — use action=\"create\" to make a new one, or action=\"list\" to see what exists", name)
	}
	merged := tempToolToCreateArgs(existing)

	// Patch top-level scalar fields when provided (present = intent to change).
	for _, f := range []string{"description", "credential", "url_template", "command_template", "method", "body_template", "response_pipe", "script_body", "script_name"} {
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
					cur[idx] = am // replace in place
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

	res, err := createGrouped(merged, sess)
	if err != nil {
		return "", err
	}
	return "Updated " + name + " in place. " + res, nil
}

func deleteGrouped(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(StringArg(args, "name"))
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
	var endpoints []TempToolAction
	switch tt.Mode {
	case TempToolModeToolbox:
		endpoints = tt.Actions
	case TempToolModeAPI, "":
		if strings.TrimSpace(tt.CommandTemplate) == "" {
			return "", fmt.Errorf("tool %q has no url_template — nothing to probe", name)
		}
		endpoints = []TempToolAction{{
			Name: name, Params: tt.Params, Required: tt.Required,
			URLTemplate: tt.CommandTemplate, Method: tt.Method,
			BodyTemplate: tt.BodyTemplate, ResponsePipe: tt.ResponsePipe,
		}}
	default:
		return "", fmt.Errorf("tool %q is mode=%q — test verifies api/toolbox tools (the ones that call live endpoints). For shell/script tools, exercise the script with local(run) instead", name, tt.Mode)
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
	failCount, writeManual := 0, 0

	for _, ep := range endpoints {
		method := strings.ToUpper(strings.TrimSpace(ep.Method))
		if method == "" {
			method = "GET"
		}
		isRead := method == "GET" || method == "HEAD"
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
			if !strings.Contains(ep.URLTemplate, "{"+r+"}") && !strings.Contains(ep.BodyTemplate, "{"+r+"}") {
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

		// B. Body template renders to valid JSON with the sample args.
		if ep.BodyTemplate != "" {
			if coversRequired(sample, ep.Required) {
				body, err := substituteJSON(ep.BodyTemplate, ep.Params, ep.Required, sample)
				if err != nil {
					fail("body_template render failed: %v", err)
				} else if jerr := json.Unmarshal([]byte(body), new(any)); jerr != nil {
					fail("body_template produced INVALID JSON: %v — rendered body: %s", jerr, oneLine(body, 200))
				} else {
					pass("body_template renders valid JSON")
				}
			} else {
				note("body_template not render-checked — no sample args covering required %v (pass a case)", ep.Required)
			}
		}

		// C. response_pipe compiles (catches a broken jq/awk filter).
		if ep.ResponsePipe != "" {
			if serr := pipeCompileError(ep.ResponsePipe); serr != "" {
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
						if perr := runPipeAgainst(ep.ResponsePipe, body); perr != "" {
							fail("response_pipe failed on the REAL response body (shape mismatch — e.g. the filter expects .posts[] but the body is a bare array): %s", perr)
						} else {
							pass("response_pipe runs clean on the real response")
						}
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

	switch {
	case failCount > 0:
		fmt.Fprintf(&b, "RESULT: %d of %d endpoint(s) FAILED. Fix each with tool_def(action=\"update\", actions=[{name, ...}]) and re-run test until green. Do NOT call this tool done or hand it to a user while any endpoint is FAIL.", failCount, len(endpoints))
	case writeManual > 0:
		fmt.Fprintf(&b, "RESULT: all automated checks passed. %d write endpoint(s) still need ONE manual live call each — fire one, confirm a 2xx, then it's done.", writeManual)
	default:
		b.WriteString("RESULT: all endpoints passed. Tool verified.")
	}
	return b.String(), nil
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
		Method: ep.Method, BodyTemplate: ep.BodyTemplate, ResponsePipe: "",
	}
	inner := canonicalizeArgKeys(cloneArgs(sample), ep.Required, ep.Params)
	raw, derr := dispatchAPIModeTempTool(sess, &syn, inner)
	if derr != nil {
		return "", raw, derr
	}
	status, body = splitStatusLine(raw)
	return status, body, nil
}

// pipeCompileError runs a response_pipe against a trivial JSON doc and
// returns a non-empty message ONLY for a syntax/compile error — those
// fire regardless of input shape and are true authoring bugs. A runtime
// error against the dummy input (null iteration, missing field) is not a
// compile bug and yields "" (the real shape is checked live for reads).
func pipeCompileError(pipe string) string {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
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
func runPipeAgainst(pipe, body string) string {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	res := RunSandboxedShellPipe(ctx, pipe, body)
	if res.TimedOut {
		return "timed out"
	}
	if res.Err != nil {
		return oneLine(res.Output, 200)
	}
	return ""
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
		if !strings.Contains(urlTpl, "{"+r+"}") && !strings.Contains(bodyTpl, "{"+r+"}") {
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
verify — action="test" (DO THIS BEFORE YOU CALL AN API TOOL DONE)
================================================================

Authoring an api/toolbox tool and NOT exercising it is how a broken
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
`
