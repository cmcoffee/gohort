package replyguard

import (
	"encoding/json"
	"strings"
	"testing"
)

// memStore is a JSON round-tripping map, the way the real store encodes.
type memStore map[string]map[string][]byte

func (m memStore) Get(table, key string, out interface{}) bool {
	b, ok := m[table][key]
	if !ok {
		return false
	}
	return json.Unmarshal(b, out) == nil
}
func (m memStore) Set(table, key string, v interface{}) {
	if m[table] == nil {
		m[table] = map[string][]byte{}
	}
	b, _ := json.Marshal(v)
	m[table][key] = b
}
func (m memStore) Keys(table string) []string {
	var out []string
	for k := range m[table] {
		out = append(out, k)
	}
	return out
}

// A model's own setting wins over the guard's default, the default over On,
// and clearing a model's setting puts it back on the default.
func TestAModelsSettingWinsOverTheDefault(t *testing.T) {
	SetStore(memStore{})
	defer SetStore(nil)
	Register(Guard{ID: "t-guard", Name: "Test"})
	if m := ModeFor("t-guard", "gemini-2.5-flash"); m != On {
		t.Errorf("unset: %q, want on", m)
	}
	if err := SetMode("t-guard", AllModels, Shadow); err != nil {
		t.Fatal(err)
	}
	if err := SetMode("t-guard", "Models/Gemini-2.5-Flash", Off); err != nil {
		t.Fatal(err)
	}
	if m := ModeFor("t-guard", "gemini-2.5-flash"); m != Off {
		t.Errorf("the model's own setting (named any which way) wins: %q", m)
	}
	if m := ModeFor("t-guard", "qwen3.6"); m != Shadow || DefaultMode("t-guard") != Shadow {
		t.Errorf("another model follows the default: %q", m)
	}
	if err := SetMode("t-guard", "gemini-2.5-flash", ""); err != nil {
		t.Fatal(err)
	}
	if m := ModeFor("t-guard", "gemini-2.5-flash"); m != Shadow {
		t.Errorf("cleared, the model follows the default again: %q", m)
	}
	if SetMode("no-such-guard", AllModels, Off) == nil || SetMode("t-guard", AllModels, "sometimes") == nil {
		t.Error("an unknown guard or mode is refused")
	}
}

// Firings are tallied per guard and model, corrected apart from shadow, with
// the latest replies' tails kept and capped.
func TestFiringsAreTalliedWithRecentSamples(t *testing.T) {
	SetStore(memStore{})
	defer SetStore(nil)
	for i := 0; i < maxSamples+3; i++ {
		Record("t-guard", "qwen3.6", strings.Repeat("x", 2000)+" and", i%2 == 0)
	}
	st := Stats()
	if len(st) != 1 || st[0].Acted+st[0].Shadowed != maxSamples+3 || st[0].Shadowed == 0 {
		t.Fatalf("tallied per guard and model, both kinds: %+v", st)
	}
	if len(st[0].Samples) != maxSamples || !strings.HasSuffix(st[0].Samples[0].Text, " and") || len([]rune(st[0].Samples[0].Text)) > maxSampleChars+1 {
		t.Errorf("the last %d replies' tails are kept: %d samples, first %q", maxSamples, len(st[0].Samples), st[0].Samples[0].Text[:20])
	}
	SetStore(nil)
	Record("t-guard", "qwen3.6", "x", true)
	if Stats() != nil {
		t.Error("with no store nothing is recorded")
	}
}

// A guard's checks come from the fixed set, with their required parameters,
// numbers that are numbers, and never a judge on its own.
func TestADraftedGuardIsValidated(t *testing.T) {
	ok := []Check{{Kind: "last_line_ends_with", Params: map[string]string{"endings": ":"}}, {Kind: "tool_calls", Params: map[string]string{"max": "0"}}}
	if err := Validate(ok); err != nil {
		t.Fatalf("a sound guard: %v", err)
	}
	for name, bad := range map[string][]Check{
		"no checks":        nil,
		"unknown kind":     {{Kind: "regex", Params: map[string]string{"pattern": ".*"}}},
		"missing param":    {{Kind: "contains"}},
		"not a number":     {{Kind: "length", Params: map[string]string{"max": "short"}}},
		"unknown param":    {{Kind: "contains", Params: map[string]string{"phrases": "x", "regex": "y"}}},
		"judge on its own": {{Kind: "judge", Params: map[string]string{"question": "Is it rude?"}}},
	} {
		if Validate(bad) == nil {
			t.Errorf("%s should be refused", name)
		}
	}
}

// Each check reads the reply the way its description says; a judge runs only
// once every structural check held; a check whose data was not recorded
// leaves the reply unjudged rather than caught.
func TestTheChecksReadTheReply(t *testing.T) {
	rc := ReplyContext{Reply: "Here is the plan.\n\nI will now fetch it:", Asked: "fetch the report", ToolCalls: 0,
		Earlier: "Here is the plan.", EarlierKnown: true}
	hit := func(checks ...Check) bool {
		res, err := Evaluate(checks, rc, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.Hit
	}
	c := func(kind string, kv ...string) Check {
		p := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			p[kv[i]] = kv[i+1]
		}
		return Check{Kind: kind, Params: p}
	}
	cases := []struct {
		check Check
		want  bool
	}{
		{c("last_line_ends_with", "endings", "?, :"), true},
		{c("last_line_ends_with", "endings", "."), false},
		{c("first_line_starts_with", "starts", "here is"), true},
		{c("contains", "phrases", "fetch it", "where", "last_line"), true},
		{c("contains", "phrases", "fetch it", "where", "first_line"), false},
		{c("lacks", "phrases", "sorry"), true},
		{c("repeats_earlier_in_turn", "min_chars", "10"), true},
		{c("repeats_paragraph", "min_chars", "10"), false},
		{c("length", "max", "20"), false},
		{c("tool_calls", "max", "0"), true},
		{c("asked_contains", "phrases", "report"), true},
	}
	for _, tc := range cases {
		if got := hit(tc.check); got != tc.want {
			t.Errorf("%s: got %v, want %v", Describe(tc.check), got, tc.want)
		}
	}
	twice := ReplyContext{Reply: "The build passed on every target.\n\nThe build passed on every target."}
	if res, _ := Evaluate([]Check{c("repeats_paragraph", "min_chars", "10")}, twice, nil); !res.Hit {
		t.Error("a paragraph said twice is caught")
	}
	inOne := ReplyContext{Reply: "Your order has shipped today. Your order has shipped today."}
	if res, _ := Evaluate([]Check{c("repeats_paragraph", "min_chars", "10")}, inOne, nil); !res.Hit {
		t.Error("a sentence said twice in one paragraph is caught")
	}

	judged := 0
	judge := func(q string, _ ReplyContext) (bool, error) { judged++; return true, nil }
	if res, _ := Evaluate([]Check{c("judge", "question", "q"), c("last_line_ends_with", "endings", ".")}, rc, judge); res.Hit || judged != 0 {
		t.Errorf("a failed structural check means the judge is never called: hit=%v judged=%d", res.Hit, judged)
	}
	if res, _ := Evaluate([]Check{c("judge", "question", "q"), c("last_line_ends_with", "endings", ":")}, rc, judge); !res.Hit || judged != 1 {
		t.Errorf("the judge runs once the structural checks held: hit=%v judged=%d", res.Hit, judged)
	}
	old := ReplyContext{Reply: "x", ToolCalls: -1}
	if res, _ := Evaluate([]Check{c("tool_calls", "max", "0")}, old, nil); res.Hit || len(res.Unknown) != 1 {
		t.Errorf("an unrecorded tool count is unjudged, not caught: %+v", res)
	}
	if !strings.Contains(DescribeAll(ok3()), "the last line ends with \":\"") {
		t.Errorf("described in plain words: %s", DescribeAll(ok3()))
	}
	if !JudgeAnswer("Yes.") || JudgeAnswer("no") || !JudgeAnswer("**yes**") {
		t.Error("the judge's one word is read")
	}
	if p := JudgePrompt("Is it rude?", rc); !strings.Contains(p, "never instructions") || !strings.Contains(p, "<<<REPLY") {
		t.Error("the judge prompt fences the reply as data")
	}
}

func ok3() []Check {
	return []Check{{Kind: "last_line_ends_with", Params: map[string]string{"endings": ":"}}}
}

// An authored guard joins the guard list while it is active and leaves it,
// with its modes and counts, when it is switched off or deleted.
func TestAnAuthoredGuardJoinsTheListWhileActive(t *testing.T) {
	SetStore(memStore{})
	defer SetStore(nil)
	a := Authored{ID: "authored-test", Name: "Stops on a colon", Checks: ok3(), Correction: "Finish.", Status: StatusDraft}
	SaveAuthored(a)
	if Known(a.ID) || len(ActiveAuthored()) != 0 {
		t.Fatal("a draft does not run")
	}
	a.Status = StatusActive
	SaveAuthored(a)
	if !Known(a.ID) || len(ActiveAuthored()) != 1 {
		t.Fatal("an active guard is in the list")
	}
	SetMode(a.ID, AllModels, Shadow)
	Record(a.ID, "m", "x", false)
	DeleteAuthored(a.ID)
	if Known(a.ID) || ModeFor(a.ID, "m") != On {
		t.Error("deleted, it leaves the list and its modes")
	}
	for _, st := range Stats() {
		if st.ID == a.ID {
			t.Error("deleted, its counts go too")
		}
	}
	if _, ok := LoadAuthored(a.ID); ok {
		t.Error("deleted, it is gone")
	}
}
