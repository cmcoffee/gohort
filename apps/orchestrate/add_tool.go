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
// type; Builder's catalog assembly (builderInternalTools) constructs
// the AgentToolDef directly from it. No other agent can find this
// tool by name, list it, or call it — it's structurally bound to
// Builder's runtime path.

// pipelineAuthoringDisabled gates pipeline-mode tool authoring from
// the chat surface. Was turned ON when pipeline tools were a recurring
// loop source — the LLM kept oscillating between authoring surfaces
// even after many guards. Re-enabling experimentally: pipelines were
// authored cleanly before the build-plan / focus-slot / two-turn
// machinery accumulated, and the standalone tool_def path bypasses
// most of that. If loops resurface, flip back; the standalone
// tool_def(mode="pipeline", ...) surface is the path to validate
// first since it skirts the agent-CRUD gates.
const pipelineAuthoringDisabled = false

type addToolTool struct{}

func (addToolTool) Name() string             { return "add_tool" }
func (addToolTool) Caps() []Capability       { return []Capability{CapWrite} }
func (addToolTool) SingleFirePerBatch() bool { return true }
func (addToolTool) Desc() string {
	base := "Attach a tool to the agent currently in authoring focus (set by your most recent get_agent or create_agent call). Pick the mode that fits the work:\n"
	if !pipelineAuthoringDisabled {
		base += "  - mode=\"pipeline\": a multi-step sub-agent flow with its own prompt + inner tools. Use for \"do X, then Y, then summarize\" patterns.\n"
	}
	base += "  - mode=\"shell\":    a sandboxed shell command template. Use for deterministic file/data ops (\"count lines\", \"extract emails\").\n" +
		"  - mode=\"api\":      a single HTTP call against a registered credential. Use for \"look this up in our system\" patterns.\n"
	if pipelineAuthoringDisabled {
		base += "\nNOTE: pipeline-mode tool authoring is currently disabled. Use shell or api modes; if a tool genuinely needs LLM reasoning between steps, design the agent without it (use the orchestrator_prompt to instruct the agent to chain primitive tools inline)."
	}
	base += "\nRe-calling with the same name overwrites — that's how you iterate. The tool is installed as a session draft so you can dispatch it by name immediately to verify it works before declaring success. If no agent is in authoring focus, call get_agent or create_agent first."
	return base
}
func (addToolTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"name": {
			Type:        "string",
			Description: "Snake_case tool name. Must be unique within the agent's tool set; re-using a name overwrites.",
		},
		"mode": {
			Type: "string",
			Description: func() string {
				if pipelineAuthoringDisabled {
					return "One of \"shell\", \"api\". Each branch validates the required fields for that mode. (Pipeline mode is currently disabled platform-wide.)"
				}
				return "One of \"pipeline\", \"shell\", \"api\". Each branch validates the required fields for that mode."
			}(),
		},
		"description": {
			Type:        "string",
			Description: "One-line summary of what the tool does — the agent reads this in its catalog at runtime to decide whether to call it.",
		},
		"params": {
			Type:        "object",
			Description: "Optional JSON object of {param_name: {type, description}}. Placeholders {name} in pipeline_prompt / command_template / url_template are substituted from caller args.",
		},
		// Pipeline-mode fields.
		"pipeline_prompt": {
			Type:        "string",
			Description: "(pipeline mode, ADAPTIVE variant) System prompt for an LLM sub-agent that runs the chain with reasoning between steps. Use when steps need judgment (\"if step 1 returned nothing, try a different angle\"). Mutually exclusive with pipeline_steps. Be DIRECTIVE about sequencing; reference inner tools by name. {param_name} placeholders get filled from caller args via plain string substitution — write them BARE (e.g. `for the topic {query}`), NEVER wrap them in quotes (`'{query}'` becomes `'AI 2026'` and the sub-agent will pass the literal quoted string to web_search).",
		},
		"pipeline_steps": {
			Type:        "array",
			Description: "(pipeline mode, DETERMINISTIC variant) Ordered list of step objects {tool, args, name?}, executed in sequence with no inner LLM. Args undergo template substitution: {param_name} → caller arg, $N → output of step N (1-indexed), $N.field.path → JSON field path into step N. Use for linear chains where no reasoning is needed between steps. Cheaper + faster than pipeline_prompt. Mutually exclusive with pipeline_prompt. Example: [{\"tool\":\"web_search\",\"args\":{\"query\":\"{topic}\"},\"name\":\"hits\"},{\"tool\":\"fetch_url\",\"args\":{\"url\":\"$hits.top_url\"}}]",
			Items:       &ToolParam{Type: "object"},
		},
		"pipeline_tools": {
			Type:        "array",
			Description: "(pipeline mode) Names of tools the sub-agent (adaptive) or step executor (deterministic) may call. Step tools must appear here.",
			Items:       &ToolParam{Type: "string"},
		},
		"pipeline_max_rounds": {
			Type:        "integer",
			Description: "(pipeline mode, adaptive only) Cap on sub-agent LLM rounds. Default 6. Ignored when pipeline_steps is set.",
		},
		// Shell-mode fields.
		"command_template": {
			Type:        "string",
			Description: "(shell mode) Shell command template. {param_name} placeholders are shell-quoted at dispatch time. Runs in a workspace sandbox.",
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
			Description: "(api mode) OPTIONAL. For AUTHENTICATED APIs (internal services, paid APIs): name of a registered credential (admin sets these up). The credential supplies the auth header and constrains the URL pattern. For PUBLIC APIs that need no authentication (Reddit JSON, Wikipedia, public data feeds): OMIT this field entirely. Do not pass placeholder strings like \"none\" / \"public\" / \"n/a\" — the framework rejects those. Empty = public HTTP call; named = authenticated via that credential.",
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
	// Resolve focus from the side-channel slot — set by get_agent /
	// create_agent on successful return. No fallback / no for_agent
	// argument; the single-source-of-truth keeps the LLM's mental
	// model clean.
	focusedID := loadAuthoringInProgress(sess.DB, sess.ChatSessionID)
	if focusedID == "" {
		return "", errors.New("add_tool: no agent in authoring focus — call get_agent (to modify an existing agent) or create_agent (to start a new one) first. The agent you act on becomes the implicit target for subsequent add_tool calls in this session")
	}
	target, ok := loadAgent(sess.DB, focusedID)
	if !ok {
		return "", fmt.Errorf("add_tool: focused agent %q is gone from storage — re-call get_agent on a valid agent to reset focus", focusedID)
	}
	if target.Owner != sess.Username {
		return "", fmt.Errorf("add_tool: focused agent %q is a read-only seed — call clone_agent to make an editable copy, then continue", target.Name)
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
			return "", errors.New("pipeline-mode tool authoring is currently disabled for diagnostic testing — author the same logic by combining shell and api tools (mode=\"shell\" or mode=\"api\") and chaining via the agent's orchestrator_prompt instructions. If the work genuinely requires multi-step sub-agent reasoning, finish the agent with shell/api tools for now and the user can author the pipeline manually later via the admin UI")
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
		if cmd == "" {
			return "", errors.New("command_template is required for mode=\"shell\"")
		}
		tt.Mode = "" // legacy shell mode key is empty string per common.go
		tt.CommandTemplate = cmd
	case "api":
		url := strings.TrimSpace(stringArg(args, "url_template"))
		if url == "" {
			return "", errors.New("url_template is required for mode=\"api\"")
		}
		credential := strings.TrimSpace(stringArg(args, "credential"))
		// Reject placeholder credential strings — Builder kept reaching
		// for "none" / "n/a" / "public" as a stand-in when no auth was
		// needed, which then failed at dispatch. For public APIs the
		// right shape is to OMIT the credential entirely; the runtime
		// branches to plain HTTP in that case.
		if credential != "" && isPlaceholderCredential(credential) {
			return "", fmt.Errorf("credential value %q is a placeholder string, not a real credential. For PUBLIC APIs (no auth needed), OMIT the credential field entirely — the runtime will route through plain HTTP. For AUTHENTICATED APIs, pass the actual registered credential name (have the user register one via the admin UI if none exists)", credential)
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
	default:
		return "", fmt.Errorf("unknown mode %q — must be one of \"shell\", \"api\", \"pipeline\"", mode)
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
	testArgs := testArgsFromArgs(args, "test_args")
	if len(testArgs) > 0 {
		copy := tt
		out, dispatchErr := temptool.DispatchTempToolDirect(sess, &copy, testArgs)
		if dispatchErr != nil {
			return fmt.Sprintf("Tool %q (mode=%s) %s on agent %q. Verification call with test_args FAILED: %v. Re-call add_tool with the same name to fix the template (re-state every field — partial updates aren't supported). Once it returns a sensible result you're done.", tt.Name, mode, verb, target.Name, dispatchErr), nil
		}
		trimmed := strings.TrimSpace(out)
		if len(trimmed) > 1200 {
			trimmed = trimmed[:1200] + "\n... [truncated]"
		}
		return fmt.Sprintf("Tool %q (mode=%s) %s on agent %q. Verification call with test_args succeeded:\n\n%s\n\nIf the result looks right, you're done — END THE TURN with a one-line summary. If the shape is off, re-call add_tool with the same name and a corrected template.", tt.Name, mode, verb, target.Name, trimmed), nil
	}

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
