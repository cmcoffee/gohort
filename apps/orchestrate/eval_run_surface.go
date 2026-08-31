// An eval suite on the run surface.
//
// A suite run IS a run: it produces a transcript, it takes minutes, and
// somebody wants to watch it and come back to it. core/pipeline_runs.go
// already serves exactly that shape for anything satisfying RunWork, and it
// carries the three things a thirty-case suite needs most — the work outlives
// the tab, a reader can reattach, and an obviously-failing suite can be
// stopped rather than paid for in full.
//
// Two records come out of one execution and that is deliberate. The
// PipelineRun the surface keeps is the TRANSCRIPT: what was watched, replayable
// per case. The EvalRun is the RECORD: per-case results and the fingerprint of
// the version graded, which is what makes a score history mean anything. One is
// for reading afterwards, the other is for comparing across weeks.
//
// See docs/eval-primitive.md.
package orchestrate

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// evalRunOwnerID namespaces a suite's runs in the shared run store. Prefixed
// because that store is keyed by "whatever the runs belong to" and a pipeline
// id and a suite id are both UUIDs — without it, one could address the other's
// history.
func evalRunOwnerID(suiteID string) string { return "eval:" + suiteID }

// evalRunSurface builds the panel protocol's host half for one suite.
func (T *OrchestrateApp) evalRunSurface(user string, suite EvalSuite) RunSurface {
	udb := UserDB(T.DB, user)
	return RunSurface{
		DB:      T.DB,
		User:    user,
		OwnerID: evalRunOwnerID(suite.ID),
		Live: RunLiveInfo{
			App:       "Evals: " + suite.Name,
			URL:       "/orchestrate/evals/" + suite.ID + "/?session={id}",
			CancelURL: "/orchestrate/api/evals/" + suite.ID + "/cancel?id={id}",
		},
		Work: func(ctx context.Context, input string, vars map[string]string, sink PipelineSink) (string, error) {
			return T.streamEvalSuite(ctx, udb, user, suite, input, sink)
		},
	}
}

// streamEvalSuite runs a suite, emitting one block per case as it lands.
func (T *OrchestrateApp) streamEvalSuite(ctx context.Context, udb Database, user string, suite EvalSuite, note string, sink PipelineSink) (string, error) {
	run := EvalRun{
		SuiteID: suite.ID, Owner: suite.Owner,
		Started: time.Now().UTC(), Total: len(suite.Cases), Note: strings.TrimSpace(note),
	}
	target, err := T.resolveEvalTarget(udb, user, suite)
	if err != nil {
		run.Err = err.Error()
		run.Finished = time.Now().UTC()
		SaveEvalRun(udb, run)
		return "", err
	}
	return T.streamEvalWith(ctx, udb, suite, note, target, sink), nil
}

// streamEvalWith is the half that runs and records, split from resolution so
// each can be exercised on its own: one asks "what am I grading", the other
// "what happened", and a test of the second should not have to stand up an
// agent to ask it.
func (T *OrchestrateApp) streamEvalWith(ctx context.Context, udb Database, suite EvalSuite, note string, target evalTarget, sink PipelineSink) string {
	run := EvalRun{
		SuiteID: suite.ID, Owner: suite.Owner,
		Started: time.Now().UTC(), Total: len(suite.Cases), Note: strings.TrimSpace(note),
	}
	run.TargetHash = target.Fingerprint
	run = SaveEvalRun(udb, run)

	if !suite.Stubbed() {
		// Said in the transcript, not only in the record. A suite running for
		// real sends the emails and spends the money, and whoever is watching
		// should be able to see that from what they are watching.
		sink(PipelineEvent{Kind: "status", Text: "LIVE MODE — tools execute for real, with their side effects"})
	}

	seq := 0
	results := runEvalCasesWith(ctx, suite.Cases, suite.RunCount(), target.Exec, func(row EvalResult) {
		seq++
		id := "case-" + strconv.Itoa(seq)
		sink(PipelineEvent{Kind: "block", ID: id, Type: "eval_case", Title: evalCaseTitle(row)})
		sink(PipelineEvent{Kind: "chunk", ID: id, Text: evalCaseBody(row)})
		sink(PipelineEvent{Kind: "block_done", ID: id})
	})

	run.Results = results
	for _, r := range results {
		if r.Passed {
			run.Passed++
		}
	}
	run.Finished = time.Now().UTC()
	SaveEvalRun(udb, run)

	// The pass rate onto the sidebar row. This is what turns a list of past
	// runs into a score history in the panel that already draws it.
	sink(PipelineEvent{Kind: "meta", Meta: map[string]string{
		"Score":   run.Rate(),
		"Outcome": evalOutcome(run),
		"Version": run.TargetHash,
	}})
	return fmt.Sprintf("%s — %s", run.Rate(), evalOutcome(run))
}

// evalOutcome is the at-a-glance word, kept coarse on purpose: a pill is read
// in a list, and "24/30" already carries the number.
func evalOutcome(run EvalRun) string {
	switch {
	case run.Total == 0:
		return "empty"
	case run.Passed == run.Total:
		return "all passed"
	case run.Passed == 0:
		return "all failed"
	default:
		return "mixed"
	}
}

func evalCaseTitle(row EvalResult) string {
	mark := "FAIL"
	if row.Passed {
		mark = "PASS"
	}
	if row.Runs > 1 {
		return fmt.Sprintf("%s  %s  (%d/%d)", mark, row.Name, row.Passes, row.Runs)
	}
	return mark + "  " + row.Name
}

// evalCaseBody is what a reader needs to act: why it failed, then what it
// actually did. The reasons come first because a passing case needs no reading
// and a failing one is opened for its reason.
func evalCaseBody(row EvalResult) string {
	var b strings.Builder
	if row.ErrText != "" {
		fmt.Fprintf(&b, "errored: %s\n\n", row.ErrText)
	}
	for _, r := range row.Reasons {
		fmt.Fprintf(&b, "- %s\n", r)
	}
	if len(row.ToolsCalled) > 0 {
		fmt.Fprintf(&b, "\ntools called: %s\n", strings.Join(row.ToolsCalled, ", "))
	}
	if strings.TrimSpace(row.Output) != "" {
		fmt.Fprintf(&b, "\n%s\n", row.Output)
	}
	return b.String()
}

// handleEvalRuns serves the panel protocol for one suite:
// /api/evals/<suiteID>/{stream|cancel|reconnect/<id>|sessions|sessions/<id>}.
func (T *OrchestrateApp) handleEvalRuns(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/evals/")
	suiteID, sub, _ := strings.Cut(rest, "/")
	suite, found := LoadEvalSuite(UserDB(T.DB, user), suiteID)
	if !found {
		http.NotFound(w, r)
		return
	}
	T.ServeRuns(w, r, T.evalRunSurface(user, suite), sub)
}
