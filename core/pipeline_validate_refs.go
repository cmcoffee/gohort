package core

import (
	"sort"
	"strconv"
	"strings"
)

// validateToolStage checks a kind="tool" stage. The tool NAME can't be
// verified here — availability is per-user and per-agent, resolved at
// run time — but everything about the call's shape can be.
func validateToolStage(s PipelineStage, done map[string]map[string]PipelineFieldType) error {
	if strings.TrimSpace(s.Tool) == "" {
		return Error("stage " + s.Name + " is kind=tool but names no tool")
	}
	if strings.TrimSpace(s.Prompt) != "" {
		return Error("stage " + s.Name + ": a tool stage takes args, not a prompt — there is no model to prompt. Put the values in args.")
	}
	if strings.TrimSpace(s.Agent) != "" {
		return Error("stage " + s.Name + ": agent does not apply to a tool stage — use kind=agent to dispatch, or kind=tool to call a tool directly")
	}
	if StageThinkMode(s) != "" {
		return Error("stage " + s.Name + ": think does not apply to a tool stage — no model runs")
	}
	// Every {stage:...} in an argument is a real reference and gets the
	// same forward/unknown check a prompt does. Arg names are sorted so
	// the FIRST error reported is stable across runs rather than
	// whichever key Go's map iteration happened to reach first.
	names := make([]string, 0, len(s.Args))
	for k := range s.Args {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		for _, ref := range stageRefs(s.Args[k]) {
			if err := checkStageRef(s.Name, "args."+k, ref, done); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateStageModel checks the per-stage tier: a closed set, and only
// on the kinds that actually make a worker call. Rejecting it elsewhere
// rather than ignoring it is the point — a tier silently dropped on an
// agent stage would read as "I asked for lead and got worker", which is
// indistinguishable from a routing bug.
func validateStageModel(s PipelineStage) error {
	tier := strings.ToLower(strings.TrimSpace(s.Model))
	if tier == "" {
		return nil
	}
	if tier != "worker" && tier != "lead" {
		return Error("stage " + s.Name + ": model must be \"worker\" or \"lead\", got " + strconv.Quote(s.Model))
	}
	switch s.Kind {
	case StageWorker, StageSynthesize, "":
		return nil
	case StageFanout:
		if strings.TrimSpace(s.Agent) != "" {
			return Error("stage " + s.Name + ": model does not apply to a fanout that dispatches to an agent — the agent's own configuration decides its tier")
		}
		return nil
	case StageAgent:
		return Error("stage " + s.Name + ": model does not apply to an agent stage — the dispatched agent's own configuration decides its tier")
	case StageBranch:
		return Error("stage " + s.Name + ": a branch makes no LLM call, so model does not apply")
	case StageTool:
		return Error("stage " + s.Name + ": a tool stage makes no LLM call, so model does not apply")
	case StageLoop:
		return Error("stage " + s.Name + ": set model on the loop's BODY stages, not the loop itself")
	}
	return nil
}

// validateBranchStage checks a kind="branch" stage. stages/at locate it
// in its own list, which is what makes the forward-only jump checkable
// at save time rather than at run time.
func validateBranchStage(s PipelineStage, stages []PipelineStage, at int, done map[string]map[string]PipelineFieldType, inLoop bool) error {
	if len(s.Output) > 0 || len(s.Body) > 0 {
		return Error("stage " + s.Name + ": a branch makes no LLM call, so it has no output or body — it only reads an earlier stage's field")
	}
	ref := strings.TrimSpace(s.When)
	if ref == "" {
		return Error("stage " + s.Name + " is kind=branch but has no when (a \"NAME.field\" bool reference to an earlier stage)")
	}
	if looksLikeCondition(ref) {
		return Error("stage " + s.Name + ": when is written as a condition (" + strconv.Quote(ref) + "). " + boolRefShape)
	}
	name, field := SplitStageRef(ref)
	if field == "" {
		return Error("stage " + s.Name + ": when must name a BOOL FIELD (e.g. \"frame.rejected\"), not just a stage. " + boolRefShape)
	}
	if err := checkStageRef(s.Name, "when", ref, done); err != nil {
		return err
	}
	if t := done[name][field]; t != FieldBool {
		return Error("stage " + s.Name + ": when references " + ref + ", which is declared " + string(t) + ", not bool")
	}
	target := strings.TrimSpace(s.SkipTo)
	if target == "" {
		// Ending the pipeline from inside a loop body is ambiguous — stop
		// the pass, the loop, or the whole run? The loop already has a
		// purpose-built early exit, so point the author at it rather than
		// inventing an answer.
		if inLoop {
			return Error("stage " + s.Name + ": a branch inside a loop body cannot end the pipeline — use the loop's until to stop early, or set skip_to to jump within the body")
		}
		return nil
	}
	for i, other := range stages {
		if other.Name != target {
			continue
		}
		if i <= at {
			return Error("stage " + s.Name + ": skip_to " + target + " points backwards — a branch only jumps FORWARD. Repeating work is what kind=loop is for, where count bounds it.")
		}
		return nil
	}
	return Error("stage " + s.Name + ": skip_to names " + target + ", which is not a later stage in the same list")
}

// doubleBraceProblem catches {{handlebars}} templating.
//
// This vocabulary uses SINGLE braces. A double brace resolves to nothing, is
// left in the prompt verbatim, and the model is handed the braces — a silent
// failure with no error anywhere, which is worse than a refusal. Seen across an
// entire authoring session: every prompt written {{input}}, every one of them
// dead, and the pipeline validated clean.
func doubleBraceProblem(stage, where, text string) error {
	if !strings.Contains(text, "{{") {
		return nil
	}
	return Error("stage " + stage + " " + where + " uses {{double braces}} — this vocabulary takes SINGLE ones: {input}, {prev}, {stage:NAME}, {stage:NAME.field}, {item}, {iteration}. A double brace resolves to nothing and reaches the model as literal braces, so it fails silently rather than loudly.")
}

// barePrefixProblem catches a reference written with the prompt-text PREFIX in
// a slot that takes a bare one.
//
// when:"stage:critic.polished" is not a forward reference and not a typo — the
// stage it names sits directly above it. But SplitStageRef reads the whole
// "stage:critic" as the stage name, nothing matches, and the generic answer was
// "has not run at that point … move it earlier", which is false and unfollowable:
// the author dutifully reordered stages that were already in order, six times.
//
// Covers every bare-reference site at once — when, until, fan_over, and a tool
// stage's args — because they share this checker.
func barePrefixProblem(stage, where, ref string) error {
	trimmed := strings.TrimSpace(ref)
	if !strings.HasPrefix(strings.ToLower(trimmed), "stage:") {
		return nil
	}
	bare := strings.TrimSpace(trimmed[len("stage:"):])
	return Error("stage " + stage + " " + where + " is written with the stage: PREFIX (" + strconv.Quote(trimmed) +
		"). Here a reference is BARE — " + strconv.Quote(bare) + " — and the stage:NAME form belongs only inside prompt TEXT.")
}

// builtinTemplateTokens are the template names the interpreter resolves itself.
// An author who writes {stage:prev} has the right idea through the wrong door:
// prev is real, it is just not a stage. (reservedTemplateVars in the
// interpreter is the same set plus "stage", guarding a different seam — what a
// submit form may not redefine.)
var builtinTemplateTokens = map[string]bool{
	"input": true, "prev": true, "item": true, "iteration": true, "iterations": true,
}

// checkStageRef verifies one "NAME" or "NAME.field" reference resolves
// to an already-validated stage (and, for the field form, to a field
// that stage actually declares). where names the site of the reference
// so the error tells the author which part of the stage to fix.
func checkStageRef(stage, where, ref string, done map[string]map[string]PipelineFieldType) error {
	// A reference written as a PROMPT template — "{stage:decompose.items}" or
	// "{decompose.items}" — is the single most common way this fails, because
	// prompts really do use that form and the two sites look alike. Say which
	// form belongs here instead of reporting the braces as part of a stage name
	// nobody named that.
	if trimmed := strings.TrimSpace(ref); strings.ContainsAny(trimmed, "{}") {
		bare := strings.TrimSpace(strings.Trim(trimmed, "{}"))
		bare = strings.TrimPrefix(bare, "stage:")
		return Error("stage " + stage + " " + where + " is written as a prompt template (" + trimmed +
			"). It takes a BARE reference — " + strconv.Quote(bare) + " — not {…}. The {stage:NAME.field} form is for PROMPT text only.")
	}
	if p := barePrefixProblem(stage, where, ref); p != nil {
		return p
	}
	name, field := SplitStageRef(ref)
	// {stage:prev} and friends: the author reached for a real token through the
	// wrong door. Reporting "no stage named prev" is true and useless — prev
	// exists, it is just spelled {prev}.
	if builtinTemplateTokens[strings.ToLower(name)] && field == "" {
		return Error("stage " + stage + " " + where + " references " + strconv.Quote(name) +
			", which is a BUILT-IN, not a stage. Write {" + strings.ToLower(name) + "} — the {stage:NAME} form is only for a stage you named yourself.")
	}
	fields, ok := done[name]
	if !ok {
		// "unknown or later stage: X" named two possibilities and neither fix.
		// They are opposite fixes — rename, or reorder — and the reader cannot
		// choose between them without knowing that position in the array IS
		// execution order. So state the rule, not just the symptom.
		return Error("stage " + stage + " " + where + " references " + strconv.Quote(name) +
			", which has not run at that point. Either no stage is named that, or it is listed AFTER this one — " +
			"stages run in the order given and a reference only ever reaches BACKWARD, so a later stage has to be moved earlier in the array.")
	}
	if field == "" {
		return nil
	}
	if _, ok := fields[field]; !ok {
		if len(fields) == 0 {
			// The loop case, and the single most repeated failure in authoring:
			// reaching into a loop for the body stage that produced the result.
			// Body names hold a different value every pass, so they are not
			// addressable from outside — the loop answers under its OWN name.
			return Error("stage " + stage + " " + where + " references " + ref + ", but " + name +
				" declares no output fields. If " + name + " is a LOOP, read its result as {stage:" + name +
				"} — a loop's body stage names are not visible outside it (they hold a different value each pass), and collect:\"all\" makes that output every pass rather than the last.")
		}
		return Error("stage " + stage + " " + where + " references " + ref + ", but stage " + name + " declares no output field " + field)
	}
	return nil
}

// SplitStageRef splits a stage reference into its stage name and
// optional field. "plan" → ("plan", ""); "plan.queries" → ("plan",
// "queries"). Only the first dot separates — stage names can't contain
// one (Validate enforces that), so anything after it is the field.
func SplitStageRef(ref string) (name, field string) {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "."); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// stageRefs extracts the inner text of every {stage:...} occurrence in a
// template — "plan" from {stage:plan}, "plan.queries" from
// {stage:plan.queries}. Unterminated occurrences are ignored (they can't
// resolve at run time either, and they're left in the prompt verbatim).
func stageRefs(tmpl string) []string {
	const open = "{stage:"
	var out []string
	rest := tmpl
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return out
		}
		rest = rest[i+len(open):]
		j := strings.Index(rest, "}")
		if j < 0 {
			return out
		}
		out = append(out, strings.TrimSpace(rest[:j]))
		rest = rest[j+1:]
	}
}

// validateOutputFields checks one stage's declared output shape and
// returns its field-name → type map for later reference checking.
// nested guards the one-level depth limit.
func validateOutputFields(stage string, fields []PipelineField, nested bool) (map[string]PipelineFieldType, error) {
	out := make(map[string]PipelineFieldType, len(fields))
	for i, f := range fields {
		if f.Name == "" {
			return nil, Error("stage " + stage + ": output field " + strconv.Itoa(i+1) + " has no name")
		}
		if !isFieldName(f.Name) {
			return nil, Error("stage " + stage + ": output field " + f.Name + " must be lowercase letters, digits, or underscore")
		}
		if _, dup := out[f.Name]; dup {
			return nil, Error("stage " + stage + ": duplicate output field " + f.Name)
		}
		switch f.resolved() {
		case FieldString, FieldNumber, FieldBool, FieldList, FieldObject:
		default:
			// Name the valid set. Without it the author has to guess which
			// spelling of "a bunch of things" this vocabulary uses — and the
			// obvious guesses ("array", "string[]") each cost a round-trip,
			// after which the usual recovery is to abandon the declared output
			// entirely and hand-parse a JSON string, losing the field
			// addressing that fan_over and until depend on.
			return nil, Error("stage " + stage + ": output field " + f.Name + " has unknown type " + strconv.Quote(string(f.Type)) +
				" — use one of: string, number, bool, list, object. A list of items (sub-questions, findings, links) is type list, which is also what fan_over reads.")
		}
		if len(f.Fields) > 0 {
			if nested {
				return nil, Error("stage " + stage + ": output field " + f.Name + " nests too deep — one level only (deeper structure still renders as JSON, it just isn't addressable)")
			}
			if k := f.resolved(); k != FieldList && k != FieldObject {
				return nil, Error("stage " + stage + ": output field " + f.Name + " is type " + string(k) + " and cannot declare nested fields")
			}
			if _, err := validateOutputFields(stage, f.Fields, true); err != nil {
				return nil, err
			}
		}
		out[f.Name] = f.resolved()
	}
	return out, nil
}

// isFieldName reports whether s is a safe output-field handle:
// lowercase letters, digits, and underscore. Keeps {stage:X.field}
// unambiguous and the JSON keys predictable. Hand-rolled rather than a
// regexp — it's three comparisons and avoids a package-level compile.
func isFieldName(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return s != ""
}
