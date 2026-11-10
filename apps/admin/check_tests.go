package admin

// Check tests on the Models page: run every check's cases (core/checktest) on
// the Judge model or on the worker, as maintenance passes so a run has the
// spinner, how far it has got, Stop, and a page that arrives mid-run rejoins
// it; and each model's last run, case by case, beside them.

import (
	"context"
	"fmt"
	"sort"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
	"github.com/cmcoffee/gohort/core/decide"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

// checkTestsGroup is the maintenance group the runs are in, laid out on the
// Models page rather than Maintenance.
const checkTestsGroup = "Check tests"

// checkTestsAnchor is the check tests' place on the page, which the Judge
// model's Test the checks link scrolls to.
const checkTestsAnchor = "check-tests"

func init() {
	RegisterMaintenanceFunc(checkTestsGroup, "check_tests", "Compare the worker and the Judge model",
		"Asks every check's test cases of the worker and the Judge model, case by case, and shows their answers side by side below. "+
			"With no Judge model set, the worker alone. Local inference: free, and a few minutes.",
		runCheckTests)
}

// runCheckTests asks every case of the worker and, when one is set, the
// Judge model, and returns how many cases both answered right.
func runCheckTests(ctx context.Context) int {
	models := []checktest.Model{}
	if w := SharedWorkerLLM(); w != nil {
		models = append(models, checktest.Model{Name: "worker", LLM: w})
	}
	if j := JudgeLLM(); j != nil {
		models = append(models, checktest.Model{Name: "judge", LLM: j})
	}
	// And whatever Services > System One has answer decisions, for the
	// checks that have that form.
	if d := decide.Model(); d != nil {
		models = append(models, checktest.Model{Name: "system one", LLM: d, Decide: true})
	}
	if len(models) == 0 {
		ReportMaintenanceOutcome(ctx, "No model is set: nothing to test.")
		return 0
	}
	rep := checktest.Run(ctx, models)
	both := 0
	for _, r := range rep.Rows {
		all := true
		for _, a := range r.Answers {
			all = all && a.Pass
		}
		if all {
			both++
		}
	}
	return both
}

// checkTestRow is one case as its row shows it: the answer it wants, and
// each model's answer, time and reason.
type checkTestRow struct {
	ID           string `json:"id"`
	Check        string `json:"check"`
	Case         string `json:"case"`
	Want         string `json:"want"`
	Worker       string `json:"worker"`
	WorkerPass   string `json:"worker_pass,omitempty"`
	WorkerTook   string `json:"worker_took,omitempty"`
	WorkerReason string `json:"worker_reason,omitempty"`
	Judge        string `json:"judge,omitempty"`
	JudgePass    string `json:"judge_pass,omitempty"`
	JudgeTook    string `json:"judge_took,omitempty"`
	JudgeReason  string `json:"judge_reason,omitempty"`
	S1           string `json:"s1,omitempty"`
	S1Pass       string `json:"s1_pass,omitempty"`
	S1Took       string `json:"s1_took,omitempty"`
	S1Reason     string `json:"s1_reason,omitempty"`
	AllPass      bool   `json:"all_pass"`
}

// handleCheckTests is the last run, case by case: cases a model got wrong
// first, then by check.
func (a *AdminApp) handleCheckTests(c *jsonapi.Call) (any, error) {
	rows := []checkTestRow{}
	rep, ok := checktest.Last()
	if !ok {
		return rows, nil
	}
	reason := func(an checktest.Answer) string {
		if an.Err != "" {
			return an.Err
		}
		return an.Reason
	}
	for i, r := range rep.Rows {
		row := checkTestRow{ID: fmt.Sprintf("%d", i), Check: r.Label, Case: r.Title, Want: r.Want, AllPass: true}
		// "pass" or "fail" where a model answered, and nothing where it did
		// not (no Judge model, or a check with no System One form): read as
		// a bool, a missing answer showed as a fail.
		mark := func(pass bool) string {
			if pass {
				return "pass"
			}
			return "fail"
		}
		if w, ok := r.Answers["worker"]; ok {
			row.Worker, row.WorkerPass, row.WorkerTook, row.WorkerReason = w.Got, mark(w.Pass), shortMillis(w.TookMS), "worker: "+reason(w)
			row.AllPass = row.AllPass && w.Pass
		}
		if j, ok := r.Answers["judge"]; ok {
			row.Judge, row.JudgePass, row.JudgeTook, row.JudgeReason = j.Got, mark(j.Pass), shortMillis(j.TookMS), "judge: "+reason(j)
			row.AllPass = row.AllPass && j.Pass
		}
		if s, ok := r.Answers["system one"]; ok {
			row.S1, row.S1Pass, row.S1Took, row.S1Reason = s.Got, mark(s.Pass), shortMillis(s.TookMS), "system one: "+reason(s)
			row.AllPass = row.AllPass && s.Pass
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].AllPass != rows[j].AllPass {
			return !rows[i].AllPass
		}
		return rows[i].Check < rows[j].Check
	})
	return rows, nil
}

// shortMillis is a duration in milliseconds as a row says it: 340 ms, 2.1 s.
func shortMillis(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	return fmt.Sprintf("%.1f s", float64(ms)/1000)
}

// checkTestsPanel is the run and its last results, for the Judge model
// section.
func checkTestsPanel() ui.Component {
	pass := func(field string) ui.Col {
		return ui.Col{Field: field, Type: "badge", Badges: []ui.BadgeMapping{
			{Value: "pass", Label: "pass", Color: "success"}, {Value: "fail", Label: "fail", Color: "danger"}}}
	}
	results := ui.Table{
		Source: "api/check-tests",
		// A run writes its results case by case in the background; the
		// table reads them as they come, and once more when the run ends.
		AutoRefreshMS: 3000,
		RowKey:        "id",
		GroupBy:       "check",
		EmptyText:     "Not run yet.",
		Columns: []ui.Col{
			{Field: "case", Label: "Case"},
			{Field: "want", Label: "Want", Mute: true},
			{Field: "worker", Label: "Worker"}, pass("worker_pass"), {Field: "worker_took", Label: "", Mute: true},
			{Field: "judge", Label: "Judge"}, pass("judge_pass"), {Field: "judge_took", Label: "", Mute: true},
			{Field: "s1", Label: "System One"}, pass("s1_pass"), {Field: "s1_took", Label: "", Mute: true},
			{Field: "worker_reason", Label: "", Mute: true, Flex: 4, Line: 2},
			{Field: "judge_reason", Label: "", Mute: true, Flex: 4, Line: 2},
			{Field: "s1_reason", Label: "", Mute: true, Flex: 4, Line: 2},
		},
	}
	return ui.Subsection{
		ID:       checkTestsAnchor,
		Title:    "Check tests",
		Subtitle: "Each check's test cases, with known right answers, asked of the worker, the Judge model, and the Judge model as System One, side by side.",
		Detail: "A run asks every case through the check's own code (the prompt it builds, the reply it reads) of each model in turn, so a row shows how the two did on the same case. " +
			"Run it after changing the Judge model, or a check's prompt. Cases a model got wrong come first, with each model's own reason; every case is also in the log ([checktest]). " +
			"The cases are built in: messages the gatekeeper should let through or not, tool results the scan should pass or flag. " +
			"With Services > System One set to use the Judge model, System One asks it the check as a fixed choice (no reason written), for the checks that have that form, with its confidence: how much faster it is, and whether it agrees. " +
			"A case both models get wrong may be a case worth questioning rather than two wrong models.",
		Body: ui.Stack{Children: []ui.Component{
			maintenanceList(checkTestsGroup, "No check tests are registered."),
			results,
		}},
	}
}
