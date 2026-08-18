// Writers — the "which kind of writing am I doing" level.
//
// CodeWriter could ground an answer three ways, all per-request and all
// advisory: a pasted Context block, Collections RAG, and an attached reference
// source. Each is a checkbox you tick for ONE question. None of them says
// "everything written here draws on THIS", which is what a body of code with
// house conventions, a schema, or an API shape needs, and which nobody wants to
// re-attach per question.
//
// A Writer is that missing level: a named configuration, chosen from a bar the
// way an agent is chosen in Agents, holding the SOURCES its work stands on.
//
// The sources are the whole mechanism, deliberately. A writer does not carry a
// summary of its domain written in advance — a summary cannot answer "which
// column holds the region" for a query nobody had asked yet. It carries the
// things that can be ASKED: reference sources, and among them agents, whose
// Fetch is a question and whose answer is to the question actually asked (see
// orchestrate's agent_reference_source.go). So the writer's material is
// consulted per request, and its tools ride the turn so the model can ask again
// when one answer raises the next question.
//
// Everything below therefore reuses the reference path CodeWriter already had
// for per-turn attachments. The only thing a Writer adds is that the selection
// is standing rather than re-picked, and that it applies to every turn.
package codewriter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const writerTable = "codewriter_writers"

// WriterRecord is one saved MODE: a snapshot of the panel's settings under a
// name.
//
// Not an entity with a life of its own — every field is a control the panel
// already has, and there is nothing here to manage anywhere else. You dial the
// panel in by working, save what it is currently set to, and click the name to
// get it back.
//
// It exists because those controls reset to their defaults on reload. Without
// it, "Kiteworks SQL queries" meant re-picking the same handful of settings at
// the start of every session, and the cost of not bothering was a turn answered
// without the material it needed — silently, since nothing in a reply says
// which sources were attached.
type WriterRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Desc string `json:"description,omitempty"`
	// Lang preselects the editor's language for NEW work. A snippet that was
	// saved in a language keeps it — opening an existing file must not silently
	// relabel it, and the language drives the mode prompts.
	Lang string `json:"lang,omitempty"`
	// Sources are the reference selections every turn under this mode draws on
	// — agents, systems, document spaces. Stored as selections rather than
	// resolved text so the material is read LIVE: a mode set up in March
	// answers from what its sources know today.
	Sources ReferenceSelections `json:"references,omitempty"`
	// Collections are the document-collection IDs searched for grounding.
	Collections []string `json:"collections,omitempty"`
	Date        string   `json:"date,omitempty"`
}

// sourceBlock is the standing note that says what this writer's material IS and
// how far it binds.
//
// The retrieved text itself rides the user message (FetchReferences), not this
// — retrieval changes per query, and a per-turn system prompt re-prefills the
// whole thread on every turn. What belongs here is the part that does not
// change: that a body of knowledge governs this work, and that guessing past it
// is not acceptable.
func (wr WriterRecord) sourceBlock() string {
	if len(wr.Sources) == 0 {
		return ""
	}
	name := strings.TrimSpace(wr.Name)
	if name == "" {
		name = "this writer"
	}
	return "\n\n## What " + name + " writes against\n\n" +
		"This work draws on an established body of knowledge, attached to this writer and read live. Treat what it says as AUTHORITATIVE: where it and your general knowledge of the language, library or ecosystem disagree, follow it — it describes how things actually are here, and your general knowledge describes how they usually are elsewhere. Say so when they disagree, in a line, so a stale source can be corrected; silently picking one leaves the reader unable to tell a choice was made.\n\n" +
		"Its tools are in your catalog. ASK before writing anything whose correctness turns on a detail you would otherwise guess — an exact table or column name, a type, a unit, a required parameter, an existing helper. Guessing produces work that looks right and is wrong, which is the expensive kind. If the answer isn't there, say which part is missing rather than filling it in.\n"
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
		// Posted by the panel: its own control state, in its own shapes. No
		// translation layer, because there is no form in between — you
		// configure the panel and save what it is currently set to.
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
		}
		req.Date = time.Now().Format(time.RFC3339)
		udb.Set(writerTable, req.ID, req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(req)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleWriter serves one writer: GET or DELETE.
func (T *CodeWriterAgent) handleWriter(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/writer/")
	if id == "" || udb == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	rec, found := loadWriter(udb, id)
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rec)
	case http.MethodDelete:
		udb.Unset(writerTable, id)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
