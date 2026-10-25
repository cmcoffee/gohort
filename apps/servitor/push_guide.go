// The per-reply "↗ Guide" save button: append an assistant reply to one of the
// user's guides that has THIS appliance/repo attached as a source — the UI
// counterpart to the push_to_guide agent tool. A guide not yet linked can be
// linked from here (or a new one made for the system), which attaches the
// system to its Sources first. Writes through the generic core DocumentTarget
// seam, so servitor never imports the guides package.
package servitor

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/gohort/core/textutil"
)

// GET  ?appliance_id=<id>  → {guides:[{id,title}], others:[{id,title}]}:
// the guides that reference this system, and the user's other guides, which
// can be linked from here.
// POST {appliance_id, guide_id, section_title, text, link, new_title} → {ok, guide_id}
// link=true attaches the system to guide_id's Sources first; an empty
// guide_id with new_title makes a new guide for it.
func (T *Servitor) handlePushToGuide(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		aid := strings.TrimSpace(r.URL.Query().Get("appliance_id"))
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"guides": guides, "others": others})

	case http.MethodPost:
		var body struct {
			ApplianceID  string `json:"appliance_id"`
			GuideID      string `json:"guide_id"`
			SectionTitle string `json:"section_title"`
			Text         string `json:"text"`
			// Link attaches this system to guide_id's Sources before writing.
			Link bool `json:"link"`
			// NewTitle, with no guide_id, makes a new guide for this system.
			NewTitle string `json:"new_title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		text := strings.TrimSpace(body.Text)
		if text == "" {
			http.Error(w, "text is required", http.StatusBadRequest)
			return
		}
		aid := strings.TrimSpace(body.ApplianceID)
		gid := strings.TrimSpace(body.GuideID)
		newTitle := strings.TrimSpace(body.NewTitle)
		if gid == "" && newTitle == "" {
			http.Error(w, "guide_id is required", http.StatusBadRequest)
			return
		}
		// Linking names this system on a guide, so it has to be one this
		// person can reach, as the guide's own Sources picker lists.
		if body.Link || gid == "" {
			if a, _, _, ok := T.resolveAppliance(userID, udb, aid); !ok || a.ID == "" {
				http.Error(w, "that system is not one of yours", http.StatusNotFound)
				return
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
				http.Error(w, "could not make the guide: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if err := docs.AttachDocumentReference(userID, "guide", id, "system", aid); err != nil {
				http.Error(w, "made the guide, but could not link this system to it: "+err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "guide_id": id})
			return
		}
		// Guard: the chosen guide must reference this appliance, or be linked
		// to it now, which only the person's own guides can be.
		if !docInList(ListDocumentsReferencing(userID, "guide", "system", aid), gid) {
			if !body.Link {
				http.Error(w, "that guide doesn't list this system as a source", http.StatusBadRequest)
				return
			}
			if !docInList(ListDocuments(userID, "guide"), gid) {
				http.Error(w, "that guide is not one of yours", http.StatusNotFound)
				return
			}
			if err := docs.AttachDocumentReference(userID, "guide", gid, "system", aid); err != nil {
				http.Error(w, "could not link this system to the guide: "+err.Error(), http.StatusInternalServerError)
				return
			}
			Log("[servitor] %s linked system %s to guide %s", userID, aid, gid)
		}
		if _, err := AppendToDocument(context.Background(), userID, "guide", gid, "", section, text); err != nil {
			http.Error(w, "push failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "guide_id": gid})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
