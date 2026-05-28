// tool_def — grouped management tool consolidating list_temp_tools,
// create_temp_tool, create_api_tool, and delete_temp_tool into one
// catalog entry with action="<list|create|delete|help>".
//
// Brief catalog description points the LLM at action="help" for the
// full usage spec. Reduces prompt budget consumed by 4 separate tool
// descriptions every round.

package temptool

import (
	"encoding/json"
	"fmt"
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
		Description: "Define a new runtime tool for THIS session. **THIS IS THE CREATION CALL — JUST CALL IT.** You do not need to ask the user for permission to call tool_def; it IS the act of creation. Admin approval for cross-session persistence is a SEPARATE downstream gate the framework queues automatically in the background — never tell the user 'an admin needs to register this' or 'want me to do that now?' as a confirmation question. After iterate-and-test (local(write) + local(run) to validate a script), the next step is ALWAYS tool_def(action=\"create\", ...) — without it you've written a script, not authored a tool. **COMPOSE BEFORE YOU BUILD**: if an existing tool already does part of the work (web_search for search, fetch_url for an HTTPS fetch, find_image / fetch_image / download_video for media), prefer chaining it via mode=\"pipeline\" (pipeline_steps) with a shell-mode tool authored alongside for the local processing — DON'T reimplement what the framework already gives you. CHOOSE MODE: (a) mode=\"api\" for a single HTTPS endpoint the framework can't already reach (with credential=\"no_auth\" for public APIs like Open-Meteo / wttr.in, or credential=\"<registered name>\" for authenticated ones); (b) mode=\"toolbox\" when wrapping MULTIPLE related endpoints under one tool name (a whole API surface: GitHub, Stripe, etc.) — surfaces as one catalog entry with action=\"<sub>\" dispatch, shares one credential across actions; (c) mode=\"shell\" for pure local computation/parsing/scripting against data the caller passes in — NOT for fetching content from the network; (d) mode=\"pipeline\" with pipeline_steps for deterministic chains (e.g. fetch_url → your shell processor), or with pipeline_prompt + pipeline_tools for adaptive multi-step LLM reasoning. Do NOT wrap an HTTPS endpoint with a Python+urllib or curl-in-shell script — that path is plagued by invented method names, homoglyph URL bugs, and JSON parse errors that don't exist in api / toolbox / pipeline mode. Required: name, description, mode, plus mode-specific fields. mode=\"api\" needs credential, url_template, method, params, optional body_template, and optional response_pipe. mode=\"toolbox\" needs credential and actions (an array of {name, description, url_template, params, ...}). mode=\"shell\" needs command_template + params; for non-trivial scripts pass script_body. mode=\"pipeline\" needs pipeline_tools plus either pipeline_prompt (adaptive) or pipeline_steps (deterministic). Tools you create here are immediately callable in this session. The framework auto-queues them for admin review in the background — admin approval governs cross-session persistence ONLY, not whether you can create or call the tool. Call action=\"help\" for the full spec including examples.",
		Params: map[string]ToolParam{
			"name":             {Type: "string", Description: "Tool name (snake_case, must not match an existing tool)."},
			"description":      {Type: "string", Description: "What the tool does. Shown to you in the catalog."},
			"mode":             {Type: "string", Description: "\"api\" for a single HTTPS endpoint. \"toolbox\" for multiple related endpoints bundled under one tool name (e.g. wrapping a whole API surface — GitHub, Stripe — with several actions sharing one credential). \"shell\" for local computation, parsing, or stateful scripts. \"pipeline\" for multi-step LLM-driven flows. Pick by the work, not by familiarity — a Python urllib wrapper around an HTTPS endpoint is the wrong answer."},
			"params":           {Type: "object", Description: "Object describing the tool's parameters. Each key is a param name, value is {type, description}. **Use the correct type:** \"integer\" for whole-number args (counts, indexes, ports), \"number\" for floats (rates, percentages), \"boolean\" for flags, \"string\" for text and identifiers. The dispatcher uses type to decide whether to shell-quote the value — a `count` typed as \"string\" gets passed to the script as `'1'` (with quotes) and any downstream int()/atoi() call fails. Default to \"string\" only when the value is genuinely free-form text."},
			"command_template": {Type: "string", Description: "(shell mode) Shell command with {param} placeholders, shell-quoted at dispatch. {workspace_dir} resolves to the tool's sandbox path. For multi-line scripts, prefer script_body — it sidesteps shell-quoting entirely."},
			"script_body":      {Type: "string", Description: "(shell mode, optional) Full source of a script to ship with the tool (Python, Bash, awk, jq, etc.). Written into the sandbox at registration as `script_name` (default \"script.py\"). Reference from command_template as {workspace_dir}/<script_name>. Auto-mints a sandbox; no setup required."},
			"script_name":      {Type: "string", Description: "(shell mode, optional) Filename for script_body. Defaults to \"script.py\". Match the script's language (e.g. \"run.sh\")."},
			"credential":       {Type: "string", Description: "(api mode) Name of the registered secure-API credential to dispatch through. Use \"none\" for unauthenticated public APIs — same machinery (allow-list, audit, rate limit) but no auth header injected."},
			"url_template":     {Type: "string", Description: "(api mode) URL template with {param} placeholders, URL-encoded at dispatch."},
			"method":           {Type: "string", Description: "(api mode) HTTP method. Default GET."},
			"body_template":    {Type: "string", Description: "(api mode) JSON body template with {param} placeholders (JSON-encoded at dispatch). Optional for GET; usually required for POST/PUT/PATCH."},
			"response_pipe":    {Type: "string", Description: "(api mode, optional) Shell command (sh -c) that receives the API response BODY on stdin and emits the LLM-visible result on stdout. The HTTP status line is stripped before piping and re-prepended to your output, so just write `jq` against the JSON body — no need for `tail -n +2`. Pipe is skipped on non-2xx responses (you'll see the raw error). Use to keep noisy responses out of your context — e.g. \"jq -c '[.items[] | {id, name, status}]'\" to project only the fields you care about, or \"jq -c '.[:20]'\" to cap a list. Runs in a tight sandbox (no network, no filesystem, /tmp tmpfs only) — jq, awk, sed, grep, head, tr available. Leave empty to see the raw response."},
			"required":         {Type: "array", Description: "Optional list of param names that must be provided by callers. Defaults to all params."},
			"state_path":       {Type: "string", Description: "Optional. Relative subdirectory inside the workspace whose contents persist between invocations. Use ONLY for tools that legitimately need runtime state (counters, accumulating logs, lookup DBs) — most tools don't and should leave this unset. Example: state_path=\"state\" with command_template=\"python3 {workspace_dir}/run.py --db {workspace_dir}/state/log.db\"."},
			"hook_capabilities": {Type: "array", Description: "(shell mode, optional) Grant the script a narrow callback channel back into gohort while the sandbox stays network-isolated. Each entry names a method (and for secrets, a specific credential) the script may invoke; the framework auto-deploys a `gohort_hook` Python module into the workspace, exposes a per-dispatch UNIX socket via $GOHORT_HOOK_PATH, and gohort proxies the operation. Use this INSTEAD of giving a shell tool raw network — same outcome (your script can do HTTP and read tokens) but every call goes through gohort's network stack with logging. Forms: \"fetch\" (HTTP request → returns {status, headers, body}); \"log\" (route a message into gohort's log stream); \"secret:<credential_name>\" (return the decrypted value of a credential registered via the admin UI — grants are per-credential, the tool record lists exactly what it can read). Example: hook_capabilities=[\"fetch\", \"secret:openweather\"]. Usage in script: `from gohort_hook import gohort; key = gohort.secret(\"openweather\"); data = gohort.fetch(f\"https://api.openweathermap.org/...&appid={key}\")`. **Prefer secret:<name> over hard-coding API keys in script_body** — keeps tokens out of the tool record (which lives in the DB and shows up in admin review). Empty / unset = no hook attached, zero surface area."},
			// Pipeline-mode params. Either pipeline_prompt OR pipeline_steps is required.
			"pipeline_prompt":     {Type: "string", Description: "(pipeline mode, ADAPTIVE variant) System prompt for an LLM sub-agent that runs the chain with reasoning between steps. Reference inner tools by name; be directive about sequencing. {param_name} placeholders get filled from caller args via plain string substitution — write them BARE (e.g. `for the topic {query}`), NEVER wrap them in quotes (`'{query}'` becomes `'AI 2026'` and the sub-agent will pass the literal quoted string to web_search). Mutually exclusive with pipeline_steps."},
			"pipeline_steps":      {Type: "array", Description: "(pipeline mode, DETERMINISTIC variant) Ordered list of step objects {tool, args, name?}, executed in sequence with no inner LLM. Args undergo template substitution: {param_name} → caller arg; $N → output of step N (1-indexed); $N.field.path → JSON field path. Mutually exclusive with pipeline_prompt."},
			"pipeline_tools":      {Type: "array", Description: "(pipeline mode) Names of tools the sub-agent (adaptive) or step executor (deterministic) may call. Must include every tool referenced in pipeline_steps."},
			"pipeline_max_rounds": {Type: "integer", Description: "(pipeline mode, adaptive only) Cap on sub-agent LLM rounds. Default 6. Ignored when pipeline_steps is set."},
			"actions":             {Type: "array", Description: "(toolbox mode) Sub-action endpoints. Array of objects, each shape: {name, description, url_template, params, required?, method?, body_template?, response_pipe?}. Every action is one api-mode endpoint with its own params + URL. Names must be unique within the toolbox; the LLM calls the toolbox as <toolbox_name>(action=\"<sub>\", ...). The toolbox's top-level credential is shared across all actions — APIs almost always have one credential per service. NOT for shell-mode sub-actions (toolbox is api-only today); use mode=\"shell\" for those."},
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

	gt.AddAction("delete", &GroupedToolAction{
		Description: "Remove a tool by name. Removes from this session AND from your persistent pool if applicable.",
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

	return gt
}

// listGrouped reuses the existing ListTempToolsTool logic via a
// shim. We can't call its RunWithSession directly without an instance,
// so reproduce the formatting inline.
func listGrouped(args map[string]any, sess *ToolSession) (string, error) {
	persistentByName := map[string]bool{}
	pendingByName := map[string]bool{}
	pendingDescByName := map[string]string{}
	pendingModeByName := map[string]string{}
	if sess.DB != nil && sess.Username != "" {
		for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
			persistentByName[p.Tool.Name] = true
		}
		for _, p := range LoadPendingTempTools(sess.DB, sess.Username) {
			pendingByName[p.Tool.Name] = true
			pendingDescByName[p.Tool.Name] = p.Tool.Description
			pendingModeByName[p.Tool.Name] = p.Tool.Mode
		}
	}
	tools := sess.CopyTempTools()
	if len(tools) == 0 && len(pendingByName) == 0 {
		return "No temp tools defined in this session.", nil
	}
	var b strings.Builder
	inSession := make(map[string]bool, len(tools))
	for i, t := range tools {
		inSession[t.Name] = true
		var tag string
		switch {
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
	return b.String(), nil
}

func modeLabel(mode string) string {
	if mode == TempToolModeAPI {
		return "api"
	}
	return "shell"
}

// queueForReview queues the named session tool into the user's
// pending-review pool so an admin can promote it later. Called after
// any successful tool_def create — agent-bundled tools that should
// NOT be in the queue get dequeued downstream when create_agent /
// add_tool claim them (via the auto-copy hook). The brief window of
// "pending until claimed" is acceptable since the admin UI isn't
// poll-refreshed.
func queueForReview(sess *ToolSession, toolName string) {
	if sess == nil || sess.DB == nil || sess.Username == "" || sess.ChatSessionID == "" || toolName == "" {
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
		return "", fmt.Errorf("credential is required for toolbox mode — every action shares one credential. Use credential=\"no_auth\" for public unauthenticated APIs")
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
		if len(actRequired) == 0 {
			// Default to every declared param being required, same as
			// shell/api mode tools.
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
		actions = append(actions, TempToolAction{
			Name:         actName,
			Description:  actDesc,
			Params:       actParams,
			Required:     actRequired,
			URLTemplate:  urlTpl,
			Method:       method,
			BodyTemplate: strings.TrimSpace(StringArg(m, "body_template")),
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
	return fmt.Sprintf("Created toolbox tool %q with %d action(s): %v. Call as %s(action=\"<sub-action>\", ...). Available in this session; admin promotes to permanent via the Tools modal.",
		name, len(actions), actionNames(actions), name), nil
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

func deleteGrouped(args map[string]any, sess *ToolSession) (string, error) {
	t := &DeleteTempToolTool{}
	res, err := t.RunWithSession(args, sess)
	if err == nil && sess != nil && sess.DB != nil && sess.Username != "" {
		// Dequeue from pending-review pool too. If the LLM cancels a
		// tool it just authored, the admin shouldn't still see it in
		// their review queue — that'd be stale work that won't fire.
		DequeuePendingTempTool(sess.DB, sess.Username, strings.TrimSpace(StringArg(args, "name")))
	}
	return res, err
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
  * Safe imports: json, re, csv, sqlite3, urllib.request,
    urllib.parse, hashlib, hmac, datetime, collections, itertools,
    functools, os, sys, subprocess, pathlib, base64, html,
    xml.etree.ElementTree, statistics, math, random.
  * Need a third-party package? PIVOT — usually a shell tool
    (curl, jq) or api mode reaches the same outcome.

SHELL
  * Interpreter is sh (POSIX), not bash. No arrays, no [[ ]],
    no <(...). Use plain sh-compatible syntax.
  * Reliably available binaries: curl, jq, awk, sed, grep, head,
    tail, sort, uniq, tr, cut, wc, basename, dirname, date, cat,
    echo, printf, tee, xargs, find.
  * NOT available: wget (use curl), bash-only features.

NETWORK
  * Network IS available. curl, wget, urllib.request etc. work
    inside shell-mode tools.
  * But: api mode is usually the better fit for HTTPS work even
    so. It handles credentials, allow-listed URLs, audit logs,
    and rate limits — none of which a curl-in-shell script gets.
    Pick api mode for any work that hits an HTTPS endpoint
    you'd otherwise wrap with curl.

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
WRAPPING A SCRIPT — iterate-and-test loop
================================================================

For non-trivial scripts, prove it works before wrapping it:

  1. local(action=write, path="script.py", content="...")
       Drops the script into the auto-minted sandbox. No setup.

  2. local(action=run, command="python3 script.py --foo bar")
       Runs in the same sandbox. Read the output, fix bugs.

  3. local(action=write, path="script.py", content="...")  # edit
     local(action=run, command="...")                       # re-run

  4. Once it works, wrap it. Two options:
     (a) tool_def(action=create, mode="shell", script_body=...,
                  command_template="python3 {workspace_dir}/script.py {arg}")
         Re-ships the script into the tool's own sandbox.
     (b) tool_def(action=create, mode="shell",
                  command_template="python3 {workspace_dir}/script.py {arg}")
         Reuses the script you wrote in step 1 — same sandbox.

Wrapping a script you haven't tested is how you end up with a tool
that fails on the first real call. Iterate first.

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
