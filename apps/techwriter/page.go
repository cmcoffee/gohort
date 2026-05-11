// Stage-1 framework migration: articles list, editor, save/delete, and
// inline chat assistant via core/ui's ArticleEditor component. Persona
// management, revisions, export/import, merge, and image generation
// still live at /techwriter/legacy until they're ported in stage 2.

package techwriter

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/editor"
	"github.com/cmcoffee/gohort/core/ui"
)

func (T *TechWriterAgent) handleNewTechwriterPage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	page := ui.Page{
		Title:     "TechWriter",
		ShowTitle: true,
		BackURL:   "/",
		// Full viewport — techwriter is editor-centric, the wider the
		// body textarea the better. 100% lets the layout fill desktop
		// monitors instead of capping at the framework's default.
		MaxWidth: "100%",
		Sections: []ui.Section{
			{
				NoChrome: true,
				Body: ui.ArticleEditor{
					ListURL:          "api/list",
					LoadURL:          "api/load/{id}",
					SaveURL:          "api/save",
					DeleteURL:        "api/delete/{id}",
					ChatURL:          "api/chat",
					IDField:          "ID",
					SubjectField:     "Subject",
					BodyField:        "Body",
					DateField:        "Date",
					EmptyText:        "No articles yet. Click + New to start.",
					PlaceholderTitle: "Article title…",
					PlaceholderBody:  "Write in markdown. Headings, code fences, lists, links — the renderer subset is documented in the assistant's prompt.",
					BulkSelect:       true,
					// Framework-managed slide-in panels (until a
					// generic SlidePanel primitive exists).
					RulesURL:         "api/rules",
					MergeURL:         "api/merge",
					MergeSourcesURL:  "api/merge-sources",
					MergeSourceURL:   "api/merge-source/{id}",
					RevisionsListURL: "api/revisions/{id}",
					RevisionLoadURL:  "api/revision/{revid}",
					ImageField:       "ImageURL",
					// Toolbar — declarative. Order in this slice =
					// visual order on the bar. Each entry maps to a
					// generic Method handler (post / open / redirect /
					// client / builtin). Most app-specific buttons
					// use "client" — the handler is registered from
					// web_assets.go via window.uiRegisterClientAction.
					Actions: []ui.ToolbarAction{
						// Rules and Merge keep "builtin" for now —
						// their slide-in panels still live in the
						// framework. They'll move out alongside a
						// generic SlidePanel primitive.
						{Label: "Rules", Title: "Edit rules the assistant must follow",
							Method: "builtin", URL: "rules"},
						{Label: "Reprocess", Title: "Run the assistant on the current body and apply its rewrite directly",
							Method: "client", URL: "techwriter_reprocess"},
						{Label: "Merge", Title: "Merge another article or pasted content into this one",
							Method: "builtin", URL: "merge"},
						{Label: "Preview", Title: "Preview the rendered article in a new tab",
							Method: "client", URL: "techwriter_preview"},
						{Label: "Export", Title: "Download as HTML",
							Method: "client", URL: "techwriter_export"},
					},
					// Less-frequent actions land in the "More ▾"
					// popover. Same declarative shape as Actions; the
					// framework only renders entries declared here.
					ExtraActions: []ui.MenuAction{
						{Label: "Suggest title", Title: "Ask the LLM for a title based on the body",
							Method: "client", URL: "techwriter_suggest_title"},
						{Label: "Generate image", Title: "Generate a header image",
							Method: "client", URL: "techwriter_generate_image"},
					},
				},
			},
		},
		// Inline editor diff renderer (chat-mode article rewrites show
		// the proposed change as a visual diff in the editor pane —
		// same pattern codewriter uses) plus this app's client-action
		// callbacks for the toolbar buttons declared above.
		ExtraHeadHTML: "<style>" + editor.DiffCSS() + "</style>" +
			"<script>" + editor.UtilsJS() + editor.DiffJS() + "</script>" +
			twWebAssets,
	}
	page.ServeHTTP(w, r)
}
