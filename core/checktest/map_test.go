package checktest

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// mapRegistry runs a test against a fresh database, map and registry.
func mapRegistry(t *testing.T) {
	t.Helper()
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	registry.mu.Lock()
	prev := registry.checks
	registry.checks = nil
	registry.mu.Unlock()
	store.mu.Lock()
	store.loaded, store.m = false, nil
	store.mu.Unlock()
	prints.mu.Lock()
	prints.m = nil
	prints.mu.Unlock()
	t.Cleanup(func() {
		RootDB = saved
		registry.mu.Lock()
		registry.checks = prev
		registry.mu.Unlock()
		store.mu.Lock()
		store.loaded, store.m = false, nil
		store.mu.Unlock()
		prints.mu.Lock()
		prints.m = nil
		prints.mu.Unlock()
	})
}

// echoCheck is a check whose model answers each case with its first word.
func echoCheck(key string, structured bool, wants ...string) Check {
	return Check{Key: key, Label: key, Structured: structured,
		Cases: func() []Case {
			var out []Case
			for i, w := range wants {
				out = append(out, Case{ID: string(rune('a' + i)), Title: "case " + w, Want: w})
			}
			return out
		},
		Run: func(ctx context.Context, llm LLM, c Case) Outcome {
			resp, err := llm.Chat(ctx, []Message{{Role: "user", Content: "say " + c.Want}})
			if err != nil {
				return Outcome{Err: err}
			}
			return Outcome{Got: strings.Fields(resp.Content + " x")[0]}
		}}
}

// A tier is given a structured check after two clean runs in a row on the
// same model and the check as it is; a miss sets it back to the start and
// is not asked again by Missing; a new model starts the count over; a check
// that is a reading is never given away, however it does.
func TestTheMapIsEarnedTwiceAndLapses(t *testing.T) {
	mapRegistry(t)
	Register(echoCheck("scan", true, "yes", "no"))
	Register(echoCheck("reading", false, "yes"))
	right := &FakeLLM{Turns: []FakeTurn{{Content: "yes", Repeat: true}}}
	judge := func(id string, llm LLM) []Model {
		return []Model{{Name: "judge", LLM: llm, Tier: MapJudge, Identity: id}}
	}
	// A model that says "yes" to everything gets the "no" case wrong.
	Run(context.Background(), judge("j1", right))
	if e, _ := MappingFor("scan"); e.Tiers[MapJudge].Clean != 0 || len(e.Tiers[MapJudge].Missed) != 1 {
		t.Fatalf("a miss: %+v", e.Tiers[MapJudge])
	}
	if m := Missing(judge("j1", right)); len(m) != 0 {
		t.Errorf("a check the judge missed is asked again: %v", m)
	}
	if m := Missing(judge("j2", right)); len(m) != 1 || m[0] != "scan" {
		t.Errorf("a new judge is not asked the structured check (and only it): %v", m)
	}

	// A model that says what it is asked gets both right.
	echo := &echoLLM{}
	Run(context.Background(), judge("j2", echo))
	e, _ := MappingFor("scan")
	if r := e.Tiers[MapJudge]; r.Clean != 1 || r.mapped("j2") {
		t.Fatalf("one clean run: %+v", r)
	}
	if m := Missing(judge("j2", echo)); len(m) != 1 {
		t.Errorf("one clean run short is not missing: %v", m)
	}
	Run(context.Background(), judge("j2", echo))
	e, _ = MappingFor("scan")
	if r := e.Tiers[MapJudge]; !r.mapped("j2") || r.mapped("j1") {
		t.Fatalf("two clean runs: %+v", r)
	}
	if m := Missing(judge("j2", echo)); len(m) != 0 {
		t.Errorf("a mapped check is still missing: %v", m)
	}
	if r, _ := MappingFor("reading"); !r.Tiers[MapJudge].mapped("j2") {
		t.Errorf("the reading's results are not kept: %+v", r)
	}

	// The gate reads the identity the tier is now; a test model has none,
	// so the gate is checked through its parts.
	if !mapGateFor("scan", MapJudge, "j2") || mapGateFor("scan", MapJudge, "j1") {
		t.Error("the gate does not follow the model")
	}
	if mapGateFor("reading", MapJudge, "j2") {
		t.Error("a reading was given to the judge")
	}
	if !strings.Contains(Standing("reading", MapJudge), "kept on the worker") {
		t.Errorf("a reading's standing: %q", Standing("reading", MapJudge))
	}

	// The check changing (its cases, so its fingerprint) lapses it.
	registry.mu.Lock()
	registry.checks[0] = echoCheck("scan", true, "yes", "no", "maybe")
	registry.mu.Unlock()
	prints.mu.Lock()
	prints.m = nil
	prints.mu.Unlock()
	if mapGateFor("scan", MapJudge, "j2") {
		t.Error("a changed check kept its mapping")
	}
	if m := Missing(judge("j2", echo)); len(m) != 1 {
		t.Errorf("a changed check is not asked again: %v", m)
	}
}

// echoLLM says the last word of what it is asked.
type echoLLM struct{}

func (echoLLM) Chat(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
	f := strings.Fields(msgs[len(msgs)-1].Content)
	return &Response{Content: f[len(f)-1]}, nil
}
func (e echoLLM) ChatStream(ctx context.Context, msgs []Message, _ StreamHandler, opts ...ChatOption) (*Response, error) {
	return e.Chat(ctx, msgs, opts...)
}

// A fingerprint is the same every time it is taken, and moves with the
// prompts a check sends.
func TestAFingerprintFollowsThePrompts(t *testing.T) {
	a := echoCheck("k", true, "yes")
	if fingerprint(a) == "" || fingerprint(a) != fingerprint(a) {
		t.Fatal("not stable")
	}
	b := a
	b.Run = func(ctx context.Context, llm LLM, c Case) Outcome {
		llm.Chat(ctx, []Message{{Role: "user", Content: "please say " + c.Want}})
		return Outcome{}
	}
	if fingerprint(a) == fingerprint(b) {
		t.Error("a reworded prompt has the same fingerprint")
	}
	if fingerprint(Check{Key: "none", Cases: func() []Case { return nil }}) != "" {
		t.Error("a check with no cases has a fingerprint")
	}
}

// System One is mapped by its answers that would have stood: an unsure one
// (live, the full answer is asked) is not a miss, a sure wrong one is; and a
// check with no System One form is never asked of it, nor missing for it.
func TestSystemOneIsMappedByWhatWouldHaveStood(t *testing.T) {
	mapRegistry(t)
	unsure := true
	gate := echoCheck("gate", true, "yes", "no")
	gate.Decide = func(ctx context.Context, llm LLM, c Case) Outcome {
		if c.Want == "no" && unsure {
			return Outcome{Got: "yes", Unsure: true}
		}
		if c.Want == "no" {
			return Outcome{Got: "yes"}
		}
		return Outcome{Got: c.Want}
	}
	Register(gate)
	Register(echoCheck("formless", true, "yes"))
	s1 := []Model{{Name: "system one", LLM: &echoLLM{}, Decide: true, Tier: MapSystemOne, Identity: "s1"}}
	Run(context.Background(), s1)
	Run(context.Background(), s1)
	if !mapGateFor("gate", MapSystemOne, "s1") {
		e, _ := MappingFor("gate")
		t.Fatalf("two runs, every answer right or unsure: %+v", e.Tiers[MapSystemOne])
	}
	if m := Missing(s1); len(m) != 0 {
		t.Errorf("missing after mapping, or a formless check asked of System One: %v", m)
	}
	unsure = false
	Run(context.Background(), s1)
	if mapGateFor("gate", MapSystemOne, "s1") {
		t.Error("a sure wrong answer kept the mapping")
	}
}

// A check that declares its question as a fixed choice gets its System One
// form from it: an answer needing a reason, or one System One is not sure
// of, is unsure; a sure reasonless answer is its answer.
func TestAQuickChoiceIsTheSystemOneForm(t *testing.T) {
	mapRegistry(t)
	Register(Check{Key: "quick", Label: "quick", Structured: true,
		Cases: func() []Case { return []Case{{ID: "a", Want: "clean"}} },
		Run:   func(context.Context, LLM, Case) Outcome { return Outcome{Got: "clean"} },
		Quick: func(c Case) QuickChoice {
			return QuickChoice{Key: "quick", Ask: "Clean?", Answers: []QuickAnswer{
				{Key: "clean", Means: "Clean"}, {Key: "flagged", Means: "Flagged", Reason: true}}}
		}})
	ch, _ := checkFor("quick")
	if ch.Decide == nil {
		t.Fatal("no System One form was built")
	}
	// A model whose first token is the label shown first: asked both ways,
	// even, so unsure.
	llm := &FakeLLM{Turns: []FakeTurn{{Content: "A", Repeat: true}}}
	o := ch.Decide(context.Background(), llm, Case{ID: "a", Want: "clean"})
	if o.Err != nil || (!o.Unsure && o.Got != "no decision") {
		t.Errorf("an even split stood: %+v", o)
	}
}

// A check whose own code asks the map whether it is mapped (as the scan does
// before asking System One) can still be fingerprinted: taking the
// fingerprint runs that code, and the map answers "not mapped" while it is
// being taken instead of taking it again, which recursed until the stack ran
// out and killed the server.
func TestAFingerprintDoesNotRecurseThroughTheMap(t *testing.T) {
	mapRegistry(t)
	asked := 0
	c := echoCheck("self", true, "yes")
	inner := c.Run
	c.Run = func(ctx context.Context, llm LLM, tc Case) Outcome {
		asked++
		if asked > 5 {
			t.Fatal("the fingerprint recursed through the map")
		}
		if mappedNow("self", MapJudge) {
			t.Error("a check counted as mapped while its fingerprint was being taken")
		}
		return inner(ctx, llm, tc)
	}
	Register(c)
	saveMapping(Mapping{Key: "self", Fingerprint: "an-old-one", Tiers: map[string]TierResult{MapJudge: {Model: "j", Clean: 2}}})
	if Fingerprint(c) == "" || asked != 1 {
		t.Errorf("fingerprint taken with the check run %d times", asked)
	}
}

// Clearing the map forgets every mapping, in memory and stored: what was
// mapped is not, and is missing again.
func TestClearingTheMapSendsEverythingBack(t *testing.T) {
	mapRegistry(t)
	Register(echoCheck("scan", true, "yes"))
	judge := []Model{{Name: "judge", LLM: &echoLLM{}, Tier: MapJudge, Identity: "j"}}
	Run(context.Background(), judge)
	Run(context.Background(), judge)
	if !mapGateFor("scan", MapJudge, "j") {
		t.Fatal("setup: not mapped")
	}
	if n := ClearMap(); n != 1 || mapGateFor("scan", MapJudge, "j") || len(RootDB.Keys(mapTable)) != 0 {
		t.Errorf("after clearing %d: still mapped, or still stored", n)
	}
	if m := Missing(judge); len(m) != 1 {
		t.Errorf("a cleared check is not missing: %v", m)
	}
	// And the last run's results go with it: the table showed answers from
	// before the clear.
	if rep, ok := Last(); ok {
		t.Errorf("the last run outlived the clear: %d rows", len(rep.Rows))
	}
}
