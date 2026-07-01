package enginseer

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// handleChatPage renders the app: an "Add repository" form plus the investigator
// chat, whose left rail is the user's repositories (CONTEXT mode) — pick one and
// ask. The active repo's id rides on every send under "repo_id".
func (T *Enginseer) handleChatPage(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// Repository picker options (dropdown beside the input — servitor's shape).
	repoOpts := []ui.SelectOption{{Value: "", Label: "— select repository —"}}
	if udb != nil {
		for _, key := range udb.Keys(repoTable) {
			var rec Repo
			if udb.Get(repoTable, key, &rec) {
				repoOpts = append(repoOpts, ui.SelectOption{Value: rec.ID, Label: rec.Name})
			}
		}
	}
	page := ui.Page{
		Title:     "Enginseer",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "100%",
		// getRepoID + the shared Memory modal (code map / reference / facts).
		ExtraHeadHTML: enginseerHeadScript + repoMemoryModalScript,
		Sections: []ui.Section{
			{
				// Small "+ Add repository" button — pops the form as a modal so
				// the chat below stays full-bleed.
				Body: ui.ModalButton{
					Label:    "＋ Add repository",
					Title:    "Add a repository",
					Subtitle: "Cloned and stored encrypted; the plaintext clone is discarded after ingest.",
					Body: ui.FormPanel{
						PostURL:     "api/repos",
						SubmitLabel: "Add",
						Invalidate:  []string{"api/repos"},
						Fields: []ui.FormField{
							{Field: "url", Label: "Git URL", Type: "text", Placeholder: "https://github.com/owner/repo"},
							{Field: "branch", Label: "Branch", Type: "text", Placeholder: "default branch if blank"},
							{Field: "token", Label: "Access token", Type: "password", Placeholder: "for private repos (optional)"},
						},
					},
				},
			},
			{
				NoChrome: true,
				Body: ui.AgentLoopPanel{
					ListURL:   "api/chat/sessions",
					LoadURL:   "api/chat/sessions/{id}",
					DeleteURL: "api/chat/sessions/{id}",
					ListTitle: "Conversations",
					SendURL:   "api/chat/send",
					CancelURL: "api/chat/cancel",
					Markdown:  true,
					ExtraFields: []ui.ChatField{
						{Name: "repo_id", Label: "Repository", Type: "select", OptionPairs: repoOpts},
					},
					Actions: []ui.ToolbarAction{
						{Label: "✨ Map Repo", Title: "Walk the codebase and build its map in the background", Method: "client", URL: "enginseer_map_repo", Variant: "primary"},
						{Label: "Memory", Title: "View the code map, findings, and facts for the selected repository", Method: "client", URL: "enginseer_repo_memory"},
						{Label: "Refresh", Title: "Re-clone the selected repository to pick up new code", Method: "client", URL: "enginseer_refresh"},
						{Label: "Clear", Title: "Clear the conversation and activity panes", Method: "client", URL: "enginseer_clear"},
						{Label: "Copy session", Title: "Copy the full session as markdown", Method: "client", URL: "copy_session"},
					},
					EmptyText:   "Pick a repository, then ask — \"how does auth work?\", \"where is user data stored?\", \"what generates this log line?\"",
					Placeholder: "Ask the Enginseer…",
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}

// handleChatSend binds the investigator to the SELECTED repository (its id rides
// on the send body as "repo_id") and runs it through orchestrate's chat path
// with that repo's read/search tools injected.
func (T *Enginseer) handleChatSend(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// Peek repo_id without consuming the body (PublicHandleSend reads it next).
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var peek struct {
		RepoID string `json:"repo_id"`
	}
	_ = json.Unmarshal(body, &peek)
	rec, found := loadRepo(udb, peek.RepoID)
	if !found {
		http.Error(w, "pick a repository first", http.StatusBadRequest)
		return
	}
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "orchestrate not initialized", http.StatusServiceUnavailable)
		return
	}
	agent, ok := orch.LookupAppAgent(user, repoInvestigatorAgentID)
	if !ok {
		http.Error(w, "investigator agent unavailable", http.StatusServiceUnavailable)
		return
	}
	orch.PublicHandleSendWithAppTools(w, r, agent, repoTools(user, rec.ID))
}

// dispatchChat forwards cancel / session routes to orchestrate's PublicHandle*.
func (T *Enginseer) dispatchChat(w http.ResponseWriter, r *http.Request, kind, sid string) {
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "orchestrate not initialized", http.StatusServiceUnavailable)
		return
	}
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agent, ok := orch.LookupAppAgent(user, repoInvestigatorAgentID)
	if !ok {
		http.Error(w, "investigator agent unavailable", http.StatusServiceUnavailable)
		return
	}
	switch kind {
	case "cancel":
		orch.PublicHandleCancel(w, r, agent)
	case "sessions":
		orch.PublicHandleSessionList(w, r, agent.ID)
	case "session-one":
		orch.PublicHandleSessionOne(w, r, agent.ID, sid)
	default:
		http.NotFound(w, r)
	}
}
