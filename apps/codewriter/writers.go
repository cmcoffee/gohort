// Writers — the "which kind of codewriter am I talking to" level.
//
// CodeWriter had three ways to ground an answer, all per-request and all
// advisory: a pasted Context block, Collections RAG, and attached reference
// sources. Each is a checkbox you tick for one question. None of them says
// "everything written here follows THIS", which is what you actually want when
// a body of code has house conventions, a schema, an API shape — the things
// that are true for every script in that world and that nobody wants to re-
// attach, re-paste, or re-explain per question.
//
// A Writer is that missing level: a named configuration, chosen from a picker
// the way an agent is chosen in orchestrate, holding a KNOWLEDGE FOUNDATION
// supplied by an agent that already knows the domain.
//
// Two decisions in here are worth stating rather than inferring:
//
//   - The foundation is a STANDING BRIEF, written once by the agent and stored,
//     not a live query per turn. Asking the agent on every keystroke-to-answer
//     round trip would put a full agent run in front of every code request, and
//     the injected text would change per turn — so nothing caches, and answers
//     drift for reasons the user cannot see. A stored brief is fast, stable,
//     and READABLE: you can open it and see exactly what the writer is being
//     told. Refresh re-asks when the underlying knowledge has moved.
//
//   - The brief is AUTHORITATIVE, not advisory. The three existing channels are
//     reference the model may quietly ignore, which is the behavior a foundation
//     exists to replace. Where the foundation and the model's general knowledge
//     disagree, the foundation wins — and the writer SAYS the two disagreed
//     rather than silently picking one, because a silent pick is how a house
//     convention gets quietly replaced by whatever is most common on the public
//     internet.
package codewriter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const writerTable = "codewriter_writers"

// WriterRecord is one configured writer.
type WriterRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Desc string `json:"description,omitempty"`
	// AgentID names the agent that supplies this writer's foundation. Stored
	// rather than resolved-and-copied so a Refresh re-asks the agent as it is
	// NOW, not as it was when the writer was created.
	AgentID string `json:"agent_id,omitempty"`
	// Brief is the foundation itself: what the agent said the writer needs to
	// know. Editable by hand — an agent's first draft is a starting point, and
	// the person who owns the conventions gets the last word on them.
	Brief string `json:"brief,omitempty"`
	// BriefDate stamps when the brief was last written, so a foundation that
	// has quietly aged past its subject is visible instead of assumed current.
	BriefDate string `json:"brief_date,omitempty"`
	Date      string `json:"date,omitempty"`
}

// foundationBlock renders a writer's brief for the system prompt.
//
// Returns "" for a writer with no brief yet, rather than an empty heading that
// announces a foundation and then supplies none — a model reading that has been
// told authoritative rules exist and shown none of them, which is worse than
// saying nothing.
func (wr WriterRecord) foundationBlock() string {
	brief := strings.TrimSpace(wr.Brief)
	if brief == "" {
		return ""
	}
	name := strings.TrimSpace(wr.Name)
	if name == "" {
		name = "this writer"
	}
	var b strings.Builder
	b.WriteString("\n\n## Knowledge foundation for ")
	b.WriteString(name)
	b.WriteString("\n\n")
	b.WriteString("This is the established background for everything written here — the conventions, interfaces, and facts this code lives inside. Treat it as AUTHORITATIVE. Where it and your general knowledge of the language, library, or ecosystem disagree, follow the foundation: it describes how things actually are here, and your general knowledge describes how they usually are elsewhere.\n\n")
	b.WriteString("Say so when they disagree. A one-line note that the usual approach would be X and the foundation calls for Y is what lets somebody correct a stale foundation; silently picking one leaves the reader unable to tell that a choice was made at all. Do NOT invent detail the foundation does not cover — say what is missing and ask, rather than filling the gap with a plausible convention.\n\n")
	b.WriteString(brief)
	b.WriteString("\n")
	return b.String()
}

// loadWriter reads one writer, or false.
func loadWriter(udb Database, id string) (WriterRecord, bool) {
	var rec WriterRecord
	if udb == nil || strings.TrimSpace(id) == "" {
		return rec, false
	}
	ok := udb.Get(writerTable, id, &rec)
	return rec, ok
}

// handleWriters is the writer list + create/update endpoint.
func (T *CodeWriterAgent) handleWriters(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if udb == nil {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "[]")
			return
		}
		var items []WriterRecord
		for _, key := range udb.Keys(writerTable) {
			var rec WriterRecord
			if udb.Get(writerTable, key, &rec) {
				items = append(items, rec)
			}
		}
		if items == nil {
			items = []WriterRecord{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)

	case http.MethodPost:
		if udb == nil {
			http.Error(w, "no database", http.StatusInternalServerError)
			return
		}
		var req WriterRecord
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if req.ID == "" {
			req.ID = UUIDv4()
		} else if prior, found := loadWriter(udb, req.ID); found {
			// An edit that carries no brief keeps the stored one. The editor
			// posts the whole record, and a form that happens not to include the
			// brief field would otherwise silently erase a foundation somebody
			// spent an agent run and a round of hand-editing on.
			if strings.TrimSpace(req.Brief) == "" {
				req.Brief, req.BriefDate = prior.Brief, prior.BriefDate
			}
		}
		req.Date = time.Now().Format(time.RFC3339)
		udb.Set(writerTable, req.ID, req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(req)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleWriter serves one writer: GET, DELETE, or POST .../refresh to have the
// bound agent (re)write the foundation brief.
func (T *CodeWriterAgent) handleWriter(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/writer/")
	refresh := false
	if strings.HasSuffix(id, "/refresh") {
		id, refresh = strings.TrimSuffix(id, "/refresh"), true
	}
	if id == "" || udb == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	rec, found := loadWriter(udb, id)
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch {
	case refresh && r.Method == http.MethodPost:
		brief, err := T.buildFoundationBrief(r.Context(), user, rec)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		rec.Brief = brief
		rec.BriefDate = time.Now().Format(time.RFC3339)
		udb.Set(writerTable, rec.ID, rec)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rec)
	case r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rec)
	case r.Method == http.MethodDelete:
		udb.Unset(writerTable, id)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// buildFoundationBrief asks the writer's agent to state what someone writing
// code in its domain has to know.
//
// Dispatched to the AGENT rather than answered by codewriter's own model on
// purpose: the whole point is that the agent already carries the background —
// its prompt, its memory, its attached sources and collections. Asking the bare
// model would produce a confident summary of nothing in particular.
func (T *CodeWriterAgent) buildFoundationBrief(ctx context.Context, user string, wr WriterRecord) (string, error) {
	agentID := strings.TrimSpace(wr.AgentID)
	if agentID == "" {
		return "", fmt.Errorf("this writer has no agent set — pick one first, or write the foundation by hand")
	}
	subject := strings.TrimSpace(wr.Desc)
	if subject == "" {
		subject = strings.TrimSpace(wr.Name)
	}
	// Asks for the FOUNDATION, not a summary of the agent. The distinction
	// matters: a summary describes what the agent knows about, and what a writer
	// needs is the subset that changes how code gets written — the shapes, the
	// names, the rules, the gotchas.
	prompt := "Write the knowledge foundation for someone writing code in this domain: " + subject + ".\n\n" +
		"This becomes standing context for every script written here, so include only what CHANGES HOW CODE IS WRITTEN: " +
		"interfaces and their exact shapes, schemas and field names, naming and structural conventions, " +
		"required patterns, and the mistakes people actually make. Be specific — real names, real signatures, real values. " +
		"Skip background prose, history, and anything a competent developer would already assume.\n\n" +
		"State only what you actually know from your own material. Where you don't have something, say so in a short " +
		"\"Not covered here\" list at the end rather than filling it in — a foundation that guesses is worse than one with holes, " +
		"because the code written on top of it will look correct."
	out, err := AskAgent(ctx, user, agentID, prompt)
	if err != nil {
		return "", fmt.Errorf("the agent could not write the foundation: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("the agent returned an empty foundation — check that %q has knowledge attached", agentID)
	}
	return strings.TrimSpace(out), nil
}
