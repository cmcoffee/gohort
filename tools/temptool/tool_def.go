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

func init() {
	gt := NewGroupedTool("tool_def",
		"Manage runtime-defined tools — wrappers around shell commands or registered API credentials. Use to list what's defined, create a new one, delete one you no longer need. Call action=\"help\" for the full usage spec including the workspace-first flow for wrapping scripts.")
	gt.SetHelpPreamble(helpText)

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
		Description:  "Define a new runtime tool. CHOOSE MODE FIRST: if the work is calling an HTTPS endpoint use mode=\"api\" (with credential=\"none\" for unauthenticated public APIs like Open-Meteo, wttr.in, etc., or credential=\"<registered name>\" for authenticated ones). If the work is local computation/parsing/scripting use mode=\"shell\". Do NOT wrap an HTTP endpoint with a Python+urllib script — that path is plagued by invented method names, homoglyph URL bugs, and JSON parse errors that don't exist in api mode. Required: name, description, mode, params, plus mode-specific fields. mode=\"api\" needs credential, url_template, method, optional body_template, and optional response_pipe (sandboxed jq/awk/sed/grep over the response body). mode=\"shell\" needs command_template; for non-trivial scripts pass script_body so the source lands on disk and the template only sees a filename. persist=true queues the tool for human approval and reuse across future sessions. Call action=\"help\" for the full spec including examples.",
		Params: map[string]ToolParam{
			"name":             {Type: "string", Description: "Tool name (snake_case, must not match an existing tool)."},
			"description":      {Type: "string", Description: "What the tool does. Shown to you in the catalog."},
			"mode":             {Type: "string", Description: "\"api\" for any HTTPS endpoint (authenticated or public — for public ones pass credential=\"none\"). \"shell\" for local computation, parsing, or stateful scripts. Pick by the work, not by familiarity — a Python urllib wrapper around an HTTPS endpoint is the wrong answer."},
			"params":           {Type: "object", Description: "Object describing the tool's parameters. Each key is a param name, value is {type, description}."},
			"command_template": {Type: "string", Description: "(shell mode) Shell command with {param} placeholders, shell-quoted at dispatch. {workspace_dir} resolves to the tool's sandbox path. For multi-line scripts, prefer script_body — it sidesteps shell-quoting entirely."},
			"script_body":      {Type: "string", Description: "(shell mode, optional) Full source of a script to ship with the tool (Python, Bash, awk, jq, etc.). Written into the sandbox at registration as `script_name` (default \"script.py\"). Reference from command_template as {workspace_dir}/<script_name>. Auto-mints a sandbox; no setup required."},
			"script_name":      {Type: "string", Description: "(shell mode, optional) Filename for script_body. Defaults to \"script.py\". Match the script's language (e.g. \"run.sh\")."},
			"credential":       {Type: "string", Description: "(api mode) Name of the registered secure-API credential to dispatch through. Use \"none\" for unauthenticated public APIs — same machinery (allow-list, audit, rate limit) but no auth header injected."},
			"url_template":     {Type: "string", Description: "(api mode) URL template with {param} placeholders, URL-encoded at dispatch."},
			"method":           {Type: "string", Description: "(api mode) HTTP method. Default GET."},
			"body_template":    {Type: "string", Description: "(api mode) JSON body template with {param} placeholders (JSON-encoded at dispatch). Optional for GET; usually required for POST/PUT/PATCH."},
			"response_pipe":    {Type: "string", Description: "(api mode, optional) Shell command (sh -c) that receives the API response BODY on stdin and emits the LLM-visible result on stdout. The HTTP status line is stripped before piping and re-prepended to your output, so just write `jq` against the JSON body — no need for `tail -n +2`. Pipe is skipped on non-2xx responses (you'll see the raw error). Use to keep noisy responses out of your context — e.g. \"jq -c '[.items[] | {id, name, status}]'\" to project only the fields you care about, or \"jq -c '.[:20]'\" to cap a list. Runs in a tight sandbox (no network, no filesystem, /tmp tmpfs only) — jq, awk, sed, grep, head, tr available. Leave empty to see the raw response."},
			"required":         {Type: "array", Description: "Optional list of param names that must be provided by callers. Defaults to all params."},
			"persist":          {Type: "boolean", Description: "If true, request that this tool be saved across future sessions (queues for admin approval). Default false (session-only)."},
			"state_path":       {Type: "string", Description: "Optional. Relative subdirectory inside the workspace whose contents persist between invocations. Use ONLY for tools that legitimately need runtime state (counters, accumulating logs, lookup DBs) — most tools don't and should leave this unset. Example: state_path=\"state\" with command_template=\"python3 {workspace_dir}/run.py --db {workspace_dir}/state/log.db\"."},
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

	RegisterChatTool(gt)
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
		return t.RunWithSession(shellArgs, sess)
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
		return t.RunWithSession(apiArgs, sess)
	default:
		return "", fmt.Errorf("mode must be \"shell\" or \"api\" (got %q)", mode)
	}
}

func deleteGrouped(args map[string]any, sess *ToolSession) (string, error) {
	t := &DeleteTempToolTool{}
	return t.RunWithSession(args, sess)
}

// suppress unused import — json is used by future expansions; keep
// the import handy.
var _ = json.Marshal

// helpText is the full usage guide returned by action="help". Kept
// inline (not loaded from disk) so it ships with the binary and
// can't drift from the action descriptions.
const helpText = `tool_def — runtime tool builder

Use this to define a wrapper around a shell command or an HTTP API
call. Two modes: "shell" and "api". Pick by what you need to do, not
by what's easier to write.

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
      rates, geocoders, etc.): pass credential="none". Same
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
  except for the tool's bound sandbox directory. The sandbox is
  managed for you — auto-minted on first use, persisted across
  invocations of the same tool. You never need to set it up.

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
