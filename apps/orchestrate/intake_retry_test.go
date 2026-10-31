package orchestrate

import (
	"strings"
	"testing"
)

// Retrying an intake resends it as an intake: its picks ride with the text and
// the bubble redraws as the form summary. Retried as plain text, the turn lost
// the picks Builder's recipe gate and app mode read.
func TestRetryingAnIntakeResendsItsPicks(t *testing.T) {
	i := strings.Index(IntakeFormHead, "function registerIntakeRetry()")
	if i < 0 {
		t.Fatal("orchestrate registers no retry for an intake bubble")
	}
	fn := IntakeFormHead[i:]
	fn = fn[:strings.Index(fn, "registerIntakeRetry();")]
	for _, want := range []string{
		"window.uiRegisterMessageRetry(",
		"bubble.dataset.uiIntake === '1'",
		"uiSetPendingMessageExtras({intake_values: values})",
		"ctx.commit(ctx.rawText)",
		"decorateIntakeBubble(latest, fields, values)",
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("the intake retry is missing %q", want)
		}
	}
}
