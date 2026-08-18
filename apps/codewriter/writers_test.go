package codewriter

import (
	"strings"
	"testing"
)

// The foundation is AUTHORITATIVE, and the prompt has to say so in the two
// ways that matter: the foundation wins a disagreement, and the disagreement
// gets mentioned. Winning silently is how a house convention is replaced by
// whatever is most common on the public internet, with nothing in the reply to
// show a choice was made.
func TestFoundationBlockIsAuthoritative(t *testing.T) {
	got := WriterRecord{Name: "Acme API", Brief: "Auth is a bearer token in X-Acme-Key."}.foundationBlock()
	if !strings.Contains(got, "Acme API") {
		t.Error("the block should name the writer it belongs to")
	}
	if !strings.Contains(got, "Auth is a bearer token in X-Acme-Key.") {
		t.Error("the brief itself must be present")
	}
	if !strings.Contains(got, "AUTHORITATIVE") {
		t.Error("the foundation must be stated as authoritative, not offered as reference")
	}
	if !strings.Contains(strings.ToLower(got), "say so when they disagree") {
		t.Error("a conflict must be surfaced, not silently resolved")
	}
	// A foundation with holes must produce a question, not a plausible guess:
	// code written on an invented convention looks correct, which is the whole
	// danger.
	if !strings.Contains(got, "Do NOT invent detail") {
		t.Error("the model must be told to name gaps rather than fill them")
	}
}

// A writer with no brief yet contributes NOTHING. Announcing a foundation and
// then supplying none tells the model authoritative rules exist and shows it
// none of them, which is worse than staying quiet.
func TestFoundationBlockEmptyWithoutABrief(t *testing.T) {
	for _, wr := range []WriterRecord{
		{Name: "Acme"},
		{Name: "Acme", Brief: "   \n  "},
	} {
		if got := wr.foundationBlock(); got != "" {
			t.Errorf("a writer with no brief must contribute nothing, got %q", got)
		}
	}
}

// An unnamed writer still produces a usable block rather than a dangling
// heading — the name is a label, not a precondition.
func TestFoundationBlockSurvivesAnUnnamedWriter(t *testing.T) {
	got := WriterRecord{Brief: "Use tabs."}.foundationBlock()
	if !strings.Contains(got, "this writer") {
		t.Errorf("expected a fallback label, got %q", got)
	}
	if !strings.Contains(got, "Use tabs.") {
		t.Error("the brief must survive a missing name")
	}
}
