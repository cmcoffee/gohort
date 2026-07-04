// The Operator agent's exclusive fleet-management catalog: create / list /
// run / pause standing (scheduled) agents and read the run-ledger.
//
// Like Builder's authoring tools, these are NOT globally registered — they're
// appended at run time ONLY for orchestrator-mode agents (the gate lives in
// runner.go's catalog assembly and the dispatch paths), so no other agent
// gets them. Owner-scoped to the runtime user; reuses the shared core spine
// (standing-agent store + run-ledger). The actual execution of a standing
// agent is handled by the registered standing runner (standing_runner.go).

package orchestrate

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// operatorAttachMarkerRe matches the phantom [ATTACH: file] delivery marker an
// agent may embed in a message it hands to a contact. Same shape as phantom's
// own marker so the two surfaces agree. The marker text is stripped at delivery
// (DeliverMessage); here we use it to resolve the referenced file from the
// agent's workspace and carry it as a real attachment.
var operatorAttachMarkerRe = regexp.MustCompile(`\[ATTACH:\s*([^\],]+?)(?:\s*,\s*cleanup\s*=\s*(true|false))?\s*\]`)

// collectMessageAttachments gathers the base64 images to send with an
// agent-originated phantom message. Two sources, both honored: (1) files the
// agent attached via workspace(action="attach") (sess.Images), and (2) files
// referenced by an [ATTACH: name] marker in the text, resolved against the
// agent's workspace dir. Without (2), an agent that used the marker convention
// instead of workspace(attach) would leak the marker as text and send nothing.
func collectMessageAttachments(sess *ToolSession, text string) []string {
	var out []string
	if sess != nil {
		out = append(out, sess.Images...)
	}
	if sess == nil || strings.TrimSpace(sess.WorkspaceDir) == "" {
		return out
	}
	for _, m := range operatorAttachMarkerRe.FindAllStringSubmatch(text, -1) {
		name := strings.TrimSpace(m[1])
		out = append(out, resolveWorkspaceImages(sess, []string{name})...)
	}
	return out
}

// collectMessageMedia is the type-AWARE version used by the channel-reply path:
// it splits a turn's outbound attachments into images and videos so a video
// rides out as a video, not a mislabeled (and undeliverable) oversized image.
// Sources: session Images/Videos (video tools accumulate into sess.Videos via
// AppendVideo) plus [ATTACH: file] markers, routed by file extension. Restores
// the outbound video channel the phantom outbox had before the bridges migration.
func collectMessageMedia(sess *ToolSession, text string) (images, videos []string) {
	if sess == nil {
		return nil, nil
	}
	images = append(images, sess.Images...)
	videos = append(videos, sess.Videos...)
	if strings.TrimSpace(sess.WorkspaceDir) == "" {
		return images, videos
	}
	for _, m := range operatorAttachMarkerRe.FindAllStringSubmatch(text, -1) {
		name := strings.TrimSpace(m[1])
		b64 := resolveWorkspaceImages(sess, []string{name})
		if len(b64) == 0 {
			continue
		}
		if isVideoAttachment(name) {
			videos = append(videos, b64...)
		} else {
			images = append(images, b64...)
		}
	}
	return images, videos
}

// isVideoAttachment classifies a workspace file path as a video by extension, so
// a [ATTACH: clip.mp4] marker routes to the video channel instead of the image
// channel.
func isVideoAttachment(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".mp4", ".mov", ".webm", ".mkv", ".avi", ".m4v":
		return true
	}
	return false
}

// resolveWorkspaceImages reads each workspace-relative path and returns its
// base64 bytes, skipping anything empty, escaping the workspace, or unreadable.
// Lets a channel agent deliver a file straight to its channel via send_message's
// `attachments` param WITHOUT workspace(action="attach") — which stages the file
// onto the agent's REPLY (the caller), not the channel. That conflation is why a
// dispatched channel agent kept "sending the image back" instead of out.
func resolveWorkspaceImages(sess *ToolSession, paths []string) []string {
	var out []string
	if sess == nil || strings.TrimSpace(sess.WorkspaceDir) == "" {
		return out
	}
	for _, name := range paths {
		name = strings.TrimSpace(name)
		clean := filepath.Clean(name)
		if name == "" || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			continue // never escape the workspace
		}
		b, err := os.ReadFile(filepath.Join(sess.WorkspaceDir, clean))
		if err != nil {
			continue
		}
		out = append(out, base64.StdEncoding.EncodeToString(b))
	}
	return out
}

// attachmentsParamDesc is the shared description for the messaging tools' explicit
// image/file param. One self-contained call ("send THIS image to X") instead of
// the implicit, easily-skipped workspace(action="attach")-first convention.
const attachmentsParamDesc = "Optional workspace file path(s) to send WITH this message — e.g. [\"find-djbk.jpg\"] from image(action=\"find\"/\"generate\"). The files ride out to the recipient. Prefer this over a separate workspace(action=\"attach\") step: pass the path here and it's delivered in one call."

// messageImages gathers every image to ride an outbound message: the explicit
// `attachments` workspace paths (the steered, self-contained path) PLUS the
// implicit sess.Images / [ATTACH:] markers (collectMessageAttachments), deduped.
// One place so send_message, message_contact and notify_me behave identically —
// the fragmented "did the model remember to attach first?" failure mode is why
// images were silently dropped.
func messageImages(sess *ToolSession, args map[string]any, text string) []string {
	images := collectMessageAttachments(sess, text)
	var paths []string
	if raw, ok := args["attachments"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				paths = append(paths, s)
			}
		}
	}
	seen := map[string]bool{}
	for _, im := range images {
		seen[im] = true
	}
	for _, im := range resolveWorkspaceImages(sess, paths) {
		if !seen[im] {
			seen[im] = true
			images = append(images, im)
		}
	}
	return images
}

// isReplyToActiveInbound reports whether recip is the very conversation this run
// is replying to (a channel inbound). Replying to whoever just messaged you is
// in-thread, not a proactive reach-out, so it skips the approval queue. False on
// web / dispatch runs (ReplyAuthorizedKey empty), leaving the gate unchanged there.
func isReplyToActiveInbound(sess *ToolSession, recip string) bool {
	return sess != nil && sess.ReplyAuthorizedKey != "" && recip != "" && recip == sess.ReplyAuthorizedKey
}

// resolveCheckAgent maps a user-supplied poll-monitor checker reference to a
// real agent id. Tries: exact id, then case-insensitive display-name match,
// then falls back to the creating channel agent (always valid). This prevents
// a monitor from being saved with a checker that doesn't exist — the failure
// the LLM hits when it sets check_agent to a conversational nickname.
func resolveCheckAgent(sess *ToolSession, owner, want, fallback string) string {
	if sess == nil || sess.DB == nil {
		return fallback
	}
	if _, ok := loadAgent(sess.DB, want); ok {
		return want
	}
	for _, a := range listAgents(sess.DB, owner) {
		if strings.EqualFold(strings.TrimSpace(a.Name), want) {
			return a.ID
		}
	}
	return fallback
}

// operatorRecipientKey is the stable identity used for pre-authorization and
// display when the Operator messages someone via phantom: the chat_id when
// targeting a known conversation or group, else the raw handle. Pre-auth keys
// on this so "Always allow" whitelists exactly the recipient that was approved
// (a group's chat_id, or an individual's handle) — never a different person.
func operatorRecipientKey(chatID, handle string) string {
	if c := strings.TrimSpace(chatID); c != "" {
		return c
	}
	return strings.TrimSpace(handle)
}

// operatorRecipientLabel renders a resolved recipient for user-facing messages:
// "DisplayName (handle)" when both are known, else whichever is present.
func operatorRecipientLabel(s MessagingChatSummary) string {
	switch {
	case s.DisplayName != "" && s.Handle != "":
		return s.DisplayName + " (" + s.Handle + ")"
	case s.DisplayName != "":
		return s.DisplayName
	case s.Handle != "":
		return s.Handle
	default:
		return s.ChatID
	}
}

// operatorDeliverMessage sends one message OUTBOUND via the messaging transport
// (Bridges) — addressed by chat_id (unambiguous; the ONLY correct way to reach a
// group) or handle. The agent composed the text, so it goes VERBATIM. This is
// the single delivery chokepoint for notify_me, message_contact, and the
// approval-execution path; routing it through Bridges' outbox is what makes them
// actually deliver (phantom's outbox is no longer drained — the daemon polls
// Bridges now). Returns the text delivered.
func operatorDeliverMessage(owner, chatID, handle, text string, images []string) (string, error) {
	ct, ok := ActiveChannelThreads()
	if !ok {
		return "", fmt.Errorf("no messaging transport is available")
	}
	// Empty service => let the transport resolve it from the conversation, so a
	// proactive send to a Telegram chat goes out Telegram (not the iMessage
	// default). Channel REPLIES already pass the inbound's service explicitly.
	if err := ct.Deliver(owner, "", chatID, handle, text, images); err != nil {
		return "", err
	}
	return text, nil
}

func operatorManagementTools(sess *ToolSession, agentID string) []AgentToolDef {
	owner := ""
	if sess != nil {
		owner = sess.Username
	}
	// The channel/orchestrator agent + session this management surface is bound
	// to (runner passes t.agent.ID). Captured before any handler shadows agentID
	// so standing-agent reports can land back in the session they were created
	// from.
	controllerAgentID := agentID
	controllerSession := ""
	if sess != nil {
		controllerSession = sess.ChatSessionID
	}
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "delegate",
				Description: "Delegate a task to an existing agent — this is how you DO work (you are a controller, you delegate rather than acting directly). If the agent is pre-authorized the delegation runs now and you get the result; otherwise it's queued for the user's approval in the Authorizations pane and you tell them it's waiting.",
				Parameters: map[string]ToolParam{
					"agent": {Type: "string", Description: "Name or id of the existing agent to delegate to."},
					"brief": {Type: "string", Description: "What the agent should do."},
				},
				Required: []string{"agent", "brief"},
			},
			Handler: func(args map[string]any) (string, error) {
				agent := strings.TrimSpace(oArgStr(args, "agent"))
				brief := strings.TrimSpace(oArgStr(args, "brief"))
				if agent == "" || brief == "" {
					return "", fmt.Errorf("agent and brief are required")
				}
				if IsDelegationBlocked(RootDB, owner, agent) {
					return fmt.Sprintf("Delegation to %q is blocked in the user's permission settings — not run.", agent), nil
				}
				if IsDelegationPreAuthorized(RootDB, owner, agent) {
					// Root the delegation in the PARENT TURN'S context (sess.Context())
					// so a Stop / cancel of the chat turn also cancels this outgoing
					// agent call — previously it ran on context.Background() and kept
					// going after the user stopped. ContextWithNetworkConnector also
					// carries the parent's network connector so a Private parent stays
					// private in the sub-run (applyForcePrivateToDispatch enforces it
					// from the blocked ctx). Both nil-safe.
					rec := RunDelegation(sess.ContextWithNetworkConnector(sess.Context()), RootDB, owner, agent, brief)
					if rec.Status == RunFailed {
						return fmt.Sprintf("Delegated to %q but it failed: %s", agent, rec.Err), nil
					}
					out := strings.TrimSpace(rec.Summary)
					if out == "" {
						out = strings.TrimSpace(rec.Raw)
					}
					return fmt.Sprintf("Delegated to %q (pre-authorized). Result:\n%s", agent, out), nil
				}
				a := SaveAuthorization(RootDB, Authorization{Owner: owner, Agent: agent, Brief: brief})
				return fmt.Sprintf("Queued a delegation to %q for the user's approval — it's in the Authorizations pane (id %s) and runs once approved.", agent, a.ID), nil
			},
		},
		{
			Tool: Tool{
				Name:        "create_standing_agent",
				Description: "Create a standing (scheduled) agent and start its schedule. agent_id must name an agent that already exists. Schedule it EITHER with cron (recurring at a wall-clock time — preferred for \"every day at HH:MM\") OR with interval_seconds + optional start_at (a specific first run, then a fixed interval).",
				Parameters: map[string]ToolParam{
					"name":             {Type: "string", Description: "Short unique name for this standing job, e.g. \"daily-weather\"."},
					"agent_id":         {Type: "string", Description: "Name or id of an existing agent to run."},
					"mission":          {Type: "string", Description: "What the agent should do each run."},
					"cron":             {Type: "string", Description: "Recurring wall-clock schedule in the human form DAY(S) HH:MM — NOT 5-field crontab (\"*/1 * * * *\" is INVALID). LOCAL time, the SAME zone get_local_time / time_in_zone report; use the time the user stated VERBATIM, do NOT convert to UTC. e.g. \"every day at 12pm\" → \"daily 12:00\"; also \"FRI 21:30\", \"weekdays 17:00\". For sub-hourly / every-N-minutes schedules cron can't express, use interval_seconds instead (e.g. 60 = every minute). Leave empty if using interval_seconds."},
					"start_at":         {Type: "string", Description: "ISO8601 first-run time, e.g. 2026-06-10T08:00:00-07:00. Use with interval_seconds for an arbitrary start + interval. Omit when using cron."},
					"interval_seconds": {Type: "number", Description: "Recurrence interval in seconds (60 = every minute, 3600 = hourly, 21600 = every 6h, 86400 = daily). This is the way to schedule sub-hourly / every-N-minutes runs (cron can't). Use with optional start_at. Omit when using cron."},
				},
				Required: []string{"name", "agent_id"},
			},
			Handler: func(args map[string]any) (string, error) {
				name := strings.TrimSpace(oArgStr(args, "name"))
				agentID := strings.TrimSpace(oArgStr(args, "agent_id"))
				cron := strings.TrimSpace(oArgStr(args, "cron"))
				interval := oArgInt(args, "interval_seconds")
				if name == "" || agentID == "" {
					return "", fmt.Errorf("name and agent_id are required")
				}
				sa := StandingAgent{
					Name: name, Owner: owner, AgentID: agentID,
					Mission: strings.TrimSpace(oArgStr(args, "mission")), Created: time.Now(),
					ReportAgentID:   controllerAgentID,
					ReportSessionID: controllerSession,
				}
				switch {
				case cron != "":
					if _, err := NextCronOccurrence(cron, time.Now()); err != nil {
						return "", fmt.Errorf("invalid cron %q: %w", cron, err)
					}
					sa.Cron = cron
				case interval > 0:
					sa.IntervalSeconds = interval
					if startStr := strings.TrimSpace(oArgStr(args, "start_at")); startStr != "" {
						t, err := time.Parse(time.RFC3339, startStr)
						if err != nil {
							return "", fmt.Errorf("invalid start_at (use ISO8601 like 2026-06-10T08:00:00-07:00): %w", err)
						}
						sa.StartAt = t
					}
				default:
					return "", fmt.Errorf("provide a schedule: either cron, or interval_seconds (with optional start_at)")
				}
				SaveStandingAgent(RootDB, sa)
				if err := ScheduleStandingAgent(RootDB, sa); err != nil {
					return "", fmt.Errorf("saved but scheduling failed: %w", err)
				}
				got, _ := GetStandingAgent(RootDB, owner, name)
				return fmt.Sprintf("Created standing agent %q running %q on %s. Next run: %s.",
					name, agentID, StandingScheduleLabel(got), got.NextRun.Local().Format("Mon Jan 2 3:04 PM")), nil
			},
		},
		{
			Tool: Tool{
				Name:        "list_standing_agents",
				Description: "List the user's standing agents with their schedule, paused state, last run status, and next run.",
			},
			Handler: func(args map[string]any) (string, error) {
				list := ListStandingAgents(RootDB, owner)
				if len(list) == 0 {
					return "No standing agents are set up yet.", nil
				}
				var b strings.Builder
				fmt.Fprintf(&b, "%d standing agent(s):\n", len(list))
				for _, sa := range list {
					status := "never run"
					if latest := ListRuns(RootDB, owner, RunFilter{Agent: sa.Name, Limit: 1}); len(latest) > 0 {
						status = string(latest[0].Status)
					}
					state := "active"
					if sa.Paused {
						state = "paused"
					}
					next := "—"
					if !sa.NextRun.IsZero() {
						next = sa.NextRun.Local().Format("Mon Jan 2 3:04 PM")
					}
					fmt.Fprintf(&b, "- %s (%s): runs %q on %s; last=%s; next=%s\n",
						sa.Name, state, sa.AgentID, StandingScheduleLabel(sa), status, next)
				}
				return strings.TrimSpace(b.String()), nil
			},
		},
		{
			Tool: Tool{
				Name:        "run_standing_now",
				Description: "Trigger a standing agent to run immediately (does not change its recurring schedule).",
				Parameters:  map[string]ToolParam{"name": {Type: "string", Description: "The standing agent's name."}},
				Required:    []string{"name"},
			},
			Handler: func(args map[string]any) (string, error) {
				name := strings.TrimSpace(oArgStr(args, "name"))
				if _, ok := GetStandingAgent(RootDB, owner, name); !ok {
					return "", fmt.Errorf("no standing agent named %q", name)
				}
				if err := RunStandingNow(RootDB, owner, name); err != nil {
					return "", err
				}
				return fmt.Sprintf("Triggered %q to run now. The result will appear in Activity shortly.", name), nil
			},
		},
		{
			Tool: Tool{
				Name:        "set_standing_paused",
				Description: "Pause or resume a standing agent's schedule.",
				Parameters: map[string]ToolParam{
					"name":   {Type: "string", Description: "The standing agent's name."},
					"paused": {Type: "boolean", Description: "true to pause, false to resume."},
				},
				Required: []string{"name", "paused"},
			},
			Handler: func(args map[string]any) (string, error) {
				name := strings.TrimSpace(oArgStr(args, "name"))
				sa, ok := GetStandingAgent(RootDB, owner, name)
				if !ok {
					return "", fmt.Errorf("no standing agent named %q", name)
				}
				sa.Paused = oArgBool(args, "paused")
				if sa.Paused {
					if sa.SchedulerID != "" {
						UnscheduleTask(sa.SchedulerID)
						sa.SchedulerID = ""
						sa.NextRun = time.Time{}
					}
					SaveStandingAgent(RootDB, sa)
					return fmt.Sprintf("Paused %q.", name), nil
				}
				SaveStandingAgent(RootDB, sa)
				if err := ScheduleStandingAgent(RootDB, sa); err != nil {
					return "", err
				}
				got, _ := GetStandingAgent(RootDB, owner, name)
				return fmt.Sprintf("Resumed %q. Next run: %s.", name, got.NextRun.Local().Format("Mon Jan 2 3:04 PM")), nil
			},
		},
		{
			Tool: Tool{
				Name:        "delete_standing_agent",
				Description: "Permanently delete a standing (scheduled) agent and cancel its schedule. Use this for a real removal; set_standing_paused only pauses.",
				Parameters:  map[string]ToolParam{"name": {Type: "string", Description: "The standing agent's name."}},
				Required:    []string{"name"},
			},
			Handler: func(args map[string]any) (string, error) {
				name := strings.TrimSpace(oArgStr(args, "name"))
				if _, ok := GetStandingAgent(RootDB, owner, name); !ok {
					return "", fmt.Errorf("no standing agent named %q", name)
				}
				DeleteStandingAgent(RootDB, owner, name)
				return fmt.Sprintf("Deleted standing agent %q and cancelled its schedule.", name), nil
			},
		},
		{
			Tool: Tool{
				Name:        "list_runs",
				Description: "List recent background/fleet runs (delegated tasks and standing-agent executions), status-level. These are NOT your own chat turns. Each line shows the run id for use with get_run.",
				Parameters: map[string]ToolParam{
					"agent": {Type: "string", Description: "Optional: restrict to one standing agent's name."},
					"limit": {Type: "number", Description: "Optional: max rows (default 15, max 50)."},
				},
			},
			Handler: func(args map[string]any) (string, error) {
				limit := oArgInt(args, "limit")
				if limit <= 0 || limit > 50 {
					limit = 15
				}
				runs := ListRuns(RootDB, owner, RunFilter{Agent: strings.TrimSpace(oArgStr(args, "agent")), Limit: limit})
				if len(runs) == 0 {
					return "No runs recorded yet.", nil
				}
				var b strings.Builder
				for _, rr := range runs {
					sum := rr.Summary
					if len(sum) > 120 {
						sum = sum[:120] + "…"
					}
					fmt.Fprintf(&b, "- [%s] %s %s (%s): %s\n",
						rr.ID, rr.Started.Local().Format("Jan 2 3:04 PM"), rr.Agent, rr.Status, sum)
				}
				return strings.TrimSpace(b.String()), nil
			},
		},
		{
			Tool: Tool{
				Name:        "get_run",
				Description: "Get the full detail and output of a DELEGATED or standing-agent RUN, identified by a run id from list_runs. A \"run\" is a background or fleet execution. This is NOT how you review your own chat replies, and NOT a way to check what you just said or sent in this conversation. Only call it with an id that came from list_runs; never construct one.",
				Parameters:  map[string]ToolParam{"id": {Type: "string", Description: "The run id."}},
				Required:    []string{"id"},
			},
			Handler: func(args map[string]any) (string, error) {
				rec, ok := GetRun(RootDB, owner, strings.TrimSpace(oArgStr(args, "id")))
				if !ok {
					// The id wasn't found — commonly because it was fabricated
					// (e.g. an attempt to "fetch" a cortex background note, which
					// is not a run). Run ids are opaque and only come from list_runs,
					// so point the model at the real ones instead of a bare error it
					// will just retry against.
					recent := ListRuns(RootDB, owner, RunFilter{Limit: 5})
					if len(recent) == 0 {
						return "", fmt.Errorf("no run with that id. Run ids come from list_runs (opaque, not constructed); there are no runs recorded yet")
					}
					var b strings.Builder
					b.WriteString("no run with that id. Run ids are opaque and come ONLY from list_runs — do not construct one. Recent run ids:\n")
					for _, rr := range recent {
						fmt.Fprintf(&b, "- %s (%s, %s)\n", rr.ID, rr.Agent, rr.Status)
					}
					return "", fmt.Errorf("%s", strings.TrimSpace(b.String()))
				}
				return fmt.Sprintf("Run %s\nagent: %s\nstatus: %s\ntrigger: %s\nbrief: %s\nsummary: %s\noutput:\n%s",
					rec.ID, rec.Agent, rec.Status, rec.Trigger, rec.Brief, rec.Summary, rec.Raw), nil
			},
		},
		{
			Tool: Tool{
				Name:        "create_event_monitor",
				Description: "Set up a monitor that WAKES you when something happens (vs a standing agent, which RUNS on a clock). Pick the CHEAPEST kind that detects the change — deterministic beats an LLM checker: \"webhook\" mints a secret URL an external system POSTs to (push, no polling); \"http_poll\" fetches a URL, extracts a value, and wakes you when it crosses a threshold (no LLM — best for numeric/value conditions); \"watch\" invokes a TOOL each interval, hashes its output, and wakes you ONLY when it changes (no LLM until it does — best for \"tell me when X changes\", e.g. a chat via read_chat); \"poll\" runs an LLM checker agent every interval (MOST expensive — reserve for FUZZY conditions a value or hash can't capture). On wake you react in this thread (report / delegate).",
				Parameters: map[string]ToolParam{
					"name":             {Type: "string", Description: "Short unique name for this monitor, e.g. \"nvda-below\" or \"ts-join\"."},
					"kind":             {Type: "string", Description: "\"webhook\", \"http_poll\", \"watch\", or \"poll\" — prefer the cheapest that fits (see the tool description)."},
					"wake_brief":       {Type: "string", Description: "What you should do when it fires (guides your reaction). Only used for notify=\"channel\"."},
					"notify":           {Type: "string", Enum: []string{"channel", "direct", "text"}, Description: "How the user is alerted when it fires. \"channel\" (default): wake here in the thread so you can react/summarize (uses an LLM). \"direct\": post the change verbatim into the channel thread with NO LLM (it just shows up here + lights the unread dot). \"text\": text the owner's phone with the change, no LLM. ASK the user which they want when setting a monitor up."},
					"deliver_to":       {Type: "string", Description: "Optional: a chat_id from list_chats (e.g. \"any;+;chat872212368359368118\"). When set, the formatted alert is posted DIRECTLY to THAT conversation with NO LLM, instead of waking you in this thread — use it to route a watch/http_poll alert straight to a group chat or other channel. Setting it forces notify=\"direct\" to that chat. Omit to alert in this thread per notify."},
					"interval_seconds": {Type: "number", Description: "http_poll/watch/poll: how often to check, in seconds (minimum 30; 900 = every 15 min, 3600 = hourly)."},
					"tool_name":        {Type: "string", Description: "watch only: the tool invoked each interval; its output is hashed and you're woken ONLY when it changes. Use an existing tool that returns the thing to watch (e.g. read_chat for a chat). No LLM runs between changes — the cheapest detection."},
					"tool_args":        {Type: "object", Description: "watch only: arguments passed to tool_name every invocation, e.g. {\"chat_id\":\"any;+;chat123\",\"limit\":10}."},
					"format_script":    {Type: "string", Description: "watch only, optional: sandboxed python that shapes the alert. It receives {\"prior\":...,\"current\":...} JSON on stdin (the previous and current tool output) and prints the notification text to stdout. Empty stdout means \"this change isn't worth alerting\" (suppressed). No network, no LLM. Omit to use the built-in diff summary. Use this to format exactly the notification you want (e.g. parse a client list and print only \"X joined\" / \"X left\")."},
					"check_agent":      {Type: "string", Description: "poll only: name/id of an existing agent that checks the condition each interval."},
					"check":            {Type: "string", Description: "poll only: the question/brief given to the checker. Tell it to answer with the match string when the event has happened."},
					"match_contains":   {Type: "string", Description: "poll only: fire when the checker's answer contains this (case-insensitive). Default \"YES\"."},
					"url":              {Type: "string", Description: "http_poll only: URL fetched each interval (e.g. a finance JSON API)."},
					"json_path":        {Type: "string", Description: "http_poll: dotted path into the JSON response, array indices included, e.g. \"quoteResponse.result.0.regularMarketPrice\". Omit json_path and regex to compare the whole body."},
					"regex":            {Type: "string", Description: "http_poll: alternative extraction — first capture group of this regex against the body."},
					"compare_op":       {Type: "string", Description: "http_poll: one of < > <= >= == != contains. Fire when extracted_value <op> threshold is true."},
					"threshold":        {Type: "string", Description: "http_poll: the value compared against (a number for < > <= >=)."},
				},
				Required: []string{"name", "kind"},
			},
			Handler: func(args map[string]any) (string, error) {
				name := strings.TrimSpace(oArgStr(args, "name"))
				kind := strings.ToLower(strings.TrimSpace(oArgStr(args, "kind")))
				if name == "" {
					return "", fmt.Errorf("name is required")
				}
				if kind != EventKindWebhook && kind != EventKindPoll && kind != EventKindHTTP && kind != EventKindWatch {
					return "", fmt.Errorf("kind must be %q, %q, %q, or %q", EventKindWebhook, EventKindHTTP, EventKindWatch, EventKindPoll)
				}
				if _, exists := GetEventMonitor(RootDB, owner, name); exists {
					return "", fmt.Errorf("a monitor named %q already exists", name)
				}
				notify := strings.ToLower(strings.TrimSpace(oArgStr(args, "notify")))
				switch notify {
				case EventNotifyText, EventNotifyDirect:
					// honored as-is
				default:
					notify = EventNotifyChannel
				}
				// deliver_to routes the formatted alert straight to a specific chat
				// (a group / channel) with no LLM — the DeliverChatID + notify=direct
				// path that already exists in operator_wake. Setting it forces direct
				// delivery to that chat (channel/text wouldn't make sense here).
				deliverTo := strings.TrimSpace(oArgStr(args, "deliver_to"))
				if deliverTo != "" {
					notify = EventNotifyDirect
				}
				m := EventMonitor{
					Name: name, Owner: owner, Kind: kind, Notify: notify,
					DeliverChatID: deliverTo,
					// Wake the agent that created this monitor, IN the session it
					// was created in, so the event lands back where the user set
					// it up (not a hardcoded default thread). WakeSession falls
					// back to the agent's channel home thread when unknown.
					WakeAgent: agentID,
					WakeSession: func() string {
						if sess != nil {
							return sess.ChatSessionID
						}
						return ""
					}(),
					WakeBrief: strings.TrimSpace(oArgStr(args, "wake_brief")), Created: time.Now(),
				}
				if kind == EventKindWebhook {
					m.Token = NewEventToken()
					SaveEventMonitor(RootDB, m)
					return fmt.Sprintf("Webhook monitor %q created. Have the external system POST JSON {\"summary\":\"...\"} to:\n  <your gohort base URL>/orchestrate/api/operator/event/%s\nEach POST wakes me in this thread.", name, m.Token), nil
				}
				if kind == EventKindHTTP {
					m.URL = strings.TrimSpace(oArgStr(args, "url"))
					m.JSONPath = strings.TrimSpace(oArgStr(args, "json_path"))
					m.Regex = strings.TrimSpace(oArgStr(args, "regex"))
					m.CompareOp = strings.TrimSpace(oArgStr(args, "compare_op"))
					m.Threshold = strings.TrimSpace(oArgStr(args, "threshold"))
					m.IntervalSeconds = oArgInt(args, "interval_seconds")
					if m.IntervalSeconds <= 0 {
						m.IntervalSeconds = 900
					}
					if m.URL == "" || m.CompareOp == "" || m.Threshold == "" {
						return "", fmt.Errorf("http_poll monitors need url, compare_op, and threshold")
					}
					switch m.CompareOp {
					case "<", ">", "<=", ">=", "==", "!=", "contains":
					default:
						return "", fmt.Errorf("compare_op must be one of < > <= >= == != contains")
					}
					extractDesc := "the response body"
					if m.JSONPath != "" {
						extractDesc = "json_path " + m.JSONPath
					} else if m.Regex != "" {
						extractDesc = "a regex match"
					}
					SaveEventMonitor(RootDB, m)
					if err := ScheduleEventMonitor(RootDB, m); err != nil {
						return "", fmt.Errorf("saved but scheduling failed: %w", err)
					}
					got, _ := GetEventMonitor(RootDB, owner, name)
					return fmt.Sprintf("HTTP monitor %q created: every %ds I fetch %s, read %s, and wake you when the value %s %s. Fires once on the crossing (and re-arms after it recovers). Next check: %s.",
						name, got.IntervalSeconds, m.URL, extractDesc, m.CompareOp, m.Threshold,
						got.NextCheck.Local().Format("Mon Jan 2 3:04 PM")) + dupMonitorWarning(m), nil
				}
				if kind == EventKindWatch {
					m.ToolName = strings.TrimSpace(oArgStr(args, "tool_name"))
					if ta, ok := args["tool_args"].(map[string]any); ok {
						m.ToolArgs = ta
					}
					m.FormatScript = oArgStr(args, "format_script")
					m.IntervalSeconds = oArgInt(args, "interval_seconds")
					if m.IntervalSeconds <= 0 {
						m.IntervalSeconds = 60
					}
					if m.ToolName == "" {
						return "", fmt.Errorf("watch monitors need tool_name (the tool whose output is hashed)")
					}
					// Seed the change baseline now from a known-good probe, so the
					// first poll detects a REAL change instead of firing on the
					// initial content.
					if body, perr := InvokeWatchTool(owner, m.ToolName, m.ToolArgs); perr == nil {
						m.LastHash = HashWatcherBody(body)
					}
					SaveEventMonitor(RootDB, m)
					if err := ScheduleEventMonitor(RootDB, m); err != nil {
						return "", fmt.Errorf("saved but scheduling failed: %w", err)
					}
					got, _ := GetEventMonitor(RootDB, owner, name)
					if m.DeliverChatID != "" {
						return fmt.Sprintf("Watch monitor %q created: every %ds I run %s and, when its output changes, post the formatted alert DIRECTLY to chat %s — no LLM, it does NOT come back to this thread. Next check: %s.",
							name, got.IntervalSeconds, m.ToolName, m.DeliverChatID, got.NextCheck.Local().Format("Mon Jan 2 3:04 PM")) + dupMonitorWarning(m), nil
					}
					return fmt.Sprintf("Watch monitor %q created: every %ds I run %s and wake you ONLY when its output changes — no LLM runs in between. Next check: %s.",
						name, got.IntervalSeconds, m.ToolName, got.NextCheck.Local().Format("Mon Jan 2 3:04 PM")) + dupMonitorWarning(m), nil
				}
				wantAgent := strings.TrimSpace(oArgStr(args, "check_agent"))
				m.Check = strings.TrimSpace(oArgStr(args, "check"))
				m.MatchContains = strings.TrimSpace(oArgStr(args, "match_contains"))
				m.IntervalSeconds = oArgInt(args, "interval_seconds")
				if wantAgent == "" || m.Check == "" {
					return "", fmt.Errorf("poll monitors need check_agent and check")
				}
				// Resolve the checker to a REAL agent. The LLM may pass a display
				// name or its own conversational nickname that isn't an actual
				// agent id; resolve by id, then by name, and finally fall back to
				// the channel agent creating the monitor (which exists and can
				// self-check with its own tools) so the monitor never saves broken.
				m.CheckAgent = resolveCheckAgent(sess, owner, wantAgent, agentID)
				SaveEventMonitor(RootDB, m)
				if err := ScheduleEventMonitor(RootDB, m); err != nil {
					return "", fmt.Errorf("saved but scheduling failed: %w", err)
				}
				match := m.MatchContains
				if match == "" {
					match = "YES"
				}
				got, _ := GetEventMonitor(RootDB, owner, name)
				return fmt.Sprintf("Poll monitor %q created: every %ds, agent %q is asked %q; I wake when the answer contains %q. Next check: %s.",
					name, got.IntervalSeconds, m.CheckAgent, m.Check, match, got.NextCheck.Local().Format("Mon Jan 2 3:04 PM")) + dupMonitorWarning(m), nil
			},
		},
		// The phantom-named read tools were removed — superseded by the
		// channel-scoped chat tools (channel_tools.go) which read the live
		// Bridges threads. A Fleet agent reaches them by holding a whole-service
		// channel.
		{
			Tool: Tool{
				Name:        "notify_me",
				Description: "Send a text to the USER'S OWN phone (a notification to yourself / the owner). No approval needed — it only goes to the owner. Use this to push an alert or a monitor result to the user's phone. To include an image/file, pass its workspace path in `attachments`.",
				Parameters: map[string]ToolParam{
					"text":        {Type: "string", Description: "The message to send to the owner."},
					"attachments": {Type: "array", Items: &ToolParam{Type: "string"}, Description: attachmentsParamDesc},
				},
				Required: []string{"text"},
			},
			Handler: func(args map[string]any) (string, error) {
				link, ok := ActiveMessagingLink()
				if !ok {
					return "", fmt.Errorf("the messaging bridge is not available")
				}
				text := strings.TrimSpace(oArgStr(args, "text"))
				if text == "" {
					return "", fmt.Errorf("text is required")
				}
				self, ok := link.OwnerHandle(owner)
				if !ok {
					return "", fmt.Errorf("no owner phone is configured (set the owner's handle in the messaging bridge settings)")
				}
				images := messageImages(sess, args, text)
					// DeliverMessage (not SendToHandle) so attachments ride along;
					// persona is inactive for the owner's own chat, so the text
					// is sent verbatim. Empty chatID resolves the owner's thread.
					if _, err := operatorDeliverMessage(owner, "", self, text, images); err != nil {
					return "", err
				}
				// The owner's channel (their phone) must see what was sent — record
				// into its cortex/session so when the owner replies, the agent knows
				// what it just told them (fixes "I sent you a joke but have no idea
				// what it was" — the reply lands over the bridge in a different
				// session than this notify_me). Recorded by AGENT ID, not by
				// matching the owner's handle to a channel address (those rarely
				// match: SelfHandle is a phone, the channel address may be an email
				// or chat-id form, so channelForChat misses and the cortex is
				// skipped). notify_me IS this agent notifying the owner, so it
				// belongs in this agent's cortex. No-op if the agent has no cortex.
				appendCortexObs(sess.DB, controllerAgentID, "Sent to you", cortexKindMessage, text)
				if len(images) > 0 {
						return fmt.Sprintf("Sent to your phone with %d attachment(s).", len(images)), nil
					}
					return "Sent to your phone.", nil
			},
		},
		{
			Tool: Tool{
				Name:        "message_contact",
				Description: "Send an iMessage to a CONTACT or a GROUP (anyone other than the owner). Set `to` to the recipient as shown by list_chats — a contact/group NAME (e.g. \"WiWee\"), a handle (phone/email), or a chat_id. Any of them resolve to the right conversation, group chats included; you don't need to track the opaque chat_id — the name works. To send an image/file, pass its workspace path in `attachments`. Your exact words are sent verbatim. Contacting real people is consequential, so it queues for the user's approval (unless they pre-authorized that recipient via 'Always allow', or you're replying to someone who just messaged you), then sends once approved.",
				Parameters: map[string]ToolParam{
					"to":          {Type: "string", Description: "Recipient as shown by list_chats: a contact/group name, a handle (phone/email), or a chat_id. Required — never omit it."},
					"text":        {Type: "string", Description: "The message to send. Do NOT type delivery markers like [ATTACH: ...] into this text; that is a different surface's convention and is stripped before sending."},
					"attachments": {Type: "array", Items: &ToolParam{Type: "string"}, Description: attachmentsParamDesc},
				},
				Required: []string{"to", "text"},
			},
			Handler: func(args map[string]any) (string, error) {
				to := strings.TrimSpace(oArgStr(args, "to"))
				text := strings.TrimSpace(oArgStr(args, "text"))
				if to == "" || text == "" {
					return "", fmt.Errorf("to and text are required")
				}
				link, ok := ActiveMessagingLink()
				if !ok {
					return "", fmt.Errorf("the messaging bridge is not available")
				}
				rec, ok := link.ResolveRecipient(owner, to)
				if !ok {
					return "", fmt.Errorf("no conversation matches %q — set `to` to a contact/group name, handle, or chat_id exactly as shown by list_chats", to)
				}
				recip := operatorRecipientKey(rec.ChatID, rec.Handle)
				label := operatorRecipientLabel(rec)
				images := messageImages(sess, args, text)
				if IsContactBlocked(RootDB, owner, recip) {
					return fmt.Sprintf("Messaging %s is blocked in the user's permission settings — not sent.", label), nil
				}
				// Replying to the conversation that just messaged us is in-thread,
				// not a proactive reach-out — deliver without the approval queue.
				if isReplyToActiveInbound(sess, recip) {
					if _, err := operatorDeliverMessage(owner, rec.ChatID, rec.Handle, text, images); err != nil {
						return "", err
					}
					return fmt.Sprintf("Sent to %s (replying in-thread).", label), nil
				}
				// Pre-authorized recipient: send immediately, skip the queue.
				if IsContactPreAuthorized(RootDB, owner, recip) {
					if _, err := operatorDeliverMessage(owner, rec.ChatID, rec.Handle, text, images); err != nil {
						return "", err
					}
					// If the target is a bound channel, make its agent see the post
					// (channel session + cortex) so it can field follow-ups.
					recordChannelPost(sess.DB, owner, rec.ChatID, rec.Handle, text)
					return fmt.Sprintf("Sent to %s (you've pre-authorized this recipient).", label), nil
				}
				a := SaveAuthorization(RootDB, Authorization{
					Owner: owner, Action: "send_message", ChatID: rec.ChatID, Handle: rec.Handle, Text: text, Images: images,
				})
				return fmt.Sprintf("Queued a message to %s for the user's approval — it's in the Authorizations pane (id %s) and sends once approved.", label, a.ID), nil
			},
		},
		{
			Tool: Tool{
				Name:        "list_event_monitors",
				Description: "List the user's event monitors (webhook + poll) with their kind, schedule, paused state, and when each last fired.",
			},
			Handler: func(args map[string]any) (string, error) {
				// Scope to THIS agent's monitors (WakeAgent set on create), not
				// every monitor the owner has across all their agents.
				var ms []EventMonitor
				for _, m := range ListEventMonitors(RootDB, owner) {
					if m.WakeAgent == controllerAgentID {
						ms = append(ms, m)
					}
				}
				if len(ms) == 0 {
					return "No event monitors are set up.", nil
				}
				var b strings.Builder
				fmt.Fprintf(&b, "%d event monitor(s):\n", len(ms))
				for _, m := range ms {
					state := "active"
					if m.Paused {
						state = "paused"
					}
					fmt.Fprintf(&b, "- %s [%s, %s]", m.Name, m.Kind, state)
					switch m.Kind {
					case EventKindPoll:
						fmt.Fprintf(&b, ": every %ds via %q", m.IntervalSeconds, m.CheckAgent)
					case EventKindHTTP:
						fmt.Fprintf(&b, ": every %ds fetch %s, value %s %s", m.IntervalSeconds, m.URL, m.CompareOp, m.Threshold)
					case EventKindWebhook:
						fmt.Fprintf(&b, ": POST .../orchestrate/api/operator/event/%s", m.Token)
					}
					if !m.LastFired.IsZero() {
						fmt.Fprintf(&b, "; last fired %s", m.LastFired.Local().Format("Jan 2 3:04 PM"))
					}
					b.WriteString("\n")
				}
				return strings.TrimSpace(b.String()), nil
			},
		},
		{
			Tool: Tool{
				Name:        "delete_event_monitor",
				Description: "Delete an event monitor by name (stops its polling / invalidates its webhook).",
				Parameters:  map[string]ToolParam{"name": {Type: "string", Description: "The monitor's name."}},
				Required:    []string{"name"},
			},
			Handler: func(args map[string]any) (string, error) {
				name := strings.TrimSpace(oArgStr(args, "name"))
				// Scope to THIS agent — don't let one agent delete another's monitor.
				if m, ok := GetEventMonitor(RootDB, owner, name); !ok || m.WakeAgent != controllerAgentID {
					return "", fmt.Errorf("no event monitor named %q", name)
				}
				DeleteEventMonitor(RootDB, owner, name)
				return fmt.Sprintf("Deleted event monitor %q.", name), nil
			},
		},
		// bridge — the friendly "connect an authenticated API to a schedule"
		// front-end over a watch monitor + call_<credential>. Same family as
		// the event-monitor tools above; defaults wake_agent to THIS agent so a
		// Fleet/Cortex agent can self-monitor a service by leaving it blank.
		ChatToolToAgentToolDefWithSession(bridgeDefTool(controllerAgentID), sess),
	}
}

// dropToolsByName removes the named tools from a parallel (tools, names) pair.
// Used to keep the generic interval scheduler ("recurring") off the Operator —
// it schedules through the fleet (create_standing_agent) instead.
func dropToolsByName(tools []AgentToolDef, names []string, drop ...string) ([]AgentToolDef, []string) {
	dropSet := map[string]bool{}
	for _, d := range drop {
		dropSet[d] = true
	}
	outT := make([]AgentToolDef, 0, len(tools))
	for _, td := range tools {
		if !dropSet[td.Tool.Name] {
			outT = append(outT, td)
		}
	}
	outN := make([]string, 0, len(names))
	for _, n := range names {
		if !dropSet[n] {
			outN = append(outN, n)
		}
	}
	return outT, outN
}

// --- arg helpers (o-prefixed to avoid collisions in this package) ------------

func oArgStr(args map[string]any, k string) string {
	if v, ok := args[k].(string); ok {
		return v
	}
	return ""
}

func oArgBool(args map[string]any, k string) bool {
	switch v := args[k].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	}
	return false
}

func oArgInt(args map[string]any, k string) int {
	switch v := args[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}
