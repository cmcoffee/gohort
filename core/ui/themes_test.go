package ui

import (
	"strings"
	"testing"
)

// "Follow system" switches with the device: the dark theme's tokens, the light
// one's under prefers-color-scheme: light, in the CSS, the first paint and the
// phone's status-bar colour alike. Nothing waits for script to decide.
func TestAThemeCanFollowTheDevice(t *testing.T) {
	if !IsValidTheme("system") {
		t.Fatal("the follow-system theme is not registered")
	}
	css := ThemeCSS()
	start := strings.Index(css, `:root[data-theme="system"]`)
	if start < 0 {
		t.Fatal("no CSS for the follow-system theme")
	}
	block := css[start:]
	if !strings.Contains(block, "--bg-0: #0f1117") || !strings.Contains(block, "@media (prefers-color-scheme: light)") || !strings.Contains(block, "--bg-0: #f6f7f9") {
		t.Errorf("the follow-system CSS does not switch between indigo and light: %.400s", block)
	}
	head := ThemeFirstPaintHead("system")
	if !strings.Contains(head, `content="dark light"`) || !strings.Contains(head, "#0f1117") || !strings.Contains(head, "prefers-color-scheme: light") || !strings.Contains(head, "#f6f7f9") {
		t.Errorf("the first paint does not carry both: %s", head)
	}
	app := AppHead("system")
	if !strings.Contains(app, `content="#0f1117" media="(prefers-color-scheme: dark)"`) || !strings.Contains(app, `content="#f6f7f9" media="(prefers-color-scheme: light)"`) {
		t.Errorf("the status-bar colour does not follow the device: %s", app)
	}
}

// Every theme with colours of its own carries the whole set the framework
// uses: a missing token renders a page half in another theme.
func TestEveryThemeCarriesEveryToken(t *testing.T) {
	want := map[string]bool{}
	for k := range themeByName["indigo"].Tokens {
		want[k] = true
	}
	for _, th := range Themes() {
		if th.Dark != "" {
			continue
		}
		for k := range want {
			if strings.TrimSpace(th.Tokens[k]) == "" {
				t.Errorf("theme %q has no %s", th.Name, k)
			}
		}
	}
	for _, name := range []string{"high-contrast", "nord", "solarized-dark", "solarized-light", "gruvbox", "terminal", "amber"} {
		if !IsValidTheme(name) {
			t.Errorf("theme %q is not registered", name)
		}
	}
	// The accent's companions tell kinds of thing apart; none may be a
	// status colour, or a kind reads as a warning.
	for _, th := range Themes() {
		for _, k := range []string{"--accent-2", "--accent-3"} {
			c := strings.ToLower(th.Tokens[k])
			for _, st := range []string{"--success", "--warning", "--danger"} {
				if c != "" && c == strings.ToLower(th.Tokens[st]) {
					t.Errorf("theme %q's %s is its %s", th.Name, k, st)
				}
			}
		}
	}
	if !isLightColor(themeByName["solarized-light"].Tokens["--bg-0"]) || isLightColor(themeByName["nord"].Tokens["--bg-0"]) {
		t.Error("a theme's light or dark reading is wrong")
	}
}

func TestAnyThemeCanBeWornRetro(t *testing.T) {
	defer RegisterStyleResolver(nil)
	render := func() string {
		var b strings.Builder
		if err := RenderPageJSON(&b, []byte(`{}`), "nord", "", "x"); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	// Unset, or naming no style, reads as Modern.
	for _, stored := range []string{"", "neon"} {
		RegisterStyleResolver(func() string { return stored })
		if ActiveStyle() != StyleModern || !strings.Contains(render(), `data-theme="nord" data-style="modern"`) {
			t.Errorf("stored style %q did not read as modern", stored)
		}
	}
	RegisterStyleResolver(func() string { return StyleRetro })
	if !strings.Contains(render(), `data-theme="nord" data-style="retro"`) {
		t.Error("a page does not carry the retro style beside its theme")
	}
	// The retro shape comes with the theme CSS every page shell includes,
	// keyed on the style alone, so it applies whatever the theme.
	css := ThemeCSS()
	i := strings.Index(css, `:root[data-style="retro"] {`)
	if i < 0 {
		t.Fatal("the theme CSS has no retro style")
	}
	block := css[i : i+strings.Index(css[i:], "}")]
	for _, want := range []string{"--btn-radius: 0px", "--font-ui:", "monospace", "--mark-line:"} {
		if !strings.Contains(block, want) {
			t.Errorf("the retro style does not set %s", want)
		}
	}
	// A theme's colours stay its own: none of them sets the shape.
	for _, th := range Themes() {
		for _, k := range []string{"--btn-radius", "--font-ui", "--mark-line"} {
			if _, ok := th.Tokens[k]; ok {
				t.Errorf("theme %q sets %s, which is the style's", th.Name, k)
			}
		}
	}
}
