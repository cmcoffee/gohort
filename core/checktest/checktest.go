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
}

var registry struct {
	mu     sync.RWMutex
	checks []Check
}

// Register adds a testable check. Called from init(); a key registered twice
// keeps its first entry.
func Register(c Check) {
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
}

// Answer is one model's outcome on one case.
type Answer struct {
	Got    string `json:"got"`
	Reason string `json:"reason,omitempty"`
	Err    string `json:"error,omitempty"`
	Pass   bool   `json:"pass"`
	TookMS int64  `json:"took_ms"`
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

// Run asks every case of every check of each model in turn, case by case so
// the models' answers to it sit side by side, logs each case with every
// model's answer, and keeps the run as the last. A stopped run keeps what it
// finished and says so.
func Run(ctx context.Context, models []Model) RunReport {
	rep := RunReport{At: time.Now()}
	for _, m := range models {
		rep.Models = append(rep.Models, m.Name)
	}
	type job struct {
		check Check
		c     Case
	}
	var jobs []job
	for _, ch := range Checks() {
		for _, c := range ch.Cases() {
			jobs = append(jobs, job{ch, c})
		}
	}
	for i, j := range jobs {
		if ctx.Err() != nil {
			rep.Canceled = true
			break
		}
		row := Row{Check: j.check.Key, Label: j.check.Label, Case: j.c.ID, Title: j.c.Title, Want: j.c.Want, Answers: map[string]Answer{}}
		var said []string
		for _, m := range models {
			ReportMaintenanceProgress(ctx, fmt.Sprintf("%d of %d: %s, %s (%s)", i+1, len(jobs), j.check.Label, j.c.Title, m.Name))
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
			a.Pass = a.Err == "" && strings.EqualFold(strings.TrimSpace(o.Got), strings.TrimSpace(j.c.Want))
			row.Answers[m.Name] = a
			why := a.Reason
			if a.Err != "" {
				why = a.Err
			}
			mark := "ok"
			if !a.Pass {
				mark = "FAIL"
			}
			said = append(said, fmt.Sprintf("%s %s %s in %dms (%s)", m.Name, mark, a.Got, a.TookMS, why))
		}
		rep.Rows = append(rep.Rows, row)
		// Kept as it goes, so a page showing the run's results fills in case
		// by case rather than only once the whole run is over.
		if RootDB != nil {
			RootDB.Set(table, lastKey, rep)
		}
		// Every case in the log, each model's answer beside the other's: the
		// comparison is readable where the rest of the trail is.
		Log("[checktest] %s / %s (%s): want %s | %s", j.check.Label, j.c.Title, j.c.ID, j.c.Want, strings.Join(said, " | "))
	}
	if RootDB != nil {
		RootDB.Set(table, lastKey, rep)
	}
	Log("[checktest] %s", Summary(rep))
	ReportMaintenanceOutcome(ctx, Summary(rep))
	return rep
}

// Last is the last run, ok false before any.
func Last() (RunReport, bool) {
	var rep RunReport
	if RootDB == nil || !RootDB.Get(table, lastKey, &rep) || rep.At.IsZero() {
		return RunReport{}, false
	}
	return rep, true
}

// Summary is a run in one line: each model's passes of the total, per check.
func Summary(rep RunReport) string {
	var parts []string
	for _, m := range rep.Models {
		pass, total := 0, 0
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
			per[r.Label] = t
		}
		var checks []string
		for _, l := range order {
			checks = append(checks, fmt.Sprintf("%s %d/%d", l, per[l][0], per[l][1]))
		}
		line := fmt.Sprintf("%s %d of %d", m, pass, total)
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
