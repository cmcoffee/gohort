package core

import (
	"strings"
	"testing"
)

// TestApplyOptsAutoDate: by default a system prompt gets the "Today's date is …"
// prepend; WithoutAutoDate() suppresses it (for callers that stamp the date onto
// the user turn instead).
func TestApplyOptsAutoDate(t *testing.T) {
	// Default: date is prepended, original prompt preserved after it.
	cfg := applyOpts("m", 100, []ChatOption{WithSystemPrompt("BASE PROMPT")})
	if !strings.HasPrefix(cfg.SystemPrompt, "Today's date is ") {
		t.Fatalf("expected date prepend, got %q", cfg.SystemPrompt)
	}
	if !strings.HasSuffix(cfg.SystemPrompt, "BASE PROMPT") {
		t.Fatalf("original prompt not preserved: %q", cfg.SystemPrompt)
	}

	// Suppressed: system prompt is untouched.
	cfg = applyOpts("m", 100, []ChatOption{WithSystemPrompt("BASE PROMPT"), WithoutAutoDate()})
	if cfg.SystemPrompt != "BASE PROMPT" {
		t.Fatalf("WithoutAutoDate should leave prompt untouched, got %q", cfg.SystemPrompt)
	}
	if !cfg.SuppressAutoDate {
		t.Fatalf("SuppressAutoDate flag not set")
	}

	// Empty system prompt: never gets a date (nothing to prepend to).
	cfg = applyOpts("m", 100, nil)
	if cfg.SystemPrompt != "" {
		t.Fatalf("empty prompt should stay empty, got %q", cfg.SystemPrompt)
	}
}

// TestCurrentContextStamp: the user-turn marker is well-formed and carries no
// em-dash (an AI tell scrubbed from product output).
func TestCurrentContextStamp(t *testing.T) {
	s := CurrentContextStamp()
	if !strings.HasPrefix(s, "[Current date & time:") || !strings.HasSuffix(s, "]") {
		t.Fatalf("malformed stamp: %q", s)
	}
	if strings.ContainsRune(s, '—') {
		t.Fatalf("stamp contains an em-dash: %q", s)
	}
}
