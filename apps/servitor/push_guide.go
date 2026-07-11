// The per-reply "↗ Guide" save button: append an assistant reply to one of the
// user's guides that already has THIS appliance/repo attached as a source — the
// UI counterpart to the push_to_guide agent tool. Writes through the generic core
// DocumentTarget seam, so servitor never imports the guides package.
package servitor

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// GET  ?appliance_id=<id>  → {guides:[{id,title}]}  — only guides that reference this system
// POST {appliance_id, guide_id, section_title, text} → {ok}
func (T *Servitor) handlePushToGuide(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		aid := strings.TrimSpace(r.URL.Query().Get("appliance_id"))
		// Only guides that list this appliance/repo as a source (kind "system").
		guides := ListDocumentsReferencing(userID, "guide", "system", aid)
		if guides == nil {
			guides = []DocItem{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"guides": guides})

	case http.MethodPost:
		var body struct {
			ApplianceID  string `json:"appliance_id"`
			GuideID      string `json:"guide_id"`
			SectionTitle string `json:"section_title"`
			Text         string `json:"text"`
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
		if strings.TrimSpace(body.GuideID) == "" {
			http.Error(w, "guide_id is required", http.StatusBadRequest)
			return
		}
		// Guard: the chosen guide must actually reference this appliance — the
		// button only offers those, so this rejects a forged guide_id.
		linked := false
		for _, g := range ListDocumentsReferencing(userID, "guide", "system", strings.TrimSpace(body.ApplianceID)) {
			if g.ID == body.GuideID {
				linked = true
				break
			}
		}
		if !linked {
			http.Error(w, "that guide doesn't list this system as a source", http.StatusBadRequest)
			return
		}
		section := strings.TrimSpace(body.SectionTitle)
		if section == "" {
			section = deriveSectionTitle(text)
		}
		if _, err := AppendToDocument(context.Background(), userID, "guide", body.GuideID, "", section, text); err != nil {
			http.Error(w, "push failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
		if len(line) > 70 {
			line = line[:70] + "…"
		}
		return line
	}
	return "Finding"
}
