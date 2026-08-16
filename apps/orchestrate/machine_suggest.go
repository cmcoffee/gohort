// Assist for one step of a machine.
//
// The agent editor has had a per-field ✨ Suggest since it existed; a
// machine's steps did not, which is backwards — writing a step's method
// is harder than writing an agent's persona, because it has to fit
// around parts the framework composes and a rule the author cannot see.
// That is exactly the case where a draft to react to beats a blank box.
//
// It answers the same contract as the agent's (POST {field, hint,
// message, draft, history, assist_prompt} → {reply, value}), so the
// shared assist workbench in core/ui drives it with no new client code.
//
// What it adds is CONTEXT the generic one cannot have: the step being
// edited, what it establishes, what earlier steps already establish, and
// where it can go. A suggestion written without those restates the field
// list or invents state that does not exist — which is the failure this
// whole editor has been chipping away at.

package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// maxAssistPrompt bounds the field framing a client may send. Advisory
// text composed INTO a system prompt is a lever a tampered client would
// otherwise use to turn this into a general LLM proxy.
const maxAssistPrompt = 2000

func (T *OrchestrateApp) handleMachineSuggest(w http.ResponseWriter, r *http.Request, udb Database, user string, def MachineDef) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.LLM == nil {
		http.Error(w, "worker LLM not configured", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Field        string         `json:"field"`
		Hint         string         `json:"hint"`
		Message      string         `json:"message"`
		Draft        string         `json:"draft"`
		History      []suggestTurn  `json:"history"`
		AssistPrompt string         `json:"assist_prompt"`
		Record       map[string]any `json:"record"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// The step is identified by the record the form already carries, so a
	// caller cannot ask about a step it is not looking at.
	name := strings.TrimSpace(fmt.Sprint(req.Record["name"]))
	ph, found := def.Phase(name)
	if !found {
		http.Error(w, "no step called "+strconv.Quote(name)+" in this machine", http.StatusNotFound)
		return
	}
	if strings.TrimSpace(req.Field) != "prompt" {
		// Deliberately narrow. The other fields are selects and toggles
		// with computed options — there is nothing for a model to draft,
		// and a suggest button on a picker would be a worse way to choose.
		http.Error(w, "only a step's instructions can be drafted", http.StatusBadRequest)
		return
	}
	if len(req.AssistPrompt) > maxAssistPrompt {
		req.AssistPrompt = req.AssistPrompt[:maxAssistPrompt]
	}

	sys := machineAssistSystem(def, ph, req.AssistPrompt)
	ctx := r.Context()

	if strings.TrimSpace(req.Message) != "" {
		msgs := make([]Message, 0, len(req.History)+1)
		for _, t := range req.History {
			role := strings.TrimSpace(t.Role)
			if role != "user" && role != "assistant" {
				continue
			}
			msgs = append(msgs, Message{Role: role, Content: t.Content})
		}
		if d := strings.TrimSpace(req.Draft); d != "" {
			sys += "\n\nThe current draft, which they can see and edit:\n" + d
		}
		msgs = append(msgs, Message{Role: "user", Content: req.Message})
		resp, err := T.LLM.Chat(ctx, msgs, WithSystemPrompt(sys),
			WithRouteKey("app.orchestrate.suggest"), WithThink(false))
		if err != nil || resp == nil {
			http.Error(w, "assist failed", http.StatusBadGateway)
			return
		}
		reply, value := splitDraftReply(resp.Content)
		writeJSON(w, map[string]any{"reply": reply, "value": strings.TrimSpace(value)})
		return
	}

	ask := "Write the instructions for this step."
	if h := strings.TrimSpace(req.Hint); h != "" {
		ask += " The person adds: " + h
	}
	resp, err := T.LLM.Chat(ctx, []Message{{Role: "user", Content: ask}},
		WithSystemPrompt(sys), WithRouteKey("app.orchestrate.suggest"), WithThink(false))
	if err != nil || resp == nil {
		http.Error(w, "assist failed", http.StatusBadGateway)
		return
	}
	Log("[orchestrate.machines] user=%q drafted instructions for %s/%s", user, def.Name, ph.Name)
	writeJSON(w, map[string]any{"value": strings.TrimSpace(resp.Content)})
}

// machineAssistSystem gives the drafter the same picture the author has —
// and the same rules, so it cannot suggest the thing the editor spends
// its help text talking people out of.
func machineAssistSystem(def MachineDef, ph MachinePhase, framing string) string {
	var b strings.Builder
	b.WriteString("You are helping someone write ONE step of a workflow an AI agent moves through.\n\n")
	b.WriteString("THE WORKFLOW: " + def.Name)
	if d := strings.TrimSpace(def.Description); d != "" {
		b.WriteString(" — " + d)
	}
	b.WriteString("\nIts steps, in order:\n")
	for _, p := range def.Phases {
		b.WriteString("- " + p.Name)
		if p.Name == ph.Name {
			b.WriteString(" ← the one being written")
		}
		if d := strings.TrimSpace(p.Desc); d != "" {
			b.WriteString(": " + d)
		}
		b.WriteString("\n")
	}

	b.WriteString("\nTHIS STEP\n")
	if ph.Resident {
		b.WriteString("The conversation WAITS here: a turn ends in this step and the person replies into it. " +
			"It receives what earlier steps established, so it must not re-ask what they answered.\n")
	} else {
		b.WriteString("This step runs and passes straight on inside one turn; the person never waits in it.\n")
	}
	if len(ph.Output) > 0 {
		b.WriteString("\nIt already declares these, and EACH IS SENT TO THE MODEL AS ITS OWN INSTRUCTION:\n")
		for _, f := range ph.Output {
			b.WriteString("- " + f.Name)
			if d := strings.TrimSpace(f.Desc); d != "" {
				b.WriteString(": " + strings.ReplaceAll(d, "\n", " "))
			}
			b.WriteString("\n")
		}
	}

	b.WriteString(`
WHAT TO WRITE

The METHOD, not the output. The declared fields above already say what to produce, and the framework
sends them with their descriptions — restating them wastes the instruction. Write what a list of
fields cannot say:

- where to look first, and what counts as having looked properly
- what a good answer requires that a plausible one does not
- the mistake this kind of step tends to make, named plainly

"Read enough to have a real hypothesis rather than a plausible one; a first ERROR line is usually the
symptom of something that went wrong several lines earlier" is the shape. "Return a hypothesis field"
is not — declaring the field already said that.

NEVER: ask for JSON, describe an output format, give an example object, paste in what an earlier
step found, or place {input}-style variables to "receive" the message. The person's message and
everything earlier steps established arrive automatically; all of that is composed by the
framework, and a second copy fights it.

Write to a person, in plain sentences. Short is better than complete. Reply with the instructions
only — no preamble, no explanation of what you wrote.`)

	if f := strings.TrimSpace(framing); f != "" {
		b.WriteString("\n\nThe editor adds, about this field specifically: " + f)
	}
	return b.String()
}
