package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The judge asks for a verbatim quote and gets one — quotation marks included,
// unescaped. That was answered with "no opinion" on a verdict the model had
// actually delivered. The exact shape from a production log.
func TestGroundingJudgeSalvagesUnescapedQuotes(t *testing.T) {
	reply := "{\"verdict\":\"ASSERTED\",\"claim\":\"`perm_delete_expired_workspaces` processes folders \"in **descending order** (deepest folders first)\" so children go before parents.\",\"basis\":\"the cleanup walks \"deepest first\" per the notes\"}"
	app, _ := judgeWith(t, reply)
	ev := TurnGroundingEvidence{
		Unchecked: []string{"the cleanup walks \"deepest first\" per the notes"},
		Reply:     "`perm_delete_expired_workspaces` processes folders \"in **descending order** (deepest folders first)\" so children go before parents.",
	}
	v, ok := app.judgeTurnGrounding(context.Background(), ev)
	if !ok || !v.Asserted {
		t.Fatalf("a verdict with unescaped quotes must be recovered, got ok=%v v=%+v", ok, v)
	}
	if !strings.Contains(v.Claim, `"in **descending order** (deepest folders first)"`) {
		t.Errorf("the claim must survive with its own quotes, got %q", v.Claim)
	}
	if v.Basis != "the cleanup walks \"deepest first\" per the notes" {
		t.Errorf("basis = %q", v.Basis)
	}

	// CLEAN with a decorative unescaped quote is still an acquittal, reported
	// as one — the loop must know the judge ran.
	app, _ = judgeWith(t, `{"verdict":"CLEAN","claim":"","basis":"said "fine" only"}`)
	if v, ok := app.judgeTurnGrounding(context.Background(), ev); !ok || v.Asserted {
		t.Errorf("a recoverable CLEAN must stand as an acquittal, got ok=%v v=%+v", ok, v)
	}

	// Prose stays no opinion: salvage only rescues an object with a verdict.
	app, _ = judgeWith(t, "Looks CLEAN to me, nothing asserted")
	if _, ok := app.judgeTurnGrounding(context.Background(), ev); ok {
		t.Error("prose in place of JSON must not stand")
	}
}

func TestSalvageJudgeJSON(t *testing.T) {
	keys := []string{"verdict", "claim", "why", "machinery"}
	// Keys in a different order than asked, whitespace, a fenced block, and
	// an escaped quote next to an unescaped one.
	raw := "```json\n{ \"why\" : \"it said \"done\" and \\\"shipped\\\"\",\n  \"verdict\": \"UNKEPT\",\n \"claim\":\"I have \"finished\" the work\" }\n```"
	got, ok := salvageJudgeJSON(raw, keys)
	if !ok {
		t.Fatal("salvage failed")
	}
	want := map[string]string{
		"verdict": "UNKEPT",
		"claim":   `I have "finished" the work`,
		"why":     `it said "done" and "shipped"`,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	if _, present := got["machinery"]; present {
		t.Error("a key the model did not write must be absent, not empty")
	}
	if _, ok := salvageJudgeJSON(`{"claim":"no verdict here"}`, keys); ok {
		t.Error("an object without a verdict must not salvage")
	}
	if _, ok := salvageJudgeJSON(`the verdict is UNKEPT`, keys); ok {
		t.Error("prose mentioning the word must not salvage")
	}
}
