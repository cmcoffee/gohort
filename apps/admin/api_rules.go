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
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	rules "github.com/cmcoffee/gohort/core/prompts"
	"github.com/cmcoffee/gohort/core/ui"
)

func (a *AdminApp) registerRulesRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/api/rules-assist", a.handleRulesAssist)
	sub.HandleFunc("/api/global-rules", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleGlobalRules, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleGlobalRulesSave, a.gate()),
	}.Serve)
	sub.HandleFunc("/api/style-rules", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleStyleRules, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleStyleRulesSave, a.gate()),
	}.Serve)
}

// handleGlobalRules is the Always rules, one per line, and how they are
// checked.
func (a *AdminApp) handleGlobalRules(c *jsonapi.Call) (any, error) {
	var lines []string
	exceptions := []ruleException{}
	for _, rule := range rules.EnabledGlobalRules() {
		lines = append(lines, rule.Text)
		if rule.ExceptOwner {
			exceptions = append(exceptions, ruleException{Rule: rule.Text, Who: exceptOwnerRow})
		}
		for _, e := range rule.Except {
			exceptions = append(exceptions, ruleException{Rule: rule.Text, Condition: e})
		}
	}
	return map[string]any{"rules": strings.Join(lines, "\n"), "exceptions": exceptions,
		"depth": rules.GlobalRulesDepth(), "if_unchecked": uncheckedWord(rules.GlobalRulesFailOpen()),
		"when_checked": whenCheckedWord(rules.GlobalRulesStreaming())}, nil
}

// handleGlobalRulesSave replaces the Always rules, and how they are checked
// when the body says.
func (a *AdminApp) handleGlobalRulesSave(c *jsonapi.Call, req struct {
	Rules string `json:"rules"`
	// Exceptions, each naming its rule by its text. Nil (not sent) keeps
	// the ones the rules have.
	Exceptions *[]ruleException `json:"exceptions"`
	// Pointers: a caller that sends only the rules leaves how they
	// are checked alone rather than resetting it.
	Depth     *string `json:"depth"`
	Unchecked *string `json:"if_unchecked"`
	When      *string `json:"when_checked"`
}) (any, error) {
	// A rule's exceptions travel with its text: the ones sent, or, when none
	// were sent, the ones it had. A rule whose wording changed starts with
	// none, and an exception naming no rule there is dropped, said in the log.
	kept := map[string]rules.StyleRule{}
	for _, r := range rules.GlobalRules() {
		kept[r.Text] = r
	}
	var sent map[string][]string
	var owner map[string]bool
	if req.Exceptions != nil {
		sent, owner = map[string][]string{}, map[string]bool{}
		for _, e := range *req.Exceptions {
			rule := strings.TrimSpace(e.Rule)
			if strings.TrimSpace(e.Who) == exceptOwnerRow {
				owner[rule] = true
				// A row says one thing; a condition typed on an owner row
				// before the choice changed is still kept as a condition.
			}
			if cond := strings.TrimSpace(e.Condition); cond != "" {
				sent[rule] = append(sent[rule], cond)
			}
		}
	}
	var list []rules.StyleRule
	for _, line := range splitRuleLines(req.Rules) {
		r := rules.StyleRule{Text: line, Except: kept[line].Except, ExceptOwner: kept[line].ExceptOwner}
		if sent != nil {
			r.Except, r.ExceptOwner = sent[line], owner[line]
			delete(sent, line)
			delete(owner, line)
		}
		list = append(list, r)
	}
	for rule := range owner {
		Log("[admin] global rules: the owner exception for %q dropped, no Always rule has that wording", rule)
	}
	for rule, conds := range sent {
		Log("[admin] global rules: %d exception(s) for %q dropped, no Always rule has that wording", len(conds), rule)
	}
	rules.SetGlobalRules(list)
	if req.Depth != nil {
		rules.SetGlobalRulesDepth(*req.Depth)
	}
	if req.Unchecked != nil {
		rules.SetGlobalRulesFailOpen(*req.Unchecked == "allow")
	}
	if req.When != nil {
		rules.SetGlobalRulesStreaming(*req.When == "stream")
	}
	Log("[admin] global rules saved by %s: %d, checked %s, if unchecked %s", c.User, len(list),
		rules.GlobalRulesDepth(), uncheckedWord(rules.GlobalRulesFailOpen()))
	return map[string]any{"ok": true}, nil
}

// ruleException is one exception row: the Always rule it belongs to, by its
// text, and the condition under which that rule does not apply.
type ruleException struct {
	Rule string `json:"rule"`
	// Who is exceptOwnerRow for the owner exception, settled by gohort from
	// who is asking; empty for a condition the check judges.
	Who       string `json:"who,omitempty"`
	Condition string `json:"condition"`
}

// exceptOwnerRow is an exception row's Who for the owner exception.
const exceptOwnerRow = "owner"

// handleStyleRules is the Style rules in force, one per line.
func (a *AdminApp) handleStyleRules(c *jsonapi.Call) (any, error) {
	var lines []string
	for _, rule := range rules.EnabledStyleRules() {
		lines = append(lines, rule.Text)
	}
	return map[string]any{"rules": strings.Join(lines, "\n")}, nil
}

// handleStyleRulesSave replaces the Style list with the lines posted.
func (a *AdminApp) handleStyleRulesSave(c *jsonapi.Call, req struct {
	Rules string `json:"rules"`
}) (any, error) {
	on, edited, custom := saveStyleRules(splitRuleLines(req.Rules))
	Log("[admin] style rules saved by %s: %d shipped live (%d edited), %d custom", c.User, on, edited, custom)
	return map[string]any{"ok": true}, nil
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

// rulesSection is the Rules section at the top of the Oversight tab: the
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

// alwaysRuleOptions are the Always rules, for an exception to name its rule.
func alwaysRuleOptions() []ui.SelectOption {
	var out []ui.SelectOption
	for _, r := range rules.EnabledGlobalRules() {
		out = append(out, ui.SelectOption{Value: r.Text, Label: r.Text})
	}
	return out
}

// alwaysRulesForm edits the Always list. No Save button: like the rest of
// the admin settings it saves as it changes (each line as it is left, each
// select as it is picked), posting the whole record so no field is blanked.
func alwaysRulesForm() ui.FormPanel {
	return ui.FormPanel{
		Assist:  true,
		Source:  "api/global-rules",
		PostURL: "api/global-rules",
		Fields: []ui.FormField{{
			Field:     "rules",
			Label:     "Always",
			Type:      "rules",
			RowEditor: true,
			Help:      "What no agent here may do, whatever it is asked: one rule per line, written as a plain instruction. Each is checked on every request, action and reply, and the picker beside it says what happens when one is broken. Nothing ships here; the list is yours.",
			// What a breach does, per rule: the same three modes, and the
			// same markers, as an agent's own guardrails, because every
			// agent's guardrail check reads these lines with that parser.
			RowModes:     alwaysRuleModes,
			SuggestURL:   "api/rules-assist",
			AssistPrompt: alwaysAssistPrompt,
		}, {
			Field: "exceptions", Type: "rows", Label: "Exceptions", AddLabel: "+ Add exception", ShowWhen: "rules",
			Columns: []ui.FormField{
				{Field: "rule", Label: "Rule", Type: "select", Options: alwaysRuleOptions()},
				{Field: "who", Label: "Kind", Type: "select", Options: []ui.SelectOption{
					{Value: "", Label: "A condition, judged by the check"},
					{Value: exceptOwnerRow, Label: "The agent's owner is asking (decided by gohort)"}}},
				{Field: "condition", Label: "Except when", Type: "text",
					Placeholder: "when this rule should not apply…"},
			},
			Help: "When a rule does not apply: pick the rule, then either the owner exception or a condition. A rule can have several, and any one that holds excuses it.",
			Detail: "The owner exception is settled by gohort itself, from the verified account asking, before the check runs: the rule is not checked at all when the agent's owner is the one asking, and no wording can claim it. " +
				"A condition is read by the check under its rule, which decides whether it holds, from who is actually asking (the framework establishes that, not the message): \"the owner\" is whoever owns the agent being checked. " +
				"Keep them few and narrow: each is weighed on every reply its rule judges. Exceptions belong to the rule's wording, so a rule reworded starts with none. " +
				"Only these loosen the deployment's rules: an agent's own exceptions never reach them. A rule added here appears in the picker once the page is reloaded.",
		}, {
			// The three settings below are read only when a global rule is
			// judged, and the list ships empty, so they wait for a rule.
			Field: "depth", Type: "select", Label: "How carefully they are checked", ShowWhen: "rules",
			Options: []ui.SelectOption{
				{Value: rules.RuleDepthQuick, Label: "Quick"},
				{Value: rules.RuleDepthModerate, Label: "Moderate"},
				{Value: rules.RuleDepthThorough, Label: "Thorough"},
			},
			Help: "Every agent's replies and actions are checked against these at this depth, whatever the agent's own setting.",
			Detail: "Quick answers straight off: fastest, and where both missed breaches and false alarms come from. " +
				"Moderate reasons briefly before deciding, a few seconds per check. Thorough reasons at length, for rules where a wrong call is expensive. " +
				"A reply is held until its check clears, so this is time added to every checked reply. " +
				"An agent whose own rules are checked more carefully than this uses its own depth when both are judged together.",
		}, {
			Field: "if_unchecked", Type: "select", Label: "If a check cannot reach a verdict", ShowWhen: "rules",
			Options: []ui.SelectOption{
				{Value: "block", Label: "Block the reply or action"},
				{Value: "allow", Label: "Let it through, and record that it went unchecked"},
			},
			Help: "The checker is a model call and can fail. Blocking is the safe side for rules written to stop something.",
		}, {
			Field: "when_checked", Type: "select", Label: "When replies are checked", ShowWhen: "rules",
			Options: []ui.SelectOption{
				{Value: "before", Label: "Before they are shown (replies do not stream)"},
				{Value: "stream", Label: "While they stream (a reply that breaks a rule is removed)"},
			},
			Help: "Before: nothing that breaks a rule is ever seen, and replies arrive whole. While streaming: replies appear as they are written, and one that breaks a rule is taken off the screen and replaced with a decline.",
			Detail: "Choose Before for rules about what must not be revealed (secrets, names, internal details): streaming would show such a reply for the second or two its check takes. " +
				"While streaming suits rules about conduct and tone, where a brief flash costs nothing. " +
				"An agent whose own guardrails check its replies still holds them for those, whatever this says.",
		}},
	}
}

// whenCheckedWord is the form's spelling of when replies are checked.
func whenCheckedWord(streaming bool) string {
	if streaming {
		return "stream"
	}
	return "before"
}

// uncheckedWord is the stored spelling of the fail policy.
func uncheckedWord(open bool) string {
	if open {
		return "allow"
	}
	return "block"
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
		Source:  "api/style-rules",
		PostURL: "api/style-rules",
		Fields: []ui.FormField{{
			Field: "rules",
			Label: "Style",
			Type:  "rules",
			// These are two-sentence rules; a one-line input shows a third of one.
			RowEditor: true,
			Help: "How replies read, one rule per line. Delete a line to drop the rule. Some shipped rules are also " +
				"enforced in code, so they hold even when the model ignores them; deleting such a line stops " +
				"its enforcement too.",
			SuggestURL:   "api/rules-assist",
			AssistPrompt: styleAssistPrompt,
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
