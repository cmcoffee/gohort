// update_notes — the LLM-in-band "Working notes" layer. A single bounded,
// REWRITABLE block of running state, distinct from store_fact (append durable
// rules) and memory_save (semantic pull-only recall). Opt-in per agent via
// AgentRecord.EnableNotes; Builder can seed the initial text via SeedNotes.
//
// Storage reuses the fact layer's per-(user, agent) scope: the same
// "agent:<id>" namespace in the caller's per-user sub-store, a different table
// (core_notes). See core/notesstore.go.

package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// agentOperatingNotes returns the effective Working notes for an agent, gated
// on EnableNotes and falling back to the record's SeedNotes when the store is
// empty. Used by the non-chatTurn prompt-assembly sites (dispatch, scheduled,
// eval). Returns the zero value (renders nothing) when notes are off.
func agentOperatingNotes(db Database, agent AgentRecord) OperatingNotes {
	if !agent.EnableNotes {
		return OperatingNotes{}
	}
	return ResolveOperatingNotes(db, factsNamespace(agent.ID), agent.SeedNotes)
}

// operatingNotes is the chatTurn variant: same as agentOperatingNotes but also
// suppresses notes in an incognito session (no stored state surfaced), matching
// how facts() nils for incognito turns — a claim this comment carried for a
// while before facts() actually did it, which is how the gap went unnoticed.
func (t *chatTurn) operatingNotes() OperatingNotes {
	if !t.agent.EnableNotes {
		return OperatingNotes{}
	}
	if t.session != nil && t.session.Incognito {
		return OperatingNotes{}
	}
	return ResolveOperatingNotes(t.udb, factsNamespace(t.agent.ID), t.agent.SeedNotes)
}

// --- HTTP handler ---------------------------------------------------------

// handleAgentNotes serves the Working notes block to the Memory modal.
//
//	GET  → the effective text (store, or the record's SeedNotes when the
//	       store is empty), when it was last rewritten, whether notes are
//	       enabled for this agent, and the cap.
//	POST → replaces the block wholesale, same semantics as update_notes.
//
// Notes were the one memory layer with no owner-facing surface: facts, graph
// and Reference Memory all had panels, notes had only the cross-layer text
// search. They are also the layer the model rewrites on its own, unprompted
// and unreviewed, and the one that renders nearest the top of the prompt — so
// a wrong note steered every turn with nowhere to go and look at it. That is
// how a stale "pending task: <tool> with <args>" survived across sessions.
//
// user is the STATE scope; the caller resolves and authorizes it, matching
// handleAgentFacts (RequireUser on the web surfaces, an appliance scope for
// the per-scope variant).
func (T *OrchestrateApp) handleAgentNotes(w http.ResponseWriter, r *http.Request, user, agentID string) {
	udb := UserDB(T.DB, user)
	if udb == nil {
		http.Error(w, "no store for user", http.StatusInternalServerError)
		return
	}
	if agentID == "" || strings.Contains(agentID, "/") {
		http.NotFound(w, r)
		return
	}
	a, ok := loadAgent(udb, agentID)
	if !ok || (a.Owner != user && a.Owner != seedOwner) {
		http.NotFound(w, r)
		return
	}
	ns := factsNamespace(agentID)
	switch r.Method {
	case http.MethodGet:
		stored := LoadOperatingNotes(udb, ns)
		eff := ResolveOperatingNotes(udb, ns, a.SeedNotes)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"text":    eff.Text,
			"enabled": a.EnableNotes,
			// Whether the READER can turn them on. An app agent's flags come
			// from its code-registered spec and its record is hidden from the
			// pickers, so there is no editor to send anyone to — telling them
			// to go to one is the same unfollowable advice as pointing a macOS
			// operator at a Linux package. The panel hides itself instead.
			"can_enable": !isAppAgent(agentID),
			"cap":        OperatingNotesCap,
			// Distinguishes "the agent wrote this" from "nobody has written
			// anything, you're looking at the configured seed" — clearing is
			// meaningless in the second case, and the panel says which it is.
			"from_seed":  strings.TrimSpace(stored.Text) == "" && strings.TrimSpace(eff.Text) != "",
			"updated_at": stored.UpdatedAt,
		})
	case http.MethodPost:
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		text := strings.TrimSpace(body.Text)
		if n := len([]rune(text)); n > OperatingNotesCap {
			http.Error(w, fmt.Sprintf("notes are %d characters, over the %d limit", n, OperatingNotesCap), http.StatusBadRequest)
			return
		}
		if _, over := SaveOperatingNotes(udb, ns, text); over {
			http.Error(w, fmt.Sprintf("over the %d character limit", OperatingNotesCap), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// updateNotesToolDef binds the always-in-prompt Working notes block. Unlike
// store_fact (append an atomic rule), this REPLACES the whole block — the agent
// keeps a compact, current running-state note.
func (t *chatTurn) updateNotesToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "update_notes",
			Description: fmt.Sprintf("REWRITE your always-in-prompt \"Working notes\" block — a compact scratchpad of the CURRENT state of your work (what you're mid-way through, the shape of the task, transient context). This REPLACES the whole block; it does NOT append. Re-state the complete current note each time, trimming what's now stale. Use it for running state that changes turn to turn (\"drafting section 3 of the guide\", \"user wants the terse version\", \"waiting on the export to finish\"). Do NOT use it for durable rules or preferences — those go in store_fact — and NOT to record a tool/API bug (that gets fixed in the tool, not remembered). NEVER park a tool call here to make later (\"pending task: some_tool with x=y\"): a note cannot call a tool, and the invocation outlives the tool — next session its schema may not be loaded, and a remembered call you have no way to make becomes an improvised workaround. Record the GOAL (\"user wants a news roundup\"), not the call. Keep it under %d characters; if it won't fit, COMPRESS — the limit exists to force a summary, not a log. Pass empty text to clear the block.", OperatingNotesCap),
			Parameters: map[string]ToolParam{
				"text": {
					Type:        "string",
					Description: fmt.Sprintf("The complete new Working notes block — concise prose or short bullets. Replaces the current block entirely (this is not an append). Max %d characters. Empty string clears it.", OperatingNotesCap),
				},
			},
			Required: []string{"text"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			text := strings.TrimSpace(stringArg(args, "text"))
			if n := len([]rune(text)); n > OperatingNotesCap {
				return "", fmt.Errorf("notes are %d characters, over the %d limit — trim or summarize and try again (this block injects into every turn, so it must stay compact)", n, OperatingNotesCap)
			}
			_, over := SaveOperatingNotes(t.udb, factsNamespace(t.agent.ID), text)
			if over {
				return "", fmt.Errorf("over the %d character limit — trim and retry", OperatingNotesCap)
			}
			if text == "" {
				return "Working notes cleared.", nil
			}
			return "Working notes updated — this block now appears in full at the top of every future turn.", nil
		},
	}
}
