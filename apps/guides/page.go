// The Guides workbench page: guide list (left) | rendered HTML document with a
// table of contents (center) | Guide Author chat (right). Built from the core/ui
// WorkbenchPanel primitive; the document styling rides in via ExtraHeadHTML so a
// guide reads like a formatted document.
package guides

import (
	"net/http"

	"github.com/cmcoffee/gohort/core/ui"
)

func (T *Guides) servePage(w http.ResponseWriter, r *http.Request) {
	wb := ui.WorkbenchPanel{
		// Left — guide list + New.
		ListURL:   "guides",
		ItemKey:   "id",
		ItemLabel: "title",
		ListTitle: "Guides",
		ListEmpty: "No guides yet — create one.",
		DeleteURL: "guide?id={id}",
		NewButton: ui.ModalButton{
			Label: "New",
			Title: "New guide",
			Body: ui.FormPanel{
				PostURL:     "new",
				SubmitLabel: "Create guide",
				Fields: []ui.FormField{
					{Field: "title", Label: "Title", Type: "text", Placeholder: "e.g. Getting Started with Kubernetes"},
					{Field: "subtitle", Label: "Subtitle", Type: "text", Placeholder: "Optional one-line description"},
				},
				Invalidate: []string{"guides"},
			},
		},
		// Center — the rendered document (server HTML: title + ToC + sections).
		RecordURL:  "guide?id={id}",
		BodyField:  "html",
		BodyIsHTML: true,
		EmptyIcon:  "📖",
		EmptyTitle: "No guide selected",
		EmptyHint:  "Pick a guide on the left, or create one. Then ask the assistant to draft sections.",
		// The agent writes sections via its tools; re-render the open guide when a
		// chat round finishes.
		RefreshOn: []string{"guides"},
		ActiveURL: "chat/active",
		// Right — the Guide Author chat (endpoints; WorkbenchPanel builds the panel).
		Chat: ui.AgentLoopPanel{
			SendURL:      "chat/send",
			CancelURL:    "chat/cancel",
			Markdown:     true,
			LockActivity: true,
			EmptyText:    "Ask me to draft or revise a section — e.g. \"Add an introduction\" or \"Expand the setup section.\"",
			Placeholder:  "Ask the Guide Author…",
		},
	}

	page := ui.Page{
		Title:         "Guides",
		ShowTitle:     true,
		BackURL:       "/",
		MaxWidth:      "100%",
		Sections:      []ui.Section{{NoChrome: true, Body: wb}},
		ExtraHeadHTML: guideDocCSS,
	}
	page.ServeHTTP(w, r)
}

// guideDocCSS styles the rendered guide so it reads like a formatted document:
// a centered measure, a contents block, numbered section headings. Scoped under
// .guide-doc so it never leaks into other surfaces. Uses theme tokens for color.
const guideDocCSS = `<style>
.guide-doc { max-width: 760px; margin: 0 auto; padding: 0.5rem 0 3rem; }
.guide-doc-head h1 { font-size: 1.9rem; line-height: 1.2; margin: 0 0 0.3rem; color: var(--text-hi); }
.guide-doc-sub { font-size: 1.02rem; color: var(--text-mute); margin: 0 0 1.4rem; }
.guide-doc-empty { color: var(--text-mute); font-style: italic; padding: 1rem 0; }
.guide-toc {
  background: var(--bg-2); border: 1px solid var(--border); border-radius: 10px;
  padding: 0.9rem 1.1rem; margin: 0 0 2rem;
}
.guide-toc-title { font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--text-mute); margin-bottom: 0.5rem; }
.guide-toc ol { margin: 0; padding-left: 1.3rem; display: flex; flex-direction: column; gap: 0.25rem; }
.guide-toc a { color: var(--accent); text-decoration: none; }
.guide-toc a:hover { text-decoration: underline; }
.guide-section { margin: 0 0 2.2rem; scroll-margin-top: 1rem; }
.guide-section > h2 {
  font-size: 1.35rem; color: var(--text-hi);
  border-bottom: 1px solid var(--border); padding-bottom: 0.3rem; margin: 0 0 0.9rem;
}
.guide-section-num { color: var(--text-mute); font-weight: 600; margin-right: 0.3rem; }
.guide-section-body { font-size: 0.95rem; line-height: 1.65; color: var(--text); }
.guide-section-body h3 { font-size: 1.08rem; color: var(--text-hi); margin: 1.3rem 0 0.5rem; }
.guide-section-body h4 { font-size: 0.98rem; color: var(--text-hi); margin: 1.1rem 0 0.4rem; }
.guide-section-body pre {
  background: var(--bg-0); border: 1px solid var(--border); border-radius: 8px;
  padding: 0.8rem 1rem; overflow-x: auto; font-size: 0.86rem;
}
.guide-section-body code { font-size: 0.88em; }
.guide-section-body :not(pre) > code { background: var(--bg-2); padding: 0.1rem 0.35rem; border-radius: 4px; }
.guide-section-body blockquote {
  border-left: 3px solid var(--border); margin: 0.8rem 0; padding: 0.2rem 0 0.2rem 1rem; color: var(--text-mute);
}
.guide-section-body table { border-collapse: collapse; margin: 0.8rem 0; }
.guide-section-body th, .guide-section-body td { border: 1px solid var(--border); padding: 0.4rem 0.7rem; text-align: left; }
</style>`
