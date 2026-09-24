package admin

// Global rules: the deployment's own obligations, which lead every agent's
// system prompt and are also the first rules every agent's guardrail check
// judges against (orchestrate's guardrailRules prepends them). Edited here,
// under Governance, because they are a governance decision; they used to sit
// in the Prompts app next to style rules, which are taste.

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	rules "github.com/cmcoffee/gohort/core/prompts"
	"github.com/cmcoffee/gohort/core/ui"
)

func (a *AdminApp) registerRulesRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/api/global-rules", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				Rules string `json:"rules"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			var list []rules.StyleRule
			for _, line := range splitRuleLines(req.Rules) {
				list = append(list, rules.StyleRule{Text: line})
			}
			rules.SetGlobalRules(list)
			Log("[admin] global rules saved by %s: %d", AuthCurrentUser(r), len(list))
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		var lines []string
		for _, rule := range rules.EnabledGlobalRules() {
			lines = append(lines, rule.Text)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"rules": strings.Join(lines, "\n")})
	})
}

// rulesSection is the Rules section at the top of the Governance tab.
func rulesSection() ui.Section {
	return ui.Section{
		Title:    "Rules",
		Subtitle: "Obligations every agent in this deployment is held to.",
		Detail: "These lead every agent's instructions, ahead of its persona and of any rules a user writes for their own agents, which can see them but not edit or remove them.\n\n" +
			"They are also enforced, not only stated: every agent's guardrail check judges replies and actions against these rules first, whether or not the agent has rules of its own.\n\n" +
			"Use this for constraints that are not a matter of preference. Style (tone, wording, formatting habits) belongs in the Prompts app's Style rules.",
		Body: ui.FormPanel{
			Source:      "api/global-rules",
			PostURL:     "api/global-rules",
			SubmitLabel: "Save rules",
			Fields: []ui.FormField{{
				Field:      "rules",
				Label:      "Rules",
				Type:       "rules",
				RowEditor:  true,
				Help:       "One rule per line, e.g. \"Do not perform any action that may potentially be deemed illegal.\" Nothing ships here; the list is yours.",
				SuggestURL: "/prompts/api/assist",
				AssistPrompt: "You write operator rules that bind an AI assistant's conduct: short imperative " +
					"lines, one obligation each. State the boundary and what to do when a request would cross " +
					"it. Be concrete about the behaviour, not aspirational about values. Do not use em-dashes.",
			}},
		},
	}
}

// splitRuleLines turns the editor's text into one rule per line, dropping
// blank lines and the list markers ("- ", "* ", "1.", "2)") people paste in.
func splitRuleLines(text string) []string {
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "- "), strings.HasPrefix(line, "* "), strings.HasPrefix(line, "+ "):
			line = strings.TrimSpace(line[2:])
		default:
			if i := strings.IndexAny(line, ".)"); i > 0 && i <= 3 && strings.Trim(line[:i], "0123456789") == "" {
				line = strings.TrimSpace(line[i+1:])
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
