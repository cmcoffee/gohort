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

// Refreshing a card that VIEWS live state, instead of replaying a snapshot.
//
// A privilege_grant block is persisted with its rows serialized into Data at
// the moment an authoring tool saved the agent. Everything else about that
// agent's permissions kept moving — the card's own Apply, the Permissions pane,
// the agent editor — and the block never did. So opening the session a day
// later replayed the authoring-time answer as though it were the current one:
// tools shown as "ask" that had since been allowed, capability toggles drawn
// unchecked that were on, and an Apply button that would write all of it back.
//
// The card is a VIEW (see privilege_card.go), so the fix is to make the served
// copy tell the truth rather than to stop serving it. Policy is re-derived from
// the live AutoApproveTools and the flags are recomputed from the live record —
// both from the record in hand, with no registry lookups, so a session load
// pays nothing for it. The stored block is left alone.
//
// Consequential-ness is NOT re-derived: whether a tool would stop and ask is
// the authoring-time classification (it needs a ToolSession this path does not
// have), so a row that ran freely then still reads "runs freely" now. The
// EDITABLE half — which is the half that goes stale and the half Apply writes —
// is exactly what gets refreshed.
func refreshPrivilegeBlocks(udb Database, blocks []UIBlock) []UIBlock {
	for i := range blocks {
		b := &blocks[i]
		if b.Resolved != "" || b.Type != "privilege_grant" {
			continue
		}
		agentID := strings.TrimSpace(b.Data["agent_id"])
		if agentID == "" {
			continue
		}
		rec, ok := loadAgent(udb, agentID)
		if !ok {
			// Nothing left to grant. Settling rather than hiding keeps the
			// record of what was once decided here, without controls that
			// would 404 on click.
			b.Resolved = "This agent no longer exists — nothing to grant."
			continue
		}
		approved := map[string]bool{}
		for _, t := range rec.AutoApproveTools {
			approved[strings.TrimSpace(t)] = true
		}
		var tools []privilegeGrant
		if json.Unmarshal([]byte(b.Data["tools"]), &tools) == nil {
			for j := range tools {
				if tools[j].Policy == "auto" {
					continue // not editable; nothing to re-derive
				}
				if approved[strings.TrimSpace(tools[j].Name)] {
					tools[j].Policy = "allow"
				} else {
					tools[j].Policy = "ask"
				}
			}
			if enc, err := json.Marshal(tools); err == nil {
				b.Data["tools"] = string(enc)
			}
		}
		if enc, err := json.Marshal(privilegeFlagRows(rec)); err == nil {
			b.Data["flags"] = string(enc)
		}
	}
	return blocks
}

// PublicHandleBlockResolve is the landing an app routes its AgentLoopPanel's
// BlockResolveURL to, so a card the user has ANSWERED stays answered.
//
// A `kind:"block"` card has two lifetimes and only one of them ends at the
// click. The DOM one does; the stored one does not — so without this, an
// actionable card replays on every session load with its controls live again,
// over a decision that was already made and applied. Anvil's edit cards carry
// a Revert button, which is exactly that shape: revert once, reload, and the
// same card offers to revert an edit that is already back.
//
// The agent id is the caller's, so a card can only be settled inside a
// conversation the caller owns.
func (T *OrchestrateApp) PublicHandleBlockResolve(w http.ResponseWriter, r *http.Request, agentID, sid, blockID string) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleSessionBlockResolve(w, r, udb, user, agentID, sid, blockID)
}
