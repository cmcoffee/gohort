package orchestrate

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// Settling an actionable card, durably.
//
// A persisted UIBlock that asks the user for a decision — credential_setup
// ("this credential needs its secret"), credential_update ("apply this config
// diff?") — used to settle only in the browser. The click's effect landed on
// the credential store; the block on the session record never changed. So the
// next session load replayed the very same card, buttons live, as though
// nothing had been answered — and clicking again re-POSTed a change that was
// already applied.
//
// Two halves fix that, because a card can be answered two ways:
//
//   - IN the chat: the renderer POSTs here, and the note it would have shown
//     is stamped onto the block. Replay renders that note instead of buttons.
//   - OUT of band: the admin finishes the credential in Admin > APIs and never
//     touches the card. Nothing POSTs, so the stamp is DERIVED at load from
//     the subject's live state (settleResolvedBlocks). The card that asks for
//     a secret has no business asking once the secret exists, regardless of
//     where it was typed.
//
// Display-only blocks (html_artifact, link_hint) have nothing to settle and
// are left alone by both halves.

// blockResolveRequest is the renderer's POST body. Note is the line the card
// shows in place of its controls once answered.
type blockResolveRequest struct {
	Note string `json:"note"`
}

// handleSessionBlockResolve stamps Resolved onto one persisted block.
// Ownership is the session's: udb is the caller's own database and the block
// must already exist on the session, so a caller can only settle their own
// cards. Idempotent — re-resolving overwrites the note and answers 204, which
// is what a double-click or a replayed-then-clicked card produces.
func (T *OrchestrateApp) handleSessionBlockResolve(w http.ResponseWriter, r *http.Request, udb Database, user, agentID, sid, blockID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req blockResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}
	note := strings.TrimSpace(req.Note)
	if note == "" {
		note = "Answered."
	}
	if len(note) > 240 {
		note = note[:240] + "…"
	}
	s, ok := loadChatSession(udb, agentID, sid)
	if !ok {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	found := false
	for i := range s.UIBlocks {
		if s.UIBlocks[i].ID == blockID {
			s.UIBlocks[i].Resolved = note
			found = true
		}
	}
	if !found {
		// Not an error worth surfacing: a card can legitimately outlive its
		// stored block (a live emission the session never persisted, a block
		// swept by an upsert). The user's click already did its real work at
		// the endpoint the card posted to; there is simply nothing to stamp.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if _, err := saveChatSession(udb, s); err != nil {
		Log("[orchestrate.blocks] resolve %s on session %s failed: %v", blockID, sid, err)
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	Log("[orchestrate.blocks] user %q resolved block %s on session %s", user, blockID, sid)
	w.WriteHeader(http.StatusNoContent)
}

// settleResolvedBlocks derives Resolved for cards whose subject can be settled
// somewhere other than the card. Applied to the COPY the session GET returns —
// it never rewrites the stored record, so a credential that is later deleted
// and re-drafted gets its card back rather than staying permanently settled.
//
// Only credential_setup qualifies today: its whole ask is "this credential has
// no secret yet", and Secure() knows whether that is still true. A
// credential_update card's ask (does the user WANT this diff?) has no such
// witness — an unanswered one stays actionable until it's answered.
//
// owner scopes the lookup the same way the card itself does: a card marked
// owned="1" names a credential in the user's OWN namespace (Extensions > My
// API credentials), everything else names a global one (Admin > APIs).
func settleResolvedBlocks(blocks []UIBlock, owner string) []UIBlock {
	for i := range blocks {
		b := &blocks[i]
		if b.Resolved != "" || b.Type != "credential_setup" {
			continue
		}
		name := strings.TrimSpace(b.Title)
		if name == "" {
			continue
		}
		var (
			c  SecureCredential
			ok bool
		)
		if b.Data["owned"] == "1" {
			c, ok = Secure().LoadUser(owner, name)
		} else {
			c, ok = Secure().Load(name)
		}
		// Only the POSITIVE case settles here. A miss is ambiguous — the
		// credential was deleted, or the store isn't attached yet — and
		// settling on it would silently retire live cards on a store that
		// simply wasn't ready. Deletion is already handled where it can be
		// told apart: the card's own Set up click 404s and says so.
		//
		// Drafts land disabled with no secret; enabling one is the act of
		// finishing it. Still disabled means the card's ask stands.
		if ok && !c.Disabled {
			b.Resolved = "Configured — the credential is live."
		}
	}
	return blocks
}
