// The "Push to guide" toolbar action: take the LATEST session's answer for an
// appliance (what the user just looked up) and append it as a section to one of
// their guides — the manual counterpart to the push_to_guide agent tool. Both
// write through the generic core DocumentTarget seam, so servitor never imports
// the guides package.
package servitor

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

//	GET  ?appliance_id=<id>  → {guides:[{id,title}], default_title, has_answer}
//	POST {appliance_id, guide, section_title} → {ok, guide, created}
func (T *Servitor) handlePushToGuide(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}

	// latestAnswer returns the last assistant message of the appliance's most
	// recent session (sessions live in the requester's udb) plus that session's
	// name, used as the default section title.
	latestAnswer := func(applianceID string) (answer, title string) {
		applianceID = strings.TrimSpace(applianceID)
		sessions := listSessions(udb, applianceID)
		if len(sessions) == 0 {
			return "", ""
		}
		title = sessions[0].Name
		if s, ok := loadSession(udb, applianceID, sessions[0].ID); ok {
			for _, m := range s.Messages {
				if m.Role == "assistant" && strings.TrimSpace(m.Content) != "" {
					answer = m.Content
				}
			}
		}
		return answer, title
	}

	switch r.Method {
	case http.MethodGet:
		answer, title := latestAnswer(r.URL.Query().Get("appliance_id"))
		type g struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		guides := []g{}
		for _, d := range ListDocuments(userID, "guide") {
			guides = append(guides, g{ID: d.ID, Title: d.Title})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"guides":        guides,
			"default_title": title,
			"has_answer":    strings.TrimSpace(answer) != "",
		})

	case http.MethodPost:
		var body struct {
			ApplianceID  string `json:"appliance_id"`
			Guide        string `json:"guide"`
			SectionTitle string `json:"section_title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		answer, defTitle := latestAnswer(body.ApplianceID)
		if strings.TrimSpace(answer) == "" {
			http.Error(w, "nothing to push yet — ask the system something first", http.StatusBadRequest)
			return
		}
		guide := strings.TrimSpace(body.Guide)
		if guide == "" {
			http.Error(w, "guide name is required", http.StatusBadRequest)
			return
		}
		section := strings.TrimSpace(body.SectionTitle)
		if section == "" {
			section = defTitle
		}
		if section == "" {
			section = "Finding"
		}
		// Append to a matching guide by name, or create a new one.
		docID, newTitle := "", ""
		for _, d := range ListDocuments(userID, "guide") {
			if strings.EqualFold(strings.TrimSpace(d.Title), guide) {
				docID = d.ID
				break
			}
		}
		if docID == "" {
			newTitle = guide
		}
		if _, err := AppendToDocument(context.Background(), userID, "guide", docID, newTitle, section, answer); err != nil {
			http.Error(w, "push failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "guide": guide, "created": newTitle != ""})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
