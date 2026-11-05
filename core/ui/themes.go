package ui

import (
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/cmcoffee/gohort/core/webui"
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
	// Dark and Light, set together and with no Tokens, make a theme that
	// follows the device: Dark's tokens, and Light's when the device asks for
	// a light page (prefers-color-scheme). Both name registered themes.
	Dark, Light string
}

// follows reports whether t follows the device between two other themes, and
// those two when it does.
func (t ThemeSpec) follows() (dark, light ThemeSpec, ok bool) {
	if t.Dark == "" || t.Light == "" {
		return ThemeSpec{}, ThemeSpec{}, false
	}
	dark, okD := themeByName[t.Dark]
	light, okL := themeByName[t.Light]
	return dark, light, okD && okL
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
	dark, light, follows := t.follows()
	themesMu.RUnlock()
	if !ok {
		return ""
	}
	if follows {
		// Both answers, chosen by the browser before anything paints.
		css := firstPaintCSS(dark) + "@media (prefers-color-scheme: light){" + firstPaintCSS(light) + "}"
		return "<meta name=\"color-scheme\" content=\"dark light\">\n<style>" + css + "</style>\n"
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

// firstPaintCSS is the page background and text colour of t, for the head.
func firstPaintCSS(t ThemeSpec) string {
	bg, fg := strings.TrimSpace(t.Tokens["--bg-0"]), strings.TrimSpace(t.Tokens["--text"])
	if bg == "" {
		return ""
	}
	if fg == "" {
		return "html,body{background:" + bg + "}"
	}
	return "html,body{background:" + bg + ";color:" + fg + "}"
}

// AppHead is webui.AppHeadTags plus the theme's background as theme-color,
// so a phone's status bar and a saved home-screen app match the page.
func AppHead(theme string) string {
	out := webui.AppHeadTags
	themesMu.RLock()
	t := themeByName[theme]
	dark, light, follows := t.follows()
	themesMu.RUnlock()
	if follows {
		// A status bar per scheme, so a phone's matches whichever it shows.
		if bg := strings.TrimSpace(dark.Tokens["--bg-0"]); bg != "" {
			out += "<meta name=\"theme-color\" content=\"" + bg + "\" media=\"(prefers-color-scheme: dark)\">\n"
		}
		if bg := strings.TrimSpace(light.Tokens["--bg-0"]); bg != "" {
			out += "<meta name=\"theme-color\" content=\"" + bg + "\" media=\"(prefers-color-scheme: light)\">\n"
		}
		return out
	}
	if bg := themeBackground(theme); bg != "" {
		out += "<meta name=\"theme-color\" content=\"" + bg + "\">\n"
	}
	return out
}

// themeBackground is a theme's --bg-0, or "" when the theme is unknown.
func themeBackground(theme string) string {
	themesMu.RLock()
	t, ok := themeByName[theme]
	themesMu.RUnlock()
	if !ok {
		return ""
	}
	return strings.TrimSpace(t.Tokens["--bg-0"])
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
	block := func(name string, tokens map[string]string) {
		keys := make([]string, 0, len(tokens))
		for k := range tokens {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString(":root[data-theme=\"" + name + "\"] {\n")
		for _, k := range keys {
			b.WriteString("  " + k + ": " + tokens[k] + ";\n")
		}
		b.WriteString("}\n")
	}
	for _, t := range Themes() {
		themesMu.RLock()
		dark, light, follows := t.follows()
		themesMu.RUnlock()
		if follows {
			// The dark theme's tokens, then the light one's for a device that
			// asks for light: the browser switches with the device, live.
			block(t.Name, dark.Tokens)
			b.WriteString("@media (prefers-color-scheme: light) {\n")
			block(t.Name, light.Tokens)
			b.WriteString("}\n")
			continue
		}
		block(t.Name, t.Tokens)
	}
	b.WriteString(retroStyleCSS)
	return b.String()
}

// Built-in themes. Add a new one here (or via RegisterTheme from anywhere) and
// it auto-appears in the picker + validates + ships its CSS — no other edits.
func init() {
	RegisterTheme(ThemeSpec{Name: "indigo", Label: "Indigo: cool slate + indigo accent", Tokens: map[string]string{
		"--bg-0": "#0f1117", "--bg-1": "#1a1d27", "--bg-2": "#232733",
		"--text": "#e4e7ef", "--text-hi": "#ffffff", "--text-mute": "#9ca3b8",
		"--border": "#333848", "--accent": "#6366f1", "--accent-hi": "#818cf8", "--text-on-accent": "#ffffff",
		"--danger": "#ef4444", "--success": "#22c55e", "--warning": "#f59e0b", "--tap": "44px",
	}})
	RegisterTheme(ThemeSpec{Name: "blackboard", Label: "Blackboard: warm navy + amber", Tokens: map[string]string{
		"--bg-0": "#0c1424", "--bg-1": "#142037", "--bg-2": "#1c2a45",
		"--text": "#f5f0e1", "--text-hi": "#ffffff", "--text-mute": "#9aa3b8",
		"--border": "#2a3a5e", "--accent": "#d4a657", "--accent-hi": "#f0c878", "--text-on-accent": "#1a1408",
		"--danger": "#c97474", "--success": "#56d364", "--warning": "#e3b341", "--tap": "44px",
	}})
	RegisterTheme(ThemeSpec{Name: "github-dark", Label: "GitHub Dark", Tokens: map[string]string{
		"--bg-0": "#0d1117", "--bg-1": "#161b22", "--bg-2": "#21262d",
		"--text": "#c9d1d9", "--text-hi": "#f0f6fc", "--text-mute": "#8b949e",
		"--border": "#30363d", "--accent": "#4f8cff", "--accent-hi": "#79c0ff", "--text-on-accent": "#ffffff",
		"--danger": "#f85149", "--success": "#56d364", "--warning": "#d29922", "--tap": "44px",
	}})
	RegisterTheme(ThemeSpec{Name: "light", Label: "Light: slate-on-white + indigo", Tokens: map[string]string{
		"--bg-0": "#f6f7f9", "--bg-1": "#ffffff", "--bg-2": "#eceef2",
		"--text": "#24292f", "--text-hi": "#0d1117", "--text-mute": "#57606a",
		"--border": "#d0d7de", "--accent": "#4f46e5", "--accent-hi": "#6366f1", "--text-on-accent": "#ffffff",
		"--danger": "#cf222e", "--success": "#1a7f37", "--warning": "#9a6700", "--tap": "44px",
	}})
	// After the two it follows, which have to be registered for it to work.
	RegisterTheme(ThemeSpec{Name: "system", Label: "Follow system: Indigo or Light, as the device is set", Dark: "indigo", Light: "light"})
	RegisterTheme(ThemeSpec{Name: "high-contrast", Label: "High contrast: black and white + yellow", Tokens: map[string]string{
		"--bg-0": "#000000", "--bg-1": "#0b0b0b", "--bg-2": "#1c1c1c",
		"--text": "#ffffff", "--text-hi": "#ffffff", "--text-mute": "#d4d4d4",
		"--border": "#8c8c8c", "--accent": "#ffd400", "--accent-hi": "#ffe866", "--text-on-accent": "#000000",
		"--danger": "#ff6161", "--success": "#45e06f", "--warning": "#ffb020", "--tap": "44px",
	}})
	RegisterTheme(ThemeSpec{Name: "nord", Label: "Nord: cool blue-grey + frost", Tokens: map[string]string{
		"--bg-0": "#2e3440", "--bg-1": "#3b4252", "--bg-2": "#434c5e",
		"--text": "#e5e9f0", "--text-hi": "#eceff4", "--text-mute": "#a3adbf",
		"--border": "#4c566a", "--accent": "#88c0d0", "--accent-hi": "#8fbcbb", "--text-on-accent": "#2e3440",
		"--danger": "#bf616a", "--success": "#a3be8c", "--warning": "#ebcb8b", "--tap": "44px",
	}})
	RegisterTheme(ThemeSpec{Name: "solarized-dark", Label: "Solarized Dark", Tokens: map[string]string{
		"--bg-0": "#002b36", "--bg-1": "#073642", "--bg-2": "#0e4250",
		"--text": "#93a1a1", "--text-hi": "#eee8d5", "--text-mute": "#7d9093",
		"--border": "#24515c", "--accent": "#268bd2", "--accent-hi": "#2aa198", "--text-on-accent": "#fdf6e3",
		"--danger": "#dc322f", "--success": "#859900", "--warning": "#b58900", "--tap": "44px",
	}})
	RegisterTheme(ThemeSpec{Name: "solarized-light", Label: "Solarized Light", Tokens: map[string]string{
		"--bg-0": "#eee8d5", "--bg-1": "#fdf6e3", "--bg-2": "#e4ddc8",
		"--text": "#586e75", "--text-hi": "#073642", "--text-mute": "#6c7f85",
		"--border": "#d3cbb7", "--accent": "#268bd2", "--accent-hi": "#2aa198", "--text-on-accent": "#fdf6e3",
		"--danger": "#dc322f", "--success": "#5f7000", "--warning": "#8a6900", "--tap": "44px",
	}})
	RegisterTheme(ThemeSpec{Name: "gruvbox", Label: "Gruvbox: warm retro brown + orange", Tokens: map[string]string{
		"--bg-0": "#282828", "--bg-1": "#32302f", "--bg-2": "#3c3836",
		"--text": "#ebdbb2", "--text-hi": "#fbf1c7", "--text-mute": "#a89984",
		"--border": "#504945", "--accent": "#fe8019", "--accent-hi": "#fabd2f", "--text-on-accent": "#282828",
		"--danger": "#fb4934", "--success": "#b8bb26", "--warning": "#fabd2f", "--tap": "44px",
	}})
	// The two phosphor screens: one colour of phosphor on black, with a faint
	// glow on the text. Paired with the Retro style they are the old screen
	// whole; the glow is theirs, since it suits a dark screen and blurs a light
	// one.
	RegisterTheme(ThemeSpec{Name: "terminal", Label: "Terminal: green phosphor on black", Tokens: map[string]string{
		"--bg-0": "#040a05", "--bg-1": "#08140b", "--bg-2": "#0e2013",
		"--text": "#41e874", "--text-hi": "#a6ffbf", "--text-mute": "#2a9a4c",
		"--border": "#1a4a28", "--accent": "#33ff66", "--accent-hi": "#8dffad", "--text-on-accent": "#021006",
		"--danger": "#ff5f56", "--success": "#33ff66", "--warning": "#e8e04a", "--tap": "44px",
		"--text-glow": "0 0 3px rgba(51, 255, 102, 0.35)",
	}})
	RegisterTheme(ThemeSpec{Name: "amber", Label: "Amber: amber phosphor on black", Tokens: map[string]string{
		"--bg-0": "#0a0600", "--bg-1": "#140d02", "--bg-2": "#1f1504",
		"--text": "#ffb000", "--text-hi": "#ffd27a", "--text-mute": "#a8740c",
		"--border": "#4a3308", "--accent": "#ffb000", "--accent-hi": "#ffcc4d", "--text-on-accent": "#140c00",
		"--danger": "#ff5a3c", "--success": "#d4e05a", "--warning": "#ffd27a", "--tap": "44px",
		"--text-glow": "0 0 3px rgba(255, 176, 0, 0.35)",
	}})
}

// Styles are the shape of the UI, chosen apart from the theme's colours, so
// any theme can be worn either way. Modern is rounded, in the system face,
// with the accent line fading out under the bar; Retro is square, monospace,
// with the line solid. A page carries its style as data-style on <html>
// beside data-theme, and the Retro block below (in ThemeCSS, so every page
// shell that themes itself has it) sets the variables the stylesheet reads.
const (
	StyleModern = "modern"
	StyleRetro  = "retro"
)

const retroStyleCSS = `:root[data-style="retro"] {
  --font-ui: ui-monospace, "SF Mono", "JetBrains Mono", Menlo, Consolas, "DejaVu Sans Mono", monospace;
  --btn-radius: 0px;
  --mark-line: var(--accent);
}
`

var styleResolver func() string

// RegisterStyleResolver installs the active-style lookup, as
// RegisterThemeResolver does the theme's. Call once at startup.
func RegisterStyleResolver(fn func() string) { styleResolver = fn }

// IsValidStyle reports whether s names a style.
func IsValidStyle(s string) bool { return s == StyleModern || s == StyleRetro }

// ActiveStyle is the resolver's style, or Modern when it has none (or names
// one that does not exist).
func ActiveStyle() string {
	if styleResolver != nil {
		if s := styleResolver(); IsValidStyle(s) {
			return s
		}
	}
	return StyleModern
}

// StyleOptions are the styles for a picker.
func StyleOptions() []SelectOption {
	return []SelectOption{
		{Value: StyleModern, Label: "Modern: rounded, in the system font"},
		{Value: StyleRetro, Label: "Retro: square, in a monospace font"},
	}
}
