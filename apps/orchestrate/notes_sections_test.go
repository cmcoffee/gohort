package orchestrate

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func notesTurn(t *testing.T) *chatTurn {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	return &chatTurn{
		udb:   UserDB(root, "u"),
		agent: AgentRecord{ID: "a1", Name: "Wren", Owner: "u", EnableNotes: true},
	}
}

func callNotes(t *testing.T, turn *chatTurn, args map[string]any) (string, error) {
	t.Helper()
	return turn.updateNotesToolDef().Handler(args)
}

func storedNotes(t *testing.T, turn *chatTurn) string {
	t.Helper()
	return LoadOperatingNotes(turn.udb, factsNamespace(turn.agent.ID)).Text
}

// The register the agent names. Writing one part must leave every other part
// exactly as it was — that is the whole reason this exists, because a rewrite
// of the whole document to change one line is how a model drops the rest.
func TestASectionWriteLeavesTheRestAlone(t *testing.T) {
	turn := notesTurn(t)
	if _, err := callNotes(t, turn, map[string]any{"section": "in flight", "text": "Drafting section 3."}); err != nil {
		t.Fatal(err)
	}
	if _, err := callNotes(t, turn, map[string]any{"section": "quirks", "text": "llama slots rotate by LRU."}); err != nil {
		t.Fatal(err)
	}
	msg, err := callNotes(t, turn, map[string]any{"section": "in flight", "text": "Now on section 4."})
	if err != nil {
		t.Fatal(err)
	}
	got := storedNotes(t, turn)
	if !strings.Contains(got, "Now on section 4.") || strings.Contains(got, "section 3") {
		t.Errorf("the section was not replaced: %q", got)
	}
	if !strings.Contains(got, "llama slots rotate by LRU.") {
		t.Errorf("a section write disturbed another section: %q", got)
	}
	// The reply has to say which register was written, or the agent cannot tell
	// a section write from the whole-block rewrite it did not mean to make.
	if !strings.Contains(msg, "in flight") || !strings.Contains(msg, "unchanged") {
		t.Errorf("reply must name the section and say the rest stands: %q", msg)
	}
}

// Empty text with a section removes just that one; empty text alone still
// clears everything, which is what "rewrite the whole block" has always meant.
func TestRemovingASectionVersusClearingTheBlock(t *testing.T) {
	turn := notesTurn(t)
	callNotes(t, turn, map[string]any{"section": "a", "text": "one"})
	callNotes(t, turn, map[string]any{"section": "b", "text": "two"})

	if _, err := callNotes(t, turn, map[string]any{"section": "a", "text": ""}); err != nil {
		t.Fatal(err)
	}
	got := storedNotes(t, turn)
	if strings.Contains(got, "one") || !strings.Contains(got, "two") {
		t.Errorf("removed the wrong thing: %q", got)
	}
	if _, err := callNotes(t, turn, map[string]any{"text": ""}); err != nil {
		t.Fatal(err)
	}
	if got := storedNotes(t, turn); got != "" {
		t.Errorf("a sectionless clear must empty the block, got %q", got)
	}
}

// A sectionless write still replaces everything, sections included. An agent
// correcting a mess needs a way to say so.
func TestAWholeBlockWriteStillReplacesSections(t *testing.T) {
	turn := notesTurn(t)
	callNotes(t, turn, map[string]any{"section": "a", "text": "one"})
	if _, err := callNotes(t, turn, map[string]any{"text": "Starting over."}); err != nil {
		t.Fatal(err)
	}
	if got := storedNotes(t, turn); got != "Starting over." {
		t.Errorf("got %q, want the whole block replaced", got)
	}
}

// Sections COMPETE for the one budget rather than adding to it, and an
// over-cap write is refused with the biggest named — never truncated. A memory
// layer that quietly loses its middle is worse than one that says no: the agent
// cannot tell it was cut, and reads back a sentence that stops.
func TestAnOverCapSectionIsRefusedAndNamesTheCost(t *testing.T) {
	turn := notesTurn(t)
	callNotes(t, turn, map[string]any{"section": "hoard", "text": strings.Repeat("x", OperatingNotesCap-200)})

	_, err := callNotes(t, turn, map[string]any{"section": "more", "text": strings.Repeat("y", 400)})
	if err == nil {
		t.Fatal("a write past the cap must be refused")
	}
	if !strings.Contains(err.Error(), "hoard") {
		t.Errorf("the refusal must name what is costing: %v", err)
	}
	// And nothing was written: a refused write is not a half-write.
	if got := storedNotes(t, turn); strings.Contains(got, "yyy") {
		t.Errorf("the refused section was stored anyway: %q", got)
	}
	if !strings.Contains(storedNotes(t, turn), "xxx") {
		t.Error("the refusal took the existing block with it")
	}
}

// A seeded agent whose store is still empty splices into the SEED, not into a
// blank document — otherwise the first section write silently discards the
// working notes the agent was configured with.
func TestASectionWriteSplicesIntoTheSeed(t *testing.T) {
	turn := notesTurn(t)
	turn.agent.SeedNotes = "## standing\nReport to Craig each morning.\n"
	if _, err := callNotes(t, turn, map[string]any{"section": "in flight", "text": "Drafting."}); err != nil {
		t.Fatal(err)
	}
	got := storedNotes(t, turn)
	if !strings.Contains(got, "Report to Craig each morning.") {
		t.Errorf("the seed was discarded by the first section write: %q", got)
	}
	if !strings.Contains(got, "## in flight") {
		t.Errorf("the new section is missing: %q", got)
	}
}

// The owner's path and the agent's path go through one implementation. Two of
// them is how they come to disagree about where a note lives, and the symptom
// is a panel showing something other than what the model reads.
func TestTheOwnerEndpointTakesASectionToo(t *testing.T) {
	raw, err := os.ReadFile("notes.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if !strings.Contains(src, "Section string `json:\"section\"`") {
		t.Error("POST …/notes must accept an optional section")
	}
	if strings.Count(src, "notes.ApplyNoteSection(") != 2 {
		t.Error("both the tool and the endpoint must splice through ApplyNoteSection — a second implementation drifts")
	}
	// The over-cap refusal is worded once, in core/notes, so the person
	// trimming the block and the model trimming it read the same sentence.
	if strings.Count(src, "notes.OverCapAdvice(") != 2 {
		t.Error("both surfaces must quote the same over-cap advice")
	}
	// And the panel is measured by the same parser rather than a browser-side
	// count that disagrees the first time a heading sits inside a code fence.
	if !strings.Contains(src, "notes.SectionSizes(") {
		t.Error("GET …/notes must serve the section sizes the panel shows")
	}
}

// The block tells the agent the section rule exists. A parameter nothing in the
// prompt mentions is one the model finds only by reading its own tool schema
// closely, which is not how it decides what to do.
func TestTheNotesBlockNamesTheSectionRule(t *testing.T) {
	block := RenderOperatingNotesBlock(OperatingNotes{Text: "## in flight\nDrafting.\n"})
	if !strings.Contains(block, "update_notes(section:") {
		t.Error("the working-notes block must say how to update one part")
	}
	if !strings.Contains(block, "compete") {
		t.Error("and that sections share the one budget rather than adding to it")
	}
	// Still nothing when there are no notes: an empty block must render empty,
	// or every agent with none pays for the instructions to a feature it is
	// not using.
	if RenderOperatingNotesBlock(OperatingNotes{}) != "" {
		t.Error("no notes, no block")
	}
}
