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
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
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
		// The monitor names the agent to wake (WakeAgent) and the session it
		// was created in (WakeSession), so the event lands back where the user
		// set it up. Fall back to the deployment default channel agent and its
		// home thread when unset (legacy monitors created before the fields).
		wakeAgent := defaultConsoleAgent
		wakeSession := ""
		chatTarget := ""
		notify := EventNotifyChannel
		if m, ok := GetEventMonitor(RootDB, owner, monitorName); ok {
			if strings.TrimSpace(m.WakeBrief) != "" {
				brief = "\n\nWhat to do: " + m.WakeBrief
			}
			if a := strings.TrimSpace(m.WakeAgent); a != "" {
				wakeAgent = a
			}
			wakeSession = strings.TrimSpace(m.WakeSession)
			chatTarget = strings.TrimSpace(m.DeliverChatID)
			if m.Notify != "" {
				notify = m.Notify
			}
		}
		if wakeSession == "" {
			wakeSession = cortexSessionID(wakeAgent)
		}
		// notify may list MULTIPLE destinations (comma-separated, e.g.
		// "direct,text") — fan out to each. The channel wake doubles as the
		// fallback when nothing else delivered, so the alert is never dropped.
		modes := map[string]bool{}
		for _, mode := range strings.Split(notify, ",") {
			if mode = strings.TrimSpace(mode); mode != "" {
				modes[mode] = true
			}
		}
		delivered := false

		// text: deliver the event straight to the owner's phone, no LLM.
		if modes[EventNotifyText] {
			sent := false
			if link, ok := ActivePhantomLink(); ok {
				if self, ok := link.OwnerHandle(owner); ok {
					if err := link.SendToHandle(owner, self, summary); err == nil {
						sent, delivered = true, true
					} else {
						Log("[operator.wake] %s/%s notify=text send failed: %v", owner, monitorName, err)
					}
				}
			}
			if !sent {
				Log("[operator.wake] %s/%s notify=text but owner phone unavailable", owner, monitorName)
			}
		}

		// direct: post the change verbatim, no LLM, WHERE the watcher was created
		// — a phantom-origin watcher (DeliverChatID set) into that conversation
		// (e.g. the group); otherwise into the Agency channel thread.
		if modes[EventNotifyDirect] {
			if chatTarget != "" {
				if link, ok := ActivePhantomLink(); ok {
					if err := link.SendToChat(owner, chatTarget, summary); err == nil {
						delivered = true
						Debug("[operator.wake] %s/%s notify=direct enqueued alert to phantom chat %s", owner, monitorName, chatTarget)
					} else {
						Log("[operator.wake] %s/%s notify=direct send to chat %s failed: %v", owner, monitorName, chatTarget, err)
					}
				} else {
					Log("[operator.wake] %s/%s notify=direct but phantom bridge unavailable", owner, monitorName)
				}
			} else if udb := UserDB(app.DB, owner); udb != nil {
				sess, ok := loadChatSession(udb, wakeAgent, wakeSession)
				if !ok {
					sess = ChatSession{ID: wakeSession, AgentID: wakeAgent}
				}
				sess.Messages = append(sess.Messages, ChatMessage{
					Role:       "assistant",
					Content:    summary,
					Created:    time.Now(),
					ReportFrom: monitorName,
					ReportKind: cortexKindMonitor,
				})
				if _, err := saveChatSession(udb, sess); err == nil {
					delivered = true
				} else {
					Log("[operator.wake] %s/%s notify=direct save failed: %v", owner, monitorName, err)
				}
			}
		}

		// channel: wake the agent in-thread to react. Also the fallback when no
		// other destination delivered, so the alert is never silently dropped.
		if modes[EventNotifyChannel] || !delivered {
			msg := fmt.Sprintf("[EVENT — monitor %q fired]\n%s%s\n\nReact in this thread: report it, delegate any needed work (delegation routes through the authorization queue), or just note it.",
				monitorName, summary, brief)
			if _, err := app.RunAgentSyncContinuing(ctx, owner, owner, wakeAgent, wakeSession, "", msg, false); err != nil {
				Log("[operator.wake] %s/%s: %v", owner, monitorName, err)
			}
		}
	})

	// Watch-tool invoker: lets a "watch" monitor poll OWNER-SCOPED tools that
	// only exist as per-session closures (read_phantom_chat, list_phantom_chats,
	// etc.) — InvokeWatcherTool can only reach globally-registered + secure-API
	// tools. We rebuild the management toolset for the monitor's owner and
	// dispatch the named tool; anything not found falls back to the global path.
	RegisterWatchToolInvoker(func(owner, toolName string, toolArgs map[string]any) (string, error) {
		sess := &ToolSession{Username: owner, DB: AuthDB()}
		// (1) operator-management tools (read_phantom_chat, list_phantom_chats…).
		for _, td := range operatorManagementTools(sess, defaultConsoleAgent) {
			if td.Tool.Name == toolName {
				return td.Handler(toolArgs)
			}
		}
		// (2) the owner's persistent temp tools — admin-promoted "global" tools
		// (e.g. an api-mode wrapper like ts3_list_clients). These live in the
		// temp-tool store, not the static chat-tool registry, so InvokeWatcherTool
		// can't reach them.
		for _, p := range LoadPersistentTempTools(AuthDB(), owner) {
			tt := p.Tool
			sess.TempTools = append(sess.TempTools, &tt)
		}
		for _, td := range temptool.BuildAgentToolDefs(sess) {
			if td.Tool.Name == toolName {
				return td.Handler(toolArgs)
			}
		}
		return "", ErrWatchToolNotHandled
	})

	// Poller: run the checker agent fresh and return its answer. If the named
	// checker doesn't exist (e.g. the LLM set check_agent to a conversational
	// nickname rather than a real agent), fall back to the default channel
	// agent so the monitor still works instead of erroring every interval.
	RegisterEventPoller(func(ctx context.Context, owner, agentID, check string) (string, error) {
		out, err := app.RunAgentSync(ctx, owner, owner, agentID, check)
		if err != nil && strings.Contains(err.Error(), "not found") && agentID != defaultConsoleAgent {
			Log("[operator.poll] checker %q not found — falling back to %s", agentID, defaultConsoleAgent)
			return app.RunAgentSync(ctx, owner, owner, defaultConsoleAgent, check)
		}
		return out, err
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
