// memory_search.go — the owner-facing "find the memory that's steering my
// agent" surface. Two modes behind one endpoint:
//
//	GET  /api/agents/{id}/memsearch?q=<query>&mode=grep    — literal sweep
//	GET  /api/agents/{id}/memsearch?q=<query>&mode=recall  — recall preview
//	DELETE /api/agents/{id}/memsearch?id=<kind:ref>        — delete one hit
//
// grep answers "what is STORED that mentions X": a case-insensitive substring
// sweep across every per-agent memory store — pinned facts, Reference-Memory
// findings, knowledge chunks, cortex observations, and the Working-notes
// block. recall answers the sharper debugging question, "what does the agent
// SEE when it thinks about X": it runs the agent's own recall pipeline
// (same embeddings, same ranking, same layer gating) read-only and returns
// the ranked hits with provenance. Storage can look clean while recall keeps
// surfacing one poisoned item above everything else — grep finds what you
// already suspect; recall preview finds what you don't.
//
// Every hit carries the same id grammar recall/forget use (fact:… mem:…
// doc:… span:… plus cortex:<nano> and notes:block for the stores recall
// doesn't front), so the DELETE route can dispatch on prefix and reuse the
// existing removal paths.
package orchestrate

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// memSearchItem is one search hit, shaped for the Memory modal's list.
type memSearchItem struct {
	Layer     string `json:"layer"` // pinned | finding | knowledge | history | cortex | notes
	ID        string `json:"id"`    // kind:ref — pass back to DELETE when deletable
	Title     string `json:"title,omitempty"`
	Text      string `json:"text"`
	Date      string `json:"date,omitempty"`
	Deletable bool   `json:"deletable"`
	Note      string `json:"note,omitempty"` // why it's not deletable / what delete does
}

const memSearchPerLayerCap = 20

// handleAgentMemorySearch serves GET (search) and DELETE (remove one hit) for
// one agent's memory. Registered under handleAgentOne's action switch, so the
// caller is already authenticated as the owning user.
func (T *OrchestrateApp) handleAgentMemorySearch(w http.ResponseWriter, r *http.Request, user, agentID string) {
	udb := UserDB(T.DB, user)
	if udb == nil {
		http.Error(w, "no user store", http.StatusInternalServerError)
		return
	}
	rec, ok := loadAgent(udb, agentID)
	if !ok {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		T.memSearchDelete(w, r, user, udb, rec)
	case http.MethodGet:
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			http.Error(w, "q is required", http.StatusBadRequest)
			return
		}
		mode := strings.TrimSpace(r.URL.Query().Get("mode"))
		var items []memSearchItem
		var note string
		if mode == "recall" {
			items, note = T.memSearchRecall(user, udb, rec, q)
		} else {
			items = T.memSearchGrep(user, udb, rec, q)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mode": mode, "items": items, "note": note,
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// memSearchGrep sweeps every stored layer for a literal (case-insensitive)
// match. Deliberately NOT semantic — this is the "I know the word, find every
// row that contains it" mode. History spans are left to recall mode (they are
// vector-indexed conversation folds, not an enumerable store).
func (T *OrchestrateApp) memSearchGrep(user string, udb Database, rec AgentRecord, q string) []memSearchItem {
	needle := strings.ToLower(q)
	match := func(parts ...string) bool {
		for _, p := range parts {
			if strings.Contains(strings.ToLower(p), needle) {
				return true
			}
		}
		return false
	}
	var items []memSearchItem

	// Pinned facts (Explicit Memory).
	n := 0
	for _, f := range ListMemoryFacts(udb, factsNamespace(rec.ID)) {
		if !match(f.Note) || n >= memSearchPerLayerCap {
			continue
		}
		n++
		date := f.Updated
		if date.IsZero() {
			date = f.Created
		}
		items = append(items, memSearchItem{
			Layer: "pinned", ID: "fact:" + f.ID, Text: f.Note,
			Date: fmtMemDate(date), Deletable: true,
		})
	}

	// Reference-Memory findings + knowledge chunks — one sweep over the
	// agent's vector rows, split by provenance, deduped per parent ReportID
	// (chunks of one finding/doc collapse to a single row).
	if VectorDB != nil {
		prefix := agentKnowledgePrefix(user, rec.ID)
		seen := map[string]bool{}
		nFind, nKnow := 0, 0
		for _, key := range VectorDB.Keys(EmbeddedChunks) {
			var c EmbeddedChunk
			if !VectorDB.Get(EmbeddedChunks, key, &c) {
				continue
			}
			if !strings.HasPrefix(c.Source, prefix) || seen[c.ReportID] {
				continue
			}
			if !match(c.Text, c.Title, c.Section) {
				continue
			}
			seen[c.ReportID] = true
			title := strings.TrimSpace(c.Title)
			if title == "" {
				title = strings.TrimSpace(strings.TrimPrefix(c.Section, "## "))
			}
			if chunkProvenance(c.Source, c.ReportID) == "derived" {
				if nFind >= memSearchPerLayerCap {
					continue
				}
				nFind++
				items = append(items, memSearchItem{
					Layer: "finding", ID: "mem:" + c.ReportID, Title: title,
					Text: memSnippet(c.Text), Date: c.Date, Deletable: true,
				})
			} else {
				if nKnow >= memSearchPerLayerCap {
					continue
				}
				nKnow++
				items = append(items, memSearchItem{
					Layer: "knowledge", ID: "doc:" + c.ReportID, Title: title,
					Text: memSnippet(c.Text), Date: c.Date, Deletable: true,
					Note: "Deletes the whole document (all its chunks) from this agent's knowledge.",
				})
			}
		}
	}

	// Cortex observations — the standing thread's report cards. These were
	// the classic poison ("tool X permanently broken" noted once, steering
	// every later fire), so they're searchable and individually removable.
	if rec.Cortex {
		if sess, ok := loadChatSession(udb, rec.ID, cortexSessionID(rec.ID)); ok {
			n := 0
			for _, m := range sess.Messages {
				if strings.TrimSpace(m.ReportFrom) == "" {
					continue // ordinary turn, not an observation card
				}
				if !match(m.Content, m.ReportFrom) || n >= memSearchPerLayerCap {
					continue
				}
				n++
				items = append(items, memSearchItem{
					Layer: "cortex", ID: "cortex:" + strconv.FormatInt(m.Created.UnixNano(), 10),
					Title: m.ReportFrom, Text: memSnippet(m.Content),
					Date: fmtMemDate(m.Created), Deletable: true,
				})
			}
		}
	}

	// Working notes — a single rewritable block; surfaced when it matches so
	// a stale "the API is broken, use fetch_url" note is findable here too.
	if notes := agentOperatingNotes(udb, rec); strings.TrimSpace(notes.Text) != "" && match(notes.Text) {
		items = append(items, memSearchItem{
			Layer: "notes", ID: "notes:block", Text: memSnippet(notes.Text),
			Date: fmtMemDate(notes.UpdatedAt), Deletable: true,
			Note: "Deleting clears the whole Working-notes block (it is one rewritable note).",
		})
	}
	return items
}

// memSearchRecall runs the agent's own recall pipeline read-only and parses
// the rendered block back into items. Reusing the real pipeline (not a
// parallel implementation) is the point: the preview is exactly what a turn
// would inject, including layer gating, relevance floors, dedup, and recency
// re-ranking. The renderer's line grammar is stable ("- [tag] …" bullet, an
// indented "id: kind:ref" line, indented snippet lines), so the parse is a
// few prefix checks rather than a format contract.
func (T *OrchestrateApp) memSearchRecall(user string, udb Database, rec AgentRecord, q string) ([]memSearchItem, string) {
	turn := &chatTurn{app: T, agent: rec, user: user, udb: udb}
	rendered, err := turn.recallSearch(q, map[string]any{"k": float64(16)})
	if err != nil {
		return nil, err.Error()
	}
	items := parseRecallRendered(rendered)
	if len(items) == 0 {
		return nil, rendered // the no-match message (or a tombstone note)
	}
	return items, ""
}

// parseRecallRendered converts recall's rendered hit list into structured
// items. Unrecognized lines attach to the current item's text, so renderer
// affordances (staleness notes, age hints) survive as part of the snippet.
func parseRecallRendered(rendered string) []memSearchItem {
	var items []memSearchItem
	var cur *memSearchItem
	flush := func() {
		if cur != nil {
			cur.Text = strings.TrimSpace(cur.Text)
			items = append(items, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- [") {
			flush()
			rest := strings.TrimPrefix(trimmed, "- [")
			tagEnd := strings.IndexByte(rest, ']')
			if tagEnd < 0 {
				continue
			}
			cur = &memSearchItem{
				Layer: rest[:tagEnd],
				Title: strings.TrimSpace(rest[tagEnd+1:]),
			}
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(trimmed, "id: ") {
			id := strings.TrimSpace(strings.TrimPrefix(trimmed, "id: "))
			cur.ID = id
			kind, _, _ := splitRecallID(id)
			switch kind {
			case "fact", "mem", "doc":
				cur.Deletable = true
				if kind == "doc" {
					cur.Note = "Deletes the whole document (all its chunks) from this agent's knowledge."
				}
			case "span":
				cur.Note = "History is the immutable record of the conversation — not deletable."
			}
			continue
		}
		if trimmed != "" {
			if cur.Text != "" {
				cur.Text += "\n"
			}
			cur.Text += trimmed
		}
	}
	flush()
	// For [pinned] hits the renderer puts the note text on the bullet line
	// itself (there is no separate snippet), so Title IS the content.
	for i := range items {
		if items[i].Text == "" {
			items[i].Text, items[i].Title = items[i].Title, ""
		}
	}
	return items
}

// memSearchDelete removes one hit by its id, dispatching on the kind prefix
// to the same removal paths the forget tool / knowledge admin use.
func (T *OrchestrateApp) memSearchDelete(w http.ResponseWriter, r *http.Request, user string, udb Database, rec AgentRecord) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	i := strings.IndexByte(id, ':')
	if i <= 0 {
		http.Error(w, "unrecognized id", http.StatusBadRequest)
		return
	}
	kind, ref := id[:i], id[i+1:]
	ok := false
	switch kind {
	case "fact":
		ok = ForgetMemoryFactByID(udb, factsNamespace(rec.ID), ref)
	case "mem":
		turn := &chatTurn{app: T, agent: rec, user: user, udb: udb}
		msg, err := turn.forgetFindingByReportID(ref)
		ok = err == nil && strings.HasPrefix(msg, "Forgot")
	case "doc":
		// Whole-document delete, scoped to this (user, agent) — mirrors
		// handleAgentKnowledgeSourceDelete's sweep.
		if VectorDB != nil {
			prefix := agentKnowledgePrefix(user, rec.ID)
			for _, key := range VectorDB.Keys(EmbeddedChunks) {
				var c EmbeddedChunk
				if VectorDB.Get(EmbeddedChunks, key, &c) && c.ReportID == ref && strings.HasPrefix(c.Source, prefix) {
					VectorDB.Unset(EmbeddedChunks, key)
					ok = true
				}
			}
		}
	case "cortex":
		ok = tombstoneCortexObservation(udb, rec.ID, ref)
	case "notes":
		_, over := SaveOperatingNotes(udb, factsNamespace(rec.ID), "")
		ok = !over
	default:
		http.Error(w, "that layer is not deletable here", http.StatusBadRequest)
		return
	}
	if !ok {
		http.Error(w, "not found (it may already be gone)", http.StatusNotFound)
		return
	}
	Log("[orchestrate.memsearch] user=%q agent=%s deleted %s", user, rec.ID, id)
	w.WriteHeader(http.StatusNoContent)
}

// tombstoneCortexObservation blanks one observation card in the cortex
// thread, matched by its Created nanosecond timestamp. Blank-in-place rather
// than remove: the cortex thread may carry a compaction cursor
// (SummarizedThrough is a message INDEX), and deleting a message would shift
// every later index out from under it. A blanked card stops rendering in the
// standing-activity block (empty ReportFrom is skipped) and stops matching
// searches, which is all "delete" needs to mean here.
func tombstoneCortexObservation(udb Database, agentID, nanoRef string) bool {
	nano, err := strconv.ParseInt(nanoRef, 10, 64)
	if err != nil {
		return false
	}
	sess, ok := loadChatSession(udb, agentID, cortexSessionID(agentID))
	if !ok {
		return false
	}
	for i := range sess.Messages {
		m := &sess.Messages[i]
		if strings.TrimSpace(m.ReportFrom) == "" || m.Created.UnixNano() != nano {
			continue
		}
		m.ReportFrom = ""
		m.ReportKind = ""
		m.Content = "(observation removed by owner)"
		_, err := saveChatSession(udb, sess)
		return err == nil
	}
	return false
}

func memSnippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func fmtMemDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}
