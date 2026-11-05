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
	for _, name := range []string{"high-contrast", "nord", "solarized-dark", "solarized-light", "gruvbox"} {
		if !IsValidTheme(name) {
			t.Errorf("theme %q is not registered", name)
		}
	}
	if !isLightColor(themeByName["solarized-light"].Tokens["--bg-0"]) || isLightColor(themeByName["nord"].Tokens["--bg-0"]) {
		t.Error("a theme's light or dark reading is wrong")
	}
}
