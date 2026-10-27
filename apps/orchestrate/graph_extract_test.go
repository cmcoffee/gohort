package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// fakeChat is a FactChatFunc that returns canned content, standing in for the
// worker LLM so extraction can be tested without a model.
func fakeChat(content string) FactChatFunc {
	return func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
		return &Response{Content: content}, nil
	}
}

// TestJudgeGraphTriples: canned JSON parses into triples; malformed or empty
// inputs fail safe to nil so nothing gets written.
func TestJudgeGraphTriples(t *testing.T) {
	got := judgeGraphTriples(fakeChat(`[{"subject":"A","subject_kind":"person","relation":"knows","object":"B","object_kind":"person"}]`), "A knows B")
	if len(got) != 1 || got[0].Subject != "A" || got[0].Relation != "knows" || got[0].Object != "B" {
		t.Fatalf("parse failed: %+v", got)
	}
	if got := judgeGraphTriples(fakeChat("sorry, no JSON here"), "x"); got != nil {
		t.Fatalf("malformed reply must yield nil, got %+v", got)
	}
	if got := judgeGraphTriples(fakeChat("[]"), "   "); got != nil {
		t.Fatalf("empty text must yield nil (no chat)")
	}
}

// TestExtractGraphFromText: valid triples become graph edges stamped observed;
// entities alias-merge by name; empty/self-loop triples are skipped.
func TestExtractGraphFromText(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ns := "test-ns"
	chat := fakeChat(`[
		{"subject":"Craig","subject_kind":"person","relation":"owns","object":"Hansel","object_kind":"thing"},
		{"subject":"Craig","subject_kind":"person","relation":"lives in","object":"Santa Cruz","object_kind":"place"},
		{"subject":"","relation":"x","object":"y"},
		{"subject":"Loop","relation":"is","object":"Loop"}
	]`)

	n := extractGraphFromText(db, ns, "Craig owns Hansel and lives in Santa Cruz.", chat)
	if n != 2 {
		t.Fatalf("expected 2 edges (empty + self-loop skipped), got %d", n)
	}

	craig, ok := FindGraphEntity(db, ns, "Craig")
	if !ok {
		t.Fatal("Craig entity not created")
	}
	edges := GraphEdgesFrom(db, ns, craig.ID)
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges from Craig (alias-merged to one node), got %d", len(edges))
	}
	for _, e := range edges {
		if e.Source != MemSourceObserved {
			t.Errorf("extracted edge %q should be Source=observed, got %v", e.Rel, e.Source)
		}
	}
}

// TestFoldUserText: the fold extraction input is the USER turns of the batch
// both sides, each labelled, whitespace-only messages skipped, and capped.
func TestFoldExtractTextShape(t *testing.T) {
	folded := []Message{
		{Role: "user", Content: "Craig owns a dog named Hansel."},
		{Role: "assistant", Content: "Noted — Hansel the dog."},
		{Role: "user", Content: "  Craig works at Acme.  "},
		{Role: "assistant", Content: "Got it."},
		{Role: "user", Content: "   "}, // whitespace-only → skipped
	}
	got := foldExtractText(folded)
	want := "USER SAID:\nCraig owns a dog named Hansel.\nCraig works at Acme.\n\nASSISTANT SAID:\nNoted — Hansel the dog.\nGot it."
	if got != want {
		t.Fatalf("foldExtractText =\n%q\nwant\n%q", got, want)
	}
	if foldExtractText(nil) != "" {
		t.Fatal("empty batch should yield empty string")
	}

	// Cap: a very long user turn is truncated to the max, and leaves no room
	// for the assistant half.
	big := strings.Repeat("x", foldExtractMaxChars+500)
	got = foldExtractText([]Message{{Role: "user", Content: big}})
	if !strings.HasPrefix(got, "USER SAID:\n") {
		t.Fatalf("expected the user label, got %.20q", got)
	}
	// The budget covers the ASSEMBLED string, labels included — it exists so
	// the worker prompt cannot blow up, and a cap measuring only the content
	// is one the labels walk straight past.
	if len(got) > foldExtractMaxChars {
		t.Fatalf("assembled input outgrew its budget: %d > %d", len(got), foldExtractMaxChars)
	}
	if len(got) < foldExtractMaxChars-64 {
		t.Fatalf("the user half should fill the budget, got %d", len(got))
	}
}

// TestMaybeExtractGraphShortCircuits: the synchronous guards (gate off, text too
// short) write nothing and never spawn the background pass.
func TestMaybeExtractGraphShortCircuits(t *testing.T) {
	longText := "Craig owns a dog named Hansel and works at Acme in Santa Cruz."
	triples := `[{"subject":"Craig","relation":"owns","object":"Hansel"}]`

	// Gate off → nothing, even with valid long text.
	off := &DBase{Store: kvlite.MemStore()}
	off.Set(WebTable, TunableGraphExtract, float64(0))
	SetTunablesDB(off)
	db1 := &DBase{Store: kvlite.MemStore()}
	maybeExtractGraph(db1, "ns", longText, fakeChat(triples))
	if ents := ListGraphEntities(db1, "ns"); len(ents) != 0 {
		SetTunablesDB(nil)
		t.Fatalf("gate off must write nothing, got %d entities", len(ents))
	}

	// Gate on but text below the length floor → nothing (no goroutine).
	on := &DBase{Store: kvlite.MemStore()}
	on.Set(WebTable, TunableGraphExtract, float64(1))
	SetTunablesDB(on)
	db2 := &DBase{Store: kvlite.MemStore()}
	maybeExtractGraph(db2, "ns", "hi", fakeChat(triples))
	if ents := ListGraphEntities(db2, "ns"); len(ents) != 0 {
		SetTunablesDB(nil)
		t.Fatalf("too-short text must write nothing, got %d entities", len(ents))
	}
	SetTunablesDB(nil)
}

// TestFoldUserTextRuneSafe: the fold-extraction cap must cut on a rune
// boundary — a byte-index slice could split a UTF-8 sequence and hand the
// worker prompt an invalid trailing byte.
func TestFoldUserTextRuneSafe(t *testing.T) {
	long := "a" + strings.Repeat("€", 4000) // 3-byte rune, misaligned by the leading ascii byte
	out := foldExtractText([]Message{{Role: "user", Content: long}})
	if len(out) > foldExtractMaxChars {
		t.Fatalf("cap not applied: %d bytes", len(out))
	}
	if !utf8.ValidString(out) {
		t.Fatal("cap split a UTF-8 sequence")
	}
}

// Automatic extraction writes into an agent's graph only where inferred memory
// may accumulate, and files entities, not code. Both checks hold whatever the
// extractor returns.

func TestGraphExtractionKeepsToTheMemorySwitches(t *testing.T) {
	for _, c := range []struct {
		name         string
		agent        AgentRecord
		keepsNothing bool
		want         bool
	}{
		{"an ordinary conversation", AgentRecord{}, false, true},
		{"an agent with inferred memory off", AgentRecord{DisableInferred: true}, false, false},
		{"an incognito session or a Clean turn", AgentRecord{}, true, false},
		{"both", AgentRecord{DisableInferred: true}, true, false},
	} {
		if got := graphExtractAllowed(c.agent, c.keepsNothing); got != c.want {
			t.Errorf("%s: allowed = %v, want %v", c.name, got, c.want)
		}
	}
}

// Every place that runs an extraction asks the rule first, so a new caller
// cannot write into a graph the switches say keeps nothing.
func TestEveryExtractionCallerAsksTheRule(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	callers := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			trimmed := strings.TrimSpace(l)
			if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			if !strings.Contains(l, "maybeExtractGraph(") && !strings.Contains(l, "extractGraphFromFold(") {
				continue
			}
			callers++
			asked := false
			for j := i - 1; j >= 0 && j >= i-3; j-- {
				if strings.Contains(lines[j], "graphExtractAllowed(") {
					asked = true
				}
			}
			if !asked {
				t.Errorf("%s:%d runs an extraction without asking graphExtractAllowed first", f, i+1)
			}
		}
	}
	if callers < 2 {
		t.Errorf("found %d extraction callers, want the per-turn one and the fold one", callers)
	}
}

func TestCodeShapedNamesAreNotEntities(t *testing.T) {
	for _, code := range []string{"operator_tools.go", "main.go", "tune_graph_cap", "/api/send", "RunAgentLoop()", "x=1", "`ls -la`", `C:\temp`} {
		if !graphNameLooksLikeCode(code) {
			t.Errorf("%q should not be filed as an entity", code)
		}
	}
	for _, name := range []string{"Santa Cruz", "Hansel", "ARC Raiders", "Robin Vale", "Dr. Smith", "U.S.", "St.Louis", "Moltbook"} {
		if graphNameLooksLikeCode(name) {
			t.Errorf("%q is a name, and was taken for code", name)
		}
	}
}

func TestExtractionFilesEntitiesNotCode(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ns := "test-ns"
	chat := fakeChat(`[
		{"subject":"Robin","subject_kind":"person","relation":"owns","object":"Hansel","object_kind":"thing"},
		{"subject":"Robin","subject_kind":"person","relation":"edited","object":"operator_tools.go","object_kind":"thing"},
		{"subject":"tune_graph_cap","subject_kind":"thing","relation":"limits","object":"Robin","object_kind":"person"}
	]`)
	if n := extractGraphFromText(db, ns, "Robin owns Hansel and edited operator_tools.go.", chat); n != 1 {
		t.Fatalf("want only the pet filed, %d edge(s) written", n)
	}
	if _, ok := FindGraphEntity(db, ns, "operator_tools.go"); ok {
		t.Error("a file name was filed as an entity")
	}
	if _, ok := FindGraphEntity(db, ns, "Hansel"); !ok {
		t.Error("a named pet was not filed")
	}
}

// Reading only the user's half was reading the wrong half: in an assistant that
// investigates, the user asks and the ANSWER carries the entities. The graph
// stayed empty while the conversation was full of what it exists to hold.

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
