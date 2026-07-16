package orchestrate

import (
	"strings"
	"testing"
)

// TestSanitizePlanText covers the "messed-up plan" symptom: a model handed a
// plan-step field a JSON-ENCODED value (literal \n and a stray trailing ",")
// instead of plain text. The sanitizer decodes it at the parse boundary.
func TestSanitizePlanText(t *testing.T) {
	// The exact leaked value the user reported (literal backslash-n, trailing ",).
	leaked := `What kind of notification bridge do you want to create?\n\n1. A different service?\n2. Modify the existing one?\n\nTell me what to monitor.",`
	got := sanitizePlanText(leaked)
	if strings.Contains(got, `\n`) {
		t.Errorf("literal \\n not decoded: %q", got)
	}
	if strings.HasSuffix(got, `",`) || strings.HasSuffix(got, `"`) {
		t.Errorf("stray trailing quote/comma not stripped: %q", got)
	}
	if !strings.Contains(got, "What kind of notification bridge") || !strings.Contains(got, "\n\n") {
		t.Errorf("expected decoded multi-line text, got %q", got)
	}

	// Clean input must pass through untouched.
	for _, clean := range []string{
		"Find the API docs",
		"Verify the credential works, then wire the tool",
		"", // empty stays empty
	} {
		if got := sanitizePlanText(clean); got != strings.TrimSpace(clean) {
			t.Errorf("clean text altered: %q -> %q", clean, got)
		}
	}

	// A legitimate trailing comma (no quote) must survive — only the exact
	// quote+comma leak signature is stripped.
	if got := sanitizePlanText("do X, then Y,"); got != "do X, then Y," {
		t.Errorf("legit trailing comma stripped: %q", got)
	}
}
