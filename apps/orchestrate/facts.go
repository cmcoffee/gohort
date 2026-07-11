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
	"time"

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
	if suffix := memoryModeCopy(t.agent.MemoryMode).StoreToolSuffix; suffix != "" {
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
			return t.storeFactNote(stringArg(args, "note"))
		},
	}
}

// storeFactNote is the shared write path for the Explicit Memory (always-
// in-prompt) layer. storeFactToolDef routes here, and so does the unified
// `remember` tool's pin=true branch — one place owns dedup, supersession,
// and the relevance gate so the two surfaces can't drift.
func (t *chatTurn) storeFactNote(note string) (string, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return "", errors.New("note is required")
	}
	// Pass the agent's memory mode + worker chat so: (a) a changed fact
	// ("moved to Austin") supersedes the stale one instead of coexisting as
	// a contradiction, and (b) in chatbot mode the relevance gate rejects
	// ephemeral chatter before it bloats the always-in-prompt block.
	res := StoreMemoryFactP(t.udb, factsNamespace(t.agent.ID), note, FactWritePolicy{
		Mode: t.agent.MemoryMode,
		Chat: t.app.WorkerChat,
	})
	switch res.Reason {
	case FactDuplicate:
		return fmt.Sprintf("Already remembered (deduped): %q. Skipping.", res.Fact.Note), nil
	case FactRejected:
		return "Not saved. That reads as a passing detail rather than a durable fact worth injecting into every future turn. If it's a lasting preference, identity fact, or standing instruction, rephrase it as one and try again.", nil
	}
	msg := fmt.Sprintf("Stored: %q. Will appear in every future turn's \"Saved facts\" block.", res.Fact.Note)
	if len(res.Superseded) > 0 {
		dropped := make([]string, len(res.Superseded))
		for i, s := range res.Superseded {
			dropped[i] = fmt.Sprintf("%q", s.Note)
		}
		msg += fmt.Sprintf(" Superseded %d now-stale fact(s): %s.", len(dropped), strings.Join(dropped, ", "))
	}
	return msg, nil
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

// searchFactsToolDef finds stored notes by semantic relevance to a query,
// falling back to substring, and lists all notes when the query is empty. It
// subsumes the old list_facts (empty query == full list) and adds the semantic
// search that RenderMemoryFactsBlock's always-in-prompt view can't offer once
// the note count grows past a screenful.
func (t *chatTurn) searchFactsToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "search_facts",
			Description: "Search your stored Explicit Memory notes by meaning (\"what's the deploy header?\" finds \"production API needs JWT in X-Auth header\"), or omit the query to list every note. Returns numbered notes (1-based) matching the \"Saved facts\" block order for the full-list case. The always-in-prompt \"Saved facts\" block already shows recent notes; reach for this when that block has grown large and you want to pinpoint a specific note (e.g. before forget_fact) rather than re-reading the whole block.",
			Parameters: map[string]ToolParam{
				"query": {Type: "string", Description: "What to look for, in natural language. Omit or leave empty to list all stored notes."},
			},
			Caps: []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			query := strings.TrimSpace(stringArg(args, "query"))
			facts := SearchMemoryFacts(t.udb, factsNamespace(t.agent.ID), query)
			if len(facts) == 0 {
				if query == "" {
					return "(no facts stored yet)", nil
				}
				// No live match — check the history set so a hole gets a record
				// ("you had X; it was dropped on <date>") instead of the model
				// falling back to a stale prior with no signal it once knew this.
				if hole := explainRetiredHole(t.udb, factsNamespace(t.agent.ID), query); hole != "" {
					return hole, nil
				}
				return fmt.Sprintf("(no stored facts match %q)", query), nil
			}
			var b strings.Builder
			// Explicit → graph nudge: list the graph once, then flag any fact that
			// names a known entity with a recall_about pointer. Empty graph → no cost.
			ents := ListGraphEntities(t.udb, factsNamespace(t.agent.ID))
			now := time.Now()
			for i, f := range facts {
				fmt.Fprintf(&b, "%d. %s%s%s\n", i+1, f.Note, factEntityNudge(ents, f.Note), FactStalenessNote(f, now))
			}
			return b.String(), nil
		},
	}
}

// explainRetiredHole builds a recall message for a query that matched no LIVE
// fact but does match one or more retired (tombstoned) ones — so the model learns
// it once knew this and why the note is gone, instead of silently answering from
// a stale prior. Case-insensitive substring match, newest-retired first, capped
// at three. Returns "" when no retired fact matches.
func explainRetiredHole(db Database, namespace, query string) string {
	ql := strings.ToLower(query)
	var b strings.Builder
	shown := 0
	for _, f := range ListRetiredFacts(db, namespace) {
		if !strings.Contains(strings.ToLower(f.Note), ql) {
			continue
		}
		if shown == 0 {
			b.WriteString("No live fact matches, but you previously stored (now retired):\n")
		}
		fmt.Fprintf(&b, "- %q — %s", f.Note, RetireReasonLabel(f.Reason))
		if !f.RetiredAt.IsZero() {
			fmt.Fprintf(&b, " on %s", f.RetiredAt.Format("2006-01-02"))
		}
		if f.Successor != "" {
			if succ, ok := GetMemoryFactByID(db, namespace, f.Successor); ok {
				fmt.Fprintf(&b, "; current note: %q", succ.Note)
			}
		}
		b.WriteString(".\n")
		if shown++; shown >= 3 {
			break
		}
	}
	if shown == 0 {
		return ""
	}
	b.WriteString("\nTreat retired notes as historical, not current — verify before relying on them.")
	return b.String()
}

// --- HTTP handler ---------------------------------------------------------

// handleAgentFacts serves the in-band memory layer for the admin UI.
//
//	GET  → returns the current MemoryFact rows for (user, agent) plus
//	       the agent's KnowledgeFraming so the modal can render the
//	       right header / intro.
//	POST → replaces the whole list. The client sends the edited set
//	       (deletes + manual adds collapsed into the new array); we
//	       diff against the existing rows so dedup + IDs stay sane.
//
// Per-(user, agent) isolation comes from the same factsNamespace
// scheme storeFactToolDef uses — the udb is already per-user, the
// namespace is per-agent.
// user is the STATE scope (the per-user/per-instance store); the caller resolves
// + authorizes it (RequireUser for the web surfaces, an appliance scope for the
// per-scope variant). loadAgent's seed fallback + the seedOwner allowance below
// keep the ownership gate satisfied when agentID is a template the scope doesn't
// own its own copy of.
func (T *OrchestrateApp) handleAgentFacts(w http.ResponseWriter, r *http.Request, user, agentID string) {
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
		facts := ListMemoryFacts(udb, ns)
		notes := make([]string, 0, len(facts))
		for _, f := range facts {
			notes = append(notes, f.Note)
		}
		c := memoryModeCopy(a.MemoryMode)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"notes": notes,
			"framing": map[string]string{
				"block_header": c.Header,
				"block_intro":  c.Intro,
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
			// Pass the worker chat so the admin path resolves contradictions the
			// same way the LLM's store_fact does — a corrected note supersedes the
			// stale one it replaces instead of coexisting as a contradiction.
			StoreMemoryFact(udb, ns, n, T.WorkerChat)
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
