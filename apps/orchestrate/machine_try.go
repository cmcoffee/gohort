// Watching a machine run, without attaching it to anything.
//
// The gap this closes is the reason machines are hard to understand: a
// machine is a BEHAVIOUR and the editor only ever showed its
// configuration. You author twelve fields per step and then open a chat
// and hope. Every concept here — a step that waits versus one that hands
// on, routing on a field, what a step passes forward — is about what
// happens across turns, and nothing showed a turn happening.
//
// So: type a message, see which steps ran, why it moved, what each one
// handed on, and where it stopped. One click, no agent to attach, no
// session to start. That teaches transient-versus-resident better than
// any amount of help text, because it is the thing itself rather than a
// description of it.
//
// It is a REAL run of the machine's own driver — AdvanceMachine, the
// same function a live turn calls — not a simulation. Two honest
// differences, both stated in the reply:
//
//   - No tools. The dry run has no agent, so a step that would search or
//     read gets nothing back. It shows the SHAPE of the traversal, not
//     the quality of the answers.
//   - It stops AT the resident step rather than running it. Where a
//     conversation lands is the question here; what it would say is the
//     agent's job, and answering it would need the agent's whole context.

package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// tryTimeout bounds a dry run. Each transient hop is a model call, and
// the cap on hops is small, so this is generous — a run that outlives it
// has something wrong with it rather than being slow.
const tryTimeout = 3 * time.Minute

// handleMachineTry runs one message through the machine and reports the
// path it took.
//
//	POST /api/machines/{id}/try  {"message": "..."}
func (T *OrchestrateApp) handleMachineTry(w http.ResponseWriter, r *http.Request, udb Database, user string, def MachineDef) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	msg := strings.TrimSpace(body.Message)
	if msg == "" {
		http.Error(w, "type a message to run through it", http.StatusBadRequest)
		return
	}
	// A machine that cannot run should say why HERE rather than failing
	// somewhere downstream with the same information in a worse place.
	if probs := def.Problems(); len(probs) > 0 {
		writeJSON(w, map[string]any{
			"blocked":   true,
			"checklist": probs,
			"note":      "This machine will not run yet. Fix the list above and try again.",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), tryTimeout)
	defer cancel()

	cur := &MachineCursor{}
	var notes []string
	// No catalog: a dry run has no agent, so there are no tools to offer.
	// Said in the reply rather than hidden, because a step that would
	// have searched will look thin and the reason should not be a mystery.
	landed, err := T.AdvanceMachine(ctx, def, cur, msg, T.PhaseWorker(nil),
		func(kind, detail string) { notes = append(notes, kind+": "+detail) })
	if err != nil {
		writeJSON(w, map[string]any{
			"failed": err.Error(),
			"path":   tryPath(cur),
			"notes":  notes,
		})
		return
	}
	Log("[orchestrate.machines] user=%q dry-ran %q → landed in %s after %d hop(s)",
		user, def.Name, landed.Name, len(cur.Log))

	writeJSON(w, map[string]any{
		"landed":      landed.Name,
		"landed_desc": strings.TrimSpace(landed.Desc),
		"path":        tryPath(cur),
		"handed_on":   tryState(cur),
		"notes":       notes,
		// Stated every time. Somebody reading a thin answer should know
		// which of the two reasons it is.
		"caveat": "A dry run has no tools and does not run the step it lands in — it shows where a turn would GO, not what it would say.",
	})
}

// tryPath renders the traversal in the words the editor uses.
func tryPath(cur *MachineCursor) []map[string]any {
	out := make([]map[string]any, 0, len(cur.Log))
	for _, h := range cur.Log {
		out = append(out, map[string]any{"from": h.From, "to": h.To, "why": h.Why})
	}
	return out
}

// tryState is what each step handed on — the blackboard, which is the
// part a person cannot otherwise see at all.
func tryState(cur *MachineCursor) map[string]any {
	out := map[string]any{}
	for phase, res := range cur.State {
		entry := map[string]any{}
		for k, v := range res.Fields {
			entry[k] = v
		}
		// A step with no declared fields still produced text, and seeing
		// it is how somebody works out whether the step did its job.
		if len(entry) == 0 && strings.TrimSpace(res.Text) != "" {
			entry["(reply)"] = truncate(res.Text, 600)
		}
		out[phase] = entry
	}
	return out
}
