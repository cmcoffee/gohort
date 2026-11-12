package admin

// Check tests on the Models page: run every check's cases (core/checktest) on
// the Judge model or on the worker, as maintenance passes so a run has the
// spinner, how far it has got, Stop, and a page that arrives mid-run rejoins
// it; and each model's last run, case by case, beside them.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
	"github.com/cmcoffee/gohort/core/decide"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

// checkTestsGroup is the maintenance group the runs are in, laid out on the
// Models page rather than Maintenance.
const checkTestsGroup = "Check tests"

// checkTestsAnchor is the check tests' place on the page, which the Offload
// model's Test the checks link scrolls to.
const checkTestsAnchor = "check-tests"

func init() {
	RegisterMaintenanceFunc(checkTestsGroup, checkMapKey, "Map the checks",
		"Tests every check on each model, and gives each to the cheapest one that gets all its cases right.",
		runCheckMap)
	RegisterMaintenanceFunc(checkTestsGroup, "check_map_clear", "Clear the map",
		"Sends every check back to the worker until it is mapped again.",
		func(ctx context.Context) int {
			n := checktest.ClearMap()
			Log("[checktest] map cleared: %d mapping(s) forgotten, every check on the worker", n)
			ReportMaintenanceOutcome(ctx, fmt.Sprintf("Cleared %d mapping(s): every check is on the worker until it is mapped again.", n))
			return n
		})
}

// checkMapKey is the one pass that maps the checks: everything when a
// person starts it, what is missing when it starts itself.
const checkMapKey = "check_tests"

// checkMapAfter and checkMapEvery are when the map looks for what is
// missing: a while after start, so it does not land on boot, and then on a
// slow tick, since what changes it (a model, a release) is rare.
const (
	checkMapAfter = 3 * time.Minute
	checkMapEvery = 15 * time.Minute
)

// checkMapAuto marks the pass the map starts itself, which tests only what
// is missing.
type checkMapAuto struct{}

// checkTestModels are the models a run asks, each for its map tier: the
// worker, which answers every check not offloaded; the Offload model when one
// is set and the Decision Offloading mode uses it; and System One when it
// answers and the mode uses it. A tier the mode does not use is not asked:
// nothing goes to it, so its runs would cost time and say nothing about
// what runs. Its kept mapping stays for when the mode uses it again.
func checkTestModels() []checktest.Model {
	models := []checktest.Model{}
	if w := SharedWorkerLLM(); w != nil {
		models = append(models, checktest.Model{Name: "worker", LLM: w, Tier: checktest.TierWorker})
	}
	if j := JudgeLLM(); j != nil && OffloadUses(MapJudge) {
		models = append(models, checktest.Model{Name: "offload", LLM: j, Tier: MapJudge})
	}
	// And whatever Services > System One has answer decisions, for the
	// checks that have that form.
	if d := decide.Model(); d != nil && OffloadUses(MapSystemOne) {
		models = append(models, checktest.Model{Name: "system one", LLM: d, Decide: true, Tier: MapSystemOne})
	}
	return models
}

// runCheckMap maps the checks. Started by a person, it tests every check;
// started by the map itself, only the checks whose mapping is missing.
// Either way, a check one clean run short is run again at once, so it ends
// with the map settled, and it says where every check landed. It returns how
// many checks are mapped off the worker.
func runCheckMap(ctx context.Context) int {
	models := checkTestModels()
	if len(models) == 0 {
		ReportMaintenanceOutcome(ctx, "No model is set: nothing to test.")
		return 0
	}
	auto, _ := ctx.Value(checkMapAuto{}).(bool)
	if !auto {
		checktest.Run(ctx, models)
	}
	for pass := 1; pass <= 2 && ctx.Err() == nil; pass++ {
		missing := checktest.Missing(models)
		if len(missing) == 0 {
			break
		}
		Log("[checktest] mapping %d check(s): %s", len(missing), strings.Join(missing, ", "))
		checktest.Run(ctx, models, missing...)
	}
	offload, s1, worker := 0, 0, 0
	for _, ch := range checktest.Checks() {
		on := false
		if MappedTo(ch.Key, MapJudge) {
			offload++
			on = true
		}
		if MappedTo(ch.Key, MapSystemOne) {
			s1++
			on = true
		}
		if !on {
			worker++
		}
	}
	ReportMaintenanceOutcome(ctx, fmt.Sprintf("Mapped: %d check(s) to the Offload model, %d to System One first; %d stay on the worker.", offload, s1, worker))
	return offload + s1
}

// startCheckMap looks for what the map is missing, a while after start and
// on a slow tick after, and maps it in the background, as the same pass the
// page shows, with its spinner and Stop.
func startCheckMap() {
	checkMapOnce.Do(func() {
		go func() {
			time.Sleep(checkMapAfter)
			for {
				if !checktest.Busy() && len(checkTestModels()) >= 2 && len(checktest.Missing(checkTestModels())) > 0 {
					RunMaintenanceFunc(context.WithValue(context.Background(), checkMapAuto{}, true), checkMapKey)
				}
				time.Sleep(checkMapEvery)
			}
		}()
	})
}

var checkMapOnce sync.Once

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
	// Detail is the row's second line: what each model answered and how
	// long it took, with its reason only where it got the case wrong.
	Detail string `json:"detail,omitempty"`
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
		// Right or wrong, and whether the answer would have been used:
		// System One's unsure answers and the ones the full check always
		// decides were not, so a wrong one did no harm.
		mark := func(an checktest.Answer) string {
			switch {
			case an.Unsure && an.Right:
				return "unsure-right"
			case an.Unsure:
				return "unsure-wrong"
			case an.Pass:
				return "pass"
			}
			return "fail"
		}
		if w, ok := r.Answers["worker"]; ok {
			row.Worker, row.WorkerPass, row.WorkerTook, row.WorkerReason = w.Got, mark(w), shortMillis(w.TookMS), "worker: "+reason(w)
			row.AllPass = row.AllPass && w.Pass
		}
		j, ok := r.Answers["offload"]
		if !ok {
			j, ok = r.Answers["judge"] // a run kept from before the rename
		}
		if ok {
			row.Judge, row.JudgePass, row.JudgeTook, row.JudgeReason = j.Got, mark(j), shortMillis(j.TookMS), "offload: "+reason(j)
			row.AllPass = row.AllPass && j.Pass
		}
		if s, ok := r.Answers["system one"]; ok {
			row.S1, row.S1Pass, row.S1Took, row.S1Reason = s.Got, mark(s), shortMillis(s.TookMS), "system one: "+reason(s)
			// Unsure is not wrong: live, the full question would be asked.
			row.AllPass = row.AllPass && (s.Pass || s.Unsure)
		}
		var said []string
		for _, m := range []struct {
			name string
			keys []string
		}{{"Worker", []string{"worker"}}, {"Offload model", []string{"offload", "judge"}}, {"System One", []string{"system one"}}} {
			for _, k := range m.keys {
				an, ok := r.Answers[k]
				if !ok {
					continue
				}
				line := fmt.Sprintf("%s: %s, %s", m.name, an.Got, shortMillis(an.TookMS))
				switch {
				case an.HandOff:
					line += " (the full check decides this answer)"
				case an.Unsure:
					line += " (unsure: below the confidence it needs)"
				}
				if why := reason(an); !an.Pass && !an.Unsure && why != "" {
					line += " (" + why + ")"
				}
				said = append(said, line)
				break
			}
		}
		row.Detail = strings.Join(said, "   ·   ")
		rows = append(rows, row)
	}
	// By check, so each check's cases sit together under it, and within a
	// check the cases a model got wrong first. Sorted the other way round,
	// a check's failures and passes landed in two groups of the same name.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Check != rows[j].Check {
			return rows[i].Check < rows[j].Check
		}
		return !rows[i].AllPass && rows[j].AllPass
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
	pass := func(field, label string) ui.Col {
		return ui.Col{Field: field, Label: label, Type: "badge", Badges: []ui.BadgeMapping{
			{Value: "pass", Label: "right", Color: "success"}, {Value: "fail", Label: "wrong", Color: "danger"},
			{Value: "unsure-right", Label: "right, unsure", Color: "warning"},
			{Value: "unsure-wrong", Label: "wrong, unsure"}}}
	}
	results := ui.Table{
		Source: "api/check-tests",
		// A run writes its results case by case in the background; the
		// table reads them as they come, and once more when the run ends.
		AutoRefreshMS: 3000,
		RowKey:        "id",
		GroupBy:       "check",
		EmptyText:     "Not run yet.",
		// One badge per model on the case's line; what each answered, how
		// long it took, and why where it was wrong, on one line under it.
		Columns: []ui.Col{
			{Field: "case", Label: "Case", Flex: 3},
			{Field: "want", Label: "Want", Mute: true},
			pass("worker_pass", "Worker"), pass("judge_pass", "Offload model"), pass("s1_pass", "System One"),
			{Field: "detail", Label: "", Mute: true, Flex: 6, Line: 2},
		},
	}
	return ui.Subsection{
		ID:    checkTestsAnchor,
		Title: "Check tests",
		Subtitle: "Each check's test cases, with known right answers, asked of the worker, the Offload model and System One, side by side. " +
			"Green right, red wrong; amber right, unsure: System One was right but its answer was not used (below its bar, or one the full check decides); grey wrong, unsure: wrong, but not used, so no harm.",
		Detail: "Map the checks asks every case through the check's own code (the prompt it builds, the reply it reads) of each model in turn, so a row shows how they did on the same case, and gives each check to the cheapest model that got all its cases right twice in a row: a check one clean run short is run again straight away, so one press settles the map. " +
			"It also runs on its own, for just the checks whose mapping is missing, a few minutes after start and every " + checkMapEvery.String() + " after. Clear the map sends every check back to the worker, to start over. Local inference: free, and a few minutes. " +
			"Cases a model got wrong come first, with each model's own reason; every case is also in the log ([checktest]). " +
			"The cases are built in: messages the gatekeeper should let through or not, tool results the scan should pass or flag. " +
			"With Services > System One set to use the Offload model, System One asks it the check as a fixed choice (no reason written), for the checks that have that form, with its confidence: how much faster it is, and whether it agrees. " +
			"A System One answer that was not used is marked unsure, graded right or wrong: one below the confidence it needs to stand alone, or one it may never give alone (a flag, a diversion, a contradiction, which need the full check's written reason). Live, the full check decides those, so only a red wrong counts against a mapping. " +
			"A case both models get wrong may be a case worth questioning rather than two wrong models.",
		Body: ui.Stack{Children: []ui.Component{
			maintenanceList(checkTestsGroup, "No check tests are registered."),
			results,
		}},
	}
}

// judgeMapRow is one check on the judge map: what answers it now, how each
// tier stands on it, and how long each took.
type judgeMapRow struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// By is what answers the check now: "judge" or "worker" for its full
	// answer, with "s1-" ahead when System One is asked first.
	By    string `json:"by"`
	Judge string `json:"judge"`
	S1    string `json:"s1"`
	Times string `json:"times,omitempty"`
	// Switchable is a check that may go to the Offload model at all, and On
	// whether it is allowed to: off, it stays on the worker whatever the
	// map says.
	Switchable bool `json:"switchable,omitempty"`
	On         bool `json:"on"`
}

// handleJudgeMap is every check on the map: the Judge model's checks, then
// the tested checks that run on the worker directly.
func (a *AdminApp) handleJudgeMap(c *jsonapi.Call) (any, error) {
	rows := []judgeMapRow{}
	seen := map[string]bool{}
	labels := map[string]string{}
	structured := map[string]bool{}
	for _, ch := range checktest.Checks() {
		labels[ch.Key] = ch.Label
		structured[ch.Key] = ch.Structured
	}
	add := func(key, label string, judgeCall bool) {
		// One switch per check that could be offloaded at all, for both the
		// Offload model and System One. A reading never is, so has none.
		allowed := OffloadAllowed(key)
		row := judgeMapRow{Key: key, Label: label, By: "worker", S1: checktest.Standing(key, MapSystemOne),
			Switchable: true, On: allowed}
		if !allowed && row.Switchable {
			row.S1 = "kept on the worker (switched off)"
		}
		if key == PreguardRoute {
			row.S1 += "\n" + preguardLine(LoadPreguard())
		}
		switch {
		case !judgeCall:
			row.Judge = "runs on the worker directly"
		case !allowed:
			row.Judge = "kept on the worker (switched off)"
		default:
			row.Judge = checktest.Standing(key, MapJudge)
			if MappedTo(key, MapJudge) {
				row.By = "judge"
			}
		}
		if MappedTo(key, MapSystemOne) {
			row.By = "s1-" + row.By
		}
		if e, ok := checktest.MappingFor(key); ok {
			var times []string
			for _, t := range []struct{ tier, name string }{{checktest.TierWorker, "worker"}, {MapJudge, "offload model"}, {MapSystemOne, "System One"}} {
				if r, ok := e.Tiers[t.tier]; ok && r.MS > 0 {
					times = append(times, t.name+" "+shortMillis(r.MS))
				}
			}
			row.Times = strings.Join(times, " · ")
		}
		rows = append(rows, row)
		seen[key] = true
	}
	// Only checks that can be offloaded are listed, each with its switch:
	// a structured check with test cases. One with no cases can never be
	// mapped, and a reading never is; both run on the worker, and a
	// reading's results still show in the check tests below.
	for _, jc := range JudgeCalls() {
		if structured[jc.Key] {
			add(jc.Key, jc.Label, true)
		}
	}
	for _, ch := range checktest.Checks() {
		if !seen[ch.Key] && ch.Structured {
			add(ch.Key, labels[ch.Key], false)
		}
	}
	return rows, nil
}

// judgeMapTable is the judge map, for the Judge model section.
func judgeMapTable() ui.Table {
	return ui.Table{
		Source: "api/judge-llm/map",
		// Mapping runs in the background; the map shows it as it lands.
		AutoRefreshMS: 5000,
		RowKey:        "key",
		EmptyText:     "No checks are registered.",
		Columns: []ui.Col{
			{Field: "label", Label: "Check"},
			{Field: "by", Label: "Answered by", Type: "badge", Badges: []ui.BadgeMapping{
				{Value: "judge", Label: "Offload model", Color: "success"}, {Value: "worker", Label: "Worker"},
				{Value: "s1-judge", Label: "System One, then Offload model", Color: "success"},
				{Value: "s1-worker", Label: "System One, then worker", Color: "success"}}},
			{Field: "judge", Label: "Offload model", Mute: true, Flex: 3, Line: 2},
			{Field: "s1", Label: "System One", Mute: true, Flex: 3, Line: 2},
			{Field: "times", Label: "Median case", Mute: true, Flex: 2},
		},
		// Allowed to go to the Offload model, on the check's own row: off, it
		// stays on the worker whatever the map says. A switch can only keep
		// a check off; the map decides what goes there.
		RowActions: []ui.RowAction{
			{Type: "toggle", Field: "on", PostTo: "api/judge-llm/calls/toggle?key={key}", OnlyIf: "switchable", Leading: true},
		},
	}
}
