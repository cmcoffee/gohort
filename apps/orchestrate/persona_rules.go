package orchestrate

// A persona's Rules section, moved into the agent's Rules.
//
// The persona outline used to offer a Rules slot, beside the agent's own Rules
// (Configure, Rules). They read as the same thing and were not: the agent's
// Rules come first in the prompt for both the model that talks and the one
// that works, and win every conflict; a Rules section in the persona reached
// only the first, mid-prompt, with no weight of its own. The slot is gone. A
// persona written with one keeps it until its owner moves it, which is one
// button on the agent's page, because rewriting somebody's persona is theirs
// to decide.

import (
	"fmt"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

var (
	personaRulesHeadingRe = regexp.MustCompile(`(?i)^(#{1,6})\s*rules\s*:?\s*$`)
	personaHeadingRe      = regexp.MustCompile(`^(#{1,6})\s`)
	personaListMarkerRe   = regexp.MustCompile(`^(?:[-*+•]|\d{1,3}[.)])\s+`)
	blankRunRe            = regexp.MustCompile(`\n{3,}`)
)

// personaRules splits a persona at its Rules section: the section's text, as
// rules (a top-level bullet or number taken off each line, so a nested list
// stays under its heading), and the persona without the section. ok is false
// when there is no Rules section, or only an empty one.
func personaRules(prompt string) (rules, rest string, ok bool) {
	lines := strings.Split(prompt, "\n")
	start, level := -1, 0
	for i, l := range lines {
		if m := personaRulesHeadingRe.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			start, level = i, len(m[1])
			break
		}
	}
	if start < 0 {
		return "", prompt, false
	}
	// The section runs to the next heading at its own level or above; a
	// deeper one is part of it.
	end := len(lines)
	for j := start + 1; j < len(lines); j++ {
		if m := personaHeadingRe.FindStringSubmatch(strings.TrimSpace(lines[j])); m != nil && len(m[1]) <= level {
			end = j
			break
		}
	}
	var out []string
	for _, l := range lines[start+1 : end] {
		// Anchored at the line's start, so an indented (nested) item keeps
		// its mark and stays under its heading.
		l = personaListMarkerRe.ReplaceAllString(l, "")
		out = append(out, strings.TrimRight(l, " \t"))
	}
	rules = strings.TrimSpace(strings.Join(out, "\n"))
	if rules == "" {
		return "", prompt, false
	}
	kept := append(append([]string{}, lines[:start]...), lines[end:]...)
	rest = strings.TrimSpace(blankRunRe.ReplaceAllString(strings.Join(kept, "\n"), "\n\n"))
	return rules, rest, true
}

// movePersonaRules moves a's persona Rules section to the end of its Rules,
// a blank line apart. The persona as it was is kept in the agent's history.
func movePersonaRules(db Database, a AgentRecord) (AgentRecord, int, error) {
	moved, rest, ok := personaRules(a.OrchestratorPrompt)
	if !ok {
		return a, 0, fmt.Errorf("this agent's persona has no Rules section to move")
	}
	merged := moved
	if r := strings.TrimSpace(a.Rules); r != "" {
		merged = r + "\n\n" + moved
	}
	if err := rulesProblem(merged); err != nil {
		return a, 0, fmt.Errorf("the persona's Rules section cannot be moved as it stands: %w", err)
	}
	a.Rules, a.OrchestratorPrompt = merged, rest
	saved, err := saveAgentAs(db, a, "moved the persona's Rules section into Rules")
	return saved, len(ruleItems(moved)), err
}

// agentPersonaRulesMove is POST /api/agents/{id}/persona-rules, the owner's.
func (T *OrchestrateApp) agentPersonaRulesMove(c *jsonapi.Call, _ struct{}) (any, error) {
	agent, err := T.ownedAgent(c)
	if err != nil {
		return nil, err
	}
	_, n, err := movePersonaRules(c.UDB, agent)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"message": fmt.Sprintf("Moved %d rule%s into Rules.", n, plural(n))}, nil
}
