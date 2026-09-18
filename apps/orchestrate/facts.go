// store_fact / forget_fact / search_facts tools — the LLM-in-band
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

// factsBlockName is the header text (sans "## ") of the always-in-prompt
// Explicit Memory block for this agent's memory mode, so tool descriptions
// and results name the heading the model actually sees ("Lessons learned" /
// "Saved notes" / "Shortcuts"), not a generic label that isn't there.
func (t *chatTurn) factsBlockName() string {
	return strings.TrimPrefix(memoryModeCopy(t.agent.MemoryMode).Header, "## ")
}

// storeFactNote is the shared write path for the Explicit Memory (always-
// in-prompt) layer. storeFactToolDef routes here, and so does the unified
// `remember` tool's pin=true branch — one place owns dedup, supersession,
// and the relevance gate so the two surfaces can't drift.
// claimDomainArg reads the model's declaration. Unrecognized or absent leaves
// it unknown, which the store then classifies from the note — a wrong-looking
// value is not worth failing a write over.
func claimDomainArg(args map[string]any) ClaimDomain {
	switch strings.ToLower(strings.TrimSpace(stringArg(args, "domain"))) {
	case "self":
		return ClaimSelf
	case "world":
		return ClaimWorld
	}
	return ClaimDomainUnknown
}

func (t *chatTurn) storeFactNote(note string, domain ClaimDomain) (string, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return "", errors.New("note is required")
	}
	// The write half of the clean room. Checked before anything else so a
	// refused session never runs the dedup, relevance and supersession machinery
	// on a note that cannot be stored anyway.
	if t.incognitoSession() {
		return "", t.refuseDurableMemoryInCleanRoom("not saved", "a pinned note would outlive the conversation that made it")
	}
	// A pinned note naming a handle that does not survive is wrong from the
	// moment it is written, and goes on being read as authoritative.
	//
	// image#N is a POSITION in the ring: it silently comes to mean a different
	// picture as new ones arrive. media#N belongs to one turn and is gone by
	// the next. Neither ages out of memory on its own — facts are durable by
	// design and leave only via the hard cap's LRU eviction — so "Reference
	// images confirmed: … (image#9), … (media#10)" sits in every future prompt
	// pointing at pictures that are no longer those pictures.
	//
	// Refused rather than rewritten, with the durable form named: keeping the
	// picture under a NAME makes image#<name> valid indefinitely, which is what
	// the note was reaching for.
	if refs := TransientImageRefs(note); len(refs) > 0 {
		return "", fmt.Errorf("not saved: %s %s a handle that will stop resolving. image#N is a POSITION in the recent list (it means a different picture as new ones arrive) and media#N lasts only the turn it arrived on, but this note is kept forever, so it would point at the wrong pictures within a few turns while still reading as fact. If these pictures matter later, keep each one under a name first (image action=\"keep\", name=…), then write the note using image#<name>, which stays valid. If the note is really about this conversation rather than something durable, don't pin it at all",
			strings.Join(refs, ", "), map[bool]string{true: "are", false: "is"}[len(refs) > 1])
	}
	// Pass the agent's memory mode + worker chat so: (a) a changed fact
	// ("moved to Austin") supersedes the stale one instead of coexisting as
	// a contradiction, and (b) in chatbot mode the relevance gate rejects
	// ephemeral chatter before it bloats the always-in-prompt block.
	res := StoreMemoryFactP(t.udb, factsNamespace(t.agent.ID), note, FactWritePolicy{
		Mode: t.agent.MemoryMode,
		Chat: t.app.WorkerChat,
		// The model authored this note from the session, not a human directly and
		// not a tool result — observed, so it does NOT license grounded specifics
		// (an LLM-recorded figure may be a stale prior). Human-entered facts get
		// MemSourceUserStated on the admin path instead.
		Source: MemSourceObserved,
		// What the model just heard, declared by the only thing that was
		// there. Unknown falls through to the store's own classifier.
		Domain: domain,
		// WHO said it. Empty on a one-to-one with the owner, which leaves the
		// note reading exactly as it did before. In a room it is the difference
		// between "Dana prefers texts before 8pm" and a preference the agent
		// applies to everyone in the thread.
		Speaker:        strings.TrimSpace(t.requesterName),
		SpeakerHandle:  strings.TrimSpace(t.requesterHandle),
		SpeakerIsOwner: t.requesterOwnerHandle,
	})
	switch res.Reason {
	case FactDuplicate:
		return fmt.Sprintf("Already remembered (deduped): %q. Skipping.", res.Fact.Note), nil
	case FactRejected:
		return "Not saved. That reads as a passing detail rather than a durable fact worth injecting into every future turn. If it's a lasting preference, identity fact, or standing instruction, rephrase it as one and try again.", nil
	}
	msg := fmt.Sprintf("Stored: %q. Will appear in every future turn's %q block.", res.Fact.Note, t.factsBlockName())
	if len(res.Superseded) > 0 {
		dropped := make([]string, len(res.Superseded))
		for i, s := range res.Superseded {
			dropped[i] = fmt.Sprintf("%q", s.Note)
		}
		msg += fmt.Sprintf(" Superseded %d now-stale fact(s): %s.", len(dropped), strings.Join(dropped, ", "))
	}
	return msg, nil
}

// explainRetiredHole builds a recall message for a query that matched no LIVE
// fact but is relevant to one or more retired (tombstoned) ones — so the model
// learns it once knew this and why the note is gone, instead of silently
// answering from a stale prior. Matching is semantic-first via
// SearchRetiredFacts (tombstones keep their vectors) with a term-overlap
// fallback — the old full-query-substring test required the entire question to
// appear inside the note, which a multi-word query essentially never
// satisfied, leaving the feature dead exactly where supersession had reworded
// the fact. Capped at three. Returns "" when no retired fact is relevant.
func explainRetiredHole(db Database, namespace, query string) string {
	matches := SearchRetiredFacts(db, namespace, query, 3)
	if len(matches) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("No live fact matches, but you previously stored (now retired):\n")
	for _, f := range matches {
		fmt.Fprintf(&b, "- %q: %s", f.Note, RetireReasonLabel(f.Reason))
		if !f.RetiredAt.IsZero() {
			fmt.Fprintf(&b, " on %s", f.RetiredAt.Format("2006-01-02"))
		}
		if f.Successor != "" {
			if succ, ok := GetMemoryFactByID(db, namespace, f.Successor); ok {
				fmt.Fprintf(&b, "; current note: %q", succ.Note)
			}
		}
		b.WriteString(".\n")
	}
	b.WriteString("\nTreat retired notes as historical, not current: verify before relying on them.")
	return b.String()
}

// applyFactListEdit reconciles the memory editor's POSTed list with the live
// rows by DIFF — not wipe+restore. Re-storing an untouched row would (a)
// promote a model-authored (Observed) note to user_stated just because the
// user hit Save, silently licensing it as a grounding source; (b) reset
// Created/AsOf/volatility, faking freshness; (c) run the supersession judge
// between two notes the user deliberately KEPT, so a plain Save could
// tombstone one of them; and (d) leave surviving tombstones pointing at
// deleted successor IDs. The GET sent each Note verbatim, so an unedited row
// round-trips byte-identical — exact match identifies the untouched set. An
// edited row is a delete + add, which is the honest shape of that change.
func applyFactListEdit(udb Database, ns string, notes []string, chat FactChatFunc) {
	existing := ListMemoryFacts(udb, ns)
	posted := make(map[string]bool, len(notes))
	var added []string
	for _, n := range notes {
		n = strings.TrimSpace(n)
		if n == "" || posted[n] {
			continue
		}
		posted[n] = true
		added = append(added, n)
	}
	for _, f := range existing {
		note := strings.TrimSpace(f.Note)
		if posted[note] {
			// Round-tripped unchanged: keep the row as-is, provenance intact.
			for i, n := range added {
				if n == note {
					added = append(added[:i], added[i+1:]...)
					break
				}
			}
			continue
		}
		// Absent from the POSTed list = deliberately removed in the editor.
		ForgetMemoryFactByID(udb, ns, f.ID)
	}
	for _, n := range added {
		// Pass the worker chat so the admin path resolves contradictions the
		// same way the LLM's store_fact does — a corrected note supersedes the
		// stale one it replaces instead of coexisting as a contradiction. These
		// are human-entered, so Source = user_stated: they DO count as a
		// grounding-eligible source (SourcedFactCorpus), unlike model-authored notes.
		StoreMemoryFactP(udb, ns, n, FactWritePolicy{Chat: chat, Source: MemSourceUserStated})
	}
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
	a, ok := T.memoryAgent(r, udb, user, agentID)
	if !ok {
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
		applyFactListEdit(udb, ns, body.Notes, T.WorkerChat)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
