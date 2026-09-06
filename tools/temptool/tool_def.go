// tool_def — grouped management tool consolidating list_temp_tools,
// create_temp_tool, create_api_tool, and delete_temp_tool into one
// catalog entry with action="<list|create|delete|help>".
//
// Brief catalog description points the LLM at action="help" for the
// full usage spec. Reduces prompt budget consumed by 4 separate tool
// descriptions every round.

package temptool

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
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
