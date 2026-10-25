// The per-reply "↗ Guide" save button: append an assistant reply to one of the
// user's guides that has THIS appliance/repo attached as a source — the UI
// counterpart to the push_to_guide agent tool. A guide not yet linked can be
// linked from here (or a new one made for the system), which attaches the
// system to its Sources first. Writes through the generic core DocumentTarget
// seam, so servitor never imports the guides package.
package servitor

import (
	"context"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/textutil"
)

// GET  ?appliance_id=<id>  → {guides:[{id,title}], others:[{id,title}]}:
// the guides that reference this system, and the user's other guides, which
// can be linked from here.
// POST {appliance_id, guide_id, section_title, text, link, new_title} → {ok, guide_id}
// link=true attaches the system to guide_id's Sources first; an empty
// guide_id with new_title makes a new guide for it.
func (T *Servitor) handlePushToGuide(c *jsonapi.Call) (any, error) {
	userID := c.User
	aid := strings.TrimSpace(c.R.URL.Query().Get("appliance_id"))
	// Guides that list this appliance/repo as a source (kind "system").
	guides := ListDocumentsReferencing(userID, "guide", "system", aid)
	if guides == nil {
		guides = []DocItem{}
	}
	linked := map[string]bool{}
	for _, g := range guides {
		linked[g.ID] = true
	}
	others := []DocItem{}
	for _, g := range ListDocuments(userID, "guide") {
		if !linked[g.ID] {
			others = append(others, g)
		}
	}
	return map[string]any{"guides": guides, "others": others}, nil
}

// handlePushToGuideSave writes a reply into a guide, linking this system to it
// or making a new guide for it first when asked.
func (T *Servitor) handlePushToGuideSave(c *jsonapi.Call, body pushToGuideReq) (any, error) {
	userID, udb := c.User, c.UDB
	text := strings.TrimSpace(body.Text)
	if text == "" {
		return nil, jsonapi.BadRequest("text is required")
	}
	aid := strings.TrimSpace(body.ApplianceID)
	gid := strings.TrimSpace(body.GuideID)
	newTitle := strings.TrimSpace(body.NewTitle)
	if gid == "" && newTitle == "" {
		return nil, jsonapi.BadRequest("guide_id is required")
	}
	// Linking names this system on a guide, so it has to be one this
	// person can reach, as the guide's own Sources picker lists.
	if body.Link || gid == "" {
		if a, _, _, ok := T.resolveAppliance(userID, udb, aid); !ok || a.ID == "" {
			return nil, jsonapi.NotFound("that system is not one of yours")
		}
	}
	section := strings.TrimSpace(body.SectionTitle)
	if section == "" {
		section = deriveSectionTitle(text)
	}
	if gid == "" {
		// A new guide for this system: the reply is its first section,
		// and the system its first source.
		id, err := AppendToDocument(context.Background(), userID, "guide", "", newTitle, section, text)
		if err != nil {
			return nil, jsonapi.Status(http.StatusInternalServerError, "could not make the guide: "+err.Error())
		}
		if err := docs.AttachDocumentReference(userID, "guide", id, "system", aid); err != nil {
			return nil, jsonapi.Status(http.StatusInternalServerError, "made the guide, but could not link this system to it: "+err.Error())
		}
		return map[string]any{"ok": true, "guide_id": id}, nil
	}
	// Guard: the chosen guide must reference this appliance, or be linked
	// to it now, which only the person's own guides can be.
	if !docInList(ListDocumentsReferencing(userID, "guide", "system", aid), gid) {
		if !body.Link {
			return nil, jsonapi.BadRequest("that guide doesn't list this system as a source")
		}
		if !docInList(ListDocuments(userID, "guide"), gid) {
			return nil, jsonapi.NotFound("that guide is not one of yours")
		}
		if err := docs.AttachDocumentReference(userID, "guide", gid, "system", aid); err != nil {
			return nil, jsonapi.Status(http.StatusInternalServerError, "could not link this system to the guide: "+err.Error())
		}
		Log("[servitor] %s linked system %s to guide %s", userID, aid, gid)
	}
	if _, err := AppendToDocument(context.Background(), userID, "guide", gid, "", section, text); err != nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, "push failed: "+err.Error())
	}
	return map[string]any{"ok": true, "guide_id": gid}, nil
}

// pushToGuideReq is what handlePushToGuideSave is posted.
type pushToGuideReq struct {
	ApplianceID  string `json:"appliance_id"`
	GuideID      string `json:"guide_id"`
	SectionTitle string `json:"section_title"`
	Text         string `json:"text"`
	// Link attaches this system to guide_id's Sources before writing.
	Link bool `json:"link"`
	// NewTitle, with no guide_id, makes a new guide for this system.
	NewTitle string `json:"new_title"`
}

// docInList reports whether id is among items.
func docInList(items []DocItem, id string) bool {
	for _, d := range items {
		if d.ID == id {
			return true
		}
	}
	return false
}

// deriveSectionTitle picks a section title from the reply's first non-empty line
// (markdown heading marks stripped, capped), falling back to "Finding".
func deriveSectionTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = textutil.Clip(line, 70)
		return line
	}
	return "Finding"
}
