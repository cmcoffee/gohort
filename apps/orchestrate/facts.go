// store_fact / forget_fact / list_facts tools — the LLM-in-band
// memory layer for orchestrate agents. Flat-note model with dedup
// at the save site (text normalize + semantic similarity); no key
// dimension, so the LLM can't accidentally create duplicates by
// picking inconsistent keys for related content. Notes are auto-
// injected into every system prompt via RenderMemoryFactsBlock.
//
// Storage: core.MemoryFact rows under namespace "agent:<id>" in the
// caller's per-user sub-store (udb). Per-(user, agent) isolation is
// preserved because each user's udb is distinct.

package orchestrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// factsNamespace returns the MemoryFact namespace for one agent.
// agent.ID is unique within udb (per-user), so this scopes facts to
// (user, agent) end-to-end.
func factsNamespace(agentID string) string {
	return "agent:" + agentID
}

// storeFactToolDef lets the model record a discrete note it's
// learned. Dedup is automatic — same or similar notes get folded
// into the existing entry rather than accumulating.
func (t *chatTurn) storeFactToolDef() AgentToolDef {
	desc := "Record a SHORT note (Explicit Memory) that needs to be ACCOUNTED FOR EVERY TIME a new question is raised — instructions, preferences, durable user/context facts. Pre-injected into your system prompt on every future turn; the LLM sees them automatically without having to search.\n\n**Use store_fact when**: the note shapes how you should respond to ANY future question (user preferences, recurring constraints, identity facts, project context). Right examples: \"user prefers metric units\", \"all responses go to a vegetarian audience\", \"production API needs JWT in X-Auth header\".\n\n**Use memory_save instead when**: the finding is complicated reference material you MIGHT need to recall later for specific questions — API specs, website navigation steps, recipes, configuration details. Those are pull-only via memory_search, not always-in-prompt. If you're tempted to dump research findings into store_fact, use memory_save instead.\n\nThe framework dedupes automatically (same wording OR semantically similar = skipped). Quantity here costs prompt tokens forever (these inject on every turn), so keep total around a screen's worth.\n\nDistinct from `knowledge_search` (read-only over user-uploaded files) and `memory_search` (your own prior memory_save findings, pull-only)."
	if _, _, suffix := memoryModeCopy(t.agent.MemoryMode); suffix != "" {
		desc = desc + "\n\n" + suffix
	}
	return AgentToolDef{
		Tool: Tool{
			Name:        "store_fact",
			Description: desc,
			Parameters: map[string]ToolParam{
				"note": {
					Type:        "string",
					Description: "The fact, as a concise self-contained sentence. Include enough context that the note makes sense out of context months later. Examples: \"User prefers Korean for casual chat, English for technical questions.\" / \"Current project is named Atlas; deadline mid-June.\" / \"Time zone is America/Los_Angeles.\"",
				},
			},
			Required: []string{"note"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			note := strings.TrimSpace(stringArg(args, "note"))
			if note == "" {
				return "", errors.New("note is required")
			}
			// Pass the worker chat so a changed fact ("moved to Austin")
			// supersedes the stale one instead of being dropped as a near-dup
			// or coexisting as a contradiction.
			f, isNew, superseded := StoreMemoryFact(t.udb, factsNamespace(t.agent.ID), note, t.app.WorkerChat)
			if !isNew {
				return fmt.Sprintf("Already remembered (deduped): %q. Skipping.", f.Note), nil
			}
			msg := fmt.Sprintf("Stored: %q. Will appear in every future turn's \"Saved facts\" block.", f.Note)
			if len(superseded) > 0 {
				dropped := make([]string, len(superseded))
				for i, s := range superseded {
					dropped[i] = fmt.Sprintf("%q", s.Note)
				}
				msg += fmt.Sprintf(" Superseded %d now-stale fact(s): %s.", len(dropped), strings.Join(dropped, ", "))
			}
			return msg, nil
		},
	}
}

// forgetFactToolDef removes one fact by its 1-based index in the
// rendered list. The LLM reads its numbered notes in the system
// prompt's "Saved facts" block and references the matching
// index here.
func (t *chatTurn) forgetFactToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "forget_fact",
			Description: "Delete a previously-stored fact by its index in the \"Saved facts\" block in your system prompt. Use when a stored fact is OBSOLETE (no longer applies — user moved jobs, project changed names, preference flipped). Index is 1-based and matches the number you see in the prompt.",
			Parameters: map[string]ToolParam{
				"index": {
					Type:        "integer",
					Description: "1-based index of the note to delete, matching the number prefix in your \"Saved facts\" block.",
				},
			},
			Required: []string{"index"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			idx := intFromArgs(args, "index")
			if idx < 1 {
				return "", errors.New("index is required and must be >= 1")
			}
			removed, ok := ForgetMemoryFactByIndex(t.udb, factsNamespace(t.agent.ID), idx)
			if !ok {
				return "", fmt.Errorf("no fact at index %d", idx)
			}
			return fmt.Sprintf("Forgot: %q.", removed.Note), nil
		},
	}
}

// listFactsToolDef enumerates stored notes. Rarely needed — the
// "Saved facts" block in the system prompt already shows
// them — but useful when the LLM is about to dedup-decide or wants
// to confirm an index before forget_fact.
func (t *chatTurn) listFactsToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "list_facts",
			Description: "List every note you've stored about the user / conversation. Returns numbered notes (1-based) in the same order as the \"Saved facts\" block in your system prompt. Use sparingly — that block is already in every turn's prompt; mostly call this right before forget_fact to confirm the index.",
			Parameters:  map[string]ToolParam{},
			Caps:        []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			facts := ListMemoryFacts(t.udb, factsNamespace(t.agent.ID))
			if len(facts) == 0 {
				return "(no facts stored yet)", nil
			}
			var b strings.Builder
			for i, f := range facts {
				fmt.Fprintf(&b, "%d. %s\n", i+1, f.Note)
			}
			return b.String(), nil
		},
	}
}

// --- HTTP handler ---------------------------------------------------------

// handleAgentFacts serves the in-band memory layer for the admin UI.
//   GET  → returns the current MemoryFact rows for (user, agent) plus
//          the agent's KnowledgeFraming so the modal can render the
//          right header / intro.
//   POST → replaces the whole list. The client sends the edited set
//          (deletes + manual adds collapsed into the new array); we
//          diff against the existing rows so dedup + IDs stay sane.
//
// Per-(user, agent) isolation comes from the same factsNamespace
// scheme storeFactToolDef uses — the udb is already per-user, the
// namespace is per-agent.
func (T *OrchestrateApp) handleAgentFacts(w http.ResponseWriter, r *http.Request, agentID string) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
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
		facts := ListMemoryFacts(udb, ns)
		notes := make([]string, 0, len(facts))
		for _, f := range facts {
			notes = append(notes, f.Note)
		}
		header, intro, _ := memoryModeCopy(a.MemoryMode)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"notes": notes,
			"framing": map[string]string{
				"block_header": header,
				"block_intro":  intro,
			},
		})
	case http.MethodPost:
		var body struct {
			Notes []string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// Replace = wipe + restore. Cheaper than a diff and matches the
		// "what the user sees IS the state" mental model — anything not
		// in the POSTed list is intentionally gone. Goes through the
		// normal StoreMemoryFact path so dedup + sane IDs apply to any
		// new manual entries the user added in the editor.
		existing := ListMemoryFacts(udb, ns)
		for _, f := range existing {
			ForgetMemoryFactByID(udb, ns, f.ID)
		}
		for _, n := range body.Notes {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			StoreMemoryFact(udb, ns, n)
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
