// watch — lets the phantom assistant set up a background WATCHER for the bridge
// owner, either on THIS conversation or on any tool the owner has (e.g. a
// tool_def-authored api tool like ts3_list_clients).
//
// It creates a normal event monitor (kind=watch): the engine polls the captured
// tool on an interval and detects changes by hash (no LLM until something
// actually changes). Event monitors are stored owner-scoped in the shared
// store, so the watcher also appears in — and is managed from — the owner's
// Agency "Event monitors" console. Delivery is chosen via notify (text the
// owner's phone / post into the channel / wake the channel agent), and an
// optional format_script shapes the alert. So an authored-in-phantom tool can be
// wrapped in a watcher right here: tool_def → call it to test → watch it.

package phantom

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func (T *Phantom) createWatcherToolDef(chatID, handle string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "create_watcher",
			Description: "Set up a background watcher for the owner so they're alerted when something changes — without you having to keep re-checking. By default it watches THIS conversation (new messages); pass tool_name (+ tool_args) to instead watch any tool the owner has, e.g. a tool_def-authored API tool like ts3_list_clients (the classic 'tell me when someone joins/leaves' watcher). It polls on an interval and only notifies on a real change (hash-based, no LLM in between). The watcher is owner-scoped and also shows up in the owner's Agency console for management. ASK the owner how they want to be alerted (notify) before creating it.",
			Parameters: map[string]ToolParam{
				"tool_name":        {Type: "string", Description: "Optional: the owner's tool to watch (its output is hashed each interval). Omit to watch THIS conversation. Use an existing tool you've proven works (e.g. one you just authored with tool_def)."},
				"tool_args":        {Type: "object", Description: "Optional: arguments passed to tool_name every check, e.g. {\"url\":\"/1/clientlist\"}. Ignored when watching this conversation."},
				"interval_seconds": {Type: "number", Description: "How often to check, in seconds (minimum 30). Default 60."},
				"notify":           {Type: "string", Description: "How to alert when it fires (no LLM). One or more of: \"direct\" (default — post back into THIS conversation, e.g. the group you set it up in), \"text\" (text the owner's phone), \"channel\" (wake the channel agent to react). Comma-separate for multiple destinations, e.g. \"direct,text\" to post in the chat AND text the owner."},
				"format_script":    {Type: "string", Description: "Optional: sandboxed python to format the alert (e.g. print \"SouthPawn joined\" instead of the raw diff). On stdin you get {\"prior\":...,\"current\":...,\"prior_status\":...,\"current_status\":...}: prior/current are the tool's body with the \"HTTP <code>\" status line split off so json.loads(current) works directly, and *_status hold that status line (e.g. \"HTTP 200 OK\") if you want to react to errors. Print the alert to stdout (one line per change). If it ERRORS, the built-in diff is used so the change still fires; if it deliberately prints NOTHING, that change is skipped (no alert) — use that to filter out changes you don't care about. No network, no LLM. Omit it to just use the built-in diff."},
				"name":             {Type: "string", Description: "Optional short unique name for the watcher. Auto-generated when omitted."},
			},
		},
		Handler: func(args map[string]any) (string, error) {
			owner := phantomToolOwner(T.DB)
			if owner == "" {
				return "", fmt.Errorf("no bridge owner is configured, so I can't create an owner-scoped watcher")
			}
			// notify can list multiple destinations (comma-separated), e.g.
			// "direct,text" to post in the chat AND text the owner. Defaults to
			// direct (post back into this conversation; origin-aware via
			// DeliverChatID below).
			notify := normalizeNotify(stringArgPhantom(args, "notify"))
			interval := 0
			if f, ok := args["interval_seconds"].(float64); ok {
				interval = int(f)
			}
			if interval <= 0 {
				interval = 60
			}
			name, _ := args["name"].(string)
			name = strings.TrimSpace(name)
			if name == "" {
				name = "watch-" + NewEventToken()[:6]
			}
			// Recreate = replace. If a watcher with this name exists, drop it and
			// rebuild from the new args, so "recreate the X watcher" actually
			// updates it (e.g. picks up a new notify target) instead of erroring
			// and silently keeping the stale record.
			replaced := false
			if _, exists := GetEventMonitor(RootDB, owner, name); exists {
				DeleteEventMonitor(RootDB, owner, name)
				replaced = true
			}
			// Default: watch this conversation. Otherwise wrap the named tool.
			toolName, _ := args["tool_name"].(string)
			toolName = strings.TrimSpace(toolName)
			var toolArgs map[string]any
			if toolName == "" {
				toolName = "read_phantom_chat"
				toolArgs = map[string]any{"chat_id": chatID, "limit": float64(20)}
			} else if ta, ok := args["tool_args"].(map[string]any); ok {
				toolArgs = ta
			}
			// Don't create a second watcher on a chat that's already watched.
			if toolName == "read_phantom_chat" {
				for _, ex := range ListEventMonitors(RootDB, owner) {
					if ex.Kind == EventKindWatch && ex.ToolName == "read_phantom_chat" {
						if cid, _ := ex.ToolArgs["chat_id"].(string); cid == chatID {
							return fmt.Sprintf("This chat is already being watched (monitor %q). Use list_watchers to review it or cancel_watcher to remove it.", ex.Name), nil
						}
					}
				}
			}
			fmtScript, _ := args["format_script"].(string)
			fmtScript = strings.TrimSpace(fmtScript)
			m := EventMonitor{
				Name: name, Owner: owner, Kind: EventKindWatch, Notify: notify,
				ToolName:        toolName,
				ToolArgs:        toolArgs,
				FormatScript:    fmtScript,
				IntervalSeconds: interval,
				DeliverChatID:   chatID, // notify=direct posts the alert back here
				Created:         time.Now(),
			}
			// Seed the change baseline now so the first poll detects a REAL
			// change instead of firing on the existing contents.
			seedBody := ""
			if body, err := InvokeWatchTool(owner, m.ToolName, m.ToolArgs); err == nil {
				m.LastHash = HashWatcherBody(body)
				seedBody = body
			}
			SaveEventMonitor(RootDB, m)
			if err := ScheduleEventMonitor(RootDB, m); err != nil {
				return "", fmt.Errorf("saved but scheduling failed: %w", err)
			}
			watched := "this chat"
			if toolName != "read_phantom_chat" {
				watched = toolName
			}
			verb := "Watching"
			if replaced {
				verb = "Replaced the existing watcher. Now watching"
			}
			msg := fmt.Sprintf("%s %s every %ds as monitor %q — I'll alert (%s) only when its output changes. It's also in the owner's Agency Event monitors view to pause or delete.", verb, watched, interval, name, notify)
			// Test the format_script the same way the watcher will, against a
			// sample change (nothing -> current state), so the assistant sees
			// whether it actually emits an alert BEFORE signing off. An
			// empty-output script silently suppresses every wake.
			msg += testFormatScript(fmtScript, seedBody)
			return msg, nil
		},
	}
}

// testFormatScript runs a watch format_script the same way the engine will, against
// a sample change (nothing -> current state), and returns a human-readable note on
// whether it would actually alert. Returns "" when there's no script. The point is
// to surface a broken or always-empty (alert-suppressing) script at CREATION time
// so the assistant can fix it before signing off, instead of the watcher silently
// never firing.
func testFormatScript(fmtScript, current string) string {
	if fmtScript == "" {
		return ""
	}
	curStatus, curBody := SplitHTTPStatus(current)
	payload, _ := json.Marshal(map[string]string{"prior": "", "current": curBody, "prior_status": "", "current_status": curStatus})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	res := RunSandboxedScript(ctx, "python3", fmtScript, string(payload))
	cancel()
	switch {
	case res.Err != nil:
		return fmt.Sprintf(" NOTE: I tested the format_script and it errored on a sample change (%v). On a real change it falls back to the built-in diff, so it WILL still fire, but fix or omit it. stderr: %s", res.Err, truncateForNote(res.Stderr))
	case strings.TrimSpace(res.Stdout) == "":
		return " NOTE: I tested the format_script and it produced no output on a sample change. Printing nothing is the \"skip this change\" signal, so this watcher would stay SILENT on that change. Make sure it prints alert text for the changes you care about, or omit it to use the built-in diff (which always fires)."
	default:
		return fmt.Sprintf(" I tested the format_script: a sample change would alert with %q.", truncateForNote(strings.TrimSpace(res.Stdout)))
	}
}

// normalizeNotify parses a comma-separated notify list, keeping only valid,
// de-duplicated modes (direct/text/channel) in order, and defaults to "direct"
// (post back into this conversation) when none are valid.
func normalizeNotify(s string) string {
	var out []string
	seen := map[string]bool{}
	for _, m := range strings.Split(strings.ToLower(s), ",") {
		m = strings.TrimSpace(m)
		if (m == EventNotifyDirect || m == EventNotifyText || m == EventNotifyChannel) && !seen[m] {
			out = append(out, m)
			seen[m] = true
		}
	}
	if len(out) == 0 {
		return EventNotifyDirect
	}
	return strings.Join(out, ",")
}

func truncateForNote(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 200 {
		return string([]rune(s)[:200]) + "…"
	}
	return s
}
