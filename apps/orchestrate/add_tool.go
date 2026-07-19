// add_tool — unified tool-authoring surface for Builder (and any
// other agent that opts in). Replaces the trio of
// create_pipeline_tool / create_temp_tool / create_api_tool with one
// tool whose `mode` argument picks the underlying type. Always
// attaches to the session's AuthoringAgentID — there is no for_agent
// parameter and no user-wide scope. Users who want a cross-cutting
// user-wide tool use the original create_*_tool surface directly
// (still registered, but gated against agent-authoring agents).
//
// Why: smaller LLMs reliably mis-routed between the three create_*
// tools because the names and schemas overlap. One tool with a mode
// discriminator + one implicit scope (AuthoringAgentID) gives them
// a single, unambiguous path: "I'm authoring tools for the agent I
// just got/created."

package orchestrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
)

// add_tool is NOT globally registered. The struct exists as a Go
// type; Builder's catalog assembly (builderAuthoringTools) constructs
// the AgentToolDef directly from it. No other agent can find this
// tool by name, list it, or call it — it's structurally bound to
// Builder's runtime path.

// pipelineAuthoringDisabled retires pipeline-MODE tool authoring from
// the chat surface. PERMANENTLY on: the declarative `pipeline` tool
// (core.PipelineDef — author, attach via attached_pipelines, run, export,
// admin-govern) fully supersedes the old "a tool that wraps a sub-agent"
// macro. Keeping both alive only bred confusion — agents ended up with a
// declarative run_<pipeline> tool AND bare-named mode="pipeline" macro
// tools doing overlapping work, and the LLM oscillated between them.
//
// This gates the AUTHORING paths only (add_tool / create_pipeline_tool).
// The dispatch machinery stays so any EXISTING mode="pipeline" tools keep
// working; they just can't be authored anew. New multi-stage workflows
// go through the `pipeline` tool.
const pipelineAuthoringDisabled = true

type addToolTool struct{}

func (addToolTool) Name() string             { return "add_tool" }
func (addToolTool) Caps() []Capability       { return []Capability{CapWrite} }
func (addToolTool) SingleFirePerBatch() bool { return true }
func (addToolTool) Desc() string {
	base := "Attach ONE single-action tool to an agent — either the one named in the `agent` argument, or (when omitted) the agent currently in authoring focus, set by your most recent get_agent or create_agent call. Note that create_agent MOVES the focus to the agent it just created, so if you've made sub-agents since you started on your intended target, pass `agent` explicitly rather than trusting focus. This builds a single shell OR api tool — it does NOT build or edit toolboxes. If you need MULTIPLE related endpoints under one tool name (a whole API surface sharing one credential, e.g. the `moltbook` or a GitHub toolbox), or you need to change one action of an existing toolbox, use the `tool_def` tool instead (mode=\"toolbox\" to create, action=\"update\" to edit one action) — not this. Pick the mode that fits the work:\n"
	if !pipelineAuthoringDisabled {
		base += "  - mode=\"pipeline\": a multi-step sub-agent flow with its own prompt + inner tools. Use for \"do X, then Y, then summarize\" patterns.\n"
	}
	base += "  - mode=\"shell\":    a sandboxed shell command template. Use for deterministic file/data ops (\"count lines\", \"extract emails\").\n" +
		"  - mode=\"api\":      a single HTTP call against a registered credential. Use for \"look this up in our system\" patterns.\n"
	if pipelineAuthoringDisabled {
		base += "\nNOTE: pipeline-mode tool authoring is retired. add_tool now builds shell + api tools only. For a multi-step workflow (do X, then Y, then summarize), author a declarative pipeline with the `pipeline` tool (action=\"create\", stages=[…]) and attach it to the agent via attached_pipelines — it surfaces as a callable run_<pipeline> tool."
	}
	base += "\nRe-calling with the same name overwrites — that's how you iterate. The tool is installed as a session draft so you can dispatch it by name immediately to verify it works before declaring success. If no agent is in authoring focus, call agents(action=\"get\") on the target agent or create_agent first."
	return base
}
func (addToolTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"name": {
			Type:        "string",
			Description: "Snake_case tool name. Must be unique within the agent's tool set; re-using a name overwrites.",
		},
		"script_body": {
			Type:        "string",
			Description: "(shell) The script's source, shipped WITH the tool record so it survives workspace wipes and travels on export — the preferred way to author a shell tool. The framework writes it into the workspace and, if you omit command_template, infers one (e.g. python3 {workspace_dir}/script.py) from the extension. Declared params reach the script as ENVIRONMENT VARIABLES, not positional argv — read them with os.environ['name']. Network calls: use `from gohort import fetch_url` — urllib/requests/curl/wget are blocked in the sandbox.",
		},
		"script_name": {
			Type:        "string",
			Description: "(shell) Optional filename for script_body. Defaults to \"script.py\". Pick an extension matching the language (e.g. \"run.sh\") so the inferred command_template uses the right interpreter.",
		},
		"agent": {
			Type:        "string",
			Description: "Optional. Name or id of the agent to attach this tool to. Omit to use the agent currently in authoring focus (your most recent get_agent / create_agent). PASS IT EXPLICITLY whenever you've created another agent since you started working on the intended target — create_agent moves the focus, so building helper sub-agents for a parent and then calling add_tool would otherwise attach the tool to the last helper instead of the parent.",
		},
		"mode": {
			Type: "string",
			Description: func() string {
				if pipelineAuthoringDisabled {
					return "One of \"shell\", \"api\" — a single-action tool. There is NO \"toolbox\" mode here: multi-action toolboxes are authored and edited via the `tool_def` tool. Each branch validates the required fields for that mode. (Pipeline mode is retired — use the `pipeline` tool for multi-stage workflows.)"
				}
				return "One of \"pipeline\", \"shell\", \"api\" — a single-action tool. There is NO \"toolbox\" mode here: multi-action toolboxes are authored and edited via the `tool_def` tool. Each branch validates the required fields for that mode."
			}(),
		},
		"description": {
			Type:        "string",
			Description: "One-line summary of what the tool does — the agent reads this in its catalog at runtime to decide whether to call it.",
		},
		"params": {
			Type:        "object",
			Description: "Optional JSON object of {param_name: {type, description}}. Placeholders {name} in command_template / url_template are substituted from caller args.",
		},
		// Pipeline-mode fields (pipeline_prompt / pipeline_steps /
		// pipeline_tools / pipeline_max_rounds) were dropped from the schema
		// when pipelineAuthoringDisabled retired the mode: emitting four dead
		// params on every catalog contradicted the "retired" note in the mode
		// description and cost prefill tokens. The handler still refuses the
		// mode explicitly, and existing mode="pipeline" tools keep dispatching.
		// Shell-mode fields.
		"command_template": {
			Type:        "string",
			Description: "(shell mode) Shell command template. {param_name} placeholders are shell-quoted at dispatch time. Runs in a workspace sandbox. Optional when you pass script_body with a recognized extension — the framework infers the template from it; state it explicitly for stdin / kwargs / non-positional shapes.",
		},
		// API-mode fields.
		"url_template": {
			Type:        "string",
			Description: "(api mode) URL template. {param_name} placeholders are URL-encoded.",
		},
		"method": {
			Type:        "string",
			Description: "(api mode) HTTP method. Default GET.",
		},
		"body_template": {
			Type:        "string",
			Description: "(api mode) Optional request body template; placeholders JSON-encoded.",
		},
		"credential": {
			Type:        "string",
			Description: "(api mode) OPTIONAL. For AUTHENTICATED APIs (internal services, paid APIs): name of a registered credential (admin sets these up). The credential supplies the auth header and constrains the URL pattern. For PUBLIC APIs that need no authentication (Reddit JSON, Wikipedia, public data feeds): OMIT this field entirely, or pass \"no_auth\" — the ONE accepted no-auth spelling (same rule as tool_def). Do not pass placeholder strings like \"none\" / \"public\" / \"n/a\" — the framework rejects those. Empty/no_auth = public HTTP call; any other name = authenticated via that credential.",
		},
		"response_pipe": {
			Type:        "string",
			Description: "(api mode) Optional shell command to post-process the raw response (jq, awk, etc.) before the LLM sees it. Empty = raw response.",
		},
		"test_args": {
			Type:        "object",
			Description: "STRONGLY RECOMMENDED. Object of sample args (one value per declared param) to dispatch the freshly-authored tool with as a verification step. The result (or error) is appended to add_tool's reply so you can see whether the template actually works before declaring success. Example: for params={subreddit:{type:\"string\"}, limit:{type:\"string\"}} pass test_args={subreddit:\"golang\", limit:\"5\"}. Omit only when the tool has no parameters or when you'll iterate the design with new args next round anyway.",
		},
	}
}
func (addToolTool) Run(map[string]any) (string, error) {
	return "", errors.New("add_tool requires a session context")
}
func (addToolTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.Username == "" || sess.DB == nil || sess.ChatSessionID == "" {
		return "", errors.New("add_tool requires an authenticated chat session")
	}
	// Target resolution: an explicit `agent` argument wins; otherwise fall back
	// to the authoring-focus slot (set by get_agent / create_agent).
	//
	// The explicit argument deliberately reverses an earlier "focus is the
	// single source of truth, no for_agent argument" decision. That model holds
	// only while focus moves where the LLM expects — and it doesn't: create_agent
	// STAMPS focus, so authoring a parent, then creating two helper sub-agents
	// for it, silently retargets the next add_tool onto the last helper. Observed
	// in the wild: a tool meant for an OSINT parent landed on its sub-agent, with
	// no error and no signal, and the model then mis-diagnosed the failure.
	// Implicit state that silently changes under you isn't a clean mental model,
	// it's a trap. Focus stays as the ergonomic default for the common
	// single-agent flow; `agent` is the escape hatch when it isn't.
	var target AgentRecord
	if key := strings.TrimSpace(stringArg(args, "agent")); key != "" {
		found, ok := findAgentByNameOrID(sess.DB, sess.Username, key)
		if !ok {
			return "", fmt.Errorf("add_tool: no agent named or id'd %q in your fleet — call agents(action=\"list\") to see the exact names", key)
		}
		target = found
	} else {
		focusedID := loadAuthoringInProgress(sess.DB, sess.ChatSessionID)
		if focusedID == "" {
			return "", errors.New("add_tool: no agent in authoring focus and no agent argument — either pass agent=\"<name or id>\" to name the target explicitly, or call agents(action=\"get\", ...) / create_agent first to set focus")
		}
		found, ok := loadAgent(sess.DB, focusedID)
		if !ok {
			return "", fmt.Errorf("add_tool: focused agent %q is gone from storage — re-call get_agent on a valid agent to reset focus, or pass agent=\"<name or id>\"", focusedID)
		}
		target = found
	}
	if target.Owner != sess.Username {
		return "", fmt.Errorf("add_tool: agent %q is a read-only seed — call clone_agent to make an editable copy, then continue", target.Name)
	}
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return "", errors.New("name is required")
	}
	mode := strings.ToLower(strings.TrimSpace(stringArg(args, "mode")))
	if mode == "" {
		return "", errors.New("mode is required (one of \"shell\", \"api\")")
	}
	desc := strings.TrimSpace(stringArg(args, "description"))
	params := paramsFromArgs(args, "params")
	required := requiredFromParams(params)

	tt := TempTool{
		Name:        name,
		Description: desc,
		Params:      params,
		Required:    required,
	}
	switch mode {
	case "pipeline":
		if pipelineAuthoringDisabled {
			return "", errors.New("pipeline-mode tool authoring is retired. For a multi-step workflow, author a declarative pipeline with the `pipeline` tool (action=\"create\", name=…, stages=[{name, kind:\"worker\"|\"agent\", prompt, agent?}]), then attach it to this agent via attached_pipelines on create_agent/update_agent — it surfaces as a callable run_<pipeline> tool. For single-step work, use mode=\"shell\" or mode=\"api\" here")
		}
		prompt := strings.TrimSpace(stringArg(args, "pipeline_prompt"))
		steps := pipelineStepsFromArgs(args, "pipeline_steps")
		if prompt == "" && len(steps) == 0 {
			return "", errors.New("either pipeline_prompt (adaptive LLM-driven) or pipeline_steps (deterministic) is required for mode=\"pipeline\"")
		}
		inner := stringSliceFromArgs(args, "pipeline_tools")
		if len(inner) == 0 {
			return "", errors.New("pipeline_tools must include at least one inner tool name for mode=\"pipeline\"")
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
		tt.Mode = TempToolModePipeline
		tt.PipelinePrompt = prompt
		tt.PipelineSteps = steps
		tt.PipelineTools = inner
		tt.PipelineMaxRounds = intFromArgs(args, "pipeline_max_rounds")
	case "shell":
		cmd := strings.TrimSpace(stringArg(args, "command_template"))
		// script_body was previously accepted-and-dropped here: the record kept
		// a command_template pointing at a file nothing ever wrote, so the first
		// dispatch died on a missing script and the error blamed a "legacy
		// tool". Route through the same seam tool_def uses — it infers the
		// template when omitted, ships the script into the workspace, and
		// returns the names to persist so the tool travels with its code.
		scriptBody := stringArg(args, "script_body")
		outCmd, scriptName, canonical, serr := temptool.PrepareScriptBody(
			sess, name, cmd, scriptBody, strings.TrimSpace(stringArg(args, "script_name")), args["params"],
		)
		if serr != nil {
			return "", serr
		}
		cmd = outCmd
		if cmd == "" {
			return "", errors.New("command_template is required for mode=\"shell\" (or pass script_body with a recognized extension — .py/.sh/.bash/.js/.jq/.rb — and the framework will infer it)")
		}
		tt.Mode = "" // legacy shell mode key is empty string per common.go
		tt.CommandTemplate = cmd
		tt.ScriptBody = scriptBody
		tt.ScriptName = scriptName
		tt.CanonicalScriptName = canonical
	case "api":
		url := strings.TrimSpace(stringArg(args, "url_template"))
		if url == "" {
			return "", errors.New("url_template is required for mode=\"api\"")
		}
		credential := strings.TrimSpace(stringArg(args, "credential"))
		// "no_auth" is tool_def's canonical public-API spelling; treat it as
		// "omitted" here so the two authoring surfaces accept the same rule
		// (public API => omit or no_auth) instead of near-miss rejecting.
		if strings.EqualFold(credential, "no_auth") {
			credential = ""
		}
		// Reject placeholder credential strings — Builder kept reaching
		// for "none" / "n/a" / "public" as a stand-in when no auth was
		// needed, which then failed at dispatch. For public APIs the
		// right shape is to OMIT the credential entirely; the runtime
		// branches to plain HTTP in that case.
		if credential != "" && isPlaceholderCredential(credential) {
			return "", fmt.Errorf("credential value %q is a placeholder string, not a real credential. For PUBLIC APIs (no auth needed), OMIT the credential field entirely — the runtime will route through plain HTTP. For AUTHENTICATED APIs, pass the actual registered credential name (have the user register one via the admin UI if none exists)", credential)
		}
		// A secured credential is locked to the tools that already use it — a
		// new/edited api tool can't declare it (that would self-grant the
		// secret). Same gate the shell path enforces on fetch_via:/secret:.
		if credential != "" {
			if cr, ok := Secure().Load(credential); ok && cr.Secured {
				return "", fmt.Errorf("credential %q is SECURED — locked to the tools that already use it; a new tool can't declare it. Ask an admin to unsecure it in Admin > APIs to change which tools use it", credential)
			}
		}
		tt.Mode = TempToolModeAPI
		tt.CommandTemplate = url
		tt.Method = strings.ToUpper(strings.TrimSpace(stringArg(args, "method")))
		if tt.Method == "" {
			tt.Method = "GET"
		}
		tt.BodyTemplate = stringArg(args, "body_template")
		tt.Credential = credential
		tt.ResponsePipe = stringArg(args, "response_pipe")
	case "toolbox":
		// add_tool only builds single shell/api tools; toolboxes (multi-
		// action tools sharing one credential) are authored and EDITED via
		// the `tool_def` tool. Without this redirect the model hit the bare
		// "unknown mode" error, concluded add_tool "can't inspect or update
		// the sub-actions", and abandoned a real fix it had already
		// diagnosed. Point it straight at the update path it needed.
		return "", fmt.Errorf("add_tool does not build toolboxes — use the `tool_def` tool for that. To EDIT one action of the existing %q toolbox without recreating it, call tool_def(action=\"update\", name=%q, actions=[{name:\"<action>\", ...just the fields you're changing}]) — other actions are preserved. To create a new toolbox, tool_def(action=\"create\", mode=\"toolbox\", credential=…, actions=[…]). Call tool_def(action=\"help\") for the full spec", name, name)
	default:
		return "", fmt.Errorf("unknown mode %q — add_tool supports \"shell\" and \"api\". For a MULTI-action toolbox, use the `tool_def` tool (mode=\"toolbox\"); for a multi-stage workflow, use the `pipeline` tool", mode)
	}

	// Attach to the focused agent's tools[]. Idempotent replace by name.
	replaced := false
	for i, existing := range target.Tools {
		if existing.Name == tt.Name {
			target.Tools[i] = tt
			replaced = true
			break
		}
	}
	if !replaced {
		target.Tools = append(target.Tools, tt)
	}
	if _, err := saveAgent(sess.DB, target); err != nil {
		return "", fmt.Errorf("save focused agent: %v", err)
	}
	// Install as session draft so the LLM can dispatch by name on the
	// next round if it wants to (the verification path below covers the
	// common case; this is the fallback for tools without test_args).
	if err := SaveSessionTempTool(sess.DB, sess.ChatSessionID, tt); err != nil {
		Log("[orchestrate.add_tool] session draft save failed for %q: %v", tt.Name, err)
	}
	// Dequeue from admin pending-review pool — tool is now owned by
	// the focused agent record, so it doesn't need separate admin
	// promotion. Mirrors the autoCopy hook in create_agent.
	if sess.Username != "" {
		DequeuePendingTempTool(sess.DB, sess.Username, tt.Name)
	}

	verb := "added"
	if replaced {
		verb = "replaced"
	}

	// Verification path: when the LLM supplied test_args, dispatch the
	// new tool immediately and fold the result (or error) into the
	// reply. This collapses the author→test→iterate loop into a single
	// tool call AND avoids the smaller-model failure mode where the
	// LLM, given both `call_<credential>` and the new named tool in its
	// catalog on the next round, picks the wrong one and produces
	// "url is required" against a perfectly-good named wrapper.
	// Every exit below records the tool's verification standing to the session
	// ledger, so the build-plan done-gate grades on what was VERIFIED rather
	// than on the model's own "step done" claim. Prose alone wasn't enough: a
	// FAILED verify here scrolled past, the model marked its step done anyway,
	// and report_build_gaps cheerfully signed off.
	testArgs := testArgsFromArgs(args, "test_args")
	if len(testArgs) > 0 {
		copy := tt
		out, dispatchErr := temptool.DispatchTempToolDirect(sess, &copy, testArgs)
		if dispatchErr != nil {
			RecordToolVerification(sess, tt.Name, false, fmt.Sprintf("verification call failed: %v", dispatchErr))
			return fmt.Sprintf("Tool %q (mode=%s) %s on agent %q. Verification call with test_args FAILED: %v. Re-call add_tool with the same name to fix the template (re-state every field — partial updates aren't supported). Once it returns a sensible result you're done.", tt.Name, mode, verb, target.Name, dispatchErr), nil
		}
		RecordToolVerification(sess, tt.Name, true, "")
		trimmed := strings.TrimSpace(out)
		if len(trimmed) > 1200 {
			trimmed = trimmed[:1200] + "\n... [truncated]"
		}
		return fmt.Sprintf("Tool %q (mode=%s) %s on agent %q. Verification call with test_args succeeded:\n\n%s\n\nIf the result looks right, you're done — END THE TURN with a one-line summary. If the shape is off, re-call add_tool with the same name and a corrected template.", tt.Name, mode, verb, target.Name, trimmed), nil
	}

	RecordToolVerification(sess, tt.Name, false, "never tested — authored without test_args")
	return fmt.Sprintf("Tool %q (mode=%s) %s on agent %q. NO test_args were provided so the tool was not verified — re-call add_tool with the same fields PLUS test_args={...} to confirm the template works against the real endpoint. (Skip only if the tool has no params or you intend to test from a follow-up round.)", tt.Name, mode, verb, target.Name), nil
}

// testArgsFromArgs pulls the test_args object out of the LLM-supplied
// args. Accepts either a native object or a JSON-encoded string —
// smaller models occasionally wrap nested objects in quotes when they
// don't quite trust their own JSON serialization, and we'd rather
// recover than reject.
func testArgsFromArgs(args map[string]any, key string) map[string]any {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	if m, ok := raw.(map[string]any); ok {
		return m
	}
	if s, ok := raw.(string); ok {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil && len(m) > 0 {
			return m
		}
	}
	return nil
}

// isPlaceholderCredential returns true when the LLM passed a value
// that LOOKS like a credential but is actually a placeholder. Smaller
// models reach for these when no auth is needed instead of correctly
// switching to a pipeline+fetch_url shape. Catch them at authoring
// time so the failure is loud and directive instead of silent at
// dispatch time.
func isPlaceholderCredential(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "none", "n/a", "na", "null", "nil", "no-auth", "noauth", "no auth",
		"public", "open", "anonymous", "anon", "<none>", "<no-auth>",
		"not required", "not_required", "no_credential", "no credential",
		"placeholder", "tbd", "todo":
		return true
	}
	return false
}

// Compile-time check that add_tool implements the session-aware tool
// contract the runner expects.
var _ interface {
	RunWithSession(map[string]any, *ToolSession) (string, error)
} = addToolTool{}
