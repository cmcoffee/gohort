// Framework-based codewriter page using core/ui's CodeEditorPanel.
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
				Body: ui.CodeEditorPanel{
					ListURL:   "api/snippets",
					LoadURL:   "api/snippet/{id}",
					SaveURL:   "api/snippets",
					DeleteURL: "api/snippet/{id}",
					// The chat is the Code Writer's, the chat every agent has (chat.go).
					Chat: codeWriterChat.Panel(ui.AgentLoopPanel{
						SendURL:      "chat/send",
						ListTitle:    "Past sessions",
						NewLabel:     "New session",
						ListPosition: "modal",
						BulkSelect:   true,
						Markdown:     true,
						LockActivity: true,
						EmptyText:    "Ask for a script or query, or for a change to the one in the editor. Changes come back as a diff to apply.",
						Placeholder:  "Ask the Code Writer…",
					}),
					SuggestNameURL:      "api/suggest-name",
					RevisionsListURL:    "api/revisions/{id}",
					RevisionLoadURL:     "api/revision/{id}",
					ValuesListURL:       "api/values",
					ValueURL:            "api/value/{id}",
					ContextsListURL:     "api/contexts",
					ContextURL:          "api/context/{id}",
					CollectionsListURL:  "api/collections",
					CollectionsNoun:     "Knowledge",
					ReferenceSourcesURL: "api/reference-sources",
					ProfilesListURL:     "api/writers",
					ProfilesSaveURL:     "api/writers",
					ProfilesDeleteURL:   "api/writer/{id}",
					ProfilesNoun:        "Mode",
					Languages:           codeWriterLanguages,
					Templates:           MarkdownDocTemplates,
					TemplatesListURL:    "api/templates",
					TemplateURL:         "api/template/{id}",
					AssistURL:           "api/assist",
					RulesURL:            "api/rules",
					EmptyText:           "No snippets yet. Click + New or chat with the LLM to generate one.",
					PlaceholderName:     "Snippet name…",
					PlaceholderCode:     "Write or paste code here. Save it for later, or chat with the LLM to generate one.\n\nUse {{NAME}} placeholders for reusable values.",
					PlaceholderCtx:      "Reference context: table schemas, API docs, notes. Sent to the LLM alongside the code on every chat turn.",
				},
			},
		},
		// The editor's clipboard, resize and import helpers. The diff a
		// proposal opens in ships with the runtime on every page.
		ExtraHeadHTML: "<script>" + editor.UtilsJS() + "</script>",
	}
	page.ServeHTTP(w, r)
}

// codeWriterLanguages is the editor's language list, defined ONCE.
//
// The Modes page builds its language options from this same slice: a mode that
// could preselect a language the editor has no option for would leave the
// dropdown showing something else, and the mismatch is invisible until somebody
// notices the wrong mode prompts firing.
var codeWriterLanguages = []string{
	"bash", "sql", "python", "powershell", "go", "markdown", "regex", "",
}
