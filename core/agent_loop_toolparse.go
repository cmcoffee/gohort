package core

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ParseTextToolCall attempts to extract a tool call from text content when the
// model doesn't use structured tool calling. Tries three forms in order:
//
//  1. XML-style: <function=name><parameter=key>value</parameter></function>,
//     optionally wrapped in <tool_call> tags. Emitted by Llama-3 / Qwen /
//     Hermes-style instruction tunes even in native function-calling mode.
//  2. JSON: {"name": "...", "parameters": {...}} or {"name": "...", "arguments": {...}}.
//  3. Natural-language tool name in prose (last-resort fallback).
//
// toolDefs is consulted to validate that any synthesized call satisfies the
// tool's `Required` fields. If the extractor produces a call missing required
// args (typical of the prose-scan fallback when the model reasons about a
// tool but doesn't emit structured args), it's rejected — better to let the
// loop count the round as "model produced content but didn't act" than to
// fire a guaranteed-to-fail tool call and burn a round on the error.
// allowProse governs only the last-resort natural-language scan; the
// XML and JSON branches always run, since those are unambiguous machine
// output rather than a reading of English.
func ParseTextToolCall(content string, handlers map[string]ToolHandlerFunc, toolDefs []Tool, allowProse bool) *ToolCall {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	// XML-style first — when the model emits this form, the JSON
	// parser would otherwise see "{" inside the body and try (and
	// fail) to JSON-parse the whole thing. Detect by the function tag.
	if strings.Contains(content, "<function=") {
		// If wrapped in <tool_call>...</tool_call>, peel that off first
		// so the inner XML parser sees the function/parameter pairs
		// directly. Some models emit with wrapper, some without.
		body := content
		if start := strings.Index(body, "<tool_call>"); start >= 0 {
			if end := strings.Index(body, "</tool_call>"); end > start {
				body = strings.TrimSpace(body[start+len("<tool_call>") : end])
			}
		}
		if name, args := parseFunctionTagToolCall(body); name != "" {
			if _, ok := handlers[name]; ok {
				tc := &ToolCall{
					ID:   fmt.Sprintf("text_%s", UUIDv4()),
					Name: name,
					Args: args,
				}
				if hasRequired(tc, toolDefs) {
					return tc
				}
				Debug("[agent_loop] dropping XML-style tool call '%s' — missing required args", name)
			}
		}
	}

	// JSON form. Validate required fields below rather than trusting blindly.
	// Peel a wrapping <tool_call>...</tool_call> first when present —
	// some models emit a JSON call (no <function=> tag pair inside) but
	// still wrap it in the tool-call envelope. Without the peel the JSON
	// parser sees raw "<tool_call>" before the brace and fails; the
	// orchestrator then renders the markup as visible text and burns the
	// round. Mirrors the same peel the XML branch above does.
	jsonBody := content
	if start := strings.Index(jsonBody, "<tool_call>"); start >= 0 {
		if end := strings.Index(jsonBody, "</tool_call>"); end > start {
			jsonBody = strings.TrimSpace(jsonBody[start+len("<tool_call>") : end])
		}
	}
	if tc := parseJSONToolCall(jsonBody, handlers); tc != nil {
		if hasRequired(tc, toolDefs) {
			return tc
		}
		Debug("[agent_loop] dropping synthesized JSON tool call '%s' — missing required args", tc.Name)
	}

	// Last-resort: scan for a known tool name mentioned in the text.
	// Thinking models often reason like "call run_healthcheck with args ..."
	// without emitting the actual structured call.
	//
	// This branch is a GUESS, unlike the markup branches above — English
	// that announces a call and English that reports a finished one scrape
	// identically. allowProse is how the caller says the guess is worth
	// making; see the clean-finish gate in the agent loop.
	if !allowProse {
		return nil
	}
	if tc := parseNaturalToolCall(content, handlers); tc != nil {
		if hasRequired(tc, toolDefs) {
			return tc
		}
		Debug("[agent_loop] dropping synthesized natural-language tool call '%s' — could not extract required args from prose", tc.Name)
	}
	return nil
}

// nearestToolName returns the registered tool whose name shares the
// longest common substring (by simple bigram overlap) with attempted.
// Returns empty if no tool overlaps meaningfully — used for the "did
// you mean foo?" hint when the LLM tried a non-existent name.
func nearestToolName(attempted string, handlers map[string]ToolHandlerFunc) string {
	if attempted == "" || len(handlers) == 0 {
		return ""
	}
	att := strings.ToLower(attempted)
	bestName := ""
	bestScore := 0
	for name := range handlers {
		score := bigramOverlap(att, strings.ToLower(name))
		if score > bestScore {
			bestScore = score
			bestName = name
		}
	}
	// Threshold: require at least 2 shared bigrams to suggest, else
	// the suggestion is probably noise.
	if bestScore < 2 {
		return ""
	}
	return bestName
}

// bigramOverlap counts how many character-bigrams from a appear in b.
func bigramOverlap(a, b string) int {
	if len(a) < 2 || len(b) < 2 {
		return 0
	}
	count := 0
	for i := 0; i < len(a)-1; i++ {
		bg := a[i : i+2]
		if strings.Contains(b, bg) {
			count++
		}
	}
	return count
}

// hasRequired reports whether tc.Args contains every key listed in the
// matching tool's Required slice. Tools with no Required restriction
// always pass. Lookup is case-insensitive so an LLM that emits "URL"
// against a tool declaring "url" doesn't silently get dropped here
// (the dispatcher's downstream canonicalization fixes the value-side
// of the same mismatch).
func hasRequired(tc *ToolCall, toolDefs []Tool) bool {
	if tc == nil {
		return false
	}
	for _, td := range toolDefs {
		if td.Name != tc.Name {
			continue
		}
		for _, req := range td.Required {
			v, ok := tc.Args[req]
			if !ok {
				// Case-insensitive fallback so capitalization
				// drift between tool definition and LLM emission
				// doesn't drop a structurally valid call.
				reqLower := strings.ToLower(req)
				for k, val := range tc.Args {
					if strings.ToLower(k) == reqLower {
						v = val
						ok = true
						break
					}
				}
				if !ok {
					return false
				}
			}
			// Treat empty string / nil as missing — the tool's
			// validation would reject those anyway, and we want the
			// loop to recover, not waste a round.
			if v == nil {
				return false
			}
			if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
				return false
			}
		}
		return true
	}
	// Unknown tool name (handler exists but no def — shouldn't happen
	// in practice). Permit, since we can't validate.
	return true
}

// parseJSONToolCall extracts a tool call from a JSON object in the text.
func parseJSONToolCall(content string, handlers map[string]ToolHandlerFunc) *ToolCall {
	// Find the first '{' and last '}' to extract a JSON object.
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return nil
	}
	jsonStr := content[start : end+1]

	var raw map[string]interface{}
	if json.Unmarshal([]byte(jsonStr), &raw) != nil {
		return nil
	}

	name, _ := raw["name"].(string)
	if name == "" {
		return nil
	}

	// Only treat it as a tool call if the name matches a registered handler.
	if _, ok := handlers[name]; !ok {
		return nil
	}

	// Extract arguments from "parameters" or "arguments".
	args := make(map[string]any)
	var params map[string]interface{}
	if p, ok := raw["parameters"].(map[string]interface{}); ok {
		params = p
	} else if a, ok := raw["arguments"].(map[string]interface{}); ok {
		params = a
	}
	for k, v := range params {
		args[k] = v
	}

	return &ToolCall{
		ID:   fmt.Sprintf("text_%s", UUIDv4()),
		Name: name,
		Args: args,
	}
}

// parseCallArgs parses a narrated call's parenthesized argument list — the
// name(key="value", key2=…) or name({…json…}) that follows a tool name when a
// model writes a call as prose instead of emitting a structured tool call. It
// returns nil when `after` has no parenthesized list. Quoted values may contain
// commas (they don't split); bare true/false/number values are coerced.
func parseCallArgs(after string) map[string]any {
	after = strings.TrimSpace(after)
	if !strings.HasPrefix(after, "(") {
		return nil
	}
	// Find the matching close paren, respecting quoted strings.
	depth := 0
	var quote rune
	end := -1
	for i, r := range after {
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '"', '\'':
			quote = r
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return nil
	}
	inner := strings.TrimSpace(after[1:end])
	// JSON-object arg form: name({"to": "...", "text": "..."}).
	if strings.HasPrefix(inner, "{") {
		var m map[string]any
		if json.Unmarshal([]byte(inner), &m) == nil && len(m) > 0 {
			return m
		}
	}
	// key=value list, splitting on top-level commas only.
	args := make(map[string]any)
	for _, pair := range splitTopLevel(inner, ',') {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(pair[:eq])
		if key == "" {
			continue
		}
		args[key] = coerceArgValue(pair[eq+1:])
	}
	if len(args) == 0 {
		return nil
	}
	return args
}

// splitTopLevel splits s on sep, ignoring separators inside single/double
// quotes (so a quoted value containing a comma stays intact).
func splitTopLevel(s string, sep rune) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	for _, r := range s {
		if quote != 0 {
			cur.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		switch {
		case r == '"' || r == '\'':
			quote = r
			cur.WriteRune(r)
		case r == sep:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

// coerceArgValue strips matching quotes from a narrated arg value and coerces
// bare true/false/number literals; everything else stays a string.
func coerceArgValue(v string) any {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	switch strings.ToLower(v) {
	case "true":
		return true
	case "false":
		return false
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		return n
	}
	return v
}

// parseNaturalToolCall scans text for a known tool name and extracts any
// arguments that follow it. This handles thinking models that reason about
// which tool to call but stop before emitting a structured call.
//
// The name match is TOKEN-BOUNDED, and a name that is a bare English word
// (no underscore) is only honored in the adjacent-paren call form. Both
// guards exist because this scan reads ordinary conversation: a session
// with a tool named "image" put the word through here every time the model
// DESCRIBED a photo to the user. Substring matching would also have fired
// it inside "images"/"imagery". A tool name is only evidence of a call when
// it can't equally be evidence of English — see mentionedUncalledTool, which
// has held the same two guards since the actionPromiseCorrection fallout.
func parseNaturalToolCall(content string, handlers map[string]ToolHandlerFunc) *ToolCall {
	lower := strings.ToLower(content)

	// Find the best (longest) matching tool name in the text.
	var bestName string
	var bestPos int = -1
	for name := range handlers {
		pos := lastTokenIndex(lower, strings.ToLower(name))
		if pos >= 0 && (bestPos < 0 || len(name) > len(bestName)) {
			bestName = name
			bestPos = pos
		}
	}

	if bestName == "" {
		return nil
	}

	// Try to extract args after the tool name mention.
	args := make(map[string]any)
	rest := content[bestPos+len(bestName):]
	after := strings.TrimSpace(rest)

	// Common-word guard. A name with no underscore is a word the user and
	// the model may simply be TALKING about, so the only shape trusted for
	// it is one prose does not produce: the paren opening immediately, with
	// no space ("image(prompt=…)" is a call; "the image (a 4x6 print)" is a
	// sentence). Snake_case names skip this — they read as tool names
	// wherever they appear, so the looser forms below stay available.
	if !strings.Contains(bestName, "_") {
		if !strings.HasPrefix(rest, "(") {
			Debug("[agent_loop] skipping tool mention %q — common-word name not in call form, treating as prose", bestName)
			return nil
		}
		callArgs := parseCallArgs(rest)
		if len(callArgs) == 0 {
			Debug("[agent_loop] skipping tool mention %q — common-word name with no parsable args, treating as prose", bestName)
			return nil
		}
		return &ToolCall{ID: fmt.Sprintf("text_%s", UUIDv4()), Name: bestName, Args: callArgs}
	}

	// Function-call narration: name(key="value", ...) or name({...json...}).
	// This is the most common shape when a model WRITES a call as text instead
	// of emitting a structured tool call (the observed message_contact failure).
	// Extracting the named args rescues it into a real call — which still runs
	// through the normal confirm/approval gate, so a consequential one (texting a
	// person) isn't silently auto-executed. A well-formed name(args) is a strong
	// intent signal, unlike a bare mention, so the false-positive risk is low.
	if callArgs := parseCallArgs(after); len(callArgs) > 0 {
		return &ToolCall{ID: fmt.Sprintf("text_%s", UUIDv4()), Name: bestName, Args: callArgs}
	}

	// Look for --flag patterns (e.g. "--to user@example.com").
	//
	// A flag must be "--" followed by a LETTER. Without that test a markdown
	// horizontal rule ("---") counts as a flag, and since the scan runs from
	// the tool name to the end of the message it then sweeps every remaining
	// word into args["args"] — any answer that names a tool and later breaks
	// a section becomes a bogus call carrying the rest of the document.
	var flag_args []string
	for _, part := range strings.Fields(after) {
		if isFlagToken(part) {
			flag_args = append(flag_args, part)
		} else if len(flag_args) > 0 {
			// Attach value to the previous flag.
			flag_args = append(flag_args, part)
		}
	}
	if len(flag_args) > 0 {
		args["args"] = strings.Join(flag_args, " ")
	}

	// Guard: don't fire on bare tool-name mentions. If no args were
	// extractable from the prose, the model was almost certainly
	// just REASONING about the tool ("I should call web_search…")
	// rather than emitting an actual call. Firing here produces a
	// missing-required-arg failure that forces a wasted round.
	// Returning nil lets the loop terminate cleanly when the model
	// already finished its turn.
	if len(args) == 0 {
		Debug("[agent_loop] skipping natural-language tool mention %q — no args extractable, treating as reasoning prose", bestName)
		return nil
	}

	Debug("[agent_loop] extracted tool call from reasoning: %s", bestName)

	return &ToolCall{
		ID:   fmt.Sprintf("text_%s", UUIDv4()),
		Name: bestName,
		Args: args,
	}
}

// mentionedUncalledTool returns the name of a known tool that appears as a
// standalone token in content, and whether that tool takes parameters —
// or "" if no tool is named. It's the re-prompt trigger for a call the
// model NAMED but never emitted.
//
// parseNaturalToolCall rescues a narration only when it can read arguments
// off the prose, so two shapes fall through it in silence: a no-arg tool
// (there is nothing to extract) and a parameterized one the model merely
// talked ABOUT ("I don't have access to read_support_bundles") rather than
// wrote out as a call. Both end the turn on the model's own account of
// what it did or couldn't do, while the tool sat in the catalog the whole
// time — the reported symptom being an agent that insists it cannot reach
// files it was holding the tools for.
//
// Deliberately conservative to avoid the false positives that got
// actionPromiseCorrection disabled:
//   - only snake_case names (an underscore) — single common words like a
//     hypothetical "help" tool would false-match ordinary prose,
//   - token-bounded match so "image" doesn't fire inside "images",
//   - the caller fires only on a SHORT reply that ran no tool this turn,
//     which is what separates a lead-in or a refusal from an answer that
//     names a tool in passing.
//
// needsArgs distinguishes the two shapes for the nudge, which has to say
// why nothing ran: a no-arg tool had nothing to extract, a parameterized
// one needs its arguments written into a real structured call. It is the
// widening that retired the old zero-parameter restriction — that test
// kept the correction off exactly the tools whose narration costs the most
// (a search or a read the answer then claims was impossible), and the
// descriptive-mention risk it was guarding lives in the caller's length
// gate and in DisableToolMentionCorrection.
func mentionedUncalledTool(content string, handlers map[string]ToolHandlerFunc, toolDefs []Tool) (name string, needsArgs bool) {
	lower := strings.ToLower(content)
	for _, td := range toolDefs {
		if td.Name == "" || !strings.Contains(td.Name, "_") {
			continue
		}
		if _, ok := handlers[td.Name]; !ok {
			continue
		}
		if mentionsToken(lower, strings.ToLower(td.Name)) && len(td.Name) > len(name) {
			name = td.Name
			needsArgs = len(td.Parameters) != 0
		}
	}
	return name, needsArgs
}

// mentionsToken reports whether needle occurs in haystack bounded by
// non-identifier characters (so a tool name matches only as a standalone token,
// not inside a longer word). Both arguments must already be lowercase.
func mentionsToken(haystack, needle string) bool {
	return lastTokenIndex(haystack, needle) >= 0
}

// lastTokenIndex returns the index of the LAST token-bounded occurrence of
// needle in haystack, or -1. Same boundary rule as mentionsToken; the prose
// scan needs the position so it can read arguments off what follows. Last
// rather than first because a model that narrates and then calls puts the
// real call at the end. Both arguments must already be lowercase.
func lastTokenIndex(haystack, needle string) int {
	if needle == "" {
		return -1
	}
	found := -1
	for from := 0; from <= len(haystack)-len(needle); {
		i := strings.Index(haystack[from:], needle)
		if i < 0 {
			break
		}
		i += from
		end := i + len(needle)
		beforeOK := i == 0 || !isIdentByte(haystack[i-1])
		afterOK := end >= len(haystack) || !isIdentByte(haystack[end])
		if beforeOK && afterOK {
			found = i
		}
		from = i + 1
	}
	return found
}

// isFlagToken reports whether s is a command-line style flag ("--verbose"),
// as opposed to a run of dashes ("--", "---") that markdown uses as a rule.
func isFlagToken(s string) bool {
	if len(s) < 3 || !strings.HasPrefix(s, "--") {
		return false
	}
	c := s[2] | 0x20 // fold case
	return c >= 'a' && c <= 'z'
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z')
}

// BuildToolPrompt generates a text description of available tools for
// injection into the system prompt when PromptTools mode is enabled.
func BuildToolPrompt(tools []AgentToolDef) string {
	if len(tools) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\nYou have access to the following tools:\n\n")
	for _, td := range tools {
		b.WriteString(fmt.Sprintf("### %s\n%s\n", td.Tool.Name, td.Tool.Description))
		if len(td.Tool.Parameters) > 0 {
			b.WriteString("Parameters:\n")
			for name, p := range td.Tool.Parameters {
				req := ""
				for _, r := range td.Tool.Required {
					if r == name {
						req = " (required)"
						break
					}
				}
				b.WriteString(fmt.Sprintf("  - %s (%s%s): %s\n", name, p.Type, req, p.Description))
			}
		}
		b.WriteString("\n")
	}
	b.WriteString(`To use a tool, respond with EXACTLY this format on its own line:
<tool_call>
{"name": "tool_name", "arguments": {"param": "value"}}
</tool_call>

After each tool result, decide whether you have enough information to fully answer the question. If not, call another tool. Only reply once you can satisfactorily answer the request.
If you do not need a tool, respond normally without any <tool_call> tags.
Only call ONE tool at a time. Wait for the result before calling another.
`)
	return b.String()
}

// ParsePromptToolCall extracts a tool call from <tool_call> tags in the
// LLM's text response. Returns the parsed ToolCall and the surrounding
// text (before the tag) so the caller can preserve any preamble.
func ParsePromptToolCall(content string, handlers map[string]ToolHandlerFunc) (*ToolCall, string) {
	start := strings.Index(content, "<tool_call>")
	if start < 0 {
		return nil, content
	}
	end := strings.Index(content, "</tool_call>")
	if end < 0 || end <= start {
		return nil, content
	}

	preamble := strings.TrimSpace(content[:start])
	body := strings.TrimSpace(content[start+len("<tool_call>") : end])

	// Try the JSON form we instruct first: {"name": "...", "arguments": {...}}.
	// Fall back to the XML-style form Llama-3/Qwen/Hermes models often
	// emit even when prompted otherwise: <function=name><parameter=foo>value</parameter></function>.
	// Different surface forms, same intent — accept both rather than
	// drop the call and burn a round.
	var name string
	args := make(map[string]any)

	var raw map[string]interface{}
	if json.Unmarshal([]byte(body), &raw) == nil {
		name, _ = raw["name"].(string)
		if a, ok := raw["arguments"].(map[string]interface{}); ok {
			for k, v := range a {
				args[k] = v
			}
		}
	} else {
		// Fallback: parse <function=NAME>...<parameter=KEY>VALUE</parameter>...</function>.
		name, args = parseFunctionTagToolCall(body)
	}

	if name == "" {
		return nil, content
	}
	if _, ok := handlers[name]; !ok {
		return nil, content
	}

	return &ToolCall{
		ID:   fmt.Sprintf("prompt_%s", UUIDv4()),
		Name: name,
		Args: args,
	}, preamble
}

// parseFunctionTagToolCall handles the XML-style tool-call body that
// Llama-3 / Qwen / Hermes-style instruction tunes often emit instead
// of the JSON form we instruct. Format:
//
//	<function=tool_name>
//	<parameter=arg1>
//	value1
//	</parameter>
//	<parameter=arg2>
//	value2
//	</parameter>
//	</function>
//
// Returns the function name and parsed args map. Empty name means
// the body wasn't recognizable in this format either; caller treats
// as "drop the call" the same as a JSON parse failure.
func parseFunctionTagToolCall(body string) (string, map[string]any) {
	args := map[string]any{}
	// Find <function=...> or <function=...
	const fnPrefix = "<function="
	si := strings.Index(body, fnPrefix)
	if si < 0 {
		return "", nil
	}
	rest := body[si+len(fnPrefix):]
	// Function name runs until '>'.
	gt := strings.IndexByte(rest, '>')
	if gt < 0 {
		return "", nil
	}
	name := strings.TrimSpace(rest[:gt])
	rest = rest[gt+1:]

	// Walk through every <parameter=KEY>VALUE</parameter> chunk.
	const pPrefix = "<parameter="
	const pClose = "</parameter>"
	for {
		pi := strings.Index(rest, pPrefix)
		if pi < 0 {
			break
		}
		rest = rest[pi+len(pPrefix):]
		gt := strings.IndexByte(rest, '>')
		if gt < 0 {
			break
		}
		paramName := strings.TrimSpace(rest[:gt])
		rest = rest[gt+1:]
		closeIdx := strings.Index(rest, pClose)
		if closeIdx < 0 {
			break
		}
		// Strip leading/trailing whitespace + newlines around the value
		// so a multi-line shell command doesn't keep its surrounding
		// blank lines.
		val := strings.TrimSpace(rest[:closeIdx])
		args[paramName] = val
		rest = rest[closeIdx+len(pClose):]
	}
	return name, args
}
