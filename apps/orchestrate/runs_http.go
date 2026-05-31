// HTTP endpoints around the run registry:
//
//   GET  /api/runs/active?session_id=…     → {run_id} | {}
//   GET  /api/runs/<id>/stream?since=N     → SSE replay + tail
//   POST /api/runs/<id>/cancel             → 200 OK
//
// The chat panel's normal send still uses /api/send and gets SSE
// streamed back inline. These three are the "reconnect to an
// already-running turn" surface — used after a desktop overlay
// closes, a tab navigation, a network blip, or a hard reload.

package orchestrate

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// handleRunsActive returns the active run ID for a session, or an
// empty object if none. Frontend uses this on session-load to decide
// whether to attach to an in-flight stream rather than rendering
// stale state and missing the live tail.
func (T *OrchestrateApp) handleRunsActive(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	run := T.runsRegistry().BySession(sessionID)
	if run == nil {
		w.Write([]byte("{}"))
		return
	}
	json.NewEncoder(w).Encode(map[string]string{
		"run_id":     run.ID,
		"session_id": sessionID,
	})
}

// handleRunsStream tails a run's event buffer as SSE. The `since`
// query param lets the client resume from a known sequence number —
// the server replays every event with Seq > since, then tails the
// live channel until the run completes or the client disconnects.
//
// Authentication: only the user who owns the run can read it. This
// mirrors agent-record ownership; a leaked run ID still can't be
// tailed by another user.
func (T *OrchestrateApp) handleRunsStream(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	runID := extractRunID(r.URL.Path, "/stream")
	if runID == "" {
		http.Error(w, "run id required", http.StatusBadRequest)
		return
	}
	run := T.runsRegistry().Get(runID)
	if run == nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	if run.UserID != user {
		// Don't leak existence to other users; 404 instead of 403.
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}

	var since uint64
	if s := r.URL.Query().Get("since"); s != "" {
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			since = n
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sub := run.Subscribe(since)
	defer run.Unsubscribe(sub)

	// Replay backlog first so the client renders the missing events
	// in order before live tailing kicks in.
	for _, ev := range sub.Backlog {
		if _, err := w.Write(ev.Frame); err != nil {
			return
		}
	}
	flusher.Flush()

	// Tail live until the run completes (channel close) OR the
	// client disconnects (r.Context().Done()). The agent loop keeps
	// running regardless of either — we're just here to deliver
	// frames while we have a live socket.
	for {
		select {
		case ev, open := <-sub.Live:
			if !open {
				return
			}
			if _, err := w.Write(ev.Frame); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleRunsCancel triggers Run.Cancel for the given run ID. Mirrors
// /api/cancel (which is keyed by session ID) but takes a run ID,
// so a subscriber tailing a run can cancel it directly without
// needing to know which session it belongs to.
func (T *OrchestrateApp) handleRunsCancel(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runID := extractRunID(r.URL.Path, "/cancel")
	if runID == "" {
		http.Error(w, "run id required", http.StatusBadRequest)
		return
	}
	run := T.runsRegistry().Get(runID)
	if run == nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	if run.UserID != user {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	run.Cancel()
	w.WriteHeader(http.StatusOK)
}

// handleRunsDispatch routes /api/runs/<id>/<verb> to the right
// handler based on the path suffix. Go's ServeMux only matches by
// prefix, so the per-verb routing lives here.
func (T *OrchestrateApp) handleRunsDispatch(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/stream"):
		T.handleRunsStream(w, r)
	case strings.HasSuffix(r.URL.Path, "/cancel"):
		T.handleRunsCancel(w, r)
	default:
		http.NotFound(w, r)
	}
}

// extractRunID pulls the {id} segment out of /api/runs/{id}/<suffix>.
// Returns empty string on shape mismatch. Lighter than mux routing
// since the run ID has no path-meaningful characters (hex).
func extractRunID(path, suffix string) string {
	const prefix = "/api/runs/"
	i := strings.Index(path, prefix)
	if i < 0 {
		return ""
	}
	rest := path[i+len(prefix):]
	if !strings.HasSuffix(rest, suffix) {
		return ""
	}
	return rest[:len(rest)-len(suffix)]
}
