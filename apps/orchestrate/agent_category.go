package orchestrate

// Agent categories: an owner's label for a group of related top-level agents.
// Today the picker lists an agent under its category, and an agent with none
// under its kind (Conversation or Specialized). The agents stay top-level and
// reachable; a category changes where they are listed, not what they are.
//
// Every reader goes through categoryKey and agentCategories, so the picker and
// anything that later collapses a delegation catalog by category ("Medical:
// Nurse, Pharmacist") agree on which agents share one, however each was typed.

import (
	"sort"
	"strings"
)

// categoryKey is a category's identity: case and runs of spaces ignored, so
// "Medical", "medical" and "Medical " are one category.
func categoryKey(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// agentCategories is the category set of an owner's agents: each key mapped to
// the spelling shown for it, the one most of its agents use (ties to the first
// alphabetically, so it does not change with list order). Only top-level
// agents count: a sub-agent is listed under its parent and has none.
func agentCategories(agents []AgentRecord) map[string]string {
	counts := map[string]map[string]int{}
	for _, a := range agents {
		if strings.TrimSpace(a.OwnedBy) != "" {
			continue
		}
		label := strings.Join(strings.Fields(a.Category), " ")
		if label == "" {
			continue
		}
		k := categoryKey(label)
		if counts[k] == nil {
			counts[k] = map[string]int{}
		}
		counts[k][label]++
	}
	out := make(map[string]string, len(counts))
	for k, spellings := range counts {
		best, n := "", 0
		for label, c := range spellings {
			if c > n || (c == n && label < best) {
				best, n = label, c
			}
		}
		out[k] = best
	}
	return out
}

// categoryNames is the shown spellings, sorted: the suggestions a category
// field offers, so a new agent joins an existing group rather than starting a
// second one spelled differently.
func categoryNames(agents []AgentRecord) []string {
	cats := agentCategories(agents)
	out := make([]string, 0, len(cats))
	for _, label := range cats {
		out = append(out, label)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}
