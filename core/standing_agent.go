// Standing agents: the persistent, scheduled jobs the Operator console
// supervises. A standing agent is config — "run agent X on schedule Y with
// mission Z" — plus the glue that fires it on the global scheduler, records
// the outcome to the run-ledger, and escalates anything that isn't clean.
//
// Split mirrors the run-ledger decision: the STORE + SCHEDULE + LEDGER glue
// live here in core (domain-agnostic); the actual agent EXECUTION is
// supplied by an agent-aware package (orchestrate) via RegisterStandingRunner.
// core can't import AgentRecord/loadAgent, so it calls the registered
// closure to do the run and only owns the lifecycle around it.
//
// Blast-radius posture: until a runner is registered, a fired run records a
// non-fatal "attention" entry rather than doing anything. And the runner an
// agent-aware package supplies must NOT auto-approve high-consequence tools
// (no Confirm=true) — its Confirm routes through the consequence policy /
// approvals queue. core deliberately can't grant that authority itself.

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	standingAgentsTable = "standing_agents" // <owner>:<name> -> StandingAgent
	standingRunKind     = "standing.run"
)

// StandingAgent is one scheduled job. Name is unique per owner and is the
// ledger's Agent field. AgentID names which agent the registered runner
// should load and run; core treats it as an opaque string.
type StandingAgent struct {
	Name    string `json:"name"`
	Owner   string `json:"owner"`
	AgentID string `json:"agent_id"`
	Mission string `json:"mission"` // the standing brief handed to the agent each run
	Cron    string `json:"cron"`    // schedule spec (NextCronOccurrence), e.g. "FRI 21:30"
	// Interval scheduling — an alternative to Cron for schedules cron can't
	// express (specific start + arbitrary interval): first run at StartAt (or
	// now+interval when StartAt is empty/past), then every IntervalSeconds.
	// e.g. "start tomorrow 8am, every 24h" or "every 6 hours". Cron takes
	// precedence when both are set.
	StartAt         time.Time `json:"start_at,omitempty"`
	IntervalSeconds int       `json:"interval_seconds,omitempty"`
	Paused          bool      `json:"paused"`
	Created         time.Time `json:"created"`
	NextRun         time.Time `json:"next_run,omitempty"`     // display: next scheduled fire
	SchedulerID     string    `json:"scheduler_id,omitempty"` // current recurring task id (for cancel-and-replace)
	// Report target — the channel agent + session this standing agent was
	// created from, so each run's result posts back where the user is watching
	// (parallels EventMonitor's WakeAgent/WakeSession). Empty on legacy records;
	// the reporter then falls back to the target agent's channel home thread.
	ReportAgentID   string `json:"report_agent_id,omitempty"`
	ReportSessionID string `json:"report_session_id,omitempty"`
}

// StandingRunResult is what a registered runner reports for one run.
type StandingRunResult struct {
	Status    RunStatus
	Summary   string
	Raw       string
	Artifacts []RunArtifact
	Err       string
}

// StandingRunnerFunc executes one run of a standing agent. Provided by an
// agent-aware package via RegisterStandingRunner.
type StandingRunnerFunc func(ctx context.Context, sa StandingAgent) StandingRunResult

// standingRunPayload is the scheduler payload for a standing.run task.
type standingRunPayload struct {
	Owner   string `json:"owner"`
	Name    string `json:"name"`
	Trigger string `json:"trigger"` // "schedule" (recurring) | "manual" (run-now)
}

var (
	standingRunner    StandingRunnerFunc
	standingEscalator func(RunRecord)
	standingReporter  func(context.Context, StandingAgent, RunRecord)
	standingMu        sync.RWMutex
	standingStarted   bool
)

// RegisterStandingRunner installs the agent-execution closure. Call once at
// startup from the agent-aware package (orchestrate).
func RegisterStandingRunner(fn StandingRunnerFunc) {
	standingMu.Lock()
	standingRunner = fn
	standingMu.Unlock()
}

// RegisterStandingEscalator installs a callback invoked when a run finishes
// with a status other than ok (attention/failed). Optional; nil = log only.
func RegisterStandingEscalator(fn func(RunRecord)) {
	standingMu.Lock()
	standingEscalator = fn
	standingMu.Unlock()
}

// RegisterStandingReporter installs a callback invoked after EVERY standing run
// (ok or not), so the agent-aware layer can post the result back into the
// channel/session the agent was created from. Distinct from the escalator,
// which fires only on non-OK runs for harder attention handling. Optional.
func RegisterStandingReporter(fn func(context.Context, StandingAgent, RunRecord)) {
	standingMu.Lock()
	standingReporter = fn
	standingMu.Unlock()
}

// --- store -------------------------------------------------------------------

func standingKey(owner, name string) string { return owner + ":" + name }

// SaveStandingAgent upserts a standing-agent definition.
func SaveStandingAgent(db Database, sa StandingAgent) {
	if db == nil || sa.Owner == "" || sa.Name == "" {
		return
	}
	db.Set(standingAgentsTable, standingKey(sa.Owner, sa.Name), sa)
}

// GetStandingAgent fetches one definition, owner-scoped.
func GetStandingAgent(db Database, owner, name string) (StandingAgent, bool) {
	if db == nil || owner == "" || name == "" {
		return StandingAgent{}, false
	}
	var sa StandingAgent
	if !db.Get(standingAgentsTable, standingKey(owner, name), &sa) {
		return StandingAgent{}, false
	}
	return sa, true
}

// ListStandingAgents returns the owner's standing agents.
func ListStandingAgents(db Database, owner string) []StandingAgent {
	if db == nil || owner == "" {
		return nil
	}
	prefix := owner + ":"
	var out []StandingAgent
	for _, k := range db.Keys(standingAgentsTable) {
		if len(k) < len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		var sa StandingAgent
		if db.Get(standingAgentsTable, k, &sa) {
			out = append(out, sa)
		}
	}
	return out
}

// DeleteStandingAgent removes a definition and cancels its recurring task.
func DeleteStandingAgent(db Database, owner, name string) {
	if db == nil {
		return
	}
	if sa, ok := GetStandingAgent(db, owner, name); ok && sa.SchedulerID != "" {
		UnscheduleTask(sa.SchedulerID)
	}
	db.Unset(standingAgentsTable, standingKey(owner, name))
}

// --- schedule + run ----------------------------------------------------------

// ScheduleStandingAgent (re)schedules the recurring task for a standing
// agent: it cancels any existing task and creates the next cron occurrence,
// storing the new task id and next-run time back on the record. Use on
// create, on resume, and after each fire.
// nextStandingRun computes the next fire time for a standing agent: cron when
// set, otherwise interval (StartAt if it's still in the future, else
// from+interval). Errors when neither schedule is configured.
func nextStandingRun(sa StandingAgent, from time.Time) (time.Time, error) {
	if strings.TrimSpace(sa.Cron) != "" {
		return NextCronOccurrence(sa.Cron, from)
	}
	if sa.IntervalSeconds > 0 {
		if sa.StartAt.After(from) {
			return sa.StartAt, nil
		}
		return from.Add(time.Duration(sa.IntervalSeconds) * time.Second), nil
	}
	return time.Time{}, fmt.Errorf("standing agent %q has no schedule (set cron or interval)", sa.Name)
}

// StandingScheduleLabel is a human-readable schedule description for display
// (cron spec, or "every <interval>").
func StandingScheduleLabel(sa StandingAgent) string {
	if strings.TrimSpace(sa.Cron) != "" {
		return sa.Cron
	}
	if sa.IntervalSeconds > 0 {
		return "every " + formatStandingInterval(sa.IntervalSeconds)
	}
	return "(no schedule)"
}

func formatStandingInterval(secs int) string {
	switch {
	case secs%86400 == 0:
		if d := secs / 86400; d == 1 {
			return "day"
		} else {
			return fmt.Sprintf("%d days", d)
		}
	case secs%3600 == 0:
		if h := secs / 3600; h == 1 {
			return "hour"
		} else {
			return fmt.Sprintf("%d hours", h)
		}
	case secs%60 == 0:
		if m := secs / 60; m == 1 {
			return "minute"
		} else {
			return fmt.Sprintf("%d minutes", m)
		}
	default:
		return fmt.Sprintf("%d seconds", secs)
	}
}

func ScheduleStandingAgent(db Database, sa StandingAgent) error {
	if sa.SchedulerID != "" {
		UnscheduleTask(sa.SchedulerID) // cancel-and-replace
	}
	next, err := nextStandingRun(sa, time.Now())
	if err != nil {
		return err
	}
	id, err := ScheduleTask(standingRunKind, standingRunPayload{
		Owner: sa.Owner, Name: sa.Name, Trigger: "schedule",
	}, next)
	if err != nil {
		return err
	}
	sa.SchedulerID = id
	sa.NextRun = next
	SaveStandingAgent(db, sa)
	return nil
}

// RunStandingNow fires a one-off manual run immediately (async, via the
// scheduler loop). It does not disturb the recurring schedule.
func RunStandingNow(db Database, owner, name string) error {
	sa, ok := GetStandingAgent(db, owner, name)
	if !ok {
		return nil
	}
	_, err := ScheduleTask(standingRunKind, standingRunPayload{
		Owner: sa.Owner, Name: sa.Name, Trigger: "manual",
	}, time.Now())
	return err
}

// StartStandingScheduler registers the standing.run handler. Call once at
// startup (idempotent). The handler reads/writes RootDB so its view matches
// the Operator console's. Execution itself is delegated to the registered
// runner.
func StartStandingScheduler() {
	standingMu.Lock()
	if standingStarted {
		standingMu.Unlock()
		return
	}
	standingStarted = true
	standingMu.Unlock()

	RegisterScheduleHandler(standingRunKind, func(ctx context.Context, raw json.RawMessage) {
		var p standingRunPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			Log("[standing] bad payload: %v", err)
			return
		}
		// ALWAYS re-arm the recurring cadence — even if the run panics. The
		// schedule is a self-rescheduling chain: each fire schedules the next.
		// Before this, a panicking run (or its registered runner) died BEFORE
		// the reschedule and left the standing agent "active" but dead forever,
		// needing a manual pause/resume to revive — the exact failure that
		// event monitors (event_monitor.go) and the trigger engine already
		// guard against with a deferred re-arm. Only scheduled fires re-arm; a
		// manual run-now is a one-off. Deferred so it runs no matter how the
		// fire exits; recover() stops one bad run from taking down the
		// scheduler goroutine; the re-read honors a pause/edit/delete that
		// happened during the run.
		if p.Trigger == "schedule" {
			defer func() {
				if r := recover(); r != nil {
					Log("[standing] run %s/%s PANICKED: %v — re-arming next fire anyway", p.Owner, p.Name, r)
				}
				if cur, ok := GetStandingAgent(RootDB, p.Owner, p.Name); ok && !cur.Paused {
					if err := ScheduleStandingAgent(RootDB, cur); err != nil {
						Log("[standing] reschedule failed for %s/%s: %v", p.Owner, p.Name, err)
					}
				}
			}()
		}
		sa, ok := GetStandingAgent(RootDB, p.Owner, p.Name)
		if !ok {
			Log("[standing] no such standing agent %s/%s — dropping task", p.Owner, p.Name)
			return
		}
		if sa.Paused {
			Log("[standing] %s/%s is paused — skipping fire", p.Owner, p.Name)
			return
		}
		executeStandingRun(ctx, RootDB, sa, p.Trigger)
	})
}

// executeStandingRun runs a standing agent via the registered runner and
// records the outcome to the run-ledger. With no runner registered it
// records a non-fatal "attention" entry rather than doing anything. Returns
// the stored ledger record. Factored out of the scheduler handler so it is
// unit-testable without the scheduler loop.
func executeStandingRun(ctx context.Context, db Database, sa StandingAgent, trigger string) RunRecord {
	standingMu.RLock()
	runner := standingRunner
	esc := standingEscalator
	rpt := standingReporter
	standingMu.RUnlock()

	if trigger == "" {
		trigger = "schedule"
	}
	rec := RunRecord{
		Owner:   sa.Owner,
		Agent:   sa.Name,
		Trigger: trigger,
		Brief:   sa.Mission,
		Started: time.Now(),
	}

	if runner == nil {
		rec.Status = RunAttention
		rec.Summary = "No standing-agent runner is registered yet; the run could not execute."
		rec.Ended = time.Now()
		rec = RecordRun(db, rec)
		Log("[standing] %s/%s fired but no runner is registered", sa.Owner, sa.Name)
		if esc != nil {
			esc(rec)
		}
		return rec
	}

	res := runner(ctx, sa)
	rec.Status = res.Status
	if rec.Status == "" {
		rec.Status = RunOK
	}
	rec.Summary = res.Summary
	rec.Raw = res.Raw
	rec.Artifacts = res.Artifacts
	rec.Err = res.Err
	rec.Ended = time.Now()
	fullOutput := rec.Raw // capture before RecordRun moves Raw to the encrypted side table
	rec = RecordRun(db, rec)

	// Report EVERY run back to the channel/session it was created from. Pass the
	// full output explicitly — RecordRun stores Raw in the encrypted side table
	// and the returned record no longer carries it.
	if rpt != nil {
		forReport := rec
		forReport.Raw = fullOutput
		rpt(ctx, sa, forReport)
	}

	if rec.Status != RunOK {
		Log("[standing] %s/%s finished with status=%s", sa.Owner, sa.Name, rec.Status)
		if esc != nil {
			esc(rec)
		}
	}
	return rec
}
