package admin

// Suggest changes, for the two rules lists: a conversation with the worker
// about the list open in the editor, which answers with a sentence and,
// usually, the revised list.
//
// The framing each list gets is chosen here by the list's label, not taken
// from the browser: the form declares it for display, and a tampered client
// cannot turn this into a general model proxy.

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
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
	if !a.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Section string `json:"section"`
		Message string `json:"message"`
		Draft   string `json:"draft"`
		History []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"history"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*rulesAssistMax)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	framing, ok := rulesAssistPrompts[strings.TrimSpace(req.Section)]
	if !ok {
		http.Error(w, "no rules list named "+req.Section, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}
	llm := SharedWorkerLLM()
	if llm == nil {
		http.Error(w, "no worker LLM is configured", http.StatusServiceUnavailable)
		return
	}
	draft := req.Draft
	if len(draft) > rulesAssistMax {
		draft = draft[:rulesAssistMax]
	}
	msgs := make([]Message, 0, len(req.History)+1)
	for _, t := range req.History {
		role := strings.TrimSpace(t.Role)
		if (role != "user" && role != "assistant") || strings.TrimSpace(t.Content) == "" {
			continue
		}
		msgs = append(msgs, Message{Role: role, Content: t.Content})
	}
	msgs = append(msgs, Message{Role: "user", Content: req.Message})
	system := framing + "\n\n" + BuildDocAssistPrompt(req.Section+" rules", req.Section, draft, "")
	resp, err := llm.Chat(r.Context(), msgs,
		WithSystemPrompt(system),
		WithRouteKey("app.admin.rules_assist"),
		WithThink(false),
	)
	if err != nil {
		http.Error(w, "assist failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	reply, value := SplitDraftReply(ResponseText(resp))
	if value != "" {
		value = StripLeadingHeading(value, req.Section)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"reply": reply, "value": value})
}
