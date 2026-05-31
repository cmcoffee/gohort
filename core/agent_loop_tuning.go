package core

import (
	"fmt"
	"sync"
)

// AgentLoopTuning bundles operator-tunable knobs that govern the
// agent loop's behavior — history compaction, future LLM-retry caps,
// etc. — without recompiling. Loaded once at startup, mirrored in
// kvlite, and overrideable from the admin UI.
//
// Conservative defaults are baked in so a fresh deployment behaves
// identically to before the knobs existed; operators only touch a
// value when they have a specific reason (e.g. very long sessions
// pushing prefill cost, or a smaller worker model that needs a
// gentler history budget).
type AgentLoopTuning struct {
	// HistoryBudgetPercent is the steady-state cap on per-round
	// history as a percent of the LLM context window. The agent loop
	// uses min(window - sysprompt - genReserve, window × percent/100)
	// as the compaction target. Lower = faster prefill, better cache
	// reuse, more aggressive elision; higher = more retained context,
	// slower prefill. 50% is a balanced default for the 200K-window
	// worker tier; tune down to 30-40% for chatty long sessions or up
	// to 70% for short sessions that need to remember more.
	// Clamped to [25, 90] on save.
	HistoryBudgetPercent int `json:"history_budget_percent"`
}

// agentLoopTuningDefaults returns the baseline values used when no
// override has been persisted. Kept as a helper so the admin UI can
// re-fetch defaults to render a "Reset to defaults" button later.
func agentLoopTuningDefaults() AgentLoopTuning {
	return AgentLoopTuning{
		HistoryBudgetPercent: 50,
	}
}

var (
	agentLoopTuningMu sync.RWMutex
	agentLoopTuning   = agentLoopTuningDefaults()
)

// SetAgentLoopTuning installs the process-wide tuning values. Clamps
// HistoryBudgetPercent to [25, 90] — below 25% the history starves
// to its hard floor anyway (compactHistory enforces a contextSize/4
// floor for safety); above 90% there's no headroom for sysprompt +
// generation, so the cap is effectively disabled.
func SetAgentLoopTuning(t AgentLoopTuning) {
	if t.HistoryBudgetPercent < 25 {
		t.HistoryBudgetPercent = 25
	}
	if t.HistoryBudgetPercent > 90 {
		t.HistoryBudgetPercent = 90
	}
	agentLoopTuningMu.Lock()
	agentLoopTuning = t
	agentLoopTuningMu.Unlock()
}

// GetAgentLoopTuning returns a snapshot of the current tuning. Cheap
// — value copy under RLock, no shared mutable state escapes. Called
// every compaction round so it must stay lock-light.
func GetAgentLoopTuning() AgentLoopTuning {
	agentLoopTuningMu.RLock()
	defer agentLoopTuningMu.RUnlock()
	return agentLoopTuning
}

// agentLoopTuningTable / agentLoopTuningKey are the kvlite storage
// coordinates for persisted tuning. Constants so reader and writer
// agree and a future migration can rename in one place.
const (
	agentLoopTuningTable = "agent_loop_tuning"
	agentLoopTuningKey   = "current"
)

// LoadAgentLoopTuningFromDB reads persisted values and installs them
// via SetAgentLoopTuning. Silent no-op when the DB is nil or no
// record exists — the process keeps the compiled defaults, which is
// the right behavior for a fresh install. Returns true when a record
// was actually loaded so the caller can Debug the source.
func LoadAgentLoopTuningFromDB(db Database) bool {
	if db == nil {
		return false
	}
	var stored AgentLoopTuning
	if !db.Get(agentLoopTuningTable, agentLoopTuningKey, &stored) {
		return false
	}
	SetAgentLoopTuning(stored)
	return true
}

// SaveAgentLoopTuningToDB persists the given values to kvlite AND
// installs them in the process. One call from the admin handler
// covers both jobs, mirroring the cost-rates save shape.
func SaveAgentLoopTuningToDB(db Database, t AgentLoopTuning) error {
	if db == nil {
		return fmt.Errorf("no database available")
	}
	// SetAgentLoopTuning clamps the percent before we store, so the
	// DB record always sits in the valid range.
	SetAgentLoopTuning(t)
	db.Set(agentLoopTuningTable, agentLoopTuningKey, GetAgentLoopTuning())
	return nil
}

// InitAgentLoopTuning is the startup wiring. Always succeeds — when
// no record is present the in-memory defaults stand.
func InitAgentLoopTuning(db Database) {
	if LoadAgentLoopTuningFromDB(db) {
		Debug("[agent_loop] tuning loaded from database (history_budget_percent=%d)", GetAgentLoopTuning().HistoryBudgetPercent)
		return
	}
	Debug("[agent_loop] tuning at defaults (history_budget_percent=%d)", GetAgentLoopTuning().HistoryBudgetPercent)
}
