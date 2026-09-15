package orchestrate

// Reading only the user's half was reading the wrong half: in an assistant that
// investigates, the user asks and the ANSWER carries the entities. The graph
// stayed empty while the conversation was full of what it exists to hold.

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestFoldExtractReadsBothSides(t *testing.T) {
	got := foldExtractText([]Message{
		{Role: "user", Content: "why did the node-7 host fail to start?"},
		{Role: "assistant", Content: "node-7 runs the batch service with ten workers."},
	})
	if !strings.Contains(got, "node-7 host fail") {
		t.Errorf("the user's half must survive: %q", got)
	}
	if !strings.Contains(got, "runs the batch service") {
		t.Errorf("the assistant's half is where the entities are: %q", got)
	}
}

// Who said it decides whether it is a fact, so the judge has to be able to tell.
func TestFoldExtractLabelsTheSpeakers(t *testing.T) {
	got := foldExtractText([]Message{
		{Role: "user", Content: "what runs there?"},
		{Role: "assistant", Content: "the batch service, ten workers."},
	})
	if !strings.Contains(got, "USER SAID:") || !strings.Contains(got, "ASSISTANT SAID:") {
		t.Errorf("both sides must be attributed: %q", got)
	}
	if strings.Index(got, "USER SAID:") > strings.Index(got, "ASSISTANT SAID:") {
		t.Errorf("the user's half goes first: %q", got)
	}
}

// Tool messages are raw capture — log lines and dumps, where a triple is as
// likely to come from example output as from a fact about this deployment.
func TestFoldExtractSkipsToolOutput(t *testing.T) {
	got := foldExtractText([]Message{
		{Role: "user", Content: "check the config"},
		{Role: "tool", Content: "SERVER_NAME=example.internal OWNER=acme-corp"},
	})
	if strings.Contains(got, "acme-corp") {
		t.Errorf("tool output must not reach the extractor: %q", got)
	}
}

// One assistant turn can outrun every user message in a fold combined, so
// appending in message order would let a single answer push the conversation
// out of the window. The verbose half is the one that gets cut.
func TestTheAssistantHalfIsWhatGetsTruncated(t *testing.T) {
	user := "which node?"
	got := foldExtractText([]Message{
		{Role: "user", Content: user},
		{Role: "assistant", Content: strings.Repeat("x", foldExtractMaxChars*2)},
	})
	if !strings.Contains(got, user) {
		t.Error("the user's half must survive a long reply")
	}
	if len(got) > foldExtractMaxChars+len("USER SAID:\n\nASSISTANT SAID:\n")+len(user)+2 {
		t.Errorf("the input outgrew its budget: %d bytes", len(got))
	}
}

// The live-turn path labels the same way, so the judge reads one shape whether
// a relationship is caught on the turn or later when it folds.
func TestTurnExtractTextMatchesTheFoldShape(t *testing.T) {
	got := turnExtractText("why did it fail?", &Response{Content: "node-7 runs the batch service."})
	if !strings.Contains(got, "USER SAID:") || !strings.Contains(got, "ASSISTANT SAID:") {
		t.Errorf("the live turn must be attributed like a fold: %q", got)
	}
	// No reply: degrades to exactly what this path passed before.
	if got := turnExtractText("just the question", nil); got != "just the question" {
		t.Errorf("a turn with no reply should pass the user text alone, got %q", got)
	}
	if got := turnExtractText("q", &Response{Content: "   "}); got != "q" {
		t.Errorf("an empty reply should not add a label, got %q", got)
	}
}

// The judge decides what is a fact, so it has to be told the labels mean
// different things — otherwise a hypothesis the assistant was still testing
// becomes an edge.
func TestTheJudgeIsToldNotToPromoteSpeculation(t *testing.T) {
	body, err := os.ReadFile("graph_extract.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	for _, want := range []string{"USER SAID", "ASSISTANT SAID", "ASSERTS", "hedged"} {
		if !strings.Contains(src, want) {
			t.Errorf("the extraction rules must mention %q: they decide whether a guess becomes an edge", want)
		}
	}
}
