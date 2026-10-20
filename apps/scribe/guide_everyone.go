package scribe

// Sharing a guide with everyone is an administrator's call.
//
// Naming people or a team is the owner's: they know who gets it. Everyone
// reaches every account in the deployment, including ones made later, so the
// share list's Everyone files a request (promotion.AskEveryone), at the level
// it asks for: everyone can view, or everyone can edit. Lowering it, or taking
// it out, is the owner's and applies at once. A guide shared with everyone
// before this keeps it.

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/promotion"
)

// guidePromotionKind is the request kind; the name is "<guide id>:<level>".
const guidePromotionKind = "guide"

// Everyone's levels on a guide, rising.
const (
	guideLevelView = "view"
	guideLevelEdit = "edit"
)

// everyoneLevel is what everyone may do with g: "", view or edit.
func (g Guide) everyoneLevel() string {
	switch {
	case !g.Shared:
		return ""
	case g.sharedForEdit():
		return guideLevelEdit
	}
	return guideLevelView
}

// setEveryoneLevel stores level as Shared and ShareMode.
func (g *Guide) setEveryoneLevel(level string) {
	g.Shared, g.ShareMode = level != "", ""
	if level == guideLevelEdit {
		g.ShareMode = ShareModeEdit
	}
}

// guideEveryoneAsk is g's Everyone as a request, before the list says what it
// wants.
func guideEveryoneAsk(owner string, g Guide, admin bool) promotion.EveryoneAsk {
	return promotion.EveryoneAsk{
		Kind: guidePromotionKind, Owner: owner, ID: g.ID,
		Levels: []string{guideLevelView, guideLevelEdit},
		Has:    g.everyoneLevel(), Admin: admin,
	}
}

// registerGuidePromotion wires what an administrator's Approve does: the
// guide reaches everyone at the level asked for.
func (T *Scribe) registerGuidePromotion() {
	promotion.RegisterApprover(guidePromotionKind, T.approveEveryone)
}

func (T *Scribe) approveEveryone(owner, name string) error {
	id, level := promotion.ParseEveryoneRequest(name)
	udb := UserDB(T.DB, owner)
	g, ok := loadGuide(udb, id)
	if !ok {
		return Error("no guide " + id + " owned by " + owner)
	}
	g.setEveryoneLevel(level)
	saveGuide(udb, g)
	indexGuideShares(T.DB, owner, id, g.Shared, g.AllowedUsers)
	return nil
}

// handleShareCandidates is the Share picker's choices, Everyone worded for
// where this guide stands with it (?id=).
func (T *Scribe) handleShareCandidates(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	granted, pending, admin := false, false, RequestIsAdmin(r)
	if g, owner, _, ok := resolveGuide(T.DB, udb, user, r.URL.Query().Get("id")); ok {
		granted = g.Shared
		pending = promotion.EveryonePending(AuthDB(), guideEveryoneAsk(owner, g, admin)) != ""
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(ShareCandidatesFor(user, true, EveryoneChoiceLabel(granted, pending, admin)))
}
