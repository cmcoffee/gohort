// Style rules are a LIST, not a set of prompt blocks.
//
// A framework block (prompt_registry.go) is a paragraph encoding an incident:
// the credential-solicitation failure, the volatile-price fabrications. You
// read one, you might turn it off, you do not casually write another.
//
// A style rule is a sentence. "Stop saying classic." "No em-dashes." "Stop
// opening every reply with Certainly." They arrive one at a time, from a person
// noticing a tic, and they are exactly the thing an operator should be able to
// add and drop without ceremony. Giving each one a Title, a Category and a Gate
// would be filling out a form to say six words.
//
// So they compose. Every enabled rule becomes a numbered item in ONE [Style:]
// clause, which is how the prompt already expressed them and keeps the cost of
// a tenth rule to one more line rather than one more block.
//
// TWO KINDS, and the difference is worth showing rather than hiding. A rule can
// carry an enforcer (RegisterRuleEnforcer, bound to the same key) and then it
// HOLDS: the transform runs at the delivery boundary whether or not the model
// cooperates, which is what the em-dash and classic rules do. An operator
// cannot supply a Go function, so an added rule only ASKS. That distinction
// belongs on the page: one is a guarantee, the other is a request.

package prompts

import (
	"fmt"
	"strings"
	"sync"
)

const customStyleKey = "prompt_custom_style_rules"

// StyleRule is one line of house style.
type StyleRule struct {
	Key     string // stable id, e.g. "style.no_em_dash"
	Text    string // the instruction, as injected
	Builtin bool   // shipped with gohort, vs added by an operator
}

var (
	styleMu      sync.Mutex
	builtinStyle []StyleRule
)

// RegisterStyleRule adds a shipped style rule. Call from an init() beside the
// transform that enforces it, where one exists.
func RegisterStyleRule(r StyleRule) {
	if r.Key == "" || r.Text == "" {
		return
	}
	r.Builtin = true
	styleMu.Lock()
	builtinStyle = append(builtinStyle, r)
	styleMu.Unlock()
}

// BuiltinStyleRules returns the shipped rules in registration order.
func BuiltinStyleRules() []StyleRule {
	styleMu.Lock()
	defer styleMu.Unlock()
	out := make([]StyleRule, len(builtinStyle))
	copy(out, builtinStyle)
	return out
}

// CustomStyleRules returns the operator's own rules.
func CustomStyleRules() []StyleRule {
	db := promptOverrideStore()
	if db == nil {
		return nil
	}
	var out []StyleRule
	db.Get(OverrideTable, customStyleKey, &out)
	return out
}

// AddStyleRule stores an operator-authored rule, replacing one with the same
// key. Never builtin: an added rule asks, it cannot enforce.
func AddStyleRule(r StyleRule) {
	db := promptOverrideStore()
	if db == nil || r.Key == "" || strings.TrimSpace(r.Text) == "" {
		return
	}
	r.Builtin = false
	rules := CustomStyleRules()
	for i := range rules {
		if rules[i].Key == r.Key {
			rules[i] = r
			db.Set(OverrideTable, customStyleKey, rules)
			return
		}
	}
	db.Set(OverrideTable, customStyleKey, append(rules, r))
}

// RemoveStyleRule deletes an operator-authored rule. A builtin is not removable
// this way: disable it instead, which is reversible and leaves the shipped text
// on the page to switch back on.
func RemoveStyleRule(key string) {
	db := promptOverrideStore()
	if db == nil {
		return
	}
	rules := CustomStyleRules()
	out := rules[:0]
	for _, r := range rules {
		if r.Key != key {
			out = append(out, r)
		}
	}
	db.Set(OverrideTable, customStyleKey, out)
}

// SetCustomStyleRules replaces the operator's whole list. The editor is a
// textarea of one rule per line, so the save is a replace rather than a diff:
// a line that is gone is a rule that is gone, which is the behaviour a text box
// promises and the only one that needs no identity matching.
func SetCustomStyleRules(rules []StyleRule) {
	db := promptOverrideStore()
	if db == nil {
		return
	}
	out := make([]StyleRule, 0, len(rules))
	for i, r := range rules {
		if strings.TrimSpace(r.Text) == "" {
			continue
		}
		r.Builtin = false
		if r.Key == "" {
			r.Key = fmt.Sprintf("style.custom.%d", i+1)
		}
		out = append(out, r)
	}
	db.Set(OverrideTable, customStyleKey, out)
}

// AllStyleRules returns builtins then operator additions, whatever their
// enabled state, for a surface to render.
func AllStyleRules() []StyleRule {
	return append(BuiltinStyleRules(), CustomStyleRules()...)
}

// EnabledStyleRules returns the rules that are actually live, honouring the
// same per-key off switch the framework blocks use and any text override.
func EnabledStyleRules() []StyleRule {
	var out []StyleRule
	for _, r := range AllStyleRules() {
		if !PromptBlockEnabled(r.Key) {
			continue
		}
		r.Text = EffectivePromptText(r.Key, r.Text)
		if strings.TrimSpace(r.Text) == "" {
			continue
		}
		out = append(out, r)
	}
	return out
}

// StyleClause assembles the live rules into the single bracketed clause the
// system prompt carries, numbered the way it always was. Returns "" when every
// rule is off, so the assembler appends nothing at all rather than an empty
// header, and a deployment that wants no house style gets none.
//
// The clause is written WITHOUT em-dashes itself, so it never models the
// behaviour it forbids.
func StyleClause() string {
	rules := EnabledStyleRules()
	if len(rules) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[Style:")
	for i, r := range rules {
		if len(rules) > 1 {
			fmt.Fprintf(&b, " (%d)", i+1)
		}
		b.WriteString(" " + strings.TrimSpace(r.Text))
	}
	b.WriteString("]")
	return b.String()
}

// EnabledStyleRulesMarkdown renders the live rules as a markdown bullet list.
// The editor's storage shape, and what every OTHER surface shows when it wants
// to display what is inherited from the deployment.
func EnabledStyleRulesMarkdown() string {
	var b strings.Builder
	for _, r := range EnabledStyleRules() {
		if t := strings.TrimSpace(r.Text); t != "" {
			b.WriteString("- " + t + "\n")
		}
	}
	return b.String()
}
