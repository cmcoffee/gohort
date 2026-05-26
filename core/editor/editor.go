// Package editor provides shared primitives for text-authoring apps —
// CodeWriter, TechWriter, and the Guide Writer (planned). Think of this
// package as a toolbox: clipboard copy with fallback, drag-resizer
// helpers, file-import helpers, and (in later passes) reusable widget
// components like context panes and chat panes.
//
// Design discipline: this package holds PRIMITIVES, not behavior.
// Anything that needs to know which app it's serving belongs in the
// app, not here. The test for "does this belong in core/editor?" is:
// can the function signature be written without referencing any
// specific app's domain? If yes, it's a primitive. If no, it stays in
// the app and possibly hooks into a primitive via a callback.
//
// Apps compose primitives alongside their own HTML/CSS/JS when
// assembling a page:
//
//	webui.RenderPage(webui.PageOpts{
//	    ...
//	    AppJS: editor.UtilsJS() + myAppJS,
//	})
//
// All static assets are embedded via go:embed so the binary stays
// single-file, parallel to how core/webui handles its assets.
package editor

import (
	"embed"
)

//go:embed static
var staticFS embed.FS

// asset returns the contents of an embedded static file. Panics on
// missing files because asset names are compile-time constants.
func asset(name string) string {
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		panic("editor: missing asset " + name + ": " + err.Error())
	}
	return string(b)
}

// UtilsJS returns the shared JavaScript utility bundle: clipboard copy
// with fallback, drag-resize helpers for horizontal and vertical panes,
// and a text-file import helper. Apps append their own JS after this
// via PageOpts.AppJS.
//
// The bundle is self-contained and has no hard dependencies on app
// HTML structure — each helper takes the target element or arguments
// it needs to act on. Apps wire button onclick handlers to these
// helpers directly.
func UtilsJS() string {
	return asset("clipboard.js") + "\n" + asset("resizer.js") + "\n" + asset("import.js")
}

// DiffJS returns the line-level diff helpers: editorDiffLines,
// editorDiffRender, editorDiffStats. Apps that want to preview an
// "apply" operation in the chat pane include this alongside UtilsJS
// and render editorDiffRender(currentEditor, newContent) where the
// Apply button used to be.
func DiffJS() string {
	return asset("diff.js")
}

// DiffCSS returns the styles for the line-diff widget. Append to
// PageOpts.AppCSS alongside the app's own styles.
func DiffCSS() string {
	return asset("diff.css")
}
