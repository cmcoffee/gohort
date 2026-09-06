package temptool

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// ----------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------

// validToolName: lowercase letters, digits, underscores only. Length
// 1-64. Mirrors the snake_case convention in the rest of the codebase.
func validToolName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}

// parseParamsArg accepts the LLM's `params` object and converts it to
// our typed ToolParam map. Tolerates two shapes the LLM commonly emits:
// a real JSON object, or a JSON-encoded string of one.
// actionOnlyFields names fields that belong to a tool or a toolbox
// ACTION, never to one of its params, mapped to how the error describes
// them. Every entry is a framework-specific compound name, which is what
// makes rejecting by name safe: no real API has a parameter called
// "body_template".
//
// Deliberately EXCLUDED: description, name, method, content_type,
// headers, url. Those are plausible parameter names — a create-issue
// endpoint really does take a "description" — and rejecting them would
// break working tools to catch a rarer mistake.
var actionOnlyFields = map[string]string{
	"body_template":    "the request body template",
	"response_pipe":    "the response post-processor",
	"url_template":     "the endpoint path",
	"command_template": "the shell command",
	"script_body":      "the script source",
	"script_name":      "the script file name",
	"pipeline_steps":   "the pipeline step list",
	"pipeline_tools":   "the pipeline tool list",
}

// checkMisplacedActionField rejects a params entry that is really an
// action-level field nested one level too deep.
//
// This mirrors the top-level guard in tool_def.go, which catches the same
// fields placed one level too SHALLOW ("%q is a PER-ACTION field on a
// toolbox, not a top-level one"). Without the inverse, the lenient
// coercion below cheerfully turns "body_template" into a param NAMED
// body_template: the update reports success, the real field is never set,
// and an action's "required" list is absorbed the same way — after which
// the write-action scaffold regenerates a body template from whatever
// params survived. Observed live: a working toolbox degraded across eight
// consecutive "successful" updates into a delete-and-recreate, with the
// model concluding the scaffold was at fault.
//
// The lenient value coercion below stays exactly as it is — it exists to
// stop a real, expensive retry loop over param-descriptor shape. What it
// cannot do is repair a key that names no parameter at all, so that case
// gets a pointer to the right shape instead of a silent guess.
func checkMisplacedActionFields(loose map[string]any) error {
	var bad []string
	for k, val := range loose {
		if _, isField := actionOnlyFields[k]; isField {
			bad = append(bad, k)
			continue
		}
		// "required" is a plausible param name on a real API, so the name
		// alone can't decide. The action's required list is an ARRAY and a
		// param descriptor never is, so the shape disambiguates.
		if k == "required" {
			if _, isList := val.([]any); isList {
				bad = append(bad, k)
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad) // deterministic: the same input always names them in the same order
	if len(bad) == 1 {
		k := bad[0]
		if k == "required" {
			return fmt.Errorf(`"required" here is a list of mandatory param NAMES, which belongs to the tool/action rather than inside params — move it out alongside params: {name: …, params: {…}, required: ["a","b"]}`)
		}
		return fmt.Errorf("%q is %s for the tool/action itself, not one of its params — it is nested one level too deep. Move it out alongside params: {name: …, params: {…}, %s: …}. params maps each PARAMETER name to {type, description}", k, actionOnlyFields[k], k)
	}
	return fmt.Errorf("%s are fields of the tool/action itself, not its params — they are nested one level too deep. Move ALL of them out alongside params in one edit: {name: …, params: {…}, %s: …}. params maps each PARAMETER name to {type, description}",
		quotedList(bad), strings.Join(bad, ": …, "))
}

// quotedList renders names as `"a", "b" and "c"` for an error sentence.
func quotedList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	if len(q) == 1 {
		return q[0]
	}
	return strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
}

func parseParamsArg(v any) (map[string]ToolParam, error) {
	out := map[string]ToolParam{}
	// A no-param tool/action is legitimate — a GET with no query string, a
	// shell command like "date", or an endpoint like /home or /list_submolts.
	// Treat absent OR empty params as a valid empty set instead of forcing the
	// author to invent a dummy "_ (Unused, required by API)" placeholder just
	// to pass validation. buildToolParamsSchema emits a valid {"type":"object"}
	// for an empty set and the dispatcher then requires nothing, so the tool is
	// callable with no args (a toolbox sub-action with just action="home").
	if v == nil {
		return out, nil
	}
	// Re-marshal whatever we got and unmarshal into our typed map. This
	// handles both the native object form and the stringified form the
	// LLM might produce when it emits a JSON blob.
	var raw any
	if s, ok := v.(string); ok {
		if strings.TrimSpace(s) == "" {
			return out, nil
		}
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			return nil, fmt.Errorf("could not parse params JSON: %w", err)
		}
	} else {
		raw = v
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("re-marshal failed: %w", err)
	}
	// Lenient per-value coercion. Models routinely emit the WRONG shape for a
	// param value — a bare bool ({"sid": true}), a bare type string ({"sid":
	// "string"}), or a description string ({"sid": "the server id"}) — instead
	// of a {type, description} object. A hard reject sends tool_def into a retry
	// loop that never converges (observed: a turn grinding 500k+ lead tokens on
	// "cannot unmarshal bool into ToolParam"), so coerce each value instead.
	var loose map[string]any
	if err := json.Unmarshal(b, &loose); err != nil {
		return nil, fmt.Errorf("params must be an object mapping each name to {type, description} (a type or description string, or true, also works) — could not read it as an object: %w", err)
	}
	// Report EVERY misplaced field at once, sorted. Reporting the first
	// offender meant a caller with three of them needed three round
	// trips — and because this ranges a map, which one surfaced was
	// random each time, so the fix looked like moving goalposts.
	// Observed live: three rejections in a row, after which the model
	// gave up on update and deleted the tool instead.
	if err := checkMisplacedActionFields(loose); err != nil {
		return nil, err
	}
	for k, val := range loose {
		if !validToolName(k) {
			return nil, fmt.Errorf("param name %q must be lowercase letters/digits/underscores only", k)
		}
		p, perr := coerceToolParam(val)
		if perr != nil {
			return nil, fmt.Errorf("param %q: %w", k, perr)
		}
		out[k] = p
	}
	return out, nil
}

// coerceToolParam turns whatever the model put as a param's value into a
// ToolParam. Accepts the correct {type, description} object, a bare type name,
// a bare description string, or a scalar (bool/number → a plain string param) —
// so a wrong-but-close SHAPE doesn't hard-fail tool_def into a grind loop. An
// object that EXPLICITLY names an unsupported type (e.g. "widget", "array") is
// still an error — the model chose a type, it's just wrong, so guide it.
func coerceToolParam(val any) (ToolParam, error) {
	switch t := val.(type) {
	case map[string]any:
		p := ToolParam{}
		if s, _ := t["type"].(string); s != "" {
			ct := canonicalParamType(s)
			if ct == "" {
				return ToolParam{}, fmt.Errorf("type %q not supported (use string/integer/number/boolean)", s)
			}
			p.Type = ct
		}
		if s, _ := t["description"].(string); s != "" {
			p.Description = s
		} else if s, _ := t["desc"].(string); s != "" {
			p.Description = s
		}
		if p.Type == "" {
			p.Type = "string" // object with a description but no type → default
		}
		return p, nil
	case string:
		if ct := canonicalParamType(t); ct != "" {
			return ToolParam{Type: ct}, nil // a bare type name
		}
		return ToolParam{Type: "string", Description: t}, nil // a bare description
	default:
		return ToolParam{Type: "string"}, nil // bool/number/null placeholder
	}
}

// canonicalParamType maps a loosely-specified type to one of the four supported
// types (tolerating common synonyms), or "" if it isn't recognizably a type.
func canonicalParamType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "string", "str", "text":
		return "string"
	case "integer", "int":
		return "integer"
	case "number", "float", "double", "decimal":
		return "number"
	case "boolean", "bool":
		return "boolean"
	}
	return ""
}

func stringSliceArg(v any) []string {
	if v == nil {
		return nil
	}
	// Already a string slice — the shape actionToArgs / tempToolToCreateArgs
	// emit when an update round-trips a stored tool. Missing this case is how
	// every action's required list silently became empty on any partial edit
	// (remove_actions, single-action upsert), which the path-placeholder gate
	// then rejected — an unfixable-from-the-model's-side loop.
	if arr, ok := v.([]string); ok {
		var out []string
		for _, s := range arr {
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	if arr, ok := v.([]any); ok {
		var out []string
		for _, e := range arr {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// validateTemplate scans cmd for `{name}` placeholders and ensures each
// one names a known param. Catches the obvious "I forgot to add this
// to params" mistake before the tool gets registered.
//
// Tolerant of literal braces in JSON / shell expressions: only treats
// `{...}` as a placeholder when the contents look like an identifier
// (letters, digits, underscores; must start with a letter or
// underscore). Anything else — `{"key": "value"}`, `${VAR}`, `${1:-x}`,
// `${array[@]}`, brace expansion `{a,b,c}`, etc. — passes through
// silently the same way `substitute` does, so api-mode body templates
// containing literal JSON object braces don't get rejected at
// validation time.
func validateTemplate(cmd string, params map[string]ToolParam) error {
	for i := 0; i < len(cmd); i++ {
		if cmd[i] != '{' {
			continue
		}
		end := strings.IndexByte(cmd[i+1:], '}')
		if end < 0 {
			// Unclosed brace is more likely a literal in shell or JSON
			// than a forgotten placeholder. Don't error — let it
			// through. (The original strict behavior caused false
			// positives on api-mode body templates with nested
			// objects.)
			return nil
		}
		name, modifier := splitPlaceholder(cmd[i+1 : i+1+end])
		if modifier != "" && isPlaceholderIdent(name) && !urlEncodeModifiers[modifier] {
			return fmt.Errorf("unknown placeholder modifier %q in {%s:%s} — the only one is \"encoded\" (\"segment\" is a synonym)", modifier, name, modifier)
		}
		if !isPlaceholderIdent(name) {
			// Not identifier-shaped — treat as a literal brace
			// expression (JSON, shell parameter expansion, brace
			// expansion). Skip past the opening brace only; the
			// closing brace and contents stay in scope so a
			// genuine placeholder later in the same string still
			// gets validated.
			continue
		}
		// Reserved placeholders the dispatcher fills in (not user
		// params): {workspace_dir} resolves to the deployed sandbox
		// path. Skip param-list lookup for these.
		if name == "workspace_dir" {
			i = i + 1 + end
			continue
		}
		if _, ok := params[name]; !ok {
			return fmt.Errorf("placeholder {%s} not in params", name)
		}
		i = i + 1 + end
	}
	return nil
}
