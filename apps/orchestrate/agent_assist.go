package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// Assist: a conversation about the WHOLE agent, which proposes changes you
// accept rather than text you paste.
//
// The per-field workbench (uiOpenAssist, handleAgentSuggest) already talks
// about one value at a time, and it is good at that. What it cannot do is
// notice anything: it sees the field it was opened on, so it cannot tell you
// that the agent has no rules, or that its allowlist and its persona disagree
// about whether it browses the web. A person editing an agent does not have a
// field problem, they have an agent problem, and the answer to "make it stop
// making things up" is a rule plus a sentence in the persona, not one line in
// whichever box happened to be focused.
//
// So this takes the whole record and returns a DIFF: field, proposed value,
// and one line of why. Reviewing a change is easy; writing one is not, which
// is the entire reason a proposal beats a blank box.

// assistableFields are the fields assist may propose changing, with the label
// a person sees. An allowlist rather than a denylist: the model is answering
// with field names, and the failure of a denylist is silent and permanent (an
// agent quietly published, a budget quietly raised) while the failure of an
// allowlist is a change that does not appear.
//
// Deliberately excluded: anything about REACH or EXPOSURE. Tools, credentials,
// exposure, hidden, budgets and the follow-a-shape link are decisions with
// consequences beyond the conversation, and a person should make them at the
// control that owns them, where the help text and the confirmation live.
var assistableFields = map[string]string{
	"name":                "Name",
	"description":         "Description",
	"orchestrator_prompt": "Persona",
	"rules":               "Rules",
	"plan_guidance":       "Plan guidance",
	"triggers":            "Triggers",
}

// assistChange is one proposed edit.
type assistChange struct {
	Field   string `json:"field"`
	Label   string `json:"label"`
	Why     string `json:"why"`
	Value   string `json:"value"`
	Current string `json:"current"`
}

type assistReply struct {
	Reply   string         `json:"reply"`
	Changes []assistChange `json:"changes,omitempty"`
}

type assistTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// handleAgentAssist answers a message about one agent with a reply and a set
// of proposed changes: POST /api/agents/{id}/assist {message, history}.
func (T *OrchestrateApp) handleAgentAssist(w http.ResponseWriter, r *http.Request, user string, udb Database, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.LLM == nil {
		http.Error(w, "worker LLM not configured", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Message string       `json:"message"`
		History []assistTurn `json:"history"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}
	rec, ok := loadAgent(udb, id)
	if !ok {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	if rec.Owner != user && rec.Owner != seedOwner {
		http.Error(w, "not yours", http.StatusForbidden)
		return
	}

	msgs := make([]Message, 0, len(req.History)+1)
	for _, t := range req.History {
		role := "user"
		if strings.EqualFold(t.Role, "assistant") {
			role = "assistant"
		}
		if strings.TrimSpace(t.Content) == "" {
			continue
		}
		msgs = append(msgs, Message{Role: role, Content: t.Content})
	}
	msgs = append(msgs, Message{Role: "user", Content: req.Message})

	resp, err := T.LLM.Chat(r.Context(), msgs,
		WithSystemPrompt(buildAgentAssistPrompt(rec)),
		WithRouteKey("app.orchestrate.assist"),
		WithThink(false),
	)
	if err != nil {
		http.Error(w, "assist failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if resp == nil {
		http.Error(w, "empty response", http.StatusBadGateway)
		return
	}
	out := parseAssistReply(resp.Content, rec)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// buildAgentAssistPrompt gives the model the agent as a person sees it, plus
// the current text of everything it may change.
//
// The plain description comes first because it is the same account the user is
// looking at, and advice that contradicts what they can see reads as a
// different agent. The shape's recipe follows when the agent has one: it is
// the framework's own written guidance for this kind of agent, and an assist
// that ignores it would re-derive worse answers than the ones already written
// down.
func buildAgentAssistPrompt(rec AgentRecord) string {
	var b strings.Builder
	b.WriteString("You are helping someone improve ONE agent they own. You are looking at the whole agent, not one field.\n\n")

	b.WriteString("## The agent as it stands\n\n")
	fmt.Fprintf(&b, "Name: %s\n", strings.TrimSpace(rec.Name))
	for _, line := range describeAgentPlainly(rec) {
		b.WriteString("- " + line + "\n")
	}
	if doc, ok := archetypeBySlug(rec.ShapeID); ok && rec.ShapeID != "" {
		b.WriteString("\n## What this kind of agent is meant to be\n\n")
		b.WriteString("This agent follows the " + doc.Slug + " shape. The framework's own guidance for that shape:\n\n")
		b.WriteString(doc.Body + "\n")
	}

	b.WriteString("\n## Its current settings, and the only ones you may change\n\n")
	for _, f := range assistableFieldOrder() {
		cur := assistFieldValue(rec, f)
		if strings.TrimSpace(cur) == "" {
			cur = "(empty)"
		}
		fmt.Fprintf(&b, "### %s (%s)\n%s\n\n", assistableFields[f], f, cur)
	}

	b.WriteString(`## How to answer

Reply with a JSON object and nothing else:

{"reply": "one or two sentences to the person", "changes": [{"field": "rules", "why": "one line", "value": "the complete new value"}]}

Rules for the changes:
- Only the field names listed above. Anything else is dropped.
- "value" is the COMPLETE new value for that field, not a patch and not an excerpt. A partial value silently deletes the rest.
- Propose a change only where you are improving something specific. An empty changes list is a good answer when the person asked a question rather than for an edit.
- Say what you are doing in "reply", briefly, and do not repeat the values there. They will see them.
- Keep rules as one constraint per line, in the imperative.
- Do not touch what the agent can REACH: tools, credentials, budgets and exposure are not yours to change, and saying you changed them would be a lie the person acts on.`)
	return b.String()
}

// assistableFieldOrder is the fixed order fields are presented in: identity
// first, then behavior. Sorted rather than map order so the prompt is the same
// every time, which is what makes a cached prefix worth anything.
func assistableFieldOrder() []string {
	out := make([]string, 0, len(assistableFields))
	for f := range assistableFields {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// assistFieldValue reads one assistable field as text.
func assistFieldValue(rec AgentRecord, field string) string {
	switch field {
	case "name":
		return rec.Name
	case "description":
		return rec.Description
	case "orchestrator_prompt":
		return rec.OrchestratorPrompt
	case "rules":
		return rec.Rules
	case "plan_guidance":
		return rec.PlanGuidance
	case "triggers":
		return strings.Join(rec.Triggers, "\n")
	}
	return ""
}

// parseAssistReply turns the model's answer into a reply and a validated set
// of changes.
//
// Anything unparseable degrades to a plain reply with no changes rather than
// an error: the person asked a question and got an answer, which is worth more
// than a failure toast. What must NOT degrade is the allowlist, so an
// unrecognized field is dropped, and a change that would leave a field
// identical is dropped too, since a diff of nothing wastes the one decision
// this dialog asks for.
func parseAssistReply(raw string, rec AgentRecord) assistReply {
	text := strings.TrimSpace(raw)
	if fenced := stripJSONFence(text); fenced != "" {
		text = fenced
	}
	var parsed struct {
		Reply   string `json:"reply"`
		Changes []struct {
			Field string `json:"field"`
			Why   string `json:"why"`
			Value string `json:"value"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return assistReply{Reply: strings.TrimSpace(raw)}
	}

	out := assistReply{Reply: strings.TrimSpace(parsed.Reply)}
	seen := map[string]bool{}
	for _, c := range parsed.Changes {
		field := strings.TrimSpace(strings.ToLower(c.Field))
		label, allowed := assistableFields[field]
		if !allowed || seen[field] {
			continue
		}
		value := strings.TrimSpace(c.Value)
		current := assistFieldValue(rec, field)
		if value == "" || value == strings.TrimSpace(current) {
			continue
		}
		seen[field] = true
		out.Changes = append(out.Changes, assistChange{
			Field:   field,
			Label:   label,
			Why:     strings.TrimSpace(c.Why),
			Value:   value,
			Current: current,
		})
	}
	if out.Reply == "" && len(out.Changes) == 0 {
		out.Reply = strings.TrimSpace(raw)
	}
	return out
}

// stripJSONFence pulls the object out of a ```json fence, which models add
// however plainly they are asked not to.
func stripJSONFence(s string) string {
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return ""
	}
	return s[i : j+1]
}
