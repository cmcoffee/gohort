package orchestrate

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// cortexSessionID is the session id of a channel agent's persistent home
// thread. Per-agent so every channel agent has its own ongoing conversation.
// The client gets this value (per agent) from the host page, so it never
// hardcodes the scheme. (The retired Operator's pre-split "operator-thread"
// bridge was removed when the Operator seed was dropped — see
// dropLegacyOperator.)
func cortexSessionID(agentID string) string {
	return "channel:" + agentID
}

// resolveSurface maps a scheduled thing's Surface mode to the session its fire
// should land in, plus whether to surface it at all. `home` is the record's own
// home session — a monitor's WakeSession, a standing agent's ReportSessionID, or
// a recurring task's SessionID. Shared by monitors / standing / recurring so all
// three behave identically:
//
//	"" / "session" → the home session (or the cortex home thread if home is
//	                 empty — the legacy fallback so a fire still surfaces).
//	"cortex"       → the agent's cortex home thread.
//	"background"   → record=false: NO agent visibility (external delivery only).
//
// The home is never overwritten by a move, so switching back to "session" works.
func resolveSurface(surface, home, agentID string) (session string, record bool) {
	switch strings.TrimSpace(surface) {
	case "background":
		return "", false
	case "cortex":
		return cortexSessionID(agentID), true
	default: // "" / "session"
		if strings.TrimSpace(home) == "" {
			return cortexSessionID(agentID), true
		}
		return home, true
	}
}

// surfaceOptionsFor returns the "Move to…" picker options for an agent, gated on
// whether it has a cortex: Cortex is offered only when the agent has one. Shared
// by the monitor / standing / recurring move pickers.
func surfaceOptions(agentHasCortex bool) []struct {
	Value string `json:"value"`
	Label string `json:"label"`
} {
	type opt = struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	var out []opt
	if agentHasCortex {
		out = append(out, opt{Value: "cortex", Label: "Cortex home thread"})
	}
	out = append(out,
		opt{Value: "session", Label: "Session (where it was created)"},
		opt{Value: "background", Label: "Background (No Agent Visibility)"},
	)
	return out
}

// scheduleSurfaceDefault picks where a schedule REPORTS when nobody chose. An
// agent with a cortex has a standing thread built for exactly this — background
// work the user reads when they choose to, instead of fires interleaved into
// whatever conversation happened to author them — so a cortex agent defaults to
// "cortex" and everything else keeps the creating session. Applied on create AND
// on edit, which is why an explicit choice must be STORED rather than left empty:
// normalizeSurface keeps "session" as "session" so a deliberate move back is not
// re-defaulted to cortex by the next timing edit.
func scheduleSurfaceDefault(chosen string, hasCortex bool) string {
	if c := strings.TrimSpace(chosen); c != "" {
		return c
	}
	if hasCortex {
		return "cortex"
	}
	return ""
}

// surfaceDestLabel names a stored Surface mode in a sentence, for the confirmation
// the tool hands back and the model repeats to the user. Where a schedule reports
// is the one thing they need told, and "" (defaulted to the session) reads the
// same as an explicit "session".
func surfaceDestLabel(surface string) string {
	switch strings.TrimSpace(surface) {
	case "cortex":
		return "this agent's Cortex mind thread"
	case "background":
		return "no thread (background — the run happens, nothing is posted)"
	default:
		return "this session"
	}
}

// surfaceSuffix is the one-clause tail a console row adds so WHERE a schedule
// reports is visible without opening it — the destination is now defaulted per
// agent, so a row that says only its cadence hides the difference. Empty for the
// session, which is what the row's own context already implies.
func surfaceSuffix(surface string) string {
	switch strings.TrimSpace(surface) {
	case "cortex":
		return " · reports to cortex"
	case "background":
		return " · background (no agent visibility)"
	}
	return ""
}

// hasCortexThread reports whether this user's agent maintains a cortex thread.
// Tolerant by design: an unknown agent is simply "no cortex", so a caller
// defaulting a surface degrades to the session rather than erroring.
func hasCortexThread(user, agentID string) bool {
	if strings.TrimSpace(agentID) == "" {
		return false
	}
	a, ok := loadAgent(agentUserDB(RootDB, user), agentID)
	return ok && a.Cortex
}

// handleConsoleSurfaceOptions is the SHARED "Move to…" picker source for
// monitors / standing agents / recurring: it returns Cortex/Session/Background,
// with Cortex offered only when the pane's agent has a cortex (?agent=<id>).
func (T *OrchestrateApp) handleConsoleSurfaceOptions(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	hasCortex := false
	if a, ok := loadAgent(agentUserDB(RootDB, user), agentID); ok && a.Cortex {
		hasCortex = true
	}
	writeJSON(w, surfaceOptions(hasCortex))
}

// normalizeSurface maps a picker value to a stored Surface mode. "session"
// stores as "session" — NOT as "" — even though resolveSurface treats the two
// identically at fire time: empty means "nobody chose", which is what
// scheduleSurfaceDefault fills in with the cortex on a cortex agent. Storing the
// word is what makes a deliberate move back to the session survive the next
// edit. An empty picker value is still accepted (a client sending nothing means
// the default) and stays empty.
func normalizeSurface(val string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "":
		return "", true
	case "session":
		return "session", true
	case "cortex":
		return "cortex", true
	case "background":
		return "background", true
	}
	return "", false
}
