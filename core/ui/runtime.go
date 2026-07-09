package ui

import (
	"embed"
	"sort"
	"strings"
)

// runtimeCSS — every component's static styling, kept in assets/runtime.css so
// it can be edited as a real stylesheet (syntax highlighting, linting,
// formatting) instead of a Go raw string. The per-theme token blocks
// (:root[data-theme="..."]) are NOT here: they're declared in the theme
// registry (themes.go) and prepended at serve time by MountRuntime via
// ThemeCSS(). Mobile-first, with safe-area insets and 44px minimum tap targets.
//
//go:embed assets/runtime.css
var runtimeCSS string

// runtimeJSParts — the shared client runtime, split across ordered fragments in
// assets/runtime/. It was one ~14k-line file; splitting it keeps each section
// (chat_panel, agent_loop_panel, pipeline_panel, …) editable on its own with
// small diffs. Kept as real .js files so backticks/template literals are legal.
//
//go:embed assets/runtime/*.js
var runtimeJSParts embed.FS

// runtimeJS — the fragments in assets/runtime/ concatenated byte-for-byte in
// filename order. The numeric prefixes make lexical order the load order:
// 00_prelude opens the single IIFE and defines the shared helpers + the
// components map, each NN_*.js adds component definitions, and 99_epilogue
// wires mounting and closes the IIFE. The pieces are therefore NOT independently
// valid scripts — they are one program joined in name order, so edits must not
// reorder or break the enclosing function across the boundaries.
var runtimeJS = assembleRuntimeJS()

func assembleRuntimeJS() string {
	const dir = "assets/runtime"
	entries, err := runtimeJSParts.ReadDir(dir)
	if err != nil {
		panic("ui: reading runtime JS parts: " + err.Error())
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".js") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		data, err := runtimeJSParts.ReadFile(dir + "/" + n)
		if err != nil {
			panic("ui: reading runtime JS part " + n + ": " + err.Error())
		}
		b.Write(data)
	}
	return b.String()
}
