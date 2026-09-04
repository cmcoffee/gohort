// Run-ledger: the shared, owner-scoped record of what every agent run
// (scheduled, dispatched, channel-triggered, or manual) did. It is the
// data spine behind the Operator console's Activity feed and the source
// the supervisory "fixer" reads to report on the fleet.
//
// It generalizes apps/phantom's dispatch_results pattern (store raw under
// a short id + a worker-LLM summary, cap and prune) along three axes the
// chat-scoped version couldn't serve:
//
//   - partitioned by OWNER, not by chat — so a user's whole fleet is one
//     queryable set, and reads are gated to the authenticated owner (no
//     cross-user leakage).
//   - carries STATUS (ok / attention / failed / running) — the fixer keys
//     escalation on this; the feed badges on it.
//   - records ANY run trigger, not just chat dispatches.
//
// Sensitivity tiering matches the leakage decision: metadata (status,
// summary, agent) lives in one table and is what the feed reads; the
// potentially-secret-bearing RAW output lives in a SEPARATE table written
// with CryptSet (extra at-rest encryption beyond the base store) and is
// only read on demand by GetRun. ListRuns never touches it, so the cheap
// surface structurally cannot leak raw.

package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	runLedgerTable      = "run_ledger"       // metadata: <owner>:<id> -> RunRecord (Raw + Steps stripped)
	runLedgerRawTable   = "run_ledger_raw"   // raw output: <owner>:<id> -> string (CryptSet)
	runLedgerStepsTable = "run_ledger_steps" // tool trace: <owner>:<id> -> []RunStep (CryptSet)
)

// maxRunsPerOwner caps retained runs per owner; oldest pruned past this.
func maxRunsPerOwner() int { return TuneInt("tune_max_runs_per_owner") }

func init() {
	RegisterTunable(TunableSpec{Key: "tune_max_runs_per_owner", Category: "Limits", Label: "Run ledger retention per owner", Help: "Cap on retained run records per owner; oldest are pruned past this.", Kind: KindInt, Default: 500, Min: 50, Max: 5000})
}

// RunStatus is the outcome of a run. The Operator badges on it and the
// fixer escalates on anything that isn't ok.
type RunStatus string

const (
	RunRunning   RunStatus = "running"   // in flight
	RunOK        RunStatus = "ok"        // completed cleanly
	RunAttention RunStatus = "attention" // completed but flagged for a human
	RunFailed    RunStatus = "failed"    // errored
)

// RunArtifact is one concrete thing a run produced — a diff, a file, a
// link. Kept structured so the console can render them and the audit
// trail is more than prose.
type RunArtifact struct {
	Kind  string `json:"kind"`  // "diff" | "file" | "link"
	Label string `json:"label"` // human label
	Value string `json:"value"` // path, URL, or inline content
}

// RunStep is one tool invocation captured during a run, so the ledger records
// WHAT the run did (its steps) and not just its final output. Populated from the
// same per-message tool trace the chat UI renders as chips; kept as a core type
// so every trigger (schedule, dispatch, standing agent) can fill it without a
// dependency on an app package. As sensitive as Raw — args/results can carry
// fetched data — so it travels ONLY in the encrypted side table (never in
// metadata / ListRuns), rehydrated only by GetRun.
type RunStep struct {
	Name   string `json:"name"`
	Args   string `json:"args,omitempty"`   // JSON-encoded call args, as the caller serialized them
	Result string `json:"result,omitempty"` // handler output (empty when Err set)
	Err    string `json:"err,omitempty"`    // set when the call failed
}

// RunRecord is one entry in the ledger. Raw is the only sensitive field
// and never travels in the metadata table or in ListRuns results — it is
// stored encrypted in a side table and rehydrated only by GetRun.
type RunRecord struct {
	ID        string        `json:"id"`
	Owner     string        `json:"owner"`
	Agent     string        `json:"agent"`           // DISPLAY label for a feed a person reads; see Subject
	Trigger   string        `json:"trigger"`         // "schedule" | "dispatch" | "channel" | "manual"
	Brief     string        `json:"brief"`           // what it was asked to do
	Status    RunStatus     `json:"status"`          // ok | attention | failed | running
	Summary   string        `json:"summary"`         // worker-LLM digest shown in the feed
	Raw       string        `json:"raw,omitempty"`   // full output — encrypted side table, GetRun only
	Steps     []RunStep     `json:"steps,omitempty"` // tool trace — encrypted side table, GetRun only
	Artifacts []RunArtifact `json:"artifacts,omitempty"`
	Started   time.Time     `json:"started"`
	Ended     time.Time     `json:"ended,omitempty"`
	Err       string        `json:"err,omitempty"` // set when Status == failed

	// Subject is the stable identity of what ran, namespaced by kind:
	// "agent:<id>", "standing:<name>", "monitor:<name>", "trigger:<name>".
	//
	// Agent cannot serve as identity. It is a label written by four kinds of
	// caller — a standing schedule writes its own name, an event monitor writes
	// its monitor name, a scheduled update writes the AGENT's display name,
	// agent CRUD writes an agent's ID — into one flat string. So a schedule
	// called "daily-stories" and an agent called "daily-stories" were the same
	// key, and asking for one's history returned the other's, silently, in a
	// place that looked authoritative.
	//
	// Namespacing by kind is what makes that impossible rather than unlikely:
	// two different KINDS that share a display name can no longer collide at
	// all, whatever anyone names anything.
	//
	// Empty on records written before this field existed. Readers must fall
	// back to matching Agent for those (see RunFilter.Subject), or a ledger's
	// whole history would vanish the day identity arrived.
	Subject string `json:"subject,omitempty"`

	// Task is the name of the SCHEDULE that fired, when the run came from one
	// a person named. It exists because Agent could not answer "did the
	// Snuglab blog post task run today?".
	//
	// A recurring fire re-runs an agent, so it records that agent's label in
	// Agent and its id in Subject — correct, and it leaves the task's own name
	// nowhere a filter can reach it. Asking the ledger for the task by name
	// matched nothing and read back as "No runs recorded yet.", which is the
	// same sentence the ledger returns when a task genuinely never fired. An
	// answer that cannot distinguish "no such name here" from "it never ran"
	// is worse than no answer, and this one was believed.
	//
	// Empty for the surfaces whose Agent already IS the schedule's name (a
	// standing agent, an event monitor, a trigger). Those stay findable the
	// way they always were; this field is for the case where the two names
	// differ.
	Task string `json:"task,omitempty"`
}

// RunFilter narrows a ListRuns query. Zero value = all of the owner's
// runs, newest first, up to the default limit.
// Run subjects: the stable identity written into RunRecord.Subject, namespaced
// by kind so nothing a user names can make two different kinds of thing share
// one identity.
//
// Reached through methods rather than exported constructors on purpose. Every
// exported name here lands in the namespace of every file that dot-imports core
// (see coreExportCeiling), and four spellings of one idea is a poor way to spend
// that. Methods also make the pairing hard to get wrong: About* on a filter sets
// the legacy label fallback at the same time, which a caller assembling the two
// fields by hand would eventually forget.
func runSubject(kind, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	return kind + ":" + id
}

// AboutAgent / AboutStanding / AboutMonitor / AboutTrigger stamp what a run was
// about. Chain them onto the literal: RecordRun(db, RunRecord{…}.AboutAgent(id)).
func (r RunRecord) AboutAgent(agentID string) RunRecord {
	r.Subject = runSubject("agent", agentID)
	return r
}
func (r RunRecord) AboutStanding(name string) RunRecord {
	r.Subject = runSubject("standing", name)
	return r
}
func (r RunRecord) AboutMonitor(name string) RunRecord {
	r.Subject = runSubject("monitor", name)
	return r
}
func (r RunRecord) AboutTrigger(name string) RunRecord {
	r.Subject = runSubject("trigger", name)
	return r
}

// AboutAgent / AboutStanding on a filter narrow to one thing's runs by
// identity, and set the display label as the fallback for records written
// before subjects existed — the two belong together, so they are set together.
func (f RunFilter) AboutAgent(agentID, displayName string) RunFilter {
	f.Subject, f.Agent = runSubject("agent", agentID), displayName
	return f
}
func (f RunFilter) AboutStanding(name string) RunFilter {
	f.Subject, f.Agent = runSubject("standing", name), name
	return f
}

type RunFilter struct {
	// Subject restricts to one thing's runs by identity (RunSubjectAgent and
	// friends). This is the correct way to ask; Agent below is a display label
	// and two unrelated things can share one.
	//
	// A record with no Subject predates the field, and falls back to matching
	// Agent when that is also set — imprecise, and exactly as imprecise as it
	// always was, but a ledger that forgets everything older than the fix is
	// worse than one that keeps answering the old way for old rows. Those rows
	// age out through pruneRuns, so the ambiguity drains rather than persists.
	Subject string
	Agent   string    // restrict by DISPLAY label — legacy fallback; prefer Subject
	Task    string    // restrict by the firing schedule's name (see RunRecord.Task)
	Status  RunStatus // restrict to one status
	Since   time.Time // only runs started at/after this time
	Limit   int       // max rows returned (0 = no extra cap beyond storage)
}

// matches reports whether one record satisfies the filter's subject/agent
// narrowing. Subject wins when both the filter and the record carry one; a
// record without a Subject is matched the old way so its history stays
// reachable.
func (f RunFilter) matches(r RunRecord) bool {
	// Task is its own axis, ANDed with the rest like Status: a caller asking
	// for one schedule's fires gets that schedule's fires. Callers that want
	// "anything answering to this name" ask twice and merge, which keeps the
	// widening at the call site where it is visible instead of hidden in here.
	if f.Task != "" && r.Task != f.Task {
		return false
	}
	switch {
	case f.Subject != "" && r.Subject != "":
		return r.Subject == f.Subject
	case f.Subject != "":
		// Legacy record. Fall back to the label only when the caller supplied
		// one — a subject-only query must not silently widen to every row that
		// happens to have no subject.
		return f.Agent != "" && r.Agent == f.Agent
	case f.Agent != "":
		return r.Agent == f.Agent
	}
	return true
}

// NewRunID returns a short stable id for a run, unique per invocation via
// the nanosecond clock (so a recurring brief gets distinct ids).
func NewRunID(owner, agent string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", owner, agent, time.Now().UnixNano())))
	return hex.EncodeToString(h[:6]) // 12 hex chars
}

// runLedgerKey is the metadata/raw key for a run. Owner-prefixed so a
// single prefix scan yields exactly one owner's runs.
func runLedgerKey(owner, id string) string { return owner + ":" + id }

// RecordRun persists a run. It fills ID/Started when unset, writes the
// metadata (Raw stripped) to the ledger table, writes any Raw to the
// encrypted side table, and prunes the owner's oldest runs past the cap.
// Returns the stored record (with ID populated).
func RecordRun(db Database, r RunRecord) RunRecord {
	if db == nil || r.Owner == "" {
		return r
	}
	if r.ID == "" {
		r.ID = NewRunID(r.Owner, r.Agent)
	}
	if r.Started.IsZero() {
		r.Started = time.Now()
	}
	key := runLedgerKey(r.Owner, r.ID)

	// Raw and Steps live only in the encrypted side tables — never in metadata
	// (ListRuns reads metadata, so a leak there would surface in the feed).
	raw := r.Raw
	steps := r.Steps
	meta := r
	meta.Raw = ""
	meta.Steps = nil
	db.Set(runLedgerTable, key, meta)
	if raw != "" {
		db.CryptSet(runLedgerRawTable, key, raw)
	}
	if len(steps) > 0 {
		db.CryptSet(runLedgerStepsTable, key, steps)
	}

	pruneRuns(db, r.Owner)
	return r
}

// GetRun returns one run with its Raw rehydrated from the encrypted side
// table. Owner-scoped: a run id is only resolvable under its owner.
func GetRun(db Database, owner, id string) (RunRecord, bool) {
	if db == nil || owner == "" || id == "" {
		return RunRecord{}, false
	}
	key := runLedgerKey(owner, id)
	var r RunRecord
	if !db.Get(runLedgerTable, key, &r) {
		return RunRecord{}, false
	}
	var raw string
	if db.Get(runLedgerRawTable, key, &raw) {
		r.Raw = raw
	}
	var steps []RunStep
	if db.Get(runLedgerStepsTable, key, &steps) {
		r.Steps = steps
	}
	return r, true
}

// ListRuns returns the owner's runs matching the filter, newest first.
// It reads metadata only — Raw is never loaded here, so the feed cannot
// leak it.
func ListRuns(db Database, owner string, filter RunFilter) []RunRecord {
	if db == nil || owner == "" {
		return nil
	}
	prefix := owner + ":"
	var out []RunRecord
	for _, k := range db.Keys(runLedgerTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var r RunRecord
		if !db.Get(runLedgerTable, k, &r) {
			continue
		}
		if !filter.matches(r) {
			continue
		}
		if filter.Status != "" && r.Status != filter.Status {
			continue
		}
		if !filter.Since.IsZero() && r.Started.Before(filter.Since) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out
}

// pruneRuns trims the owner's runs to maxRunsPerOwner, dropping oldest
// first (metadata and its encrypted raw together). Cheap: the per-owner
// list is bounded.
func pruneRuns(db Database, owner string) {
	all := ListRuns(db, owner, RunFilter{})
	maxRuns := maxRunsPerOwner()
	if len(all) <= maxRuns {
		return
	}
	for _, r := range all[maxRuns:] { // newest-first; drop the tail
		key := runLedgerKey(owner, r.ID)
		db.Unset(runLedgerTable, key)
		db.Unset(runLedgerRawTable, key)
		db.Unset(runLedgerStepsTable, key)
	}
}
