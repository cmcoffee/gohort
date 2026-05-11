// Framework-based codewriter page using core/ui's CodeWriterPanel.
// Replaces the legacy hand-rolled cwBody/cwCSS/cwJS that used to
// render the entire desktop UI inline. The component handles the
// snippet sidebar, code editor, lang dropdown, and save/copy/new
// toolbar; chat, diff, variables, and the values/contexts libraries
// are layered in by subsequent milestones.

package codewriter

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/editor"
	"github.com/cmcoffee/gohort/core/ui"
)

func (T *CodeWriterAgent) handleCodeWriterPage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	page := ui.Page{
		Title:     "CodeWriter",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "100%",
		Sections: []ui.Section{
			{
				NoChrome: true,
				Body: ui.CodeWriterPanel{
					ListURL:          "api/snippets",
					LoadURL:          "api/snippet/{id}",
					SaveURL:          "api/snippets",
					DeleteURL:        "api/snippet/{id}",
					ChatURL:          "api/chat",
					SuggestNameURL:   "api/suggest-name",
					RevisionsListURL: "api/revisions/{id}",
					RevisionLoadURL:  "api/revision/{id}",
					ValuesListURL:    "api/values",
					ValueURL:         "api/value/{id}",
					ContextsListURL:  "api/contexts",
					ContextURL:       "api/context/{id}",
					Languages: []string{
						"bash", "sql", "python", "powershell", "go", "regex", "",
					},
					EmptyText:       "No snippets yet. Click + New or chat with the LLM to generate one.",
					PlaceholderName: "Snippet name…",
					PlaceholderCode: "Write or paste code here. Save it for later, or chat with the LLM to generate one.\n\nUse {{NAME}} placeholders for reusable values.",
					PlaceholderCtx:  "Reference context — table schemas, API docs, notes. Sent to the LLM alongside the code on every chat turn.",
					PlaceholderChat: "Discuss with Chat, or click Edit to apply changes.",
				},
			},
		},
		// Pull in the inline diff helper used by Edit-mode chat
		// responses. Both sets of statics are added verbatim into the
		// document head so the renderer's chat handler can call
		// window.editorShowDiff / window.editorDiffStats.
		ExtraHeadHTML: "<style>" + editor.DiffCSS() + "</style>" +
			"<script>" + editor.UtilsJS() + editor.DiffJS() + "</script>",
	}
	page.ServeHTTP(w, r)
}
