package servitor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// --- Chat ---

func (T *Servitor) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		ApplianceID string `json:"appliance_id"`
		SessionID   string `json:"session_id"`
		WorkspaceID string `json:"workspace_id"`
		Message     string `json:"message"`
		History     []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"history"`
		// Optional image attachments — base64-encoded payloads from
		// the paperclip picker. Decoded into Message.Images bytes
		// and attached to the final user turn so the worker LLM
		// can see screenshots (e.g. system error dialogs, GUI
		// state) the user is asking about.
		Images []string `json:"images"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Message == "" && len(req.Images) == 0 {
		http.Error(w, "message or image required", http.StatusBadRequest)
		return
	}
	if req.ApplianceID == "" || udb == nil {
		http.Error(w, "appliance_id required", http.StatusBadRequest)
		return
	}
	// Resolve own OR shared. Sessions stay in the requester's udb; a shared
	// repo's clone/store is read under ownerUser inside runSession.
	appliance, ownerUser, _, found := T.resolveAppliance(userID, udb, req.ApplianceID)
	if !found {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}

	label := appliance.Name + ": " + req.Message
	if len(label) > 80 {
		label = label[:80]
	}

	// Resolve the persisted chat session — create one (titled from this
	// message) on a fresh conversation, or continue the supplied one.
	// The session id doubles as the run id, so it's stable across turns
	// and the client's session_id / EventsURL / cancel / deep-link all
	// key off this single value. The stale-cleanup race from reusing an
	// id across runs is handled by the pointer-guard in
	// LiveSessionMap.ScheduleCleanupAfter.
	sid := ensureSession(udb, appliance.ID, req.SessionID, req.Message)
	ctx, cancel := context.WithCancel(AppContext())
	probeSessions.Register(sid, label, cancel).SetOwner(userID)
	sessionAppliances.Store(sid, appliance.ID)
	ch := make(chan bool, 1)
	confirmChans.Store(sid, pendingConfirm{ch: ch, owner: userID, interactive: true})

	var hist []Message
	for _, h := range req.History {
		if h.Role != "user" && h.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(h.Content) == "" {
			continue
		}
		hist = append(hist, Message{Role: h.Role, Content: h.Content})
	}
	// Decode any image attachments — base64 → bytes — and attach to
	// the final user message so the LLM sees text + images on the
	// same turn. Skips silently on decode failure so a corrupt
	// upload doesn't break the whole chat request.
	var userImages [][]byte
	for _, b64 := range req.Images {
		if strings.TrimSpace(b64) == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err == nil && len(raw) > 0 {
			userImages = append(userImages, raw)
		}
	}
	hist = append(hist, Message{Role: "user", Content: req.Message, Images: userImages})

	// Pre-create the injection queue so /api/inject finds it immediately
	// (the user could plausibly inject before the worker goroutine even
	// installs OnRoundStart). Servitor doesn't gate by owner today —
	// RequireUser in handleInject confirms the requester is logged in,
	// no per-queue cross-check — so the registration leaves Owner empty.
	RegisterInjectionQueue(sid, userID, "")

	go T.runSession(ctx, sid, userID, ownerUser, appliance, ch, hist, udb, false)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"session_id": sid})
}

// handleInject manages mid-flight user notes for an active chat session.
//
//	POST   {id, text}                     → queue a new note. Returns {note_id}.
//	POST   {id, note_id, action:"lock"}   → mark a queued note as being edited.
//	POST   {id, note_id, action:"unlock"} → cancel an in-progress edit.
//	PATCH  {id, note_id, text}            → commit edited text (auto-unlocks).
//	DELETE {id, note_id}                  → remove an unread note.
//
// Notes the orchestrator has already drained can no longer be edited or
// deleted — those return 410 Gone. Edit-locked notes stay in the queue but
// are skipped by Drain so the orchestrator can't grab them mid-edit.
func (T *Servitor) handleInject(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		ID     string `json:"id"`
		NoteID string `json:"note_id"`
		Text   string `json:"text"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	q := LookupInjectionQueue(req.ID)
	if q == nil {
		http.Error(w, "session not found or not interjectable", http.StatusNotFound)
		return
	}
	// Only into your own run, the same check orchestrate's handleInject makes
	// on the same contract (see apps/orchestrate/interjections.go). This copy
	// dropped it, and registered its queues with an empty owner, so any session
	// id was enough: a note pushed here is read by the orchestrator on its next
	// decision, which made it a way to put words in front of somebody else's
	// agent mid-investigation, on an appliance the sender may have no way to
	// reach themselves. The same id also reads and edits the queue, so this
	// sits above the switch and covers every method. An empty Owner now matches
	// nobody rather than everybody.
	if q.Owner == "" || q.Owner != user {
		// Same answer as "no such session": whether someone else is running
		// something is not the caller's business.
		http.Error(w, "session not found or not interjectable", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodPost:
		// Lock/unlock are POST + action so we don't need separate routes.
		switch req.Action {
		case "lock":
			if req.NoteID == "" {
				http.Error(w, "note_id required", http.StatusBadRequest)
				return
			}
			if !q.Lock(req.NoteID) {
				http.Error(w, "note already delivered or not found", http.StatusGone)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "unlock":
			if req.NoteID == "" {
				http.Error(w, "note_id required", http.StatusBadRequest)
				return
			}
			if !q.Unlock(req.NoteID) {
				http.Error(w, "note already delivered or not found", http.StatusGone)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "":
			// Falls through to create-new-note path.
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
		req.Text = strings.TrimSpace(req.Text)
		if req.Text == "" {
			http.Error(w, "text required", http.StatusBadRequest)
			return
		}
		noteID, clean := q.Push(req.Text)
		if !clean {
			emit(req.ID, probeEvent{Kind: "status", Text: "Note queued (oldest dropped — queue at capacity)"})
		} else {
			emit(req.ID, probeEvent{Kind: "status", Text: "Note queued — orchestrator will see it on its next decision."})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"note_id": noteID})
	case http.MethodPatch:
		req.Text = strings.TrimSpace(req.Text)
		if req.NoteID == "" || req.Text == "" {
			http.Error(w, "note_id and text required", http.StatusBadRequest)
			return
		}
		if !q.Update(req.NoteID, req.Text) {
			http.Error(w, "note already delivered or not found", http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if req.NoteID == "" {
			http.Error(w, "note_id required", http.StatusBadRequest)
			return
		}
		if !q.Delete(req.NoteID) {
			http.Error(w, "note already delivered or not found", http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
