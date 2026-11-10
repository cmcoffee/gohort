package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The last run reads back case by case, the worker's and the judge's
// answers side by side, cases either got wrong first, each with its time and
// reason; and a run with no model set says so instead of testing nothing.
func TestCheckTestsShowFailuresFirst(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	RootDB.Set("check_tests", "last", checktest.RunReport{Models: []string{"worker", "judge"}, At: time.Now(), Rows: []checktest.Row{
		{Label: "Alpha", Title: "fine", Want: "allow", Answers: map[string]checktest.Answer{
			"worker": {Got: "allow", Pass: true, TookMS: 340}, "judge": {Got: "allow", Pass: true, TookMS: 200}}},
		{Label: "Beta", Title: "split", Want: "allow", Answers: map[string]checktest.Answer{
			"worker": {Got: "allow", Pass: true, TookMS: 900, Reason: "rule 2"}, "judge": {Got: "block", Reason: "no rule fires", TookMS: 2100}}},
	}})
	a := &AdminApp{db: RootDB}
	out, err := a.handleCheckTests(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/check-tests", nil)})
	if err != nil {
		t.Fatal(err)
	}
	got := out.([]checkTestRow)
	// By check (Alpha, then Beta), each check's wrong cases first within it.
	if len(got) != 2 || got[1].Case != "split" || got[1].AllPass || got[1].WorkerPass != "pass" || got[1].JudgePass != "fail" || got[1].S1Pass != "" ||
		got[1].Judge != "block" || got[1].JudgeReason != "offload: no rule fires" || got[1].JudgeTook != "2.1 s" || got[0].WorkerTook != "340 ms" {
		t.Errorf("rows: %+v", got)
	}
	// The second line says what each answered, with a reason only where wrong.
	if d := got[1].Detail; !strings.Contains(d, "Offload model: block, 2.1 s (no rule fires)") || strings.Contains(d, "rule 2") {
		t.Errorf("detail: %q", d)
	}
	SetSharedJudgeLLM(nil, false, "", "")
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	SetSharedLLMs(nil, nil)
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL) })
	if n := runCheckMap(context.Background()); n != 0 {
		t.Errorf("with no model set, %d passed", n)
	}
}

// The check tests sit at the foot of the Judge model's section, found from
// a link beside its Test connection that scrolls to them, and their results
// refresh themselves while a run writes them case by case.
func TestTheCheckTestsAreFoundAndRefresh(t *testing.T) {
	var judge ui.Section
	for _, s := range (&AdminApp{}).llmSections() {
		if s.Title == "Decision Offloading" {
			judge = s
		}
	}
	b, _ := json.Marshal(judge.Body)
	page := string(b)
	for _, want := range []string{`"test_link":{"label":"Test the checks ↓","href":"#check-tests"}`, `"id":"check-tests"`,
		`"source":"api/check-tests"`, `"auto_refresh_ms":3000`} {
		if !strings.Contains(page, want) {
			t.Errorf("the Judge model section lacks %s", want)
		}
	}
}

// The map lists every check the Judge model may take and every other tested
// one, each with what answers it now and how each tier stands: a structured
// check is mapped only by its cases, a reading never, a check with none is
// said to have none, and one switched off stays on the worker.
func TestTheJudgeMapSaysWhatAnswersEachCheck(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	prevGate := JudgeMapGate
	JudgeMapGate = func(key, tier string) bool {
		return (key == "test.map_scan" && tier == MapJudge) || (key == "test.map_quick" && tier == MapSystemOne)
	}
	t.Cleanup(func() { RootDB = saved; JudgeMapGate = prevGate })
	RegisterJudgeCall(JudgeCall{Key: "test.map_scan", Label: "A mapped check"})
	RegisterJudgeCall(JudgeCall{Key: "test.map_untested", Label: "A check with no cases"})
	RegisterJudgeCall(JudgeCall{Key: "test.map_quick", Label: "A check System One is asked first"})
	one := func() []checktest.Case { return []checktest.Case{{ID: "a", Want: "x"}} }
	checktest.Register(checktest.Check{Key: "test.map_scan", Label: "A mapped check", Cases: one, Structured: true})
	checktest.Register(checktest.Check{Key: "test.map_quick", Label: "A check System One is asked first", Cases: one, Structured: true})
	RegisterJudgeCall(JudgeCall{Key: "test.map_reading", Label: "A reading"})
	checktest.Register(checktest.Check{Key: "test.map_reading", Label: "A reading", Cases: one})
	a := &AdminApp{db: RootDB}
	rows := func() map[string]judgeMapRow {
		out, err := a.handleJudgeMap(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/judge-llm/map", nil)})
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]judgeMapRow{}
		for _, r := range out.([]judgeMapRow) {
			m[r.Key] = r
		}
		return m
	}
	got := rows()
	if r := got["test.map_scan"]; r.By != "judge" {
		t.Errorf("a mapped check: %+v", r)
	}
	if r := got["test.map_quick"]; r.By != "s1-worker" {
		t.Errorf("a check System One is asked first does not say so: %+v", r)
	}
	if r, listed := got["test.map_untested"]; listed {
		t.Errorf("a check with no cases, which can never be mapped, is listed: %+v", r)
	}
	if r, listed := got["test.map_reading"]; listed {
		t.Errorf("a reading, which is never offloaded, is listed: %+v", r)
	}
	RootDB.Set(JudgeLLMTable, "off", []string{"test.map_scan"})
	if r := got["test.map_scan"]; !r.Switchable || !r.On {
		t.Errorf("a check the Offload model may take has no switch, or it is off: %+v", r)
	}
	if r := rows()["test.map_scan"]; r.By != "worker" || r.On || !strings.Contains(r.Judge, "switched off") {
		t.Errorf("a mapped check switched off: %+v", r)
	}
}

// The Decision Offloading section spells out the way a check goes, from
// what is set: the worker alone, the Offload model before it when one is
// set, and System One first when it answers.
func TestTheDecisionPathSaysWhatIsInUse(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved; SetSharedJudgeLLM(nil, false, "", "") })
	SetSharedJudgeLLM(nil, false, "", "")
	if p := decisionPath(); !strings.HasPrefix(p, "Worker only") {
		t.Errorf("nothing set: %q", p)
	}
	SetSharedJudgeLLM(&FakeLLM{}, false, "vLLM/small", "")
	if p := decisionPath(); !strings.HasPrefix(p, "Offload model (vLLM/small) → Worker") {
		t.Errorf("an Offload model: %q", p)
	}
	if decisionPathFor(LLMTable) != "" {
		t.Error("the worker's form shows the path")
	}
}

// The mode saves, reads back, refuses a name it does not know, and the
// Decision path follows it, naming what is set but not used.
func TestTheOffloadModeIsChosenAndShown(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved; SetSharedJudgeLLM(nil, false, "", "") })
	a := &AdminApp{db: RootDB}
	save := func(mode string) error {
		_, err := a.handleOffloadModeSave(&jsonapi.Call{R: httptest.NewRequest("POST", "/api/judge-llm/mode", nil)}, struct {
			Mode string `json:"mode"`
		}{mode})
		return err
	}
	if err := save("sideways"); err == nil {
		t.Error("an unknown mode was saved")
	}
	if err := save(OffloadSystemOne); err != nil || OffloadMode() != OffloadSystemOne {
		t.Fatalf("saving System One only: %v, mode %q", err, OffloadMode())
	}
	out, _ := a.handleOffloadMode(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/judge-llm/mode", nil)})
	if out.(map[string]any)["mode"] != OffloadSystemOne {
		t.Errorf("read back %+v", out)
	}
	SetSharedJudgeLLM(&FakeLLM{}, false, "vLLM/small", "")
	if p := decisionPath(); !strings.HasPrefix(p, "Worker only") || !strings.Contains(p, "Set but not used in this mode: the Offload model") {
		t.Errorf("System One only, with an Offload model set and no System One: %q", p)
	}
	save(OffloadBoth)
	if p := decisionPath(); !strings.HasPrefix(p, "Offload model (vLLM/small) → Worker") {
		t.Errorf("both: %q", p)
	}
}

// The pre-guard is a row on the map like any structured check: its switch
// starts off, the row switch turns it on (clearing why it last turned
// itself off, keeping its counts), and its record shows in the row.
func TestThePreguardIsARowOnTheMap(t *testing.T) {
	saved, prevGate := RootDB, JudgeMapGate
	RootDB = &DBase{Store: kvlite.MemStore()}
	JudgeMapGate = nil
	t.Cleanup(func() { RootDB, JudgeMapGate = saved, prevGate })
	// The pre-guard's own check is registered by orchestrate; a stand-in.
	checktest.Register(checktest.Check{Key: PreguardRoute, Label: "Guardrail pre-guard (warden)", Structured: true,
		Cases: func() []checktest.Case { return []checktest.Case{{ID: "a", Want: "engaged"}} }})
	a := &AdminApp{db: RootDB}
	UpdatePreguard(func(r *PreguardRecord) {
		r.Skipped, r.Verified, r.Missed, r.OffWhy = 12, 3, 1, "a skipped rule was found violated"
	})
	row := func() judgeMapRow {
		out, _ := a.handleJudgeMap(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/judge-llm/map", nil)})
		for _, r := range out.([]judgeMapRow) {
			if r.Key == PreguardRoute {
				return r
			}
		}
		t.Fatal("the pre-guard is not on the map")
		return judgeMapRow{}
	}
	r := row()
	if !r.Switchable || r.On || !strings.Contains(r.S1, "Pre-guard off") || !strings.Contains(r.S1, "Skipped 12") || !strings.Contains(r.S1, "Turned itself off") {
		t.Errorf("off, after a miss: %+v", r)
	}
	_, err := a.handleJudgeCallToggle(&jsonapi.Call{R: httptest.NewRequest("POST", "/api/judge-llm/calls/toggle?key="+PreguardRoute, nil)}, struct {
		On bool `json:"on"`
	}{true})
	rec := LoadPreguard()
	if err != nil || !rec.On || rec.OffWhy != "" || rec.Skipped != 12 {
		t.Errorf("switched on: %v %+v", err, rec)
	}
	if r := row(); !r.On || !strings.Contains(r.S1, "not used yet") {
		t.Errorf("on, not mapped: %+v", r)
	}
}
