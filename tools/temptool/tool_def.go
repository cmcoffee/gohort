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
		Description:  "List all session-scoped + persistent tools currently available to you. Returns name, mode (shell|api), and a one-line description for each.",
		Params:       map[string]ToolParam{},
		Required:     nil,
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
		Description:  "Define a new runtime tool for THIS session. CHOOSE MODE FIRST: if the work is calling an HTTPS endpoint use mode=\"api\" (with credential=\"no_auth\" for unauthenticated public APIs like Open-Meteo, wttr.in, etc., or credential=\"<registered name>\" for authenticated ones). If the work is local computation/parsing/scripting use mode=\"shell\". If the work needs multi-step LLM reasoning (search → fetch → summarize, look-up-then-act, chains where each step depends on the previous step's reasoning), use mode=\"pipeline\" with pipeline_prompt + pipeline_tools (adaptive sub-agent) or pipeline_steps (deterministic chain). Do NOT wrap an HTTP endpoint with a Python+urllib script — that path is plagued by invented method names, homoglyph URL bugs, and JSON parse errors that don't exist in api mode. Required: name, description, mode, params, plus mode-specific fields. mode=\"api\" needs credential, url_template, method, optional body_template, and optional response_pipe. mode=\"shell\" needs command_template; for non-trivial scripts pass script_body. mode=\"pipeline\" needs pipeline_tools plus either pipeline_prompt (adaptive) or pipeline_steps (deterministic). Tools you create here live for this conversation only — the user (admin) decides whether to keep them past the session via the Tools modal. Call action=\"help\" for the full spec including examples.",
		Params: map[string]ToolParam{
			"name":             {Type: "string", Description: "Tool name (snake_case, must not match an existing tool)."},
			"description":      {Type: "string", Description: "What the tool does. Shown to you in the catalog."},
			"mode":             {Type: "string", Description: "\"api\" for any HTTPS endpoint (authenticated or public — for public ones pass credential=\"no_auth\"). \"shell\" for local computation, parsing, or stateful scripts. \"pipeline\" for multi-step LLM-driven flows where each step depends on the previous step's reasoning. Pick by the work, not by familiarity — a Python urllib wrapper around an HTTPS endpoint is the wrong answer."},
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
			// Pipeline-mode params. Either pipeline_prompt OR pipeline_steps is required.
			"pipeline_prompt":    {Type: "string", Description: "(pipeline mode, ADAPTIVE variant) System prompt for an LLM sub-agent that runs the chain with reasoning between steps. Reference inner tools by name; be directive about sequencing. {param_name} placeholders get filled from caller args via plain string substitution — write them BARE (e.g. `for the topic {query}`), NEVER wrap them in quotes (`'{query}'` becomes `'AI 2026'` and the sub-agent will pass the literal quoted string to web_search). Mutually exclusive with pipeline_steps."},
			"pipeline_steps":     {Type: "array", Description: "(pipeline mode, DETERMINISTIC variant) Ordered list of step objects {tool, args, name?}, executed in sequence with no inner LLM. Args undergo template substitution: {param_name} → caller arg; $N → output of step N (1-indexed); $N.field.path → JSON field path. Mutually exclusive with pipeline_prompt."},
			"pipeline_tools":     {Type: "array", Description: "(pipeline mode) Names of tools the sub-agent (adaptive) or step executor (deterministic) may call. Must include every tool referenced in pipeline_steps."},
			"pipeline_max_rounds": {Type: "integer", Description: "(pipeline mode, adaptive only) Cap on sub-agent LLM rounds. Default 6. Ignored when pipeline_steps is set."},
		},
		Required:     []string{"name", "description", "mode", "params"},
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
		Description:  "Remove a tool by name. Removes from this session AND from your persistent pool if applicable.",
		Params: map[string]ToolParam{
			"name": {Type: "string", Description: "Name of the tool to remove."},
		},
		Required:     []string{"name"},
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
	default:
		return "", fmt.Errorf("mode must be \"shell\", \"api\", or \"pipeline\" (got %q)", mode)
	}
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

If the work is an HTTPS request → use api mode.
If the work is local computation, parsing, or stateful logic → use shell mode.

That's the rule. The most common mistake is reaching for shell mode
+ a Python urllib script when the task is just "call this HTTPS
endpoint and pass the response back." That path has more failure
modes than the work warrants — invented method names, JSON parsing,
URL-encoding bugs, even invisible homoglyphs in URLs. api mode
handles encoding, error responses, redaction, and audit logging
uniformly, with nothing for the model to get wrong.

api mode — for HTTPS endpoints, authenticated or not.
  Use when:
    - The task is to fetch / post against an HTTPS URL. Period.
    - Authenticated: the operator registered a credential (Bearer,
      header, query, basic_auth) — pass credential="<name>".
    - Unauthenticated public API (Open-Meteo, wttr.in, exchange
      rates, geocoders, etc.): pass credential="no_auth". Same
      machinery — allow-list pattern, audit log, rate limit — but
      no auth header injected and no secret required.
  Do NOT write a Python urllib client around an HTTPS endpoint.
  Use api mode. There is no situation where a hand-rolled HTTP
  client in shell mode is the right answer for a public or
  registered API.

shell mode — for local computation in a sandbox.
  Use when:
    - You need to parse, transform, or aggregate data with a
      script (Python, Bash, jq, awk, sed).
    - You need a multi-step computation that doesn't make sense
      as a single HTTP call.
    - You need persistent state across invocations (StatePath).
  Sandbox: bubblewrap, no network by default, read-only filesystem
  except for the tool's bound sandbox directory. Constraints
  documented in the SANDBOX FACT SHEET at the top of this help —
  read that before authoring shell-mode tools.

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
persist
================================================================

persist=false (default): the tool exists only for the current
session. Disappears at session end. No approval required.

persist=true: the tool is queued for operator approval. Once
approved it survives across sessions and shows up in your tool
catalog every time. Use this for tools you'll reuse; don't use it
for one-off transformations.

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
