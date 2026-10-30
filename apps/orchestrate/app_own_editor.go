package orchestrate

// An app page with an editor of its own for a document the person writes
// with the agent.
//
// gohort has that editor: a workbench section with editable: true. Its first
// song app was built as one html page instead, with a contenteditable editor
// and highlight-to-revise written from scratch, and its paste handler called
// methods that do not exist, so nothing could be pasted into it. The prompt
// already said to use the workbench; this is the check that holds a build to
// it, the way a typed-only app is held to a page (builder_app_mode.go).

import (
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// editorMarks are what a page's own document editor is built from: an element
// made editable, or a textarea whose selection the page reads.
var (
	contentEditableRE = regexp.MustCompile(`(?i)\bcontenteditable\s*(=\s*["']?\s*(true|plaintext-only)|[\s>/])`)
	textareaRE        = regexp.MustCompile(`(?i)<textarea\b`)
	selectionReadRE   = regexp.MustCompile(`selectionStart|selectionEnd|getSelection\(|selectionchange`)
	pageAsksRE        = regexp.MustCompile(`\bapp\.ask\(`)
)

// appOwnEditor reports whether the app's page is a document editor of its
// own written with an agent: a page that makes an element editable, or reads
// the selection of a textarea, in an app with an agent (agent_id, or a page
// that asks it). A form's textareas are not this; nor is a page with no
// agent to write with.
func appOwnEditor(spec AppSpec) bool {
	for _, sec := range appSectionList(spec) {
		switch strings.ToLower(strings.TrimSpace(mapStr(sec, "kind"))) {
		case "html", "card":
		default:
			continue
		}
		html := mapStr(sec, "html")
		if html == "" {
			continue
		}
		editor := contentEditableRE.MatchString(html) ||
			(textareaRE.MatchString(html) && selectionReadRE.MatchString(html))
		if !editor {
			continue
		}
		if strings.TrimSpace(spec.AgentID) != "" || pageAsksRE.MatchString(html) {
			return true
		}
	}
	return false
}

// appOwnEditorOnPurpose is an app whose notes record that the person asked
// for an editor of its own ("own editor: <why>").
func appOwnEditorOnPurpose(spec AppSpec) bool {
	return strings.Contains(strings.ToLower(spec.Notes), "own editor:")
}

// appOwnEditorProblem is the objection to an app page that writes its own
// document editor, or "".
func appOwnEditorProblem(spec AppSpec) string {
	if !appOwnEditor(spec) || appOwnEditorOnPurpose(spec) {
		return ""
	}
	return "this page writes its own editor for a document the person writes with the agent (an element made contenteditable, or a textarea whose selection it reads). " +
		"gohort has that editor: ONE workbench section with editable: true (and the app's agent_id). It already does what a hand-written one gets wrong: paste, Save and Revert, " +
		"highlighting a passage to talk about it in the chat, the agent's changes reviewed as a diff and applied or not, quick actions (selection_actions), " +
		"buttons about the whole document (document_actions), and the files the agent makes kept on each one (files_field). Rebuild it as that workbench. " +
		"Keep the page only if the person asked for an editor of its own: then write \"own editor: <why>\" into the app's notes."
}
