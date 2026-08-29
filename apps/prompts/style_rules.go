package prompts

// The style-rule editor: its page and the endpoint behind it.
//
// Style rules are a LIST of one-line instructions, not a block of prose, so the
// editor is the framework's "rules" field (one input per row, add and remove,
// saved as a newline-joined string) rather than the workbench's big text pane.
// That is the same control the persona editor uses, so this screen behaves like
// every other rule list in gohort instead of being a bespoke one that almost
// matches. The assist workbench comes with the field type, which is where
// "consult the AI" lives.
//
// The save is a REPLACE, not a diff. A list promises that a line you deleted is
// gone, and honouring that needs no identity matching between what is on screen
// and what is on disk.

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	styles "github.com/cmcoffee/gohort/core/prompts"
	"github.com/cmcoffee/gohort/core/ui"
)

// handleStyleRules serves the rule list (GET) and replaces it (POST).
//
// One rule per line, the same shape as the document rules panel, because these
// are the same kind of thing: short instructions a person adds and drops as
// they notice tics. Add, remove and edit are all just editing the text.
//
// The list is the WHOLE truth. A shipped rule is a line like any other, so
// deleting its line switches it off and re-adding it switches it back on. That
// keeps one mental model instead of two, at the cost noted on the client: a
// shipped rule whose wording you change becomes your rule, and if it was one of
// the code-enforced ones its transform stops with it. Reconciled by text
// because text is all a line has; the shipped wording is never lost, since
// nothing here deletes a builtin from the registry.
func (T *PromptsApp) handleStyleRules(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if r.Method == http.MethodPost {
		T.saveStyleRules(w, r)
		return
	}

	var lines []string
	for _, rule := range styles.EnabledStyleRules() {
		lines = append(lines, rule.Text)
	}
	writeJSON(w, map[string]any{"rules": strings.Join(lines, "\n")})
}

func (T *PromptsApp) saveStyleRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rules string `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	lines := splitRuleLines(req.Rules)

	// A shipped rule is live when one of the lines still says what it says,
	// either its shipped wording or the override currently in force. Matching on
	// the override too means an edited-then-saved rule stays that rule rather
	// than turning into a duplicate of itself on the next save.
	used := make([]bool, len(lines))
	on, edited := 0, 0
	for _, b := range styles.BuiltinStyleRules() {
		effective := b.Text
		if o, ok := styles.PromptOverride(b.Key); ok {
			effective = o
		}
		match := -1
		for i, l := range lines {
			if used[i] {
				continue
			}
			if l == strings.TrimSpace(b.Text) || l == strings.TrimSpace(effective) {
				match = i
				break
			}
		}
		if match < 0 {
			styles.SetPromptBlockEnabled(b.Key, false)
			continue
		}
		used[match] = true
		styles.SetPromptBlockEnabled(b.Key, true)
		on++
		// Back to the shipped wording clears the override rather than storing a
		// copy of it, so the rule keeps tracking any future change to the default.
		if lines[match] == strings.TrimSpace(b.Text) {
			styles.ClearPromptOverride(b.Key)
		} else {
			styles.SetPromptOverride(b.Key, lines[match])
			edited++
		}
	}

	var custom []styles.StyleRule
	for i, l := range lines {
		if !used[i] {
			custom = append(custom, styles.StyleRule{Text: l})
		}
	}
	styles.SetCustomStyleRules(custom)

	Log("[prompts] style rules saved: %d shipped live (%d edited), %d custom",
		on, edited, len(custom))
	writeJSON(w, map[string]any{"ok": true})
}

// splitRuleLines pulls one rule per line.
//
// Bullet and number prefixes are stripped even though the rules field already
// does that on its side: this endpoint is reachable by other means, and text
// pasted from a document arrives with its markers attached. A rule that reached
// the prompt reading "- Do NOT use em-dashes" would be teaching the model the
// punctuation of its own configuration.
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
			if i := strings.IndexAny(line, ".)"); i > 0 && i <= 3 && isAllDigits(line[:i]) {
				line = strings.TrimSpace(line[i+1:])
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// styleRulesForm is the rule editor, as a component spec the page mounts in a
// modal.
//
// Not a hand-built dialog and not a separate page. It is a FormPanel over one
// "rules" field, the framework's line-separated list editor (one input per row,
// add and remove, saved as a newline-joined string) — the same control the
// persona editor uses, so this behaves like every other rule list in gohort.
// The assist workbench comes with the field type, which is where "consult the
// AI" lives.
//
// A modal rather than a page because navigating away took the whole Prompts
// list with it: editing one thing in a workbench should not cost you the
// workbench.
func styleRulesForm() ui.FormPanel {
	return ui.FormPanel{
		Source:  "/prompts/api/style",
		PostURL: "/prompts/api/style",
		Fields: []ui.FormField{{
			Field: "rules",
			Label: "Rules",
			Type:  "rules",
			// These are two-sentence rules; a one-line input shows a third of one.
			RowEditor: true,
			Help: "One rule per line, appended to every reply as a single Style instruction. " +
				"Delete a line to drop the rule. Some of these are also enforced in code, " +
				"so they hold even when the model ignores them; deleting such a line stops " +
				"its enforcement too.",
			SuggestURL: "/prompts/api/assist",
			AssistPrompt: "You write house-style rules for an AI assistant: short imperative lines, " +
				"one behaviour each. Name the tic concretely and say what to do instead. " +
				"Where a word or character has a legitimate use, carve it out so the rule does not " +
				"forbid that too. Do not use em-dashes.",
		}},
	}
}

// handleStyleForm serves that spec, so the client can mount it without the page
// having to inline a blob of JSON into its head.
func (T *PromptsApp) handleStyleForm(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	writeJSON(w, styleRulesForm())
}

// --- global rules ------------------------------------------------------------
//
// Same editor, different list. Style rules are taste; these are obligations, so
// the copy is firmer and there is nothing shipped to switch off — a deployment
// writes its own.

func (T *PromptsApp) handleGlobalRules(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
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
		var rules []styles.StyleRule
		for _, line := range splitRuleLines(req.Rules) {
			rules = append(rules, styles.StyleRule{Text: line})
		}
		styles.SetGlobalRules(rules)
		Log("[prompts] global rules saved: %d", len(rules))
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	var lines []string
	for _, rule := range styles.EnabledGlobalRules() {
		lines = append(lines, rule.Text)
	}
	writeJSON(w, map[string]any{"rules": strings.Join(lines, "\n")})
}

// globalRulesForm is the editor spec, mounted in a modal like the style one.
func globalRulesForm() ui.FormPanel {
	return ui.FormPanel{
		Source:  "/prompts/api/global",
		PostURL: "/prompts/api/global",
		Fields: []ui.FormField{{
			Field:     "rules",
			Label:     "Rules",
			Type:      "rules",
			RowEditor: true,
			Help: "One rule per line. These apply to EVERY agent and come before any rules a user adds " +
				"to their own workspace, which see them but cannot edit or remove them. Use this for the " +
				"constraints that are not a matter of preference, e.g. \"Do not perform any action that " +
				"may potentially be deemed illegal.\" Nothing ships here; the list is yours.",
			SuggestURL: "/prompts/api/assist",
			AssistPrompt: "You write operator rules that bind an AI assistant's conduct: short imperative " +
				"lines, one obligation each. State the boundary and what to do when a request would cross " +
				"it. Be concrete about the behaviour, not aspirational about values. Do not use em-dashes.",
		}},
	}
}

func (T *PromptsApp) handleGlobalForm(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	writeJSON(w, globalRulesForm())
}
