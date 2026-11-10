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
	if len(got) != 2 || got[0].Case != "split" || got[0].AllPass || got[0].WorkerPass != "pass" || got[0].JudgePass != "fail" || got[0].S1Pass != "" ||
		got[0].Judge != "block" || got[0].JudgeReason != "judge: no rule fires" || got[0].JudgeTook != "2.1 s" || got[1].WorkerTook != "340 ms" {
		t.Errorf("rows: %+v", got)
	}
	SetSharedJudgeLLM(nil, false, "", "")
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	SetSharedLLMs(nil, nil)
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL) })
	if n := runCheckTests(context.Background()); n != 0 {
		t.Errorf("with no model set, %d passed", n)
	}
}

// The check tests sit at the foot of the Judge model's section, found from
// a link beside its Test connection that scrolls to them, and their results
// refresh themselves while a run writes them case by case.
func TestTheCheckTestsAreFoundAndRefresh(t *testing.T) {
	var judge ui.Section
	for _, s := range (&AdminApp{}).llmSections() {
		if s.Title == "Judge model" {
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
