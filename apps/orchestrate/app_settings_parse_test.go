package orchestrate

import (
	"strings"
	"testing"
)

// TestAppSettingsParse covers the settings declaration parser: names become
// env-var-safe, defaults are typed per kind, a bad type or scope is corrected
// with a note rather than dropped, a choice's default must be an option, and
// a duplicate or nameless entry is reported.
func TestAppSettingsParse(t *testing.T) {
	raw := []any{
		map[string]any{"name": "Refresh Minutes", "type": "number", "default": 5, "min": 1, "max": 60, "label": "Refresh every"},
		map[string]any{"name": "alerts", "type": "toggle", "default": "yes"},
		map[string]any{"name": "units", "type": "choice", "options": []any{"metric", "imperial"}, "default": "furlongs", "scope": "user"},
		map[string]any{"name": "city", "scope": "team"},
		map[string]any{"name": "mode", "type": "dropdown"},
		map[string]any{"name": "flavor", "type": "choice"},
		map[string]any{"name": "count", "type": "number", "default": "lots"},
		map[string]any{"name": "alerts"},
		map[string]any{"type": "string"},
		"not an object",
	}
	out, notes := appSettings(raw)
	if len(out) != 7 {
		t.Fatalf("got %d settings: %+v", len(out), out)
	}
	byName := map[string]int{}
	for i, s := range out {
		byName[s.Name] = i
	}
	if i, ok := byName["refresh_minutes"]; !ok || out[i].Type != "number" || out[i].Default != "5" || out[i].Min != 1 || out[i].Max != 60 || out[i].Label != "Refresh every" {
		t.Fatalf("refresh_minutes = %+v", out)
	}
	if out[byName["alerts"]].Default != "true" {
		t.Fatalf("a toggle default is normalized to true/false: %+v", out[byName["alerts"]])
	}
	if u := out[byName["units"]]; u.Default != "metric" || u.Scope != "user" || len(u.Options) != 2 {
		t.Fatalf("units = %+v", u)
	}
	if c := out[byName["city"]]; c.Scope != "" || c.Type != "string" {
		t.Fatalf("an unknown scope falls back to owner: %+v", c)
	}
	if out[byName["mode"]].Type != "string" {
		t.Fatal("an unknown type falls back to string")
	}
	if out[byName["flavor"]].Type != "string" {
		t.Fatal("a choice with no options renders as text")
	}
	if out[byName["count"]].Default != "" {
		t.Fatal("a non-numeric default on a number is cleared")
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{
		`"Refresh Minutes" is registered as "refresh_minutes"`,
		`"units" defaults to "furlongs"`,
		`"city" has unknown scope "team"`,
		`"mode" has unknown type "dropdown"`,
		`"flavor" is a choice with no options`,
		`"count" is a number but defaults to "lots"`,
		`"alerts" declared twice`,
		`entry 9 IGNORED — needs a name`,
		`entry 10 IGNORED — not an object`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("notes lack %q:\n%s", want, joined)
		}
	}
	if out, notes := appSettings("nope"); out != nil || notes != nil {
		t.Fatal("a non-array declares nothing")
	}
}
