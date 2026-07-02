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

// ListReferencing returns the user's guides that have (srcKind, srcItemID) among
// their attached reference sources — e.g. guides with a given servitor System
// linked. This is what lets servitor offer "push this finding into a guide that's
// already about this system." Implements core.ReferencingDocumentTarget.
func (g *guideTarget) ListReferencing(user, srcKind, srcItemID string) []DocItem {
	udb := UserDB(g.app.DB, user)
	if udb == nil {
		return nil
	}
	out := []DocItem{}
	for _, gd := range listGuides(udb) {
		for _, ref := range gd.References {
			if ref.Kind == srcKind && ref.ItemID == srcItemID {
				out = append(out, DocItem{ID: gd.ID, Title: firstNonEmpty(gd.Title, "Untitled guide")})
				break
			}
		}
	}
	return out
}

// Append lands pushed content in the user's guide docID. For an EXISTING guide it
// hands the content to the Guide Author to INCORPORATE coherently (merge into a
// section or add one, in the guide's voice) rather than blind-appending a raw
// block — so a pushed finding reads as part of the document. For a NEW guide
// (docID empty) there's nothing to weave into, so it just creates the guide with
// the content as its first section. Writes land on the OWNER's store as revisions
// (undoable); edit access is enforced (owner, or shared-for-edit).
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
		sectionTitle = "Finding"
	}

	// New guide — nothing to incorporate into; create it with the finding as its
	// first section.
	if strings.TrimSpace(docID) == "" {
		gd := saveGuideRev(udb, Guide{
			ID:    newID(),
			Title: firstNonEmpty(strings.TrimSpace(newDocTitle), "Untitled guide"),
			Owner: user,
		}, "Created guide")
		gd.Sections = append(gd.Sections, Section{
			ID: newID(), Title: sectionTitle, Markdown: markdown, Order: gd.nextOrder(),
		})
		saveGuideRev(udb, gd, "Added section (pushed): "+sectionTitle)
		return gd.ID, nil
	}

	// Existing guide — verify access, then let the Guide Author weave it in.
	resolved, owner, ownerUDB, ok := resolveGuide(g.app.DB, udb, user, docID)
	if !ok {
		return "", fmt.Errorf("guide %q not found", docID)
	}
	if !(CanManageShared(user, owner, false) || resolved.sharedForEdit()) {
		return "", fmt.Errorf("you don't have edit access to that guide")
	}
	if orch := findOrchestrate(); orch != nil {
		if _, err := g.app.runIncorporate(ctx, udb, orch, user, docID, sectionTitle, markdown, resolved.Private); err != nil {
			return "", err
		}
		return docID, nil
	}
	// Orchestrate unavailable — fall back to a raw append so the push still lands.
	resolved.Sections = append(resolved.Sections, Section{
		ID: newID(), Title: sectionTitle, Markdown: markdown, Order: resolved.nextOrder(),
	})
	saveGuideRev(ownerUDB, resolved, "Added section (pushed): "+sectionTitle)
	return docID, nil
}
