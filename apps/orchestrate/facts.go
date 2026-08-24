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
	"time"

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

// storeFactToolDef lets the model record a discrete note it's
// learned. Dedup is automatic — same or similar notes get folded
// into the existing entry rather than accumulating.
func (t *chatTurn) storeFactToolDef() AgentToolDef {
	desc := "Record a SHORT note (Explicit Memory) that needs to be ACCOUNTED FOR EVERY TIME a new question is raised — instructions, preferences, durable user/context facts. Pre-injected into your system prompt on every future turn; the LLM sees them automatically without having to search.\n\n**Use store_fact when**: the note shapes how you should respond to ANY future question (user preferences, recurring constraints, identity facts, project context). Right examples: \"user prefers metric units\", \"all responses go to a vegetarian audience\", \"production API needs JWT in X-Auth header\".\n\n**Use memory(save) instead when**: the finding is complicated reference material you MIGHT need to recall later for specific questions — API specs, website navigation steps, recipes, configuration details. Those are pull-only via memory(search), not always-in-prompt. If you're tempted to dump research findings into store_fact, use memory(save) instead.\n\nThe framework dedupes automatically (same wording OR semantically similar = skipped). Quantity here costs prompt tokens forever (these inject on every turn), so keep total around a screen's worth.\n\nDistinct from `knowledge_search` (read-only over user-uploaded files) and `memory(search)` (your own prior memory(save) findings, pull-only)."
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
				"domain": {
					Type:        "string",
					Enum:        []string{"self", "world"},
					Description: "Whether the person telling you this SETTLES it. \"self\" = about them — a preference, their name, their goals, how they want you to work; they are the authority and there is nothing to check it against. \"world\" = true or false independently of who said it — a server, a library, a version, a price, how some system behaves; being told it is not the same as having checked it, and recall marks these so a passing remark is not quoted back later as established fact. When a note is both, ask what it ASSERTS: \"prefers the API to return JSON\" is a preference (self); \"the API returns JSON\" is a claim about the API (world).",
				},
			},
			Required: []string{"note"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			return t.storeFactNote(stringArg(args, "note"), claimDomainArg(args))
		},
	}
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
		return "", fmt.Errorf("not saved: %s %s a handle that will stop resolving. image#N is a POSITION in the recent list (it means a different picture as new ones arrive) and media#N lasts only the turn it arrived on, but this note is kept forever — so it would point at the wrong pictures within a few turns while still reading as fact. If these pictures matter later, keep each one under a name first (image action=\"keep\", name=…), then write the note using image#<name>, which stays valid. If the note is really about this conversation rather than something durable, don't pin it at all",
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

// forgetFactToolDef removes one fact by its 1-based index in the
// rendered list. The LLM reads its numbered notes in the system
// prompt's always-in-prompt facts block and references the matching index
// here, plus a verbatim quote from the note — the index alone can go
// stale mid-turn (a store_fact can trigger supersession or a sweep
// that shifts the list), and a stale index deletes the wrong note.
func (t *chatTurn) forgetFactToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "forget_fact",
			Description: fmt.Sprintf("Delete a previously-stored fact by its index in the %q block in your system prompt. Use when a stored fact is OBSOLETE (no longer applies — user moved jobs, project changed names, preference flipped). Index is 1-based and matches the number you see in the prompt. ALWAYS also pass quote — a distinctive phrase copied verbatim from that note — so the right note is deleted even if the list shifted since you read it.", t.factsBlockName()),
			Parameters: map[string]ToolParam{
				"index": {
					Type:        "integer",
					Description: fmt.Sprintf("1-based index of the note to delete, matching the number prefix in your %q block.", t.factsBlockName()),
				},
				"quote": {
					Type:        "string",
					Description: "A distinctive phrase copied verbatim from the note you're deleting. Protects against the numbered list having shifted since you read it — on a mismatch, nothing is deleted.",
				},
			},
			Required: []string{"index"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			// Refused in a clean room, and this one is not symmetry for its own
			// sake. The index is documented as "matching the number prefix in
			// your facts block" — and an incognito prompt has no facts block,
			// so the number would be aimed at a list this turn was never shown.
			// A blind index into somebody's durable memory, on the destructive
			// tool, is the worst of the three to leave open.
			if t.incognitoSession() {
				return "", t.refuseDurableMemoryInCleanRoom("not deleted", "your prompt carries no facts block, so an index here points into a list this session cannot see")
			}
			idx := intFromArgs(args, "index")
			if idx < 1 {
				return "", errors.New("index is required and must be >= 1")
			}
			quote := strings.TrimSpace(stringArg(args, "quote"))
			removed, reason, ok := ForgetMemoryFactByIndexQuoted(t.udb, factsNamespace(t.agent.ID), idx, quote)
			if !ok {
				return "", fmt.Errorf("nothing deleted: %s", reason)
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
			Description: fmt.Sprintf("Search your stored Explicit Memory notes by meaning (\"what's the deploy header?\" finds \"production API needs JWT in X-Auth header\"), or omit the query to list every note. Returns numbered notes (1-based) matching the %q block order for the full-list case. The always-in-prompt %q block already shows recent notes; reach for this when that block has grown large and you want to pinpoint a specific note (e.g. before forget_fact) rather than re-reading the whole block.", t.factsBlockName(), t.factsBlockName()),
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
	}
	b.WriteString("\nTreat retired notes as historical, not current — verify before relying on them.")
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
		applyFactListEdit(udb, ns, body.Notes, T.WorkerChat)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
