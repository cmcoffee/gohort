// guideTarget registers Guides as a core DocumentTarget so OTHER apps (servitor
// today) can push a section INTO one of the user's guides without importing this
// package. It's the write-side counterpart to the reference sources guides pulls
// FROM — same "services reaching into services" seam, opposite direction.
package guides

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

type guideTarget struct{ app *Guides }

func (g *guideTarget) Kind() string  { return "guide" }
func (g *guideTarget) Label() string { return "Guides" }

// List returns the user's own guides as push targets.
func (g *guideTarget) List(user string) []DocItem {
	udb := UserDB(g.app.DB, user)
	if udb == nil {
		return nil
	}
	out := []DocItem{}
	for _, gd := range listGuides(udb) {
		out = append(out, DocItem{ID: gd.ID, Title: firstNonEmpty(gd.Title, "Untitled guide")})
	}
	return out
}

// Append adds a section to the user's guide docID (creating a new guide titled
// newDocTitle when docID is empty). Writes to the guide OWNER's store and records
// a revision, so a pushed section is undoable like any other edit. Enforces edit
// access: the pusher must own the guide (or it must be shared-for-edit).
func (g *guideTarget) Append(ctx context.Context, user, docID, newDocTitle, sectionTitle, markdown string) (string, error) {
	udb := UserDB(g.app.DB, user)
	if udb == nil {
		return "", fmt.Errorf("no store for user")
	}
	markdown = strings.TrimSpace(markdown)
	if markdown == "" {
		return "", fmt.Errorf("content is required")
	}
	sectionTitle = strings.TrimSpace(sectionTitle)
	if sectionTitle == "" {
		sectionTitle = "Untitled section"
	}

	var gd Guide
	var store Database
	if strings.TrimSpace(docID) == "" {
		gd = saveGuideRev(udb, Guide{
			ID:    newID(),
			Title: firstNonEmpty(strings.TrimSpace(newDocTitle), "Untitled guide"),
			Owner: user,
		}, "Created guide")
		store = udb
		docID = gd.ID
	} else {
		resolved, owner, ownerUDB, ok := resolveGuide(g.app.DB, udb, user, docID)
		if !ok {
			return "", fmt.Errorf("guide %q not found", docID)
		}
		if !(CanManageShared(user, owner, false) || resolved.sharedForEdit()) {
			return "", fmt.Errorf("you don't have edit access to that guide")
		}
		gd = resolved
		store = ownerUDB
	}

	gd.Sections = append(gd.Sections, Section{
		ID: newID(), Title: sectionTitle, Markdown: markdown, Order: gd.nextOrder(),
	})
	saveGuideRev(store, gd, "Added section (pushed): "+sectionTitle)
	return docID, nil
}
