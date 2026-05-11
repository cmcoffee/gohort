// Stage-1 chat page using core/ui's ChatPanel. Mounted at /chat/v2
// alongside the legacy /chat. Streaming + tool-call rendering work;
// the heavier features (per-message retry/edit, file attachments,
// Private/Explorer/Voice mode toggles) still live at /chat until
// stage 2 of the migration ports them.

package chat

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func (T *ChatAgent) handleNewChatPage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	page := ui.Page{
		Title:     "Chat",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "100%",
		Sections: []ui.Section{
			{
				// NoChrome — ChatPanel is its own visually-distinct
				// two-pane layout; wrapping it in a section card just
				// makes the sidebar and message column blend together
				// (all three would share --bg-1).
				NoChrome: true,
				Body: ui.ChatPanel{
					SessionsListURL:  "api/sessions",
					SessionLoadURL:   "api/sessions/{id}",
					SessionDeleteURL: "api/sessions/delete/{id}",
					SendURL:          "api/send",
					CancelURL:        "api/cancel",
					ToolsURL:         "api/tools",
					EmptyText:        "Pick a session from the sidebar or start a new one.",
					Voice:            true,
					Attachments:      true,
					Markdown:         true,
					BulkSelect:       true,
					Modes: []ui.ChatMode{
						{
							Label:     "Private",
							Title:     "Disable internet tools (search, fetch, generic API)",
							GetURL:    "api/settings/private",
							PostURL:   "api/settings/private/set",
							Field:     "private_mode",
							SendField: "private_mode",
						},
						{
							Label:     "Explorer",
							Title:     "API Explorer mode: 30-round budget; LLM saves working API patterns as persistent tools",
							GetURL:    "api/settings/explorer",
							PostURL:   "api/settings/explorer/set",
							Field:     "api_explorer_mode",
							SendField: "api_explorer_mode",
						},
					},
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}
