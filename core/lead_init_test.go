package core

// A lead model that IS configured and failed to start is a different problem
// from no lead at all, and they produce the identical symptom.

import (
	"errors"
	"strings"
	"testing"
)

func TestLeadInitErrorIsReportedAheadOfNoneConfigured(t *testing.T) {
	t.Cleanup(func() { SetLeadInitError("", "", nil) })

	// Nothing recorded: an app with no lead reads as "none configured".
	SetLeadInitError("", "", nil)
	if got := leadUnavailableReason(&AppCore{}); got != "no lead model is configured for this app" {
		t.Fatalf("with nothing recorded the reason should be 'none configured', got %q", got)
	}

	// Recorded: the SAME nil lead now reports why it is nil, and names the
	// provider so somebody knows where to look.
	SetLeadInitError("bedrock", "claude-sonnet-5", errors.New("no resolvable AWS credentials"))
	got := leadUnavailableReason(&AppCore{})
	for _, want := range []string{"bedrock", "claude-sonnet-5", "could not be initialized", "AWS credentials"} {
		if !strings.Contains(got, want) {
			t.Errorf("the reason must carry %q so it is actionable; got %q", want, got)
		}
	}
	if strings.Contains(got, "no lead model is configured") {
		t.Error("a configured-but-broken lead must NOT read as 'none configured' — that sends somebody to re-enter settings that were right")
	}

	// Cleared on recovery: a complaint that outlives its cause is how somebody
	// stops believing the diagnostics.
	SetLeadInitError("", "", nil)
	if LeadInitError() != "" {
		t.Error("a cleared init error must not survive")
	}
}
