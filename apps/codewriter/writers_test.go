package codewriter

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A writer's standing note is what does not change per turn: that a body of
// knowledge governs this work, that it wins a disagreement, and that the model
// should ASK rather than guess. The retrieved text itself rides the user
// message, because retrieval changes per query and a per-turn system prompt
// re-prefills the whole thread.
func TestSourceBlockBindsAndSendsYouToAsk(t *testing.T) {
	wr := WriterRecord{
		Name:    "Acme API",
		Sources: ReferenceSelections{{Kind: "agent", ItemID: "a1"}},
	}
	got := wr.sourceBlock()
	if !strings.Contains(got, "Acme API") {
		t.Error("the block should name the writer it belongs to")
	}
	if !strings.Contains(got, "AUTHORITATIVE") {
		t.Error("the sources must bind, not merely be offered as reference")
	}
	if !strings.Contains(strings.ToLower(got), "say so when they disagree") {
		t.Error("a conflict must be surfaced, not silently resolved")
	}
	if !strings.Contains(got, "ASK before writing") {
		t.Error("the model must be told to consult before guessing a detail")
	}
	// The block is standing context. Anything query-specific belongs on the
	// user message, so nothing here may promise retrieved content.
	if strings.Contains(got, "```") {
		t.Error("the standing note must not carry retrieved material")
	}
}

// A writer with no sources contributes NOTHING. Announcing an authoritative
// body of knowledge and attaching none tells the model rules exist and shows it
// none of them, which is worse than staying quiet.
func TestSourceBlockEmptyWithoutSources(t *testing.T) {
	if got := (WriterRecord{Name: "Acme"}).sourceBlock(); got != "" {
		t.Errorf("a writer with no sources must contribute nothing, got %q", got)
	}
}

// An unnamed writer still produces a usable block rather than a dangling
// heading — the name is a label, not a precondition.
func TestSourceBlockSurvivesAnUnnamedWriter(t *testing.T) {
	got := WriterRecord{Sources: ReferenceSelections{{Kind: "agent", ItemID: "a1"}}}.sourceBlock()
	if !strings.Contains(got, "this writer") {
		t.Errorf("expected a fallback label, got %q", got)
	}
}

// A mode is the panel's saved state, so the record has to carry every control
// that resets on refresh — that reset is the whole reason it exists.
func TestAModeCarriesThePanelSettings(t *testing.T) {
	wr := WriterRecord{
		Name:        "Kiteworks SQL queries",
		Lang:        "sql",
		Sources:     ReferenceSelections{{Kind: "agent", ItemID: "kw"}},
		Collections: []string{"c1", "c2"},
	}
	if wr.Lang == "" || len(wr.Sources) == 0 || len(wr.Collections) == 0 {
		t.Fatal("a mode must carry language, sources and collections together")
	}
	// The standing note keys off SOURCES: a mode that only pins a language has
	// no body of knowledge to bind to, and claiming otherwise would tell the
	// model authoritative material exists where none is attached.
	if (WriterRecord{Name: "Bash", Lang: "bash"}).sourceBlock() != "" {
		t.Error("a mode with no sources must not claim an authoritative body of knowledge")
	}
}
