package admin

// Suggest changes, for the two rules lists: a conversation with the worker
// about the list open in the editor, which answers with a sentence and,
// usually, the revised list.
//
// The framing each list gets is chosen here by the list's label, not taken
// from the browser: the form declares it for display, and a tampered client
// cannot turn this into a general model proxy.

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/assist"
)

const alwaysAssistPrompt = "You write operator rules that bind an AI assistant's conduct: short imperative " +
	"lines, one obligation each. State the boundary and what to do when a request would cross " +
	"it. Be concrete about the behaviour, not aspirational about values. Do not use em-dashes."

const styleAssistPrompt = "You write house-style rules for an AI assistant: short imperative lines, " +
	"one behaviour each. Name the tic concretely and say what to do instead. " +
	"Where a word or character has a legitimate use, carve it out so the rule does not " +
	"forbid that too. Do not use em-dashes."

// rulesAssistPrompts is each list's framing, by the label its field carries.
var rulesAssistPrompts = map[string]string{
	"Always": alwaysAssistPrompt,
	"Style":  styleAssistPrompt,
}

// rulesAssistMax bounds the draft and the history one call sends.
const rulesAssistMax = 24000

func (a *AdminApp) handleRulesAssist(w http.ResponseWriter, r *http.Request) {
	assist.Handler(a.rulesAssistSpec())(w, r)
}

// rulesAssistSpec is the rules lists' assist on core/assist: its own prompt
// (the list's framing, then the document assist), its own reading of the
// answer (the list without a heading that restates its name).
func (a *AdminApp) rulesAssistSpec() assist.Spec {
	return assist.Spec{
		Route: "app.admin.rules_assist", Label: "Admin: rules assist", App: "/admin",
		Allow: a.requireAdmin, Think: assist.Off,
		Prompt: func(r *http.Request, req assist.Request) (string, error) {
			section := req.String("section")
			framing, ok := rulesAssistPrompts[section]
			if !ok {
				return "", assist.Refuse(http.StatusBadRequest, "no rules list named "+section)
			}
			draft := req.Draft
			if len(draft) > rulesAssistMax {
				draft = draft[:rulesAssistMax]
			}
			return framing + "\n\n" + BuildDocAssistPrompt(section+" rules", section, draft, "", req.Mode), nil
		},
		Parse: func(req assist.Request, raw string) assist.Reply {
			reply, value := SplitDraftReply(raw)
			if value != "" {
				value = StripLeadingHeading(value, req.String("section"))
			}
			return assist.Reply{Reply: reply, Value: value}
		},
	}
}
