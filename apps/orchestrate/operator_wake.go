// Operator wake wiring: the agent-aware halves of the event-monitor engine.
// core/event_monitor.go owns the store, the poll schedule, and the run-ledger;
// this file supplies the two closures it can't:
//
//   - the WAKER: when an event fires, inject it into the Operator's ongoing
//     thread (operator-thread) and run a turn so the Operator reacts — report,
//     delegate (which routes through the authorization queue), or take note.
//     The reply persists to the thread, so the user sees the reaction next time
//     they open Operator.
//   - the POLLER: run a checker agent against a brief and return its answer, so
//     the engine can decide whether the condition tripped.
//
// Plus the public webhook endpoint external systems POST to.

package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// registerOperatorWake installs the event-monitor closures and starts the poll
// scheduler. Call once at startup (alongside registerStandingRunner).
func registerOperatorWake(app *OrchestrateApp) {
	// Waker: run the Operator on its pinned thread with the event injected as a
	// user message. RunAgentSyncContinuing loads operator-thread's history,
	// runs the turn, and persists the exchange — so the wake lands inside the
	// same conversation the user has with the Operator. The Operator's
	// consequential action (delegate) self-gates through the authorization
	// queue, so auto-approve at the loop level is safe here.
	RegisterEventWaker(func(ctx context.Context, owner, monitorName, summary string) {
		brief := ""
		if m, ok := GetEventMonitor(RootDB, owner, monitorName); ok && strings.TrimSpace(m.WakeBrief) != "" {
			brief = "\n\nWhat to do: " + m.WakeBrief
		}
		msg := fmt.Sprintf("[EVENT — monitor %q fired]\n%s%s\n\nReact in this thread: report it, delegate any needed work (delegation routes through the authorization queue), or just note it.",
			monitorName, summary, brief)
		if _, err := app.RunAgentSyncContinuing(ctx, owner, owner, defaultConsoleAgent, operatorPinnedSession, "", msg, false); err != nil {
			Log("[operator.wake] %s/%s: %v", owner, monitorName, err)
		}
	})

	// Poller: run the checker agent fresh and return its answer.
	RegisterEventPoller(func(ctx context.Context, owner, agentID, check string) (string, error) {
		return app.RunAgentSync(ctx, owner, owner, agentID, check)
	})

	StartEventMonitorScheduler()
}

// handleOperatorEvent is the public webhook endpoint external watchers POST to:
//
//	POST /api/operator/event/<token>   body: {"summary": "...", "detail": "..."}
//
// It is intentionally unauthenticated — the unguessable token IS the
// credential (the TeamSpeak-style "secret URL" model). A bad/unknown token
// gets the same 404 a missing path does, so the endpoint can't be used to
// enumerate monitors.
func (T *OrchestrateApp) handleOperatorEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Extract the token after the route marker — works regardless of the
	// app's mount prefix (the full path is /orchestrate/api/operator/event/…).
	const marker = "/api/operator/event/"
	i := strings.Index(r.URL.Path, marker)
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	token := r.URL.Path[i+len(marker):]
	if token == "" || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	m, ok := FindEventMonitorByToken(RootDB, token)
	if !ok || m.Kind != EventKindWebhook {
		http.NotFound(w, r)
		return
	}
	if m.Paused {
		// Accept the POST so the caller doesn't retry, but don't wake.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var body struct {
		Summary string `json:"summary"`
		Detail  string `json:"detail"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	summary := strings.TrimSpace(body.Summary)
	if summary == "" {
		summary = "(event fired with no summary)"
	}
	if d := strings.TrimSpace(body.Detail); d != "" {
		summary += "\n\n" + d
	}
	// Wake asynchronously — the external caller shouldn't block on the
	// Operator's full turn.
	go FireEventMonitor(context.Background(), RootDB, m, summary)
	w.WriteHeader(http.StatusNoContent)
}
