package ui

import (
	"sort"
	"strconv"
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

// ThemeFirstPaintHead returns the <head> fragment that makes a page's FIRST
// paint already themed, for a theme that may not be registered.
//
// The runtime stylesheet is an external link served no-cache, so every
// navigation asks for it before the new document can paint. Until it answers —
// even with a 304 — the browser has nothing but its own default canvas, which
// is white. On a dark theme that is a full white frame between one page and the
// next, and it is most visible exactly where people move most: between two hub
// apps that look identical either side of it.
//
// Two lines fix it, and they have to come BEFORE the stylesheet link.
// color-scheme is the one that reaches furthest: the browser applies it during
// parsing, before any CSS at all, and it decides the canvas, the scrollbars and
// the default form-control rendering. The inline background is the belt to that
// braces — it pins the exact token rather than the browser's idea of dark.
//
// Deliberately NOT the whole theme. This is the two properties that are visible
// with no content on screen; everything else can wait for the stylesheet, and
// duplicating a theme into every page's head would be a second copy to keep
// true. Returns "" for an unregistered theme or one without the tokens, which
// leaves the old behaviour exactly as it was.
func ThemeFirstPaintHead(theme string) string {
	themesMu.RLock()
	t, ok := themeByName[theme]
	themesMu.RUnlock()
	if !ok {
		return ""
	}
	bg := strings.TrimSpace(t.Tokens["--bg-0"])
	if bg == "" {
		return ""
	}
	scheme := "dark"
	if isLightColor(bg) {
		scheme = "light"
	}
	fg := strings.TrimSpace(t.Tokens["--text"])
	css := "html,body{background:" + bg + "}"
	if fg != "" {
		css = "html,body{background:" + bg + ";color:" + fg + "}"
	}
	return "<meta name=\"color-scheme\" content=\"" + scheme + "\">\n<style>" + css + "</style>\n"
}

// isLightColor reports whether a #rgb / #rrggbb is light enough that the
// browser should treat the page as a light one.
//
// Inferred from the background rather than declared on ThemeSpec, so a theme
// registered from outside this package cannot forget to say — and getting it
// wrong is not cosmetic: color-scheme decides scrollbar and form-control
// rendering, so a dark theme announcing itself light gets white scrollbars.
// Rec. 601 luma, halfway as the line.
func isLightColor(hex string) bool {
	hex = strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	if len(hex) != 6 {
		return false // unparseable: dark is the safer guess, it is the default
	}
	var rgb [3]int64
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseInt(hex[i*2:i*2+2], 16, 32)
		if err != nil {
			return false
		}
		rgb[i] = v
	}
	return (299*rgb[0]+587*rgb[1]+114*rgb[2])/1000 > 127
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
