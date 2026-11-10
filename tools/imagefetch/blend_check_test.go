package imagefetch

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// checkSession is a session whose model is the scripted fake.
func checkSession(t *testing.T, llm *FakeLLM) *ToolSession {
	t.Helper()
	SetImageDir(t.TempDir())
	return &ToolSession{Username: "alice", WorkspaceDir: t.TempDir(), LLM: (&AppCore{LLM: llm}).CreateSession(WORKER)}
}

var twoSources = []sourceImage{{ref: "image#r.ship1", data: []byte("SHIP-ONE")}, {ref: "image#r.ship2", data: []byte("SHIP-TWO")}}

// describeTurns is one blind description per picture (the result and each
// source), then the judge's answer: the descriptions run together, so each is
// the same scripted text whichever picture it is for.
func describeTurns(n int, judge string) []FakeTurn {
	var turns []FakeTurn
	for i := 0; i < n; i++ {
		turns = append(turns, FakeTurn{Content: "- a sleek white ship with twin engine pods, left of frame\nSetting: space"})
	}
	return append(turns, FakeTurn{Content: judge})
}

// Nothing that judges sees what it is looking for: each picture is described
// by a call that sees only that picture, and the judge sees no picture at all,
// only the instruction and the descriptions, and thinks.
func TestNothingThatJudgesSeesWhatItIsLookingFor(t *testing.T) {
	llm := &FakeLLM{Turns: describeTurns(3, "Source 1 takes: the white starship whole\nSource 1 in result: none has the saucer section\nSource 1 FOUND: no\n"+
		"Source 2 takes: the red battleship whole\nSource 2 in result: the battleship on the right\nSource 2 FOUND: yes")}
	got := checkSourcesInResult(checkSession(t, llm), twoSources, []byte("RESULT"), "the ship from the first picture fires on the ship from the second"+editCompositingGuard(), true)
	if llm.Calls() != 4 {
		t.Fatalf("%d calls, want three descriptions and one judge", llm.Calls())
	}
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		msgs := llm.Sent(i)
		if len(msgs) != 1 || len(msgs[0].Images) != 1 || msgs[0].Content != describeBlindPrompt {
			t.Fatalf("description %d did not see exactly one picture, blind: %+v", i, msgs)
		}
		seen[string(msgs[0].Images[0])] = true
	}
	for _, want := range []string{"RESULT", "SHIP-ONE", "SHIP-TWO"} {
		if !seen[want] {
			t.Errorf("%s was never described", want)
		}
	}
	judge := llm.Sent(3)
	if len(judge) != 1 || len(judge[0].Images) != 0 {
		t.Fatalf("the judge saw a picture: %+v", judge)
	}
	for _, want := range []string{"the ship from the first picture fires", "SOURCE 1, as described", "SOURCE 2, as described", "The RESULT, as described", "A part the instruction meant to replace is not missing"} {
		if !strings.Contains(judge[0].Content, want) {
			t.Errorf("the judge's question lacks %q", want)
		}
	}
	if strings.Contains(judge[0].Content, strings.TrimSpace(editCompositingGuard())) {
		t.Error("the compositing guard was handed to the judge as part of the instruction")
	}
	if cfg := llm.Config(3); cfg.Think == nil || !*cfg.Think || cfg.Caller != "imagefetch/source_check" {
		t.Errorf("judge config = think %v caller %q", cfg.Think, cfg.Caller)
	}
	if len(got) != 2 || got[0].found || !got[1].found || got[0].takes != "the white starship whole" {
		t.Fatalf("verdicts = %+v", got)
	}
	note := sourceCheckNote(got, 2, true, false)
	for _, want := range []string{
		"outranks your own look",
		"source 1 (image#r.ship1), from which the instruction takes the white starship whole",
		"in the result: none has the saucer section",
		"Do NOT present this picture as containing it",
		"Do NOT try again on your own",
		"that you can try again if they want",
		"tell the person plainly what may not have come through",
		"Do NOT build the next pass on this step",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "source 2") {
		t.Errorf("a found source was reported lost: %s", note)
	}
	// No cleverer retry is prescribed: the loss was the model, not the prompt.
	for _, gone := range []string{"a pass of its own", "placeholder", "do NOT describe its appearance", "larger, clearer"} {
		if strings.Contains(note, gone) {
			t.Errorf("note still says %q:\n%s", gone, note)
		}
	}
}

// A lost source whose picture is small is named with its size, and the person
// is told a larger picture may come through better: the one change on this
// side that can help when the model could not draw it.
func TestASmallLostSourceIsNamedWithItsSize(t *testing.T) {
	var tiny, big bytes.Buffer
	png.Encode(&tiny, image.NewRGBA(image.Rect(0, 0, 421, 237)))
	png.Encode(&big, image.NewRGBA(image.Rect(0, 0, 1024, 768)))
	if got := smallSize(tiny.Bytes()); got != "421x237" {
		t.Errorf("small picture size = %q", got)
	}
	if got := smallSize(big.Bytes()); got != "" {
		t.Errorf("a large picture was called small: %q", got)
	}
	if got := smallSize([]byte("not a picture")); got != "" {
		t.Errorf("an undecodable picture was called small: %q", got)
	}
	sources := []sourceImage{{ref: "image#r.ship1", data: tiny.Bytes()}, {ref: "image#r.ship2", data: big.Bytes()}}
	llm := &FakeLLM{Turns: describeTurns(3, "Source 1 FOUND: no\nSource 2 FOUND: no")}
	note := sourceCheckNote(checkSourcesInResult(checkSession(t, llm), sources, []byte("RESULT"), "both ships", true), 2, false, false)
	if !strings.Contains(note, "source 1 (image#r.ship1); its picture is only 421x237") || !strings.Contains(note, "a larger, clearer picture of it may come through better") {
		t.Errorf("the small source was not named with its size:\n%s", note)
	}
	if strings.Contains(note, "1024x768") {
		t.Errorf("a large source was called small:\n%s", note)
	}
}

// A face swap takes only the face from one source and replaces the face in
// the other: the judge is asked what the instruction takes, so the replaced
// face is not reported as a loss.
func TestAFaceSwapIsJudgedByWhatItTakes(t *testing.T) {
	llm := &FakeLLM{Turns: describeTurns(3, "Source 1 takes: the body, pose and background, its face meant to be replaced\nSource 1 in result: the same armoured figure and hall\nSource 1 FOUND: yes\n"+
		"Source 2 takes: the face\nSource 2 in result: the armoured figure's face has the same square jaw and grey beard\nSource 2 FOUND: yes")}
	got := checkSourcesInResult(checkSession(t, llm), twoSources, []byte("RESULT"), "the face from the second picture on the person in the first", true)
	if note := sourceCheckNote(got, 2, false, false); !strings.Contains(note, "what the instruction takes from each source was found") {
		t.Errorf("a correct swap was not reported as found: %q", note)
	}
}

// One-photo edits are checked too: the agent's look at a source beside its
// result is the look that reported a lost ship as present.
func TestAOnePhotoEditIsCheckedToo(t *testing.T) {
	llm := &FakeLLM{Turns: describeTurns(2, "Source 1 takes: the man, his face and build\nSource 1 in result: a different, younger man\nSource 1 FOUND: no")}
	got := checkSourcesInResult(checkSession(t, llm), twoSources[:1], []byte("RESULT"), "give him a crown", true)
	note := sourceCheckNote(got, 1, false, false)
	if !strings.Contains(note, "NOT in the result: source 1") || strings.Contains(note, "a pass of its own") {
		t.Errorf("one-photo note = %q", note)
	}
}

// A description or verdict that did not come back says nothing either way:
// an abstention counted as "missing" would send the agent redoing a good render.
func TestAnAbstentionSaysNothing(t *testing.T) {
	// The result could not be described: nothing to judge against.
	blind := &FakeLLM{Turns: []FakeTurn{{Content: "I can't see any image in your message.", Repeat: true}}}
	if got := checkSourcesInResult(checkSession(t, blind), twoSources, []byte("R"), "x", true); got != nil {
		t.Errorf("judged with no description of the result: %+v", got)
	}
	// The judge gave no FOUND line for anything.
	llm := &FakeLLM{Turns: describeTurns(3, "Source 1 takes: a ship\nSource 1 in result: hard to say")}
	if note := sourceCheckNote(checkSourcesInResult(checkSession(t, llm), twoSources, []byte("R"), "x", true), 2, false, false); note != "" {
		t.Errorf("an undecided judge produced %q", note)
	}
	// No model, no result: no check.
	if got := checkSourcesInResult(&ToolSession{}, twoSources, []byte("R"), "x", true); got != nil {
		t.Error("checked with no model")
	}
}

// Only some answered: the lost ones are still named, but "each was found" is
// not claimed on a partial set.
func TestAPartialAnswerClaimsOnlyWhatItKnows(t *testing.T) {
	if note := sourceCheckNote([]sourceVerdict{{n: 1, ref: "a", found: true}}, 2, false, false); note != "" {
		t.Errorf("claimed all found from one answer of two: %q", note)
	}
	note := sourceCheckNote([]sourceVerdict{{n: 2, ref: "b", takes: "the battleship"}}, 3, false, false)
	if !strings.Contains(note, "source 2 (b), from which the instruction takes the battleship") {
		t.Errorf("a lost source in a partial answer went unnamed: %q", note)
	}
}

// The judge's lines are read by source number, whatever markdown wraps them.
func TestTheJudgesLinesAreRead(t *testing.T) {
	got := parseSourceVerdicts("**Source 1 takes:** the saucer ship\n**Source 1 in result:** nothing like it\n**Source 1 FOUND:** No.\nSource 2 FOUND: yes\nSource 3 takes: a cat")
	if v, ok := got[1]; !ok || v.found || v.takes != "the saucer ship" || v.result != "nothing like it" {
		t.Errorf("source 1 = %+v %v", v, ok)
	}
	if v, ok := got[2]; !ok || !v.found {
		t.Errorf("source 2 = %+v %v", v, ok)
	}
	if _, ok := got[3]; ok {
		t.Error("a source with no FOUND line read as a verdict")
	}
}
