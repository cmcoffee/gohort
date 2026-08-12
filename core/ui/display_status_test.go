package ui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestStatusFieldSurvivesSerialization — the panel is configured server-side and
// rendered client-side, so a field the marshaller drops is a feature that exists
// only in Go.
func TestStatusFieldSurvivesSerialization(t *testing.T) {
	b, err := json.Marshal(DisplayPair{Label: "Confined", Field: "x", StatusField: "x_status"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"status_field":"x_status"`) {
		t.Errorf("StatusField did not serialize: %s", b)
	}
	// Absent when unset, so every existing pair's payload is unchanged.
	b, _ = json.Marshal(DisplayPair{Label: "Users", Field: "n"})
	if strings.Contains(string(b), "status_field") {
		t.Errorf("an unset StatusField was serialized: %s", b)
	}
}

// TestTheRendererOnlyHonorsKnownSeverities — a payload is data, and a field
// holding "ok'; drop table" or an attacker-chosen class name must not become a
// class name. Only three values do anything.
func TestTheRendererOnlyHonorsKnownSeverities(t *testing.T) {
	src, err := os.ReadFile("assets/runtime/10_basics.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "components.display_panel =")
	if i < 0 {
		t.Fatal("display_panel has moved")
	}
	block := body[i:min(i+1600, len(body))]
	if !strings.Contains(block, `if (sev === 'ok' || sev === 'warn' || sev === 'bad') cls += ' ' + sev;`) {
		t.Error("the renderer no longer restricts severity to the three known values — " +
			"a payload field would become an arbitrary CSS class")
	}
}

// TestEverySeverityIsStyled — a severity the server can emit with no rule
// behind it renders identically to plain text, which is the invisible-row
// problem this was added to solve, reintroduced silently.
func TestEverySeverityIsStyled(t *testing.T) {
	css, err := os.ReadFile("assets/runtime.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, sev := range []string{"ok", "warn", "bad"} {
		if !strings.Contains(string(css), ".ui-display-value."+sev) {
			t.Errorf("severity %q has no CSS rule", sev)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
