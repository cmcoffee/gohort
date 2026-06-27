package ui

import (
	"sort"
	"strings"
	"sync"
)

// ThemeSpec is one selectable UI theme: Name is the data-theme value, Label is
// the human label for the picker, Tokens is the full CSS-variable set. Register
// one with RegisterTheme and it becomes selectable everywhere — the served CSS
// (ThemeCSS, prepended by MountRuntime), the admin picker (Themes), and save
// validation (IsValidTheme) all derive from the registry, so adding a theme is
// a SINGLE declaration. Provide every token the framework uses (see the
// built-ins below); a partial set renders an incomplete theme.
type ThemeSpec struct {
	Name   string
	Label  string
	Tokens map[string]string // CSS var (e.g. "--bg-0") -> value
}

var (
	themesMu    sync.RWMutex
	themeOrder  []string
	themeByName = map[string]ThemeSpec{}
)

// RegisterTheme adds (or replaces by Name) a selectable theme. Call from init().
func RegisterTheme(t ThemeSpec) {
	if t.Name == "" {
		return
	}
	themesMu.Lock()
	if _, ok := themeByName[t.Name]; !ok {
		themeOrder = append(themeOrder, t.Name)
	}
	themeByName[t.Name] = t
	themesMu.Unlock()
}

// Themes returns every registered theme in registration order.
func Themes() []ThemeSpec {
	themesMu.RLock()
	defer themesMu.RUnlock()
	out := make([]ThemeSpec, 0, len(themeOrder))
	for _, n := range themeOrder {
		out = append(out, themeByName[n])
	}
	return out
}

// IsValidTheme reports whether name is a registered theme.
func IsValidTheme(name string) bool {
	themesMu.RLock()
	defer themesMu.RUnlock()
	_, ok := themeByName[name]
	return ok
}

// ActiveTheme returns the resolved active theme name: the registered resolver's
// value, or the built-in default when unset. Use it for chrome pages (login,
// dashboard) that render outside ui.Page but still need the right data-theme.
func ActiveTheme() string {
	if themeResolver != nil {
		if t := themeResolver(); t != "" {
			return t
		}
	}
	return "indigo"
}

// ThemeCSS assembles the :root[data-theme="..."] block for every registered
// theme; MountRuntime prepends it to the runtime CSS. Tokens are emitted in
// SORTED order so the output is byte-stable — the runtime CSS is served with a
// content-hash ETag, and unstable map iteration would flap it on every restart
// (constant cache misses). Token order within a block is cosmetic, so sorting
// is free.
func ThemeCSS() string {
	var b strings.Builder
	for _, t := range Themes() {
		keys := make([]string, 0, len(t.Tokens))
		for k := range t.Tokens {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString(":root[data-theme=\"" + t.Name + "\"] {\n")
		for _, k := range keys {
			b.WriteString("  " + k + ": " + t.Tokens[k] + ";\n")
		}
		b.WriteString("}\n")
	}
	return b.String()
}

// Built-in themes. Add a new one here (or via RegisterTheme from anywhere) and
// it auto-appears in the picker + validates + ships its CSS — no other edits.
func init() {
	RegisterTheme(ThemeSpec{Name: "indigo", Label: "Indigo — cool slate + indigo accent", Tokens: map[string]string{
		"--bg-0": "#0f1117", "--bg-1": "#1a1d27", "--bg-2": "#232733",
		"--text": "#e4e7ef", "--text-hi": "#ffffff", "--text-mute": "#9ca3b8",
		"--border": "#333848", "--accent": "#6366f1", "--accent-hi": "#818cf8",
		"--danger": "#ef4444", "--success": "#22c55e", "--warning": "#f59e0b", "--tap": "44px",
	}})
	RegisterTheme(ThemeSpec{Name: "blackboard", Label: "Blackboard — warm navy + amber", Tokens: map[string]string{
		"--bg-0": "#0c1424", "--bg-1": "#142037", "--bg-2": "#1c2a45",
		"--text": "#f5f0e1", "--text-hi": "#ffffff", "--text-mute": "#9aa3b8",
		"--border": "#2a3a5e", "--accent": "#d4a657", "--accent-hi": "#f0c878",
		"--danger": "#c97474", "--success": "#56d364", "--warning": "#e3b341", "--tap": "44px",
	}})
	RegisterTheme(ThemeSpec{Name: "github-dark", Label: "GitHub Dark", Tokens: map[string]string{
		"--bg-0": "#0d1117", "--bg-1": "#161b22", "--bg-2": "#21262d",
		"--text": "#c9d1d9", "--text-hi": "#f0f6fc", "--text-mute": "#8b949e",
		"--border": "#30363d", "--accent": "#4f8cff", "--accent-hi": "#79c0ff",
		"--danger": "#f85149", "--success": "#56d364", "--warning": "#d29922", "--tap": "44px",
	}})
	RegisterTheme(ThemeSpec{Name: "light", Label: "Light — slate-on-white + indigo", Tokens: map[string]string{
		"--bg-0": "#f6f7f9", "--bg-1": "#ffffff", "--bg-2": "#eceef2",
		"--text": "#24292f", "--text-hi": "#0d1117", "--text-mute": "#57606a",
		"--border": "#d0d7de", "--accent": "#4f46e5", "--accent-hi": "#6366f1",
		"--danger": "#cf222e", "--success": "#1a7f37", "--warning": "#9a6700", "--tap": "44px",
	}})
}
