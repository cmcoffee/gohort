package admin

// The deployment's rules, both kinds, under Governance: they are the operator
// directing every agent from above the user.
//
//   - Always: obligations. They lead every agent's system prompt as binding
//     ("not yours to set aside") and are the first rules every agent's
//     guardrail check judges against (orchestrate's guardrailRules prepends
//     them), whether or not the agent has rules of its own.
//   - Style: how replies read. Stated as a Style instruction the model follows,
//     which never justifies refusing anything, and some are also enforced in
//     code when a reply is delivered.
//
// Two lists, not one, because the prompt weighs them differently: one list
// would have to give every line either the binding framing (and refuse over
// an em-dash) or the style one (and weigh a real rule against a determined
// request).

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
	sub.HandleFunc("/api/style-rules", func(w http.ResponseWriter, r *http.Request) {
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
			on, edited, custom := saveStyleRules(splitRuleLines(req.Rules))
			Log("[admin] style rules saved by %s: %d shipped live (%d edited), %d custom", AuthCurrentUser(r), on, edited, custom)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		var lines []string
		for _, rule := range rules.EnabledStyleRules() {
			lines = append(lines, rule.Text)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"rules": strings.Join(lines, "\n")})
	})
}

// saveStyleRules replaces the style list with these lines.
//
// The list is the WHOLE truth. A shipped rule is a line like any other, so
// deleting its line switches it off and re-adding it switches it back on. A
// shipped rule is live when one of the lines still says what it says, either
// its shipped wording or the override in force; matching the override too
// means an edited-then-saved rule stays that rule rather than turning into a
// duplicate of itself. Changing a shipped rule's wording makes it yours, and
// if it was one of the code-enforced ones its transform stops with it. Lines
// no shipped rule claims are the custom rules.
func saveStyleRules(lines []string) (on, edited, custom int) {
	used := make([]bool, len(lines))
	for _, b := range rules.BuiltinStyleRules() {
		effective := b.Text
		if o, ok := rules.PromptOverride(b.Key); ok {
			effective = o
		}
		match := -1
		for i, l := range lines {
			if !used[i] && (l == strings.TrimSpace(b.Text) || l == strings.TrimSpace(effective)) {
				match = i
				break
			}
		}
		if match < 0 {
			rules.SetPromptBlockEnabled(b.Key, false)
			continue
		}
		used[match] = true
		rules.SetPromptBlockEnabled(b.Key, true)
		on++
		// Back to the shipped wording clears the override rather than storing a
		// copy of it, so the rule keeps tracking any future change to the default.
		if lines[match] == strings.TrimSpace(b.Text) {
			rules.ClearPromptOverride(b.Key)
		} else {
			rules.SetPromptOverride(b.Key, lines[match])
			edited++
		}
	}
	var list []rules.StyleRule
	for i, l := range lines {
		if !used[i] {
			list = append(list, rules.StyleRule{Text: l})
		}
	}
	rules.SetCustomStyleRules(list)
	return on, edited, len(list)
}

// rulesSection is the Rules section at the top of the Governance tab: the
// Always list, then the Style list, each saved on its own.
func rulesSection() ui.Section {
	return ui.Section{
		Title:    "Rules",
		Subtitle: "How every agent in this deployment is directed, above anything its owner or user writes.",
		Detail: "Both lists lead every agent's instructions, ahead of its persona and of any rules a user writes for their own agents, which can see them but not edit or remove them.\n\n" +
			"Always rules are obligations. The model is told they are not its to set aside, and they are enforced as well as stated: every agent's guardrail check judges replies and actions against them first, and an owner who suspends their own guardrails still keeps these.\n\n" +
			"Style rules are how replies read: tone, wording, formatting habits. The model follows them by default, but a style rule never justifies refusing a request. Some shipped style rules are also applied in code when a reply is delivered.",
		Body: ui.Stack{Children: []ui.Component{alwaysRulesForm(), styleRulesForm()}},
	}
}

// alwaysRulesForm edits the Always list.
func alwaysRulesForm() ui.FormPanel {
	return ui.FormPanel{
		Source:      "api/global-rules",
		PostURL:     "api/global-rules",
		SubmitLabel: "Save Always rules",
		Fields: []ui.FormField{{
			Field:     "rules",
			Label:     "Always",
			Type:      "rules",
			RowEditor: true,
			Help:      "Obligations, checked on every reply. One per line, e.g. \"Do not perform any action that may potentially be deemed illegal.\" The picker beside each rule says what happens when a reply breaks it. Nothing ships here; the list is yours.",
			// What a breach does, per rule: the same three modes, and the
			// same markers, as an agent's own guardrails, because every
			// agent's guardrail check reads these lines with that parser.
			RowModes:   alwaysRuleModes,
			SuggestURL: "/prompts/api/assist",
			AssistPrompt: "You write operator rules that bind an AI assistant's conduct: short imperative " +
				"lines, one obligation each. State the boundary and what to do when a request would cross " +
				"it. Be concrete about the behaviour, not aspirational about values. Do not use em-dashes.",
		}},
	}
}

// alwaysRuleModes are what a breach of an Always rule does. The markers are the
// guardrail grammar orchestrate's parseGuardrailRule reads ("?" correctable,
// "~" contestable), and the labels match an agent's own guardrail editor.
var alwaysRuleModes = []ui.RowMode{
	{Label: "Block",
		Help: "A breach ends the turn and a separate model writes the refusal, with no attempt at a compliant version. The default."},
	{Marker: "?", Label: "Attempt recovery",
		Help: "A breach sends the reply back for one rewrite, and declines if it still breaks the rule. For a rule that shapes an answer rather than forbidding it."},
	{Marker: "~", Label: "Allow appeal",
		Help: "The agent may dispute a block once, by quoting the user's own words; the framework looks the quote up itself and re-checks. For a rule with a condition the check cannot see."},
}

// styleRulesForm edits the Style list, shipped rules included.
func styleRulesForm() ui.FormPanel {
	return ui.FormPanel{
		Source:      "api/style-rules",
		PostURL:     "api/style-rules",
		SubmitLabel: "Save Style rules",
		Fields: []ui.FormField{{
			Field: "rules",
			Label: "Style",
			Type:  "rules",
			// These are two-sentence rules; a one-line input shows a third of one.
			RowEditor: true,
			Help: "How replies read, one rule per line. Delete a line to drop the rule. Some shipped rules are also " +
				"enforced in code, so they hold even when the model ignores them; deleting such a line stops " +
				"its enforcement too.",
			SuggestURL: "/prompts/api/assist",
			AssistPrompt: "You write house-style rules for an AI assistant: short imperative lines, " +
				"one behaviour each. Name the tic concretely and say what to do instead. " +
				"Where a word or character has a legitimate use, carve it out so the rule does not " +
				"forbid that too. Do not use em-dashes.",
		}},
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
