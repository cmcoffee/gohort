package temptool

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// dispatchAPIModeTempTool handles a TempTool whose Mode is api. The
// URL template gets URL-path-encoded args; the optional body template
// gets JSON-encoded args. The resolved request is then dispatched
// through DispatchSecureAPICredentialCall, which validates the URL
// against the credential's allowlist and injects the encrypted secret.
// dispatchPipelineModeTempTool runs a pipeline-mode TempTool by
// spawning a sub-agent loop via the host session's SubAgentRunner.
// Args are substituted into PipelinePrompt as plain-text
// `{arg_name}` placeholders (it's a system prompt, not a shell or
// URL — no quoting/encoding rules). The user message handed to the
// sub-agent is a compact JSON dump of the args, so the sub-agent
// can also read arg values directly without re-parsing the prompt.
//
// Sub-agent's tool catalog is PipelineTools (subset of the parent
// session's). MaxRounds defaults to 6 when unset.
func dispatchPipelineModeTempTool(sess *ToolSession, tt *TempTool, args map[string]any) (string, error) {
	// Structured-steps path takes precedence — when the author set
	// pipeline_steps, run deterministically; no sub-agent, no LLM
	// per step. Cheaper, faster, predictable.
	if len(tt.PipelineSteps) > 0 {
		return dispatchPipelineStepsTempTool(sess, tt, args)
	}
	if sess.SubAgentRunner == nil {
		return "", fmt.Errorf("pipeline tool %q: host app didn't wire SubAgentRunner", tt.Name)
	}
	sys := tt.PipelinePrompt
	for k, v := range args {
		sys = strings.ReplaceAll(sys, "{"+k+"}", fmt.Sprint(v))
	}
	// The substituted system prompt already carries every arg value
	// in its intended position. Passing the same args AGAIN as a JSON
	// blob in the user message ("Inputs:\n{...}") was a recurring
	// foot-gun: smaller sub-agents pattern-matched the JSON-quoted
	// string values (e.g. "AI 2026") and emitted their downstream
	// tool calls with the quotes still on, which then read as a
	// shell-quoted literal by web_search etc. The kickoff message is
	// just a trigger now — the sub-agent works from the system prompt.
	userMsg := "Begin."
	maxRounds := tt.PipelineMaxRounds
	if maxRounds <= 0 {
		maxRounds = 6
	}
	argsJSON, _ := json.Marshal(args)
	Log("[temptool.pipeline] dispatch tool=%q inner_tools=%v max_rounds=%d args=%s",
		tt.Name, tt.PipelineTools, maxRounds, string(argsJSON))
	// Dump the substituted system prompt for verification when an
	// author reports "the framework is quoting my values" — the
	// substitution is plain text (fmt.Sprint + ReplaceAll above) so
	// any quotes the sub-agent sees come from the author's prompt.
	debugSys := sys
	if len(debugSys) > 600 {
		debugSys = debugSys[:600] + "...[truncated]"
	}
	Debug("[temptool.pipeline] tool=%q substituted system prompt: %s", tt.Name, debugSys)
	// Wall-clock ceiling: without this the nested agent ran on
	// context.Background() (bounded only by max_rounds), so a stalled or looping
	// pipeline hung the parent turn with no recovery. The deadline propagates
	// into the sub-agent's LLM calls, which cancel at the boundary.
	pctx, cancel := context.WithTimeout(sess.Context(), TuneDuration("tune_pipeline_tool_timeout"))
	defer cancel()
	out, err := sess.SubAgentRunner(pctx, sys, userMsg, tt.PipelineTools, maxRounds)
	if err != nil {
		Log("[temptool.pipeline] tool=%q FAILED: %v", tt.Name, err)
		return "", fmt.Errorf("pipeline tool %q: %v", tt.Name, err)
	}
	Log("[temptool.pipeline] tool=%q OK (output=%d chars)", tt.Name, len(out))
	return out, nil
}

// dispatchPipelineStepsTempTool executes a structured pipeline step
// by step, with no inner LLM. Each step's args undergo template
// substitution against (caller args, prior step outputs) before the
// step's tool fires. The final step's output is the pipeline's
// return value.
//
// Substitution patterns inside string args:
//   - {param_name}    → caller's arg value
//   - $N              → entire string output of step N (1-indexed)
//   - $N.field.path   → JSON field path into step N's output;
//     returns empty string when the output isn't
//     JSON or the path doesn't resolve
//   - $name / $name.field — same as above but referencing a step's
//     Name (set by the author for readability)
//
// Aborts on the first error (no retry / no on_error policy in V1).
func dispatchPipelineStepsTempTool(sess *ToolSession, tt *TempTool, args map[string]any) (string, error) {
	allowed := map[string]bool{}
	for _, n := range tt.PipelineTools {
		allowed[n] = true
	}
	// Outputs is indexed two ways: by integer step number (1-based)
	// stored as the stringified int, AND by the optional step Name.
	// Both maps point into the same slice via parallel keys.
	rawOutputs := make([]string, 0, len(tt.PipelineSteps))
	jsonOutputs := make([]any, 0, len(tt.PipelineSteps))
	nameIndex := map[string]int{}

	Log("[temptool.pipeline_steps] dispatch tool=%q steps=%d", tt.Name, len(tt.PipelineSteps))
	for i, step := range tt.PipelineSteps {
		stepNum := i + 1
		toolName := strings.TrimSpace(step.Tool)
		if toolName == "" {
			return "", fmt.Errorf("step %d: tool name is required", stepNum)
		}
		if !allowed[toolName] {
			return "", fmt.Errorf("step %d: tool %q is not in this pipeline's allowed_tools list %v", stepNum, toolName, tt.PipelineTools)
		}
		// Resolve args — walk every value, substitute templates in
		// strings. Non-string values pass through unchanged so the
		// LLM can still pass numbers / bools / arrays.
		resolved := map[string]any{}
		for k, v := range step.Args {
			resolved[k] = resolvePipelineArg(v, args, rawOutputs, jsonOutputs, nameIndex)
		}
		// Dispatch the tool. The lookup goes through the session-
		// aware helper so session-scoped temp tools (drafts, inner
		// pipelines) are reachable.
		defs, err := GetAgentToolsWithSession(sess, toolName)
		if err != nil || len(defs) == 0 {
			return "", fmt.Errorf("step %d: tool %q not found in catalog: %v", stepNum, toolName, err)
		}
		out, err := defs[0].Handler(resolved)
		if err != nil {
			Log("[temptool.pipeline_steps] tool=%q step %d (%s) FAILED: %v", tt.Name, stepNum, toolName, err)
			return "", fmt.Errorf("step %d (%s): %v", stepNum, toolName, err)
		}
		Log("[temptool.pipeline_steps] tool=%q step %d (%s) OK (output=%d chars)", tt.Name, stepNum, toolName, len(out))
		rawOutputs = append(rawOutputs, out)
		// Best-effort JSON parse for later field-path resolution.
		// nil on parse failure is fine — accessor falls back to raw.
		var parsed any
		if err := json.Unmarshal([]byte(out), &parsed); err != nil {
			parsed = nil
		}
		jsonOutputs = append(jsonOutputs, parsed)
		if name := strings.TrimSpace(step.Name); name != "" {
			nameIndex[name] = i
		}
	}
	if len(rawOutputs) == 0 {
		return "", fmt.Errorf("pipeline tool %q: no steps defined", tt.Name)
	}
	return rawOutputs[len(rawOutputs)-1], nil
}

// resolvePipelineArg walks a single arg value applying template
// substitution rules. Non-string values pass through unchanged so
// numbers, bools, arrays etc. survive the round trip.
func resolvePipelineArg(v any, callerArgs map[string]any, rawOutputs []string, jsonOutputs []any, nameIndex map[string]int) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	return substitutePipelineTemplate(s, callerArgs, rawOutputs, jsonOutputs, nameIndex)
}

// substitutePipelineTemplate applies the three substitution patterns
// in order: {param}, $N.field, $name.field, $N, $name. Returns the
// resulting string. Unknown references render as empty string rather
// than the literal token so a typo doesn't leak template syntax into
// the next tool's input.
func substitutePipelineTemplate(s string, callerArgs map[string]any, rawOutputs []string, jsonOutputs []any, nameIndex map[string]int) string {
	// {param} substitution.
	for k, v := range callerArgs {
		s = strings.ReplaceAll(s, "{"+k+"}", fmt.Sprint(v))
	}
	// $N / $N.field / $name / $name.field — walk char-by-char to
	// avoid regex complexity and gracefully handle adjacency.
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] != '$' {
			out.WriteByte(s[i])
			i++
			continue
		}
		// Read the identifier (digits or letters/underscores).
		j := i + 1
		for j < len(s) && (isIdentChar(s[j])) {
			j++
		}
		ident := s[i+1 : j]
		if ident == "" {
			// Lone '$' — emit literally.
			out.WriteByte('$')
			i++
			continue
		}
		// Optional dotted path.
		path := ""
		k := j
		if k < len(s) && s[k] == '.' {
			pathStart := k + 1
			for k < len(s) && (s[k] == '.' || isIdentChar(s[k])) {
				k++
			}
			path = s[pathStart:k]
		}
		// Resolve the identifier to a step index.
		var stepIdx int = -1
		if n, err := strconv.Atoi(ident); err == nil {
			stepIdx = n - 1 // 1-indexed → 0-indexed
		} else if idx, ok := nameIndex[ident]; ok {
			stepIdx = idx
		}
		if stepIdx < 0 || stepIdx >= len(rawOutputs) {
			// Unresolved reference — emit empty (silent miss).
			i = k
			continue
		}
		if path == "" {
			out.WriteString(rawOutputs[stepIdx])
		} else {
			out.WriteString(extractJSONPath(jsonOutputs[stepIdx], path))
		}
		i = k
	}
	return out.String()
}

func isIdentChar(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

// extractJSONPath walks a dotted path into a parsed JSON value.
// Returns the resolved value as a string (json-encoded for objects /
// arrays, stringified for scalars). Empty string on miss.
func extractJSONPath(root any, path string) string {
	if root == nil || path == "" {
		return ""
	}
	cur := root
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		next, ok := m[part]
		if !ok {
			return ""
		}
		cur = next
	}
	switch v := cur.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
