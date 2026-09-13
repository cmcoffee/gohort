// scribeUserData exposes the per-user document library to the admin
// reassign / purge flow (core.UserDataHandler). Registered once T.DB is wired.
package scribe

import (
	"errors"

	. "github.com/cmcoffee/gohort/core"
)

type scribeUserData struct {
	app *Scribe
}

// scribeUserTables are the per-user tables that hold what a user wrote: the
// documents and their revision trails. The active-document marker and rules
// are conveniences, not content, and are left behind.
var scribeUserTables = []string{guidesTable, revisionsTable}

func (h *scribeUserData) AppName() string { return "scribe" }

func (h *scribeUserData) Describe(uid string) UserDataSummary {
	sum := UserDataSummary{
		AppName: "scribe",
		Counts:  map[string]int{},
		Actions: []string{"reassign", "purge"},
	}
	udb := UserDB(h.app.DB, uid)
	if udb == nil {
		return sum
	}
	articles, guides := 0, 0
	for _, g := range listGuides(udb) {
		if g.isArticle() {
			articles++
		} else {
			guides++
		}
	}
	sum.Counts["guides"] = guides
	sum.Counts["articles"] = articles
	return sum
}

// Reassign moves every document (and its revisions) from one user's store to
// another's, re-stamping the owner. A shared document's entry in the app-wide
// shared index follows it, so readers keep finding it.
func (h *scribeUserData) Reassign(from, to string) error {
	src := UserDB(h.app.DB, from)
	dst := UserDB(h.app.DB, to)
	if src == nil || dst == nil {
		return errors.New("invalid user")
	}
	for _, k := range src.Keys(guidesTable) {
		var g Guide
		if !src.Get(guidesTable, k, &g) {
			continue
		}
		g.Owner = to
		dst.Set(guidesTable, k, g)
		src.Unset(guidesTable, k)
		var rl guideRevisions
		if src.Get(revisionsTable, k, &rl) {
			dst.Set(revisionsTable, k, rl)
			src.Unset(revisionsTable, k)
		}
		if g.Shared {
			SetSharedOwner(h.app.DB, sharedGuidesIndex, g.ID, to, true)
		}
	}
	return nil
}

func (h *scribeUserData) Anonymize(uid string) error {
	return ErrUserDataActionNotSupported
}

// Purge deletes a user's documents outright, including each one's research
// collection and any shared-index entry.
func (h *scribeUserData) Purge(uid string) error {
	udb := UserDB(h.app.DB, uid)
	if udb == nil {
		return errors.New("invalid user")
	}
	for _, k := range udb.Keys(guidesTable) {
		deleteGuide(udb, uid, k)
		SetSharedOwner(h.app.DB, sharedGuidesIndex, k, "", false)
	}
	for _, tbl := range scribeUserTables {
		for _, k := range udb.Keys(tbl) {
			udb.Unset(tbl, k)
		}
	}
	return nil
}
