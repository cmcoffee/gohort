package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func evalDB(t *testing.T) Database {
	t.Helper()
	return &DBase{Store: kvlite.MemStore()}
}

func goodSuite() EvalSuite {
	return EvalSuite{
		Name: "Debate quality", TargetKind: EvalTargetPipeline, TargetID: "p1",
		Cases: []EvalCase{
			{Name: "picks_a_side", Prompt: "Should we adopt a monorepo?", MustInclude: []string{"wins"}},
			{Name: "cites", Prompt: "Is a four-day week worth it?", JudgePrompt: "the verdict names the argument that decided it"},
		},
	}
}

// Stub mode is the whole safety story, so "unset" has to read as ON — a record
// written before anybody thought about the field must not run for real.
func TestStubDefaultsOnEvenWhenUnset(t *testing.T) {
	if !(EvalSuite{}).Stubbed() {
		t.Error("an unset Stub must read as stubbed; an eval that runs for real sends the emails")
	}
	off := false
	if (EvalSuite{Stub: &off}).Stubbed() {
		t.Error("an explicit false must turn stubbing off")
	}
	on := true
	if !(EvalSuite{Stub: &on}).Stubbed() {
		t.Error("an explicit true must stay on")
	}
}

// A case that asserts nothing PASSES unconditionally, which is worse than
// having no case: it raises the score while grading nothing.
func TestASuiteThatGradesNothingIsRefused(t *testing.T) {
	s := goodSuite()
	s.Cases = append(s.Cases, EvalCase{Name: "vacuous", Prompt: "hello"})
	err := s.Validate()
	if err == nil {
		t.Fatal("expected a refusal for a case with no assertions")
	}
	if !strings.Contains(err.Error(), "asserts nothing") {
		t.Errorf("the error should say why: %v", err)
	}
}

func TestSuiteValidationCatchesTheRest(t *testing.T) {
	for name, mut := range map[string]func(*EvalSuite){
		"no name":        func(s *EvalSuite) { s.Name = "" },
		"no target kind": func(s *EvalSuite) { s.TargetKind = "" },
		"bad kind":       func(s *EvalSuite) { s.TargetKind = "wishful" },
		"no target":      func(s *EvalSuite) { s.TargetID = "" },
		"no cases":       func(s *EvalSuite) { s.Cases = nil },
		"unnamed case":   func(s *EvalSuite) { s.Cases[0].Name = "" },
		"duplicate name": func(s *EvalSuite) { s.Cases[1].Name = s.Cases[0].Name },
		"no prompt":      func(s *EvalSuite) { s.Cases[0].Prompt = "" },
	} {
		s := goodSuite()
		mut(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: expected a refusal", name)
		}
	}
	if err := goodSuite().Validate(); err != nil {
		t.Errorf("a good suite was refused: %v", err)
	}
}

func TestSuiteRoundTripAndHistory(t *testing.T) {
	db := evalDB(t)
	saved, err := SaveEvalSuite(db, goodSuite())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.ID == "" || saved.Created.IsZero() {
		t.Fatal("save should allocate an id and stamp created")
	}
	back, ok := LoadEvalSuite(db, saved.ID)
	if !ok || back.Name != saved.Name || len(back.Cases) != 2 {
		t.Fatalf("round trip lost something: %+v", back)
	}

	// Two runs of the same target share a hash; the history is newest-first.
	hash := EvalTargetFingerprint("prompt v1", "tools:a,b")
	SaveEvalRun(db, EvalRun{SuiteID: saved.ID, Passed: 24, Total: 30, TargetHash: hash, Started: back.Created})
	SaveEvalRun(db, EvalRun{SuiteID: saved.ID, Passed: 29, Total: 30, TargetHash: EvalTargetFingerprint("prompt v2", "tools:a,b"), Started: back.Created.Add(1)})
	runs := ListEvalRuns(db, saved.ID)
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].Rate() != "29/30" {
		t.Errorf("history should be newest first, got %s", runs[0].Rate())
	}
	// The point of the hash: an edit is visible as a different fingerprint.
	if runs[0].TargetHash == runs[1].TargetHash {
		t.Error("editing the target should change its fingerprint, or the history says nothing")
	}
}

// A rename or a new timestamp must NOT read as a behaviour change, or every
// comparison is against a different hash and the score history is noise.
func TestFingerprintCoversBehaviourOnly(t *testing.T) {
	a := EvalTargetFingerprint("you are a judge", "lead")
	b := EvalTargetFingerprint("you are a judge", "lead")
	if a != b {
		t.Error("the same behaviour must fingerprint the same")
	}
	if a == EvalTargetFingerprint("you are a judge", "worker") {
		t.Error("a tier change must fingerprint differently")
	}
}

// A run keyed to a deleted suite has a score and no way back to what was
// scored, so the history goes with the suite.
func TestDeletingASuiteTakesItsHistory(t *testing.T) {
	db := evalDB(t)
	saved, _ := SaveEvalSuite(db, goodSuite())
	other, _ := SaveEvalSuite(db, goodSuite())
	SaveEvalRun(db, EvalRun{SuiteID: saved.ID, Total: 2})
	SaveEvalRun(db, EvalRun{SuiteID: other.ID, Total: 2})

	DeleteEvalSuite(db, saved.ID)
	if _, ok := LoadEvalSuite(db, saved.ID); ok {
		t.Error("suite survived deletion")
	}
	if n := len(ListEvalRuns(db, saved.ID)); n != 0 {
		t.Errorf("%d orphaned runs left behind", n)
	}
	if n := len(ListEvalRuns(db, other.ID)); n != 1 {
		t.Errorf("another suite's history was deleted too: %d runs left", n)
	}
}
