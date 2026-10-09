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

// A tier's own setting wins over the all-tiers one, which wins over the
// defaults; each part follows the scope above until it is set; clearing a part
// follows the scope above again; Revert puts the guard back as shipped. A
// tier setting remembers the model the tier was running.
func TestATiersSettingWinsOverAllTiers(t *testing.T) {
	SetStore(memStore{})
	defer SetStore(nil)
	Register(Guard{ID: "t-guard", Name: "Test", Note: "Shipped note."})
	Register(Guard{ID: "t-coded", Name: "Coded note"})
	if e := Resolve("t-guard", Worker); e.Mode != On || e.Retries != DefaultRetries || e.Note != "" {
		t.Errorf("unset: %+v", e)
	}
	NoteModel(Worker, "Qwen3.6")
	must := func(s Setting) {
		t.Helper()
		if err := Put(s); err != nil {
			t.Fatal(err)
		}
	}
	must(Setting{ID: "t-guard", Scope: AllTiers, Mode: Shadow, Retries: 3})
	must(Setting{ID: "t-guard", Scope: Worker, Mode: Off})
	must(Setting{ID: "t-guard", Scope: Worker, Note: "Qwen, finish the sentence."})
	w, l := Resolve("t-guard", Worker), Resolve("t-guard", Lead)
	if w.Mode != Off || w.Retries != 3 || w.Note != "Qwen, finish the sentence." {
		t.Errorf("the worker's own parts win, the rest follow all tiers: %+v", w)
	}
	if l.Mode != Shadow || l.Retries != 3 || l.Note != "" {
		t.Errorf("the lead follows all tiers: %+v", l)
	}
	if s, _ := SettingFor("t-guard", Worker); s.SetOn != "qwen3.6" || s.Mode != Off {
		t.Errorf("a put merges, and remembers the tier's model: %+v", s)
	}
	ClearPart("t-guard", Worker, "mode")
	if m := ModeFor("t-guard", Worker); m != Shadow {
		t.Errorf("a cleared part follows all tiers again: %q", m)
	}
	if !Customized("t-guard") {
		t.Error("a guard with settings is customized")
	}
	Revert("t-guard")
	if e := Resolve("t-guard", Worker); Customized("t-guard") || e.Mode != On || e.Retries != DefaultRetries || e.Note != "" {
		t.Errorf("reverted, it is as shipped: %+v", e)
	}
	for name, bad := range map[string]Setting{
		"unknown guard":        {ID: "no-such-guard", Scope: AllTiers, Mode: Off},
		"unknown scope":        {ID: "t-guard", Scope: "gemini-2.5-flash", Mode: Off},
		"unknown mode":         {ID: "t-guard", Scope: AllTiers, Mode: "sometimes"},
		"too many retries":     {ID: "t-guard", Scope: AllTiers, Retries: MaxRetries + 1},
		"coded note":           {ID: "t-coded", Scope: AllTiers, Note: "x"},
		"checks on a built-in": {ID: "t-guard", Scope: AllTiers, Checks: []Check{{Kind: "length", Params: map[string]string{"max": "5"}}}},
	} {
		if Put(bad) == nil {
			t.Errorf("%s should be refused", name)
		}
	}
}

// Firings are tallied per guard and model, corrected apart from shadow, with
// the latest replies' tails kept and capped.
func TestFiringsAreTalliedWithRecentSamples(t *testing.T) {
	SetStore(memStore{})
	defer SetStore(nil)
	for i := 0; i < maxSamples+3; i++ {
		Record("t-guard", Worker, "qwen3.6", strings.Repeat("x", 2000)+" and", i%2 == 0)
	}
	st := Stats()
	if len(st) != 1 || st[0].Acted+st[0].Shadowed != maxSamples+3 || st[0].Shadowed == 0 || st[0].Tier != Worker {
		t.Fatalf("tallied per guard and model, both kinds: %+v", st)
	}
	if len(st[0].Samples) != maxSamples || !strings.HasSuffix(st[0].Samples[0].Text, " and") || len([]rune(st[0].Samples[0].Text)) > maxSampleChars+1 {
		t.Errorf("the last %d replies' tails are kept: %d samples, first %q", maxSamples, len(st[0].Samples), st[0].Samples[0].Text[:20])
	}
	SetStore(nil)
	Record("t-guard", Worker, "qwen3.6", "x", true)
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
	Put(Setting{ID: a.ID, Scope: AllTiers, Mode: Shadow})
	Put(Setting{ID: a.ID, Scope: Worker, Checks: []Check{{Kind: "length", Params: map[string]string{"max": "40"}}}})
	if e := Resolve(a.ID, Worker); len(e.Checks) != 1 {
		t.Errorf("a drafted guard can carry its own checks for a tier: %+v", e)
	}
	Record(a.ID, Worker, "m", "x", false)
	DeleteAuthored(a.ID)
	if Known(a.ID) || ModeFor(a.ID, Worker) != On || Customized(a.ID) {
		t.Error("deleted, it leaves the list and its settings")
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

// A correction is judged by what it came to. The replies people got on
// 2026-10-08 after the promise guard misfired read as answers to it.
func TestACorrectionIsJudgedByWhatItCameTo(t *testing.T) {
	caught := "If the tag didn't make it into the corner, say the word and I'll fix that part."
	for _, c := range []struct {
		after   string
		toolRan bool
		want    string
	}{
		{"Here's the fixed version.", true, OutcomeFixed},
		{"If the tag didn't make it into the corner, say the word and I'll fix that part.", false, OutcomeUnchanged},
		{"Nothing is stopping me, there's simply nothing left to run. The dragon picture is done and already delivered.", false, OutcomeAnswered},
		{"Nothing's pending. The picture went out with my last message and there's no job left in flight.", false, OutcomeAnswered},
		{"The \"I'll reroll it\" line was conditional on you asking.", false, OutcomeAnswered},
		{"The tag is in the corner now; it rendered small.", false, OutcomeRewritten},
	} {
		if got := ClassifyOutcome(caught, c.after, c.toolRan); got != c.want {
			t.Errorf("%q: %s, want %s", c.after, got, c.want)
		}
	}
	if !Misfire(OutcomeAnswered) || !Misfire(OutcomeUnchanged) || Misfire(OutcomeFixed) || Misfire(OutcomeRewritten) {
		t.Error("Misfire reads the outcomes wrong")
	}
}
