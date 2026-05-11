// Framework-based Answer page using core/ui's ChatPanel. Mounted at
// /answer/. The Q&A flow maps onto a chat session: the question becomes
// the first user message and the orchestrator's answer (plus sources)
// becomes the first assistant message. Subsequent followups extend the
// session normally. Legacy hand-written UI lives at /answer/legacy.

package answer

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func (T *AnswerAgent) handleNewAnswerPage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	page := ui.Page{
		Title:     "Answer",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "100%",
		Sections: []ui.Section{
			{
				NoChrome: true,
				Body: ui.ChatPanel{
					SessionsListURL:  "api/sessions",
					SessionLoadURL:   "api/sessions/{id}",
					SessionDeleteURL: "api/sessions/delete/{id}",
					SendURL:          "api/send",
					EmptyText:        "Ask a question to start. Each answer becomes a saved session you can follow up on.",
					// Field overrides: AnswerRecord uses Question / Date,
					// not Title / LastAt.
					SessionTitleField:  "Question",
					SessionLastAtField: "Date",
					Markdown:           true,
					BulkSelect:         true,
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}
