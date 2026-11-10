// Package checktest runs gohort's checks against fixed cases with known right
// answers, on a model chosen for the run: the Judge model or the worker.
//
// A check (the channel gatekeeper, the tool-result scan) registers its cases
// and a Run that decides one case on a given model through the check's own
// code: the prompt it builds and the reply it parses are the live ones, only
// the model is the test's. So a run measures the check on that model, not a
// copy of it, and answers the question a Judge model raises: is this model
// good enough for this check? Live traffic answers it too, but slowly and
// without known answers; a run answers it in minutes, after a model or a
// prompt changes.
package checktest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/decide"
)

// Case is one test of a check: what it is given (Input, in the check's own
// fields) and the verdict it should reach (Want, in the check's own words:
// "allow", "block", "clean", "flagged").
type Case struct {
	ID    string
	Title string
	Want  string
	Input map[string]string
}

// Outcome is how a check decided a case: its verdict and the reason it gave,
// or the error that stopped it.
type Outcome struct {
	Got    string
	Reason string
	Err    error
	// Unsure is a System One answer below the confidence it needs to stand
	// alone: live, the full question would be asked instead. It is neither
	// a pass nor a fail, whatever it answered.
	Unsure bool
	// HandOff marks an Unsure answer that is one System One may never give
	// alone (it needs the full answer's written reason: a flag, a
	// diversion), as against one it was not sure enough of.
	HandOff bool
	// Coarse marks an answer from a check's quick choice, which says less
	// than the full answer ("contradicts" where the case wants "contradicts
	// 1, 3", "keep" for "keep fact"): it is right when the case's answer is
	// it or begins with it.
	Coarse bool
}

// Check is a check that can be tested: its key (its JudgeCall key), what it is
// called, its cases, and how it decides one case on a given model.
type Check struct {
	Key   string
	Label string
	Cases func() []Case
	Run   func(ctx context.Context, llm LLM, c Case) Outcome
	// Decide is the check asked as a System One decision (core/decide): a
	// pick among fixed answers read from the model's first token, with no
	// reason written. Nil for a check with no such form yet.
	Decide func(ctx context.Context, llm LLM, c Case) Outcome
	// Structured marks a check that may be offloaded: a fixed question with
	// fixed answers about short, concrete input (does it match, is it
	// stated, did it repeat). Only these are mapped to the Judge model or
	// System One. A check that weighs meaning, intent, or rules in any
	// wording is a reading: tested, so how each model does is known, but
	// kept on the worker whatever its cases say. A small model passing a
	// check's cases says it can answer them, not that the check is the kind
	// of question to give it.
	Structured bool
	// Quick is the check's question as a fixed choice for a case (the
	// declaration its live code asks System One with, core.QuickChoice).
	// With it and no Decide, the check's System One form is built from it:
	// an answer that needs a reason, or one System One is not sure of, is
	// unsure, since live the full answer is asked for it.
	Quick func(c Case) QuickChoice
}

var registry struct {
	mu     sync.RWMutex
	checks []Check
}

// Register adds a testable check. Called from init(); a key registered twice
// keeps its first entry.
func Register(c Check) {
	if c.Decide == nil && c.Quick != nil {
		c.Decide = quickForm(c.Quick)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for _, have := range registry.checks {
		if have.Key == c.Key {
			return
		}
	}
	registry.checks = append(registry.checks, c)
}

// Checks is every testable check, by label.
func Checks() []Check {
	registry.mu.RLock()
	out := append([]Check(nil), registry.checks...)
	registry.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// Model is one model a run asks: its name in the results ("worker",
// "judge") and the model itself.
type Model struct {
	Name string
	LLM  LLM
	// Decide asks each check's System One form (Check.Decide) rather than
	// its full question; a check without one is left out for this model.
	Decide bool
	// Tier is the map tier the model's results are kept for (TierWorker,
	// core.MapJudge, core.MapSystemOne); empty, its Name, or System One
	// when Decide.
	Tier string
	// Identity is what the model is for the map; empty, what its tier is
	// now (TierModel). Set by a test.
	Identity string
}

// Answer is one model's outcome on one case.
type Answer struct {
	Got    string `json:"got"`
	Reason string `json:"reason,omitempty"`
	Err    string `json:"error,omitempty"`
	Pass   bool   `json:"pass"`
	// Unsure: System One answered below its bar (Outcome.Unsure), so the
	// answer would not have stood live. Pass is false.
	Unsure bool `json:"unsure,omitempty"`
	// HandOff: the answer is one that always goes to the full check
	// (Outcome.HandOff). Unsure is set too.
	HandOff bool `json:"hand_off,omitempty"`
	// Right is whether the answer was the case's, whatever became of it: an
	// unsure or handed-off answer that pointed the right way is graded too.
	Right  bool  `json:"right,omitempty"`
	TookMS int64 `json:"took_ms"`
}

// Row is one case in a run, with each model's answer to it.
type Row struct {
	Check   string            `json:"check"`
	Label   string            `json:"label"`
	Case    string            `json:"case"`
	Title   string            `json:"title"`
	Want    string            `json:"want"`
	Answers map[string]Answer `json:"answers"`
}

// RunReport is one run: which models, when, and every case with each
// model's answer side by side.
type RunReport struct {
	Models   []string  `json:"models"`
	At       time.Time `json:"at"`
	Rows     []Row     `json:"rows"`
	Canceled bool      `json:"canceled,omitempty"`
}

// CaseTimeout is how long one model has for one case. A small model that
// fell into a loop generated for five minutes on a single gatekeeper case
// and held the whole run there; past this the case is a failure, "timed
// out", and the run goes on.
var CaseTimeout = 60 * time.Second

// table and lastKey are where the last run is kept, the one under way
// included: it is written after every case.
const (
	table   = "check_tests"
	lastKey = "last"
)

// runMu holds one run at a time: two would ask the same models the same
// cases at once and write the map from both.
var runMu sync.Mutex

// Busy reports whether a run is under way.
func Busy() bool {
	if runMu.TryLock() {
		runMu.Unlock()
		return false
	}
	return true
}

// Run asks every case of every check of each model in turn, case by case so
// the models' answers to it sit side by side, logs each case with every
// model's answer, keeps the run as the last, and records it on the judge
// map. A stopped run keeps what it finished and says so. With only, it asks
// just those checks, and the last run keeps the other checks' rows from
// before.
func Run(ctx context.Context, models []Model, only ...string) RunReport {
	runMu.Lock()
	defer runMu.Unlock()
	rep := RunReport{At: time.Now()}
	for _, m := range models {
		rep.Models = append(rep.Models, m.Name)
	}
	type job struct {
		check Check
		c     Case
	}
	asked := map[string]bool{}
	for _, k := range only {
		asked[k] = true
	}
	var jobs []job
	for _, ch := range Checks() {
		if len(only) > 0 && !asked[ch.Key] {
			continue
		}
		for _, c := range ch.Cases() {
			jobs = append(jobs, job{ch, c})
		}
	}
	// The rows of the checks not asked this time, from the last run, so the
	// results table still shows every check.
	var kept []Row
	if len(only) > 0 {
		if last, ok := Last(); ok {
			for _, r := range last.Rows {
				if !asked[r.Check] {
					kept = append(kept, r)
				}
			}
		}
	}
	// Progress on a steady tick, in a line of steady length: a line naming
	// each case and model changed length every second, so the row it sits in
	// re-wrapped and its buttons moved under the pointer.
	var said time.Time
	progress := func(done int) {
		if time.Since(said) < 2*time.Second && done < len(jobs) {
			return
		}
		said = time.Now()
		ReportMaintenanceProgress(ctx, fmt.Sprintf("%d of %d cases - %s", done, len(jobs), time.Since(rep.At).Round(time.Second)))
	}
	progress(0)
	for i, j := range jobs {
		if ctx.Err() != nil {
			rep.Canceled = true
			break
		}
		progress(i)
		row := Row{Check: j.check.Key, Label: j.check.Label, Case: j.c.ID, Title: j.c.Title, Want: j.c.Want, Answers: map[string]Answer{}}
		var lines []string
		for _, m := range models {
			run := j.check.Run
			if m.Decide {
				if j.check.Decide == nil {
					continue
				}
				run = j.check.Decide
			}
			began := time.Now()
			cctx, cancel := context.WithTimeout(ctx, CaseTimeout)
			o := run(cctx, m.LLM, j.c)
			timedOut := cctx.Err() == context.DeadlineExceeded && ctx.Err() == nil
			cancel()
			a := Answer{Got: o.Got, Reason: o.Reason, TookMS: time.Since(began).Milliseconds()}
			switch {
			case timedOut:
				a.Err, a.Got = fmt.Sprintf("no answer in %s", CaseTimeout), "timed out"
			case o.Err != nil:
				a.Err, a.Got = o.Err.Error(), "error"
			}
			a.Unsure = a.Err == "" && (o.Unsure || o.HandOff)
			a.HandOff = a.Err == "" && o.HandOff
			a.Right = a.Err == "" && answers(o, j.c.Want)
			a.Pass = a.Right && !a.Unsure
			row.Answers[m.Name] = a
			why := a.Reason
			if a.Err != "" {
				why = a.Err
			}
			mark := "ok"
			switch {
			case a.HandOff:
				mark = "to-full-check" + agreed(a.Right)
			case a.Unsure:
				mark = "unsure" + agreed(a.Right)
			case !a.Pass:
				mark = "FAIL"
			}
			lines = append(lines, fmt.Sprintf("%s %s %s in %dms (%s)", m.Name, mark, a.Got, a.TookMS, why))
		}
		rep.Rows = append(rep.Rows, row)
		// Kept as it goes, so a page showing the run's results fills in case
		// by case rather than only once the whole run is over.
		if RootDB != nil {
			RootDB.Set(table, lastKey, withRows(rep, kept))
		}
		// Every case in the log, each model's answer beside the other's: the
		// comparison is readable where the rest of the trail is.
		Log("[checktest] %s / %s (%s): want %s | %s", j.check.Label, j.c.Title, j.c.ID, j.c.Want, strings.Join(lines, " | "))
	}
	record(rep, models)
	if RootDB != nil {
		RootDB.Set(table, lastKey, withRows(rep, kept))
	}
	Log("[checktest] %s", Summary(rep))
	ReportMaintenanceOutcome(ctx, Summary(rep))
	return rep
}

// answers reports whether o is the case's answer want: the same, or, for a
// coarse answer, what want begins with.
func answers(o Outcome, want string) bool {
	got, want := strings.ToLower(strings.TrimSpace(o.Got)), strings.ToLower(strings.TrimSpace(want))
	return got == want || (o.Coarse && got != "" && strings.HasPrefix(want, got+" "))
}

// agreed is how an unsure or handed-off answer is graded in the log.
func agreed(right bool) string {
	if right {
		return " (agreed)"
	}
	return " (disagreed)"
}

// withRows is rep with the kept rows of checks it did not ask after its own.
func withRows(rep RunReport, kept []Row) RunReport {
	if len(kept) == 0 {
		return rep
	}
	out := rep
	out.Rows = append(append([]Row(nil), rep.Rows...), kept...)
	return out
}

// Last is the last run, ok false before any.
func Last() (RunReport, bool) {
	var rep RunReport
	if RootDB == nil || !RootDB.Get(table, lastKey, &rep) || rep.At.IsZero() {
		return RunReport{}, false
	}
	return rep, true
}

// Summary is a run in one line: each model's passes of the total, per check,
// and how many answers were unsure (not counted as passes).
func Summary(rep RunReport) string {
	var parts []string
	for _, m := range rep.Models {
		pass, total, unsure, right := 0, 0, 0, 0
		per := map[string][2]int{}
		var order []string
		for _, r := range rep.Rows {
			a, ok := r.Answers[m]
			if !ok {
				continue
			}
			t, seen := per[r.Label]
			if !seen {
				order = append(order, r.Label)
			}
			t[1]++
			total++
			if a.Pass {
				t[0]++
				pass++
			}
			if a.Unsure {
				unsure++
			}
			if a.Right {
				right++
			}
			per[r.Label] = t
		}
		var checks []string
		for _, l := range order {
			checks = append(checks, fmt.Sprintf("%s %d/%d", l, per[l][0], per[l][1]))
		}
		line := fmt.Sprintf("%s %d of %d", m, pass, total)
		if unsure > 0 {
			line += fmt.Sprintf(", %d unsure (%d right in all)", unsure, right)
		}
		if len(checks) > 0 {
			line += " (" + strings.Join(checks, ", ") + ")"
		}
		parts = append(parts, line)
	}
	out := strings.Join(parts, "; ")
	if out == "" {
		out = "nothing was run"
	}
	if rep.Canceled {
		out += " (stopped early)"
	}
	return out
}

// quickForm is a check's System One form, built from its quick choice.
func quickForm(quick func(Case) QuickChoice) func(ctx context.Context, llm LLM, c Case) Outcome {
	return func(ctx context.Context, llm LLM, c Case) Outcome {
		q := quick(c)
		a, err := decide.Ask(ctx, llm, decide.QuickQuestion(q), WithRouteKey(q.Key))
		if err != nil {
			return Outcome{Err: err}
		}
		if !a.OK {
			return Outcome{Got: "no decision"}
		}
		reason := fmt.Sprintf("P(%s)=%.2f%s, firm %.2f", q.Answers[a.Choice].Key, a.Probs[a.Choice], a.OrderLine(a.Choice), a.Firm())
		out := Outcome{Got: q.Answers[a.Choice].Key, Reason: reason, Coarse: true}
		if q.Answers[a.Choice].Reason {
			out.HandOff = true // never stands: the full check writes its reason
		} else {
			out.Unsure = !decide.QuickStands(q, a.Choice, a.Firm())
		}
		return out
	}
}
