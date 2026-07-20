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
		// set it up. There is NO implicit default anymore — the old fallback
		// (the framework Chat seed) is retired, and waking it would post into
		// a thread no user can open; the empty case parks the monitor broken
		// below instead.
		m, ok := GetEventMonitor(RootDB, owner, monitorName)
		if !ok {
			Log("[operator.wake] %s/%s: monitor no longer exists — wake dropped", owner, monitorName)
			return
		}
		if strings.TrimSpace(m.WakeBrief) != "" {
			brief = "\n\nWhat to do: " + m.WakeBrief
		}
		wakeAgent := strings.TrimSpace(m.WakeAgent)
		wakeSession := strings.TrimSpace(m.WakeSession)
		chatTarget := strings.TrimSpace(m.DeliverChatID)
		wakeChannel := strings.TrimSpace(m.WakeChannel)
		notify := EventNotifyChannel
		if m.Notify != "" {
			notify = m.Notify
		}

		// Channel target (Stage B, unified source→channel): deliver the change
		// INTO the bound channel — its agent reacts in the channel's thread, and
		// if the channel has a live transport (iMessage/etc) the reaction flows
		// back out it. This REPLACES the default cortex-thread wake (an explicit
		// destination was chosen); text/direct fan-out below still applies if the
		// monitor also asked for them.
		channelTargetDelivered := false
		if wakeChannel != "" {
			if ch, ok := GetChannel(RootDB, owner, wakeChannel); ok {
				text := fmt.Sprintf("[EVENT — bridge %q fired]\n%s%s", monitorName, summary, brief)
				if _, err := RunChannelAgent(ctx, ChannelInbound{
					Owner:            owner,
					AgentID:          ch.AgentID,
					SessionID:        ChannelSessionKey(ch, "bridge:"+monitorName),
					Text:             text,
					SenderName:       "bridge:" + monitorName,
					ConversationName: ch.Name,
				}); err == nil {
					channelTargetDelivered = true
				} else {
					Log("[operator.wake] %s/%s channel target %q delivery failed: %v", owner, monitorName, wakeChannel, err)
				}
			} else {
				Log("[operator.wake] %s/%s channel target %q not found — falling back to agent thread", owner, monitorName, wakeChannel)
			}
		}
		// No wake agent (a legacy monitor from before the field): if the
		// channel target above delivered, that IS the wake; every other path
		// below needs an agent thread, so park the monitor broken — the owner
		// relinks an agent from the console instead of the alert vanishing
		// into the retired Chat seed's unreachable thread. The poll-tick
		// dependency guard (eventMonitorDependencyError) parks these
		// proactively too; this covers webhook-kind monitors and races.
		if wakeAgent == "" {
			if channelTargetDelivered {
				return
			}
			MarkEventMonitorBroken(RootDB, owner, monitorName, "it has no wake agent — its old implicit default (the retired Chat seed) no longer runs; relink an agent to resume")
			Log("[operator.wake] %s/%s parked broken: no wake agent (legacy Chat-seed default retired)", owner, monitorName)
			return
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
		// A successful channel-target delivery IS the wake: it consumes the
		// default "channel" mode (so the cortex thread isn't also woken) and
		// counts as delivered (so the never-drop fallback doesn't double-fire).
		// An explicit text/direct on the same monitor still fans out below.
		if channelTargetDelivered {
			delete(modes, EventNotifyChannel)
			delivered = true
		}

		// text: deliver the event straight to the owner's phone, no LLM.
		if modes[EventNotifyText] {
			sent := false
			if link, ok := ActiveMessagingLink(); ok {
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
			// A direct fire does NOT wake the agent (no LLM turn — that's the whole
			// point of direct). But it should still leave a durable trace in the
			// agent's cortex session — an event-monitor card — so the agent has
			// CONTEXT that the monitor fired next time it runs, EVEN when the alert
			// went straight out to an external channel. recordMonitorCard appends
			// that card without running the agent. Same ReportKind the in-thread
			// direct path already used, so it renders as a monitor card either way.
			recordMonitorCard := func(content string) bool {
				udb := UserDB(app.DB, owner)
				if udb == nil {
					return false
				}
				sess, ok := loadChatSession(udb, wakeAgent, wakeSession)
				if !ok {
					sess = ChatSession{ID: wakeSession, AgentID: wakeAgent}
				}
				sess.Messages = append(sess.Messages, ChatMessage{
					Role:       "assistant",
					Content:    content,
					Created:    time.Now(),
					ReportFrom: monitorName,
					ReportKind: cortexKindMonitor,
				})
				if _, err := saveChatSession(udb, sess); err != nil {
					Log("[operator.wake] %s/%s record monitor card failed: %v", owner, monitorName, err)
					return false
				}
				return true
			}
			if chatTarget != "" {
				// External channel (a phantom chat / group): post the alert out,
				// THEN record the card so the agent sees it fired. A failed external
				// send leaves delivered=false so the never-drop fallback wake still
				// fires — the card is a trace, not a substitute for delivery.
				if link, ok := ActiveMessagingLink(); ok {
					if err := link.SendToChat(owner, chatTarget, summary); err == nil {
						delivered = true
						Debug("[operator.wake] %s/%s notify=direct enqueued alert to phantom chat %s", owner, monitorName, chatTarget)
						recordMonitorCard(fmt.Sprintf("%s\n\n(auto-posted directly to %s — no reply needed)", summary, chatTarget))
					} else {
						Log("[operator.wake] %s/%s notify=direct send to chat %s failed: %v", owner, monitorName, chatTarget, err)
					}
				} else {
					Log("[operator.wake] %s/%s notify=direct but phantom bridge unavailable", owner, monitorName)
				}
			} else if recordMonitorCard(summary) {
				// No external target: the cortex card IS the delivery.
				delivered = true
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
	RegisterWatchToolInvoker(func(owner, agentID, toolName string, toolArgs map[string]any) (string, error) {
		sess := &ToolSession{Username: owner, DB: AuthDB()}
		// Give the invoker session the owner's real workspace. Without it a
		// SHELL/script temp tool (script_body + "python3 {workspace_dir}/x.py")
		// bails every tick with "references {workspace_dir} but the session has
		// no sandbox" — so a scoped script tool could never run on a monitor,
		// and the model fell back to hand-authoring a self-sending script. An
		// api-mode wrapper doesn't need this (it dispatches through the
		// credential), but not every watch tool is api-mode. Best-effort: on
		// failure WorkspaceDir stays empty and shell tools still get the clear
		// no-sandbox error rather than a silent one.
		if wd, werr := EnsureWorkspaceDir(owner); werr == nil {
			sess.WorkspaceDir = wd
		}
		// (1) operator-management tools (read_phantom_chat, list_phantom_chats…).
		for _, td := range operatorManagementTools(sess, defaultConsoleAgent) {
			if td.Tool.Name == toolName {
				return td.Handler(toolArgs)
			}
		}
		// (2) channel-scoped tools (read_chat, list_chats, list_members): built
		// from a specific agent's bound channels, so they need the monitor's agent
		// id (now threaded through the watch path). This is what an await_result on
		// read_chat polls — without it the watch fails "read_chat is not registered"
		// every tick and never catches the reply it's waiting for.
		if agentID != "" {
			for _, td := range channelChatTools(sess, owner, agentID) {
				if td.Tool.Name == toolName {
					return td.Handler(toolArgs)
				}
			}
		}
		// (3) the owner's authored temp tools across EVERY scope a watch can
		// reach — not just the admin-promoted global pool. A watch tool (e.g. the
		// api-mode wrapper ts3_list_clients, which dispatches through a credential)
		// need not be promoted to global: it may live in the shared/deployment
		// pool or be AGENT-SCOPED (authored on/for one agent, on its record). All
		// three live in the temp-tool store, not the static chat-tool registry, so
		// InvokeWatcherTool can't reach them. Load them all here; de-dup by name so
		// a tool present in more than one scope is built once (first scope wins).
		seen := map[string]bool{}
		addTool := func(tt TempTool) {
			if seen[tt.Name] {
				return
			}
			seen[tt.Name] = true
			c := tt
			sess.TempTools = append(sess.TempTools, &c)
		}
		for _, p := range LoadPersistentTempTools(AuthDB(), owner) { // owner's own pool
			addTool(p.Tool)
		}
		adoptedGlobal := LoadAdoptedGlobalTools(AuthDB(), owner)
		for _, p := range LoadSharedPersistentTempTools(AuthDB()) { // global pool: opt-in only
			if adoptedGlobal[p.Tool.Name] {
				addTool(p.Tool)
			}
		}
		for _, a := range listAgents(agentUserDB(RootDB, owner), owner) { // agent-scoped tools
			for _, tt := range a.Tools {
				addTool(tt)
			}
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

	// Label event.poll tasks in the admin scheduler view + scheduler logs with
	// the monitor name and the agent it wakes, instead of a bare "event.poll" +
	// uuid. Registered here (not in core) because resolving the wake-agent id to
	// a friendly name needs the orchestrate agent store; core stays generic.
	RegisterTaskDescriber(EventPollKind, func(payload json.RawMessage) string {
		m, ok := EventMonitorForTaskPayload(payload)
		if !ok {
			return ""
		}
		agent := "default channel agent"
		if id := strings.TrimSpace(m.WakeAgent); id != "" {
			agent = id
			if a, ok := loadAgent(UserDB(app.DB, m.Owner), id); ok && strings.TrimSpace(a.Name) != "" {
				agent = a.Name
			}
		}
		return fmt.Sprintf("%s (agent: %s)", m.Name, agent)
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
