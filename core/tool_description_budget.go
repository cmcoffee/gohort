package core

// Length budgets for LLM-authored tool text.
//
// Every character of a tool's description and param descriptions is
// re-sent on EVERY turn the tool is in the catalog, for the life of the
// tool. An authoring LLM has no feel for that cost — left unguided it
// writes a tutorial where a sentence would do, and the bill lands on
// every future conversation rather than on the turn that wrote it.
//
// The caps are deliberately generous: a tool that genuinely needs more
// than two sentences is rare, so anything that trips these is bloat, not
// a tool with unusually much to say. Rules that don't fit belong in the
// tool's own error messages (read only when they fire) or in the param
// descriptions (read only when that param is in play) — not in the
// catalog line that every turn pays for.
//
// Enforced at the AUTHORING ENTRY POINTS only (tool_def create/update,
// add_tool), never inside the shared create path: update round-trips a
// stored tool back through creation, and a legacy over-long description
// must not block an edit that isn't touching it.

import (
	"fmt"
	"sort"
)

const (
	// MaxToolDescription caps a tool's top-level description.
	MaxToolDescription = 500
	// MaxActionDescription caps one toolbox sub-action's description.
	// Tighter than the tool cap: a toolbox pays it once per action.
	MaxActionDescription = 250
	// MaxParamDescription caps one parameter's description.
	MaxParamDescription = 250
)

// CheckDescriptionBudget rejects an over-long description with an error
// that says what to cut. where names the offender for the LLM
// ("description", "actions[2] (list_issues) description", …).
func CheckDescriptionBudget(where, text string, max int) error {
	n := len([]rune(text))
	if n <= max {
		return nil
	}
	return fmt.Errorf("%s is %d characters — the cap is %d. This text is re-sent on every turn for the life of the tool, so it has to earn its length. "+
		"Rewrite it as one or two sentences saying WHAT the tool does and WHEN to reach for it. Cut: worked examples, restated param docs, "+
		"failure modes (put those in the error the tool returns, where they're read only when they happen), and anything the caller learns by simply calling it",
		where, n, max)
}

// CheckAuthoredToolText walks the description-bearing fields of a
// create/update arg map and enforces the budgets. Only fields actually
// PRESENT are checked.
func CheckAuthoredToolText(args map[string]any) error {
	if s, ok := args["description"].(string); ok {
		if err := CheckDescriptionBudget("description", s, MaxToolDescription); err != nil {
			return err
		}
	}
	if err := checkParamDescriptions("params", args["params"]); err != nil {
		return err
	}
	acts, _ := args["actions"].([]any)
	for i, raw := range acts {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		where := fmt.Sprintf("actions[%d]", i)
		if name, _ := m["name"].(string); name != "" {
			where = fmt.Sprintf("actions[%d] (%s)", i, name)
		}
		if s, ok := m["description"].(string); ok {
			if err := CheckDescriptionBudget(where+" description", s, MaxActionDescription); err != nil {
				return err
			}
		}
		if err := checkParamDescriptions(where+" params", m["params"]); err != nil {
			return err
		}
	}
	return nil
}

// checkParamDescriptions enforces the per-param budget over a
// {param: {type, description}} object. Params are walked in sorted order
// so a retry reports the same offender first — a map walk would name a
// different param each time and read like the cap is moving.
func checkParamDescriptions(where string, raw any) error {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		spec, ok := m[name].(map[string]any)
		if !ok {
			continue
		}
		s, ok := spec["description"].(string)
		if !ok {
			continue
		}
		if err := CheckDescriptionBudget(fmt.Sprintf("%s.%s description", where, name), s, MaxParamDescription); err != nil {
			return err
		}
	}
	return nil
}
