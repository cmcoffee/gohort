// The "bridge" authoring tool — Builder's friendly front-end for wiring an
// authenticated API to a schedule so an agent gets fed when an external
// service changes. It is pure composition over primitives that already exist:
//
//   - a SecureAPI credential (drafted via draft_api_credential /
//     draft_oauth_credential, the admin fills the secret) IS the "minted API";
//   - a "watch"-kind EventMonitor (core/event_monitor.go) IS the scheduler +
//     change-detector + agent-wake;
//   - the call_<credential> watch-tool route (core/watcher.go) dispatches the
//     API call through SecureAPI at fire time, so there's no session-scoped or
//     pending-approval tool to resolve in the background.
//
// A bridge is therefore just a watch monitor whose captured tool is
// call_<credential> with the URL in its args: every interval the framework
// calls the API, hashes the response, and wakes the target agent ONLY when the
// response changes. No new core machinery; the bridge tool validates the
// credential, seeds the change-baseline from a probe, and saves + schedules.
//
// Builder-exclusive (added to builderAuthoringTools). The LLM never handles the
// credential secret — it references the credential by name; the admin owns the
// secret in Admin > APIs.

package orchestrate

import (
	"fmt"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// bridgeToolName is the catalog name + the call_<cred> watch-tool prefix used
// to recognize bridge-created monitors in list/get/delete.
const bridgeCredToolPrefix = "call_"

// bridgeDefTool builds the grouped "bridge" authoring tool. Mirrors skill_def /
// pipeline: one tool, action-discriminated, single-fire-per-batch (you author
// one bridge at a time).
//
// defaultWakeAgent is the agent woken when "wake_agent" is omitted. Builder
// passes "" (it authors bridges FOR other agents, so the target is required);
// an orchestrator/Fleet agent passes its own id so it can self-monitor by
// leaving wake_agent blank.
func bridgeDefTool(defaultWakeAgent string) ChatTool {
	gt := NewGroupedTool("bridge",
		"Connect an authenticated API to an agent on a schedule: poll the API every N minutes and WAKE the chosen agent ONLY when the response changes (no LLM runs between changes).")
	gt.SetSingleFirePerBatch(true)
	gt.SetHelpPreamble(strings.TrimSpace(`
A bridge marries a registered API credential to a schedule. It does NOT handle
secrets: the credential must already exist (draft it first with
draft_api_credential or draft_oauth_credential; the admin pastes the secret and
enables it in Admin > APIs). The bridge references the credential by name.

A bridge's SOURCE (how the watched info is generated) is one of:
  - source_kind="tool"     — run a named, TESTABLE api/toolbox tool each interval
                             (preferred: a reusable, verifiable artifact). Author
                             + verify it with tool_def (action="test") first, then
                             point the bridge at it with tool= + tool_args=.
  - source_kind="url"       — a raw call through a credential (credential= + url=).
                             The simple path; the default when source_kind is omitted.
  - source_kind="pipeline"  — a declarative pipeline (not wired yet).

VERIFY-BEFORE-STANDING (hard gate): the source is EXERCISED once at create time
and the bridge is REFUSED if it doesn't return a 2xx — a standing poll on a
broken source (stale key, wrong URL) would 401/404-loop in the background where
nobody sees it. Fix the source, then create.

How it fires: every interval the framework runs the source, hashes the output,
and wakes the target agent ONLY when the output differs from the last poll — the
cheap "tell me when X changes" path, zero LLM cost until something changes. The
source must return STABLE output for identical inputs (a value that always
changes fires every cycle). The first poll records the baseline (no wake).

Typical flow when wiring a new service:
  1. draft_api_credential (or draft_oauth_credential) for the service.
  2. (admin enables it + pastes the secret in Admin > APIs)
  3. Either point the bridge at a tested tool (source_kind="tool", tool=...), or
     use source_kind="url" with credential= + url=. Plus wake_agent=,
     interval_minutes=, wake_brief="what changed and what to do about it".`))

	gt.AddAction("create", &GroupedToolAction{
		Description: "Create a bridge: poll credential's url every interval_minutes; wake wake_agent on change.",
		Params: map[string]ToolParam{
			"name":             {Type: "string", Description: "Unique short name for this bridge (per user)."},
			"source_kind":      {Type: "string", Description: "How the watched info is generated: \"tool\" (a named, TESTABLE api/toolbox tool — preferred: it's a reusable, verifiable artifact), \"url\" (a raw call through a credential — the simple path), or \"pipeline\" (a declarative pipeline for multi-step/aggregate sources). Defaults to \"url\" when omitted (back-compat). A tool/pipeline source is exercised before the bridge is created; the url source is hard-probed. Whatever the kind, the bridge fires only when the source's output CHANGES, so the source must return STABLE output for identical inputs (a value that always changes — a timestamp/nonce — fires every cycle)."},
			"tool":             {Type: "string", Description: "(source_kind=tool) Name of an existing api/toolbox tool to run each interval; its output is hashed and the bridge wakes on change. Author + test it first with tool_def (action=\"test\"). Pass its inputs via tool_args."},
			"tool_args":        {Type: "object", Description: "(source_kind=tool) Arguments passed to `tool` on every invocation, as a {name: value} object. Keep them CONSTANT — the bridge sends the same args each cycle and watches for the response changing."},
			"credential":       {Type: "string", Description: "(source_kind=url) Name of a registered SecureAPI credential. The bridge calls the API through it for auth + URL allow-list. Draft it first if it doesn't exist yet."},
			"url":              {Type: "string", Description: "(source_kind=url) The URL to poll each interval. PREFER a path-only URL (e.g. \"/api/v1/notifications\") — it resolves against the credential's Base URL at poll time, so the host can never disagree with the admin's config. A full https:// URL also works but must match the credential's Base URL host EXACTLY (www vs bare host count as different)."},
			"wake_agent":       {Type: "string", Description: "Name or id of the agent to wake when the response changes; its OWN home thread receives the change-alert. Omit to wake the agent creating the bridge (self-monitoring). Ignored when `channel` is set (the channel's bound agent is used instead)."},
			"channel":          {Type: "string", Description: "(optional) Deliver the change INTO this channel instead of the agent's home thread — name or id of one of the user's channels. The channel's bound agent reacts in the channel's conversation, and if the channel has a live transport (iMessage/etc) the reaction flows out it. This is the \"source → channel\" shape. When set, wake_agent is derived from the channel."},
			"interval_minutes": {Type: "number", Description: "How often to poll, in minutes (minimum 1; 15 = every 15 min, 60 = hourly)."},
			"wake_brief":       {Type: "string", Description: "Guidance handed to the woken agent on each change — what the data means and what to do about it."},
			"method":           {Type: "string", Description: "(source_kind=url, optional) HTTP method; defaults to GET."},
			"body":             {Type: "string", Description: "(source_kind=url, optional) request body for POST/PUT etc."},
		},
		Required: []string{"name", "interval_minutes"},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			return bridgeCreate(args, sess, defaultWakeAgent)
		},
	})
	gt.AddAction("list", &GroupedToolAction{
		Description: "List the bridges you've created (credential, url, interval, target agent, last fire).",
		Handler:     bridgeList,
	})
	gt.AddAction("get", &GroupedToolAction{
		Description: "Show one bridge's full configuration.",
		Params:      map[string]ToolParam{"name": {Type: "string", Description: "The bridge name."}},
		Required:    []string{"name"},
		Handler:     bridgeGet,
	})
	gt.AddAction("update", &GroupedToolAction{
		Description: "Re-target an EXISTING bridge's destination: hook it to a channel (its bound agent then reacts in that conversation), or detach it back to waking an agent's own thread. Only the destination is editable here; to change url/interval/credential, delete and recreate.",
		Params: map[string]ToolParam{
			"name":    {Type: "string", Description: "The bridge to update."},
			"channel": {Type: "string", Description: "Channel name or id to deliver into. Pass an empty string to DETACH (revert to waking the agent's own thread)."},
		},
		Required: []string{"name"},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			owner := bridgeOwner(sess)
			if owner == "" {
				return "", fmt.Errorf("bridge requires an authenticated session")
			}
			if _, present := args["channel"]; !present {
				return "", fmt.Errorf("nothing to update — pass channel=<name/id> to hook a channel, or channel=\"\" to detach")
			}
			return setBridgeChannel(owner, strings.TrimSpace(stringArg(args, "name")), strings.TrimSpace(stringArg(args, "channel")))
		},
	})
	gt.AddAction("delete", &GroupedToolAction{
		Description: "Delete a bridge by name (stops its polling).",
		Params:      map[string]ToolParam{"name": {Type: "string", Description: "The bridge name."}},
		Required:    []string{"name"},
		Handler:     bridgeDelete,
	})
	return gt
}

// bridgeWhereToManage tells the user WHERE an API-poll bridge lives —
// the gap a user hit when an agent said a bridge existed and left them
// hunting for it. An API-poll bridge is a POLL-source bridge (same
// "source → channel/agent" concept as the /bridges/ messaging app's
// PUSH-source bridges; they converge, they aren't rivals). Until the
// two views merge, the poll bridges are managed here.
const bridgeWhereToManage = "\n\nManage these under Admin → Bridges (and per-agent under the chat rail's Event monitors). Note: an API-poll bridge is a POLL source that wakes an agent; the /bridges/ app currently shows PUSH sources (iMessage/SMS) — same bridge concept, two views that are converging."

// bridgeOwner pulls the runtime user from the session; "" when unknown.
func bridgeOwner(sess *ToolSession) string {
	if sess == nil {
		return ""
	}
	return sess.Username
}

// isBridgeMonitor reports whether an EventMonitor was created as a bridge: a
// watch monitor whose captured tool is call_<credential>, OR any watch monitor
// with a non-empty SourceKind (tool/pipeline bridges don't use the call_ prefix).
func isBridgeMonitor(m EventMonitor) bool {
	if m.Kind != EventKindWatch {
		return false
	}
	return strings.HasPrefix(m.ToolName, bridgeCredToolPrefix) || strings.TrimSpace(m.SourceKind) != ""
}

// watchProbeHTTPError returns "HTTP <code>" when a bridge source's probe output
// carries a >=400 status line, else "". The underlying credential/api dispatch
// returns "HTTP <code> <text>\n<body>" as a NORMAL result even on 4xx/5xx (the
// transport succeeded), so the hard-fail verify must inspect the body, not just
// the Go error — this is what catches the 401/404 class the bug loops on.
func watchProbeHTTPError(out string) string {
	out = strings.TrimSpace(out)
	if !strings.HasPrefix(out, "HTTP ") {
		return ""
	}
	line := out
	if nl := strings.IndexByte(out, '\n'); nl >= 0 {
		line = out[:nl]
	}
	if f := strings.Fields(line); len(f) >= 2 {
		if code, err := strconv.Atoi(f[1]); err == nil && code >= 400 {
			return "HTTP " + strconv.Itoa(code)
		}
	}
	return ""
}

// truncateForError caps a probe body for inclusion in a create-time error.
func truncateForError(s string, max int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func bridgeCreate(args map[string]any, sess *ToolSession, defaultWakeAgent string) (string, error) {
	owner := bridgeOwner(sess)
	if owner == "" {
		return "", fmt.Errorf("bridge requires an authenticated session")
	}
	name := strings.TrimSpace(stringArg(args, "name"))
	wantAgent := strings.TrimSpace(stringArg(args, "wake_agent"))
	// Channel target (Stage B, unified source→channel): when set, the bridge
	// delivers into this channel and its BOUND agent is the wake target, so
	// wake_agent is derived from the channel rather than supplied.
	wakeChannelID := ""
	channelNote := ""
	if want := strings.TrimSpace(stringArg(args, "channel")); want != "" {
		ch, ok := resolveOwnerChannel(owner, want)
		if !ok {
			return "", fmt.Errorf("no channel named %q for this user — create it in Agents first, or omit channel to wake an agent's own thread. (list the user's channels to see valid names/ids)", want)
		}
		wakeChannelID = ch.ID
		wantAgent = ch.AgentID // the channel's bound agent is the target
		label := ch.Name
		if label == "" {
			label = ch.ID
		}
		channelNote = fmt.Sprintf(" Delivers into channel %q (its bound agent reacts there).", label)
	}
	if wantAgent == "" {
		// No explicit target → self-monitor (the creating agent), when one was
		// provided by the caller. Builder passes "" here, so omitting wake_agent
		// there falls through to the required-target error below.
		wantAgent = strings.TrimSpace(defaultWakeAgent)
	}
	mins := oArgInt(args, "interval_minutes")
	if mins < 1 {
		mins = 1
	}
	if _, exists := GetEventMonitor(RootDB, owner, name); exists {
		return "", fmt.Errorf("a bridge (or monitor) named %q already exists", name)
	}
	if wantAgent == "" {
		return "", fmt.Errorf("wake_agent is required — name the agent to wake when the source changes")
	}
	// Resolve the agent the bridge feeds. Empty fallback → hard error rather
	// than silently waking the wrong agent.
	wakeAgent := resolveCheckAgent(sess, owner, wantAgent, "")
	if wakeAgent == "" {
		return "", fmt.Errorf("no agent named %q to wake — pass a real agent name or id for wake_agent", wantAgent)
	}

	// Resolve the SOURCE — how the watched info is generated. Each kind sets the
	// underlying watch monitor's ToolName + ToolArgs. "url" is the back-compat
	// default (a raw call through a credential); "tool" points at a first-class,
	// testable api/toolbox tool.
	sourceKind := strings.ToLower(strings.TrimSpace(stringArg(args, "source_kind")))
	if sourceKind == "" {
		sourceKind = "url"
	}
	var toolName, sourceDesc string
	var toolArgs map[string]any
	switch sourceKind {
	case "url":
		cred := strings.TrimSpace(stringArg(args, "credential"))
		url := strings.TrimSpace(stringArg(args, "url"))
		if cred == "" || url == "" {
			return "", fmt.Errorf("source_kind=url needs both credential and url")
		}
		// Credential must exist AND be ready. A bridge is only created once its
		// source verifiably works (hard-fail), so a disabled/secretless credential
		// is a precondition to finish first — not a pending bridge that silently
		// fails on a schedule.
		exists, enabled, hasSecret := Secure().CredentialStatus(cred)
		if !exists {
			return "", fmt.Errorf("no API credential named %q — draft one first with draft_api_credential or draft_oauth_credential, then have the admin enable it in Admin > APIs", cred)
		}
		if !enabled || !hasSecret {
			return "", fmt.Errorf("credential %q isn't live yet (enabled=%v, secret set=%v) — a bridge is only created once its source verifiably works. Have the admin finish it in Admin > APIs, then create the bridge", cred, enabled, hasSecret)
		}
		// Host-mismatch tripwire. A bridge is a STANDING dispatch: an absolute URL
		// whose scheme+host disagrees with the credential's Base URL is refused at
		// CREATE time — observed failure: a bridge aimed at a lookalike domain kept
		// shipping the bearer token to a third party every 5 minutes. A path-only
		// URL is immune (it inherits the credential's host).
		if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
			if c, ok := Secure().Load(cred); ok {
				if base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"); base != "" &&
					url != base && !strings.HasPrefix(url, base+"/") {
					return "", fmt.Errorf("bridge url %q is outside credential %q's Base URL (%s) — refusing to create a standing poll that would send this credential's auth to a different host. Pass a PATH-ONLY url (e.g. \"/api/v1/notifications\") so the bridge always inherits the credential's host; if the host itself is wrong, that's a credential fix for the admin, not a bridge parameter", url, cred, c.BaseURL)
				}
			}
		}
		toolName = bridgeCredToolPrefix + cred
		toolArgs = map[string]any{"url": url}
		if method := strings.TrimSpace(stringArg(args, "method")); method != "" {
			toolArgs["method"] = method
		}
		if body := stringArg(args, "body"); strings.TrimSpace(body) != "" {
			toolArgs["body"] = body
		}
		sourceDesc = fmt.Sprintf("call %s (via credential %q)", url, cred)
	case "tool":
		tool := strings.TrimSpace(stringArg(args, "tool"))
		if tool == "" {
			return "", fmt.Errorf("source_kind=tool needs a `tool` (name of an existing, PERSISTENT api/toolbox tool — author it with tool_def and verify it with tool_def action=\"test\" first)")
		}
		toolName = tool
		if ta, ok := args["tool_args"].(map[string]any); ok {
			toolArgs = ta
		} else {
			toolArgs = map[string]any{}
		}
		sourceDesc = fmt.Sprintf("run tool %q", tool)
	case "pipeline":
		return "", fmt.Errorf("source_kind=pipeline isn't wired yet — for now use source_kind=\"tool\" (point the bridge at an api/toolbox tool, which can itself chain calls) or source_kind=\"url\"")
	default:
		return "", fmt.Errorf("unknown source_kind %q — use \"tool\", \"url\", or \"pipeline\"", sourceKind)
	}

	m := EventMonitor{
		Name:       name,
		Owner:      owner,
		Kind:       EventKindWatch,
		Notify:     EventNotifyChannel,
		SourceKind: sourceKind,
		ToolName:   toolName,
		ToolArgs:   toolArgs,
		// Wake the TARGET agent in its OWN home thread (WakeSession empty), not
		// this authoring session — the bridge feeds the agent it was built for.
		WakeAgent:       wakeAgent,
		WakeChannel:     wakeChannelID,
		WakeBrief:       strings.TrimSpace(stringArg(args, "wake_brief")),
		IntervalSeconds: mins * 60,
	}

	// HARD-FAIL verify: exercise the source ONCE before it goes on a schedule.
	// A bridge whose source errors would 401/404-loop in the background where
	// nobody sees it — the verify-gate discipline (interactive tools) applied to
	// the standing case. A transport error fails the probe; an HTTP error body
	// (401/404 — which the underlying dispatch returns as a normal result, not an
	// error) is caught by the status check. On either, refuse to create.
	probe, perr := InvokeWatchTool(owner, m.WakeAgent, m.ToolName, m.ToolArgs)
	if perr != nil {
		return "", fmt.Errorf("bridge source verification FAILED — not creating the bridge (its source must work before it goes on a schedule). Error: %v", perr)
	}
	if status := watchProbeHTTPError(probe); status != "" {
		return "", fmt.Errorf("bridge source returned %s — not creating the bridge (a standing poll on a failing source is worse than none). Fix the source (auth, URL, params) and retry. Response: %s", status, truncateForError(probe, 300))
	}
	m.LastHash = HashWatcherBody(probe)

	SaveEventMonitor(RootDB, m)
	if err := ScheduleEventMonitor(RootDB, m); err != nil {
		return "", fmt.Errorf("saved but scheduling failed: %w", err)
	}
	got, _ := GetEventMonitor(RootDB, owner, name)
	return fmt.Sprintf(
		"Bridge %q created: every %d min I %s and wake agent %q only when the output changes. Next check: %s.%s Verified: the source responded (2xx) and the change-baseline is seeded.",
		name, mins, sourceDesc, wakeAgent,
		got.NextCheck.Local().Format("Mon Jan 2 3:04 PM"), channelNote) + dupMonitorWarning(m) + bridgeWhereToManage, nil
}

// setBridgeChannel hooks an EXISTING poll bridge to a channel (or detaches it
// with an empty channelID) — the shared core behind the bridge tool's `update`
// action AND the Bridges app's per-row Connect/Detach. Setting a channel makes
// the bridge deliver into it and re-points WakeAgent to the channel's bound
// agent (Stage B delivery); detaching reverts to waking that agent's own thread.
func setBridgeChannel(owner, name, channelID string) (string, error) {
	m, ok := GetEventMonitor(RootDB, owner, name)
	if !ok || !isBridgeMonitor(m) {
		return "", fmt.Errorf("no bridge named %q", name)
	}
	if strings.TrimSpace(channelID) == "" {
		m.WakeChannel = ""
		SaveEventMonitor(RootDB, m)
		return fmt.Sprintf("Bridge %q detached from its channel — changes now wake agent %q's own thread.", name, m.WakeAgent), nil
	}
	ch, ok := resolveOwnerChannel(owner, channelID)
	if !ok {
		return "", fmt.Errorf("no channel %q for this user — create it in Agents first, or list the user's channels to see valid names/ids", channelID)
	}
	m.WakeChannel = ch.ID
	m.WakeAgent = ch.AgentID // deliver via the channel's bound agent
	SaveEventMonitor(RootDB, m)
	return fmt.Sprintf("Bridge %q now delivers into channel %q — its bound agent reacts there on each change.", name, bridgeChannelLabel(owner, ch.ID)), nil
}

// resolveOwnerChannel finds one of the owner's channels by id (exact) or by
// name (case-insensitive). Channels live in RootDB. Returns false when nothing
// matches, so the bridge tool can refuse rather than silently pick wrong.
func resolveOwnerChannel(owner, want string) (Channel, bool) {
	if ch, ok := GetChannel(RootDB, owner, want); ok {
		return ch, true
	}
	for _, ch := range ListChannels(RootDB, owner) {
		if strings.EqualFold(strings.TrimSpace(ch.Name), want) {
			return ch, true
		}
	}
	return Channel{}, false
}

// bridgeChannelLabel resolves a channel id to its friendly name for display,
// falling back to the id when the channel is gone or unnamed.
func bridgeChannelLabel(owner, id string) string {
	if c, ok := GetChannel(RootDB, owner, id); ok && strings.TrimSpace(c.Name) != "" {
		return c.Name
	}
	return id
}

// dupMonitorWarning returns a soft heads-up suffix when other monitors already
// watch the same source AND deliver to the same place as m — the "two agents,
// one feed, one chat → doubled alerts" case that name-only dedup can't see. It
// WARNS, never blocks: a same-source monitor with a different intent is valid.
// Empty string when there's no overlap (so callers append it unconditionally).
func dupMonitorWarning(m EventMonitor) string {
	dups := FindDuplicateMonitors(RootDB, m)
	if len(dups) == 0 {
		return ""
	}
	quoted := make([]string, len(dups))
	for i, n := range dups {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	subj := "another monitor is"
	if len(dups) > 1 {
		subj = "other monitors are"
	}
	return fmt.Sprintf(" ⚠️ Possible duplicate: %s already watching this same source with the same delivery target (%s) — the user may get doubled alerts. Remove one if that's unintended.",
		subj, strings.Join(quoted, ", "))
}

func bridgeList(args map[string]any, sess *ToolSession) (string, error) {
	owner := bridgeOwner(sess)
	if owner == "" {
		return "", fmt.Errorf("bridge requires an authenticated session")
	}
	var bs []EventMonitor
	for _, m := range ListEventMonitors(RootDB, owner) {
		if isBridgeMonitor(m) {
			bs = append(bs, m)
		}
	}
	if len(bs) == 0 {
		return "No bridges set up.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d bridge(s):\n", len(bs))
	for _, m := range bs {
		cred := strings.TrimPrefix(m.ToolName, bridgeCredToolPrefix)
		state := "active"
		if m.Paused {
			state = "paused"
		}
		dest := fmt.Sprintf("wake %q", m.WakeAgent)
		if ch := strings.TrimSpace(m.WakeChannel); ch != "" {
			dest = fmt.Sprintf("→ channel %q", bridgeChannelLabel(owner, ch))
		}
		fmt.Fprintf(&b, "- %s [%s]: every %ds call %v (credential %q) %s",
			m.Name, state, m.IntervalSeconds, m.ToolArgs["url"], cred, dest)
		if !m.LastFired.IsZero() {
			fmt.Fprintf(&b, "; last fired %s", m.LastFired.Local().Format("Jan 2 3:04 PM"))
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()) + bridgeWhereToManage, nil
}

func bridgeGet(args map[string]any, sess *ToolSession) (string, error) {
	owner := bridgeOwner(sess)
	if owner == "" {
		return "", fmt.Errorf("bridge requires an authenticated session")
	}
	name := strings.TrimSpace(stringArg(args, "name"))
	m, ok := GetEventMonitor(RootDB, owner, name)
	if !ok || !isBridgeMonitor(m) {
		return "", fmt.Errorf("no bridge named %q", name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Bridge %q:\n", m.Name)
	kind := strings.TrimSpace(m.SourceKind)
	if kind == "" {
		kind = "url"
	}
	fmt.Fprintf(&b, "  source kind: %s\n", kind)
	switch kind {
	case "tool":
		fmt.Fprintf(&b, "  tool:        %s\n", m.ToolName)
		if len(m.ToolArgs) > 0 {
			fmt.Fprintf(&b, "  tool args:   %v\n", m.ToolArgs)
		}
	default: // url
		fmt.Fprintf(&b, "  credential:  %s\n", strings.TrimPrefix(m.ToolName, bridgeCredToolPrefix))
		fmt.Fprintf(&b, "  url:         %v\n", m.ToolArgs["url"])
		if v, ok := m.ToolArgs["method"]; ok {
			fmt.Fprintf(&b, "  method:      %v\n", v)
		}
	}
	fmt.Fprintf(&b, "  interval:    %ds\n", m.IntervalSeconds)
	if ch := strings.TrimSpace(m.WakeChannel); ch != "" {
		fmt.Fprintf(&b, "  delivers to: channel %q (agent %s reacts there)\n", bridgeChannelLabel(owner, ch), m.WakeAgent)
	} else {
		fmt.Fprintf(&b, "  wakes agent: %s\n", m.WakeAgent)
	}
	if m.WakeBrief != "" {
		fmt.Fprintf(&b, "  wake brief:  %s\n", m.WakeBrief)
	}
	if m.Paused {
		b.WriteString("  state:       paused\n")
	} else {
		b.WriteString("  state:       active\n")
	}
	if !m.LastFired.IsZero() {
		fmt.Fprintf(&b, "  last fired:  %s\n", m.LastFired.Local().Format("Mon Jan 2 3:04 PM"))
	}
	return strings.TrimSpace(b.String()), nil
}

func bridgeDelete(args map[string]any, sess *ToolSession) (string, error) {
	owner := bridgeOwner(sess)
	if owner == "" {
		return "", fmt.Errorf("bridge requires an authenticated session")
	}
	name := strings.TrimSpace(stringArg(args, "name"))
	m, ok := GetEventMonitor(RootDB, owner, name)
	if !ok || !isBridgeMonitor(m) {
		return "", fmt.Errorf("no bridge named %q", name)
	}
	DeleteEventMonitor(RootDB, owner, name)
	return fmt.Sprintf("Deleted bridge %q.", name), nil
}
