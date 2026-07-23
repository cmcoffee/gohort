package core

import (
	"fmt"
	"strings"
	"testing"
)

// The failure-shape guard should ask for advice before it settles for telling
// a stuck model to stop retrying — and must fall back cleanly when the consult
// is unavailable, fails, or answers with nothing.
func TestFailureShapeConsultFallback(t *testing.T) {
	cases := []struct {
		name    string
		consult func(string, string) (string, error)
		// wantAdvice: history should carry the consulted answer.
		// wantDirective: history should carry the generic "stop retrying" text.
		wantAdvice, wantDirective bool
	}{
		{"nil consult falls back", nil, false, true},
		{"error falls back", func(string, string) (string, error) {
			return "", fmt.Errorf("lead unreachable")
		}, false, true},
		{"empty answer falls back", func(string, string) (string, error) {
			return "   ", nil
		}, false, true},
		{"answer is injected as advice", func(string, string) (string, error) {
			return "Put firstMessage inside assistantOverrides.", nil
		}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, consulted := failureShapeCorrection(3, "property x should not exist", "raw error body", tc.consult)
			if consulted != tc.wantAdvice {
				t.Fatalf("consulted=%v want %v", consulted, tc.wantAdvice)
			}
			hasAdvice := strings.Contains(msg, "ADVICE") || strings.Contains(msg, "assistantOverrides")
			hasDirective := strings.Contains(msg, "Stop retrying variations")
			if hasAdvice != tc.wantAdvice {
				t.Errorf("advice in message = %v, want %v: %q", hasAdvice, tc.wantAdvice, msg)
			}
			if hasDirective != tc.wantDirective {
				t.Errorf("directive in message = %v, want %v: %q", hasDirective, tc.wantDirective, msg)
			}
			// Either way the model must be told it hit the same wall repeatedly.
			if !strings.Contains(msg, "SAME failure") {
				t.Errorf("correction must name the repetition: %q", msg)
			}
		})
	}
}

// Advice must never be presented as settled fact — the same session has seen
// both tiers be confidently wrong about one API.
func TestConsultAdviceIsLabelled(t *testing.T) {
	msg, _ := failureShapeCorrection(3, "shape", "body", func(string, string) (string, error) {
		return "Use assistantOverrides.", nil
	})
	for _, want := range []string{"advice, not fact", "VERIFY"} {
		if !strings.Contains(msg, want) {
			t.Errorf("advice must carry %q: %q", want, msg)
		}
	}
}
