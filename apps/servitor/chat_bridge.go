// Bridge between servitor's legacy probeEvent stream and the
// AgentLoopPanel SSE protocol. The existing runSession goroutine
// emits probeEvents into a per-session queue (probeSessions); this
// file's handler taps that queue, translates each event into the
// shape AgentLoopPanel understands, and ships them over SSE.
//
// All servitor-specific event kinds (intent, plan_*, notes_consumed,
// confirm_technique, draft) become `kind: "block"` events with an
// app-registered renderer name. The renderers live in
// apps/servitor/web_assets.go and inject into the conversation pane
// via window.uiRegisterBlockRenderer — keeping the AgentLoopPanel
// primitive free of any servitor-specific knowledge.

package servitor

import (
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// translateProbeEvent converts a legacy probeEvent into the
// AgentLoopPanel SSE event shape. Returns the map ready for sse.Send.
//
// Mapping:
//
//	status            → status              (status pill)
//	cmd               → activity{type:cmd}
//	output            → activity{type:output}
//	watch             → activity{type:watch}
//	error             → error
//	message           → activity{type:status} (intermediate narration)
//	reply             → message+message_done (final assistant reply)
//	done              → done
//	confirm           → confirm (operator approval card)
//	confirm_technique → confirm with save/skip buttons
//	intent            → block{type:servitor_intent}
//	plan_set/step     → block{type:servitor_plan}
//	notes_consumed    → block{type:servitor_notes_consumed} (drops the
//	                    edit/delete affordances on consumed notes)
//	draft             → block{type:servitor_draft}
func translateProbeEvent(ev probeEvent) map[string]any {
	switch ev.Kind {
	case "status":
		// Legacy shows every status update as a row in the
		// activity log so the user sees a running history of
		// "what stage are we in" — not a single pill that gets
		// overwritten on each new event. Skip the connection-
		// chatter that legacy also filtered out.
		if ev.Text == "Connected." ||
			strings.HasPrefix(ev.Text, "SSH reconnected") {
			return map[string]any{"kind": "status", "text": ev.Text}
		}
		return map[string]any{
			"kind": "activity", "type": "status",
			"id":   "a-" + cheapID(),
			"text": ev.Text,
		}
	case "cmd":
		return map[string]any{
			"kind": "activity", "type": "cmd",
			"id":   "a-" + cheapID(),
			"text": ev.Text,
		}
	case "output":
		return map[string]any{
			"kind": "activity", "type": "output",
			"id":   "a-" + cheapID(),
			"text": ev.Text,
		}
	case "watch":
		return map[string]any{
			"kind": "activity", "type": "watch",
			"id":   "a-" + cheapID(),
			"text": ev.Text,
		}
	case "error":
		return map[string]any{"kind": "error", "text": ev.Text}
	case "message":
		return map[string]any{
			"kind": "activity", "type": "status",
			"id":   "a-" + cheapID(),
			"text": ev.Text,
		}
	case "reply":
		// Final assistant reply — emit as a complete message bubble.
		// The runtime will markdown-render on message_done since the
		// panel was configured with Markdown: true.
		id := "m-" + cheapID()
		// Single payload carries both 'message' (creates the bubble
		// + sets initial text) and the runtime will run mdToHTML on
		// the followup message_done event.  But the bridge can only
		// emit one event per call, so the caller emits both —
		// the helper below produces a slice for that case.
		return map[string]any{
			"kind": "message", "role": "assistant", "id": id, "text": ev.Text,
			"_finalize": id, // sentinel — caller emits message_done after
		}
	case "done":
		return map[string]any{"kind": "done"}
	case "confirm":
		return map[string]any{
			"kind":   "confirm",
			"id":     "c-" + cheapID(),
			"prompt": "Destructive command",
			"detail": ev.Text + "\n\nReason: " + ev.Reason,
			"actions": []map[string]string{
				{"label": "Allow", "value": "allow", "variant": "primary"},
				{"label": "Always", "value": "always"},
				{"label": "Deny", "value": "deny", "variant": "danger"},
			},
		}
	case "confirm_technique":
		return map[string]any{
			"kind":   "confirm",
			"id":     "c-" + cheapID(),
			"prompt": "Save technique?",
			"detail": ev.Text,
			"actions": []map[string]string{
				{"label": "Save", "value": "allow", "variant": "primary"},
				{"label": "Skip", "value": "deny"},
			},
		}
	case "intent":
		return map[string]any{
			"kind": "block", "type": "servitor_intent",
			"id":     "b-" + cheapID(),
			"text":   ev.Text,
			"reason": ev.Reason,
		}
	case "plan_set", "plan_step":
		return map[string]any{
			"kind": "block", "type": "servitor_plan",
			"id":   "servitor-plan",
			"plan": ev.Plan,
		}
	case "notes_consumed":
		return map[string]any{
			"kind": "block", "type": "servitor_notes_consumed",
			"ids": ev.IDs,
		}
	case "draft":
		return map[string]any{
			"kind": "block", "type": "servitor_draft",
			"id":   "b-" + cheapID(),
			"text": ev.Text,
		}
	}
	// Unknown kinds — surface as a generic status so events aren't
	// silently dropped; helps catch translator drift.
	return map[string]any{
		"kind": "activity", "type": "status",
		"id":   "a-" + cheapID(),
		"text": "[unhandled: " + ev.Kind + "] " + ev.Text,
	}
}

// cheapID returns a short, monotonic id. Each translated event needs
// a unique id so the runtime can target it for updates; the legacy
// probeEvent shape doesn't carry one, so we generate here. Resolution
// is nanoseconds which is comfortably finer than the event rate.
var idCounter uint64

func cheapID() string {
	idCounter++
	return time.Now().Format("150405.000000") + "-" + uitoa(idCounter)
}

func uitoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 20)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	// reverse
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}

// handleChatEvents is the AgentLoopPanel-facing SSE endpoint. It
// taps probeSessions's queue for the requested session id and
// streams translated events to the response. Reuses the queue's
// snapshot+poll pattern so reconnects after a page reload replay
// buffered events and pick up new ones live.
func (T *Servitor) handleChatEvents(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	events, done := probeSessions.SnapshotEvents(id)
	if events == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// First event: tell the client which session it's bound to,
	// matching the AgentLoopPanel protocol. Include the appliance
	// id so the client can auto-set the appliance picker on
	// reconnect (without it the picker stays empty and the
	// terminal can't auto-connect).
	applianceID := ""
	if v, ok := sessionAppliances.Load(id); ok {
		if aid, ok := v.(string); ok {
			applianceID = aid
		}
	}
	sse.Send(map[string]any{
		"kind":         "session",
		"id":           id,
		"appliance_id": applianceID,
	})

	// Helper that emits one probeEvent through the translator and
	// handles the reply→message+message_done two-event special case.
	sendEvent := func(ev probeEvent) bool {
		out := translateProbeEvent(ev)
		if finalize, ok := out["_finalize"].(string); ok {
			delete(out, "_finalize")
			if err := sse.Send(out); err != nil {
				return false
			}
			if err := sse.Send(map[string]any{
				"kind": "message_done", "id": finalize,
			}); err != nil {
				return false
			}
			return true
		}
		return sse.Send(out) == nil
	}

	// Replay buffered events.
	for _, ev := range events {
		if !sendEvent(ev) {
			return
		}
	}
	if done {
		return
	}

	// Stream new events as they arrive. Same poll cadence as
	// LiveSessionMap.HandleReconnect — keep the cost cheap and
	// fire a comment heartbeat when there's been no activity to
	// keep proxies + browsers from timing the stream out.
	const heartbeat = 15 * time.Second
	sent := len(events)
	lastActivity := time.Now()
	for {
		time.Sleep(500 * time.Millisecond)
		current, isDone := probeSessions.SnapshotEvents(id)
		if current == nil {
			return
		}
		if len(current) > sent {
			for i := sent; i < len(current); i++ {
				if !sendEvent(current[i]) {
					return
				}
			}
			sent = len(current)
			lastActivity = time.Now()
		} else if time.Since(lastActivity) >= heartbeat {
			if err := sse.SendComment("heartbeat"); err != nil {
				return
			}
			lastActivity = time.Now()
		}
		if isDone {
			return
		}
	}
}
