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
	runLedgerTable    = "run_ledger"     // metadata: <owner>:<id> -> RunRecord (Raw stripped)
	runLedgerRawTable = "run_ledger_raw" // raw output: <owner>:<id> -> string (CryptSet)
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

// RunRecord is one entry in the ledger. Raw is the only sensitive field
// and never travels in the metadata table or in ListRuns results — it is
// stored encrypted in a side table and rehydrated only by GetRun.
type RunRecord struct {
	ID        string        `json:"id"`
	Owner     string        `json:"owner"`
	Agent     string        `json:"agent"`         // standing-agent / job name
	Trigger   string        `json:"trigger"`       // "schedule" | "dispatch" | "channel" | "manual"
	Brief     string        `json:"brief"`         // what it was asked to do
	Status    RunStatus     `json:"status"`        // ok | attention | failed | running
	Summary   string        `json:"summary"`       // worker-LLM digest shown in the feed
	Raw       string        `json:"raw,omitempty"` // full output — encrypted side table, GetRun only
	Artifacts []RunArtifact `json:"artifacts,omitempty"`
	Started   time.Time     `json:"started"`
	Ended     time.Time     `json:"ended,omitempty"`
	Err       string        `json:"err,omitempty"` // set when Status == failed
}

// RunFilter narrows a ListRuns query. Zero value = all of the owner's
// runs, newest first, up to the default limit.
type RunFilter struct {
	Agent  string    // restrict to one agent/job
	Status RunStatus // restrict to one status
	Since  time.Time // only runs started at/after this time
	Limit  int       // max rows returned (0 = no extra cap beyond storage)
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

	// Raw lives only in the encrypted side table — never in metadata.
	raw := r.Raw
	meta := r
	meta.Raw = ""
	db.Set(runLedgerTable, key, meta)
	if raw != "" {
		db.CryptSet(runLedgerRawTable, key, raw)
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
		if filter.Agent != "" && r.Agent != filter.Agent {
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
	}
}
