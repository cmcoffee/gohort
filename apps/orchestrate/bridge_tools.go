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

How it fires: every interval the framework calls the credential's URL, hashes
the response, and wakes the target agent in its own thread ONLY when the
response differs from the last poll — the cheap "tell me when X changes" path,
zero LLM cost until something actually changes. The first poll just records the
baseline (no wake).

Typical flow when wiring a new service:
  1. draft_api_credential (or draft_oauth_credential) for the service.
  2. (admin enables it + pastes the secret in Admin > APIs)
  3. bridge(action="create", name=..., credential=..., url=..., wake_agent=...,
     interval_minutes=..., wake_brief="what changed and what to do about it").`))

	gt.AddAction("create", &GroupedToolAction{
		Description: "Create a bridge: poll credential's url every interval_minutes; wake wake_agent on change.",
		Params: map[string]ToolParam{
			"name":             {Type: "string", Description: "Unique short name for this bridge (per user)."},
			"credential":       {Type: "string", Description: "Name of a registered SecureAPI credential (the one that becomes fetch_url_<name>). The bridge calls the API through it for auth + URL allow-list. Draft it first if it doesn't exist yet."},
			"url":              {Type: "string", Description: "The URL to poll each interval. PREFER a path-only URL (e.g. \"/api/v1/notifications\") — it resolves against the credential's Base URL at poll time, so the host can never disagree with the admin's config. A full https:// URL also works but must match the credential's Base URL host EXACTLY (www vs bare host count as different)."},
			"wake_agent":       {Type: "string", Description: "Name or id of the agent to wake when the response changes; its OWN home thread receives the change-alert. Omit to wake the agent creating the bridge (self-monitoring). Ignored when `channel` is set (the channel's bound agent is used instead)."},
			"channel":          {Type: "string", Description: "(optional) Deliver the change INTO this channel instead of the agent's home thread — name or id of one of the user's channels. The channel's bound agent reacts in the channel's conversation, and if the channel has a live transport (iMessage/etc) the reaction flows out it. This is the \"source → channel\" shape. When set, wake_agent is derived from the channel."},
			"interval_minutes": {Type: "number", Description: "How often to poll, in minutes (minimum 1; 15 = every 15 min, 60 = hourly)."},
			"wake_brief":       {Type: "string", Description: "Guidance handed to the woken agent on each change — what the data means and what to do about it."},
			"method":           {Type: "string", Description: "(optional) HTTP method; defaults to GET."},
			"body":             {Type: "string", Description: "(optional) request body for POST/PUT etc."},
		},
		Required: []string{"name", "credential", "url", "interval_minutes"},
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
// watch monitor whose captured tool is call_<credential>.
func isBridgeMonitor(m EventMonitor) bool {
	return m.Kind == EventKindWatch && strings.HasPrefix(m.ToolName, bridgeCredToolPrefix)
}

func bridgeCreate(args map[string]any, sess *ToolSession, defaultWakeAgent string) (string, error) {
	owner := bridgeOwner(sess)
	if owner == "" {
		return "", fmt.Errorf("bridge requires an authenticated session")
	}
	name := strings.TrimSpace(stringArg(args, "name"))
	cred := strings.TrimSpace(stringArg(args, "credential"))
	url := strings.TrimSpace(stringArg(args, "url"))
	wantAgent := strings.TrimSpace(stringArg(args, "wake_agent"))
	// Channel target (Stage B, unified source→channel): when set, the bridge
	// delivers into this channel and its BOUND agent is the wake target, so
	// wake_agent is derived from the channel rather than supplied.
	wakeChannelID := ""
	channelNote := ""
	if want := strings.TrimSpace(stringArg(args, "channel")); want != "" {
		ch, ok := resolveOwnerChannel(owner, want)
		if !ok {
			return "", fmt.Errorf("no channel named %q for this user — create it in Agency first, or omit channel to wake an agent's own thread. (list the user's channels to see valid names/ids)", want)
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
	method := strings.TrimSpace(stringArg(args, "method"))
	body := stringArg(args, "body")
	mins := oArgInt(args, "interval_minutes")

	if mins < 1 {
		mins = 1
	}
	if _, exists := GetEventMonitor(RootDB, owner, name); exists {
		return "", fmt.Errorf("a bridge (or monitor) named %q already exists", name)
	}

	// Credential must exist. Surface its readiness so the user knows whether an
	// admin still has to finish it before the bridge can authenticate.
	exists, enabled, hasSecret := Secure().CredentialStatus(cred)
	if !exists {
		return "", fmt.Errorf("no API credential named %q — draft one first with draft_api_credential or draft_oauth_credential, then have the admin enable it in Admin > APIs", cred)
	}
	credWarn := ""
	if !enabled || !hasSecret {
		credWarn = fmt.Sprintf(" NOTE: credential %q isn't fully live yet (enabled=%v, secret set=%v) — an admin must finish it in Admin > APIs before the bridge can authenticate.", cred, enabled, hasSecret)
	}

	// Host-mismatch tripwire. A bridge is a STANDING dispatch: once the
	// credential's config later lines up with this absolute URL, the poll
	// sends the credential's auth to this host every interval, forever,
	// with nobody watching. An absolute URL whose scheme+host disagrees
	// with the credential's Base URL is therefore refused at CREATE time
	// — observed failure: a bridge aimed at a lookalike docs domain kept
	// polling after an admin config change made it pass the allowlist,
	// shipping the bearer token to a third party every 5 minutes. A
	// path-only URL is immune (it inherits the credential's host).
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		if c, ok := Secure().Load(cred); ok {
			if base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"); base != "" &&
				url != base && !strings.HasPrefix(url, base+"/") {
				return "", fmt.Errorf("bridge url %q is outside credential %q's Base URL (%s) — refusing to create a standing poll that would send this credential's auth to a different host. Pass a PATH-ONLY url (e.g. \"/api/v1/notifications\") so the bridge always inherits the credential's host; if the host itself is wrong, that's a credential fix for the admin, not a bridge parameter", url, cred, c.BaseURL)
			}
		}
	}

	if wantAgent == "" {
		return "", fmt.Errorf("wake_agent is required — name the agent to wake when the API changes")
	}
	// Resolve the agent the bridge feeds. Empty fallback → hard error rather
	// than silently waking the wrong agent.
	wakeAgent := resolveCheckAgent(sess, owner, wantAgent, "")
	if wakeAgent == "" {
		return "", fmt.Errorf("no agent named %q to wake — pass a real agent name or id for wake_agent", wantAgent)
	}

	toolArgs := map[string]any{"url": url}
	if method != "" {
		toolArgs["method"] = method
	}
	if strings.TrimSpace(body) != "" {
		toolArgs["body"] = body
	}

	m := EventMonitor{
		Name:     name,
		Owner:    owner,
		Kind:     EventKindWatch,
		Notify:   EventNotifyChannel,
		ToolName: bridgeCredToolPrefix + cred,
		ToolArgs: toolArgs,
		// Wake the TARGET agent in its OWN home thread (WakeSession empty), not
		// this authoring session — the bridge feeds the agent it was built for.
		WakeAgent:       wakeAgent,
		WakeChannel:     wakeChannelID,
		WakeBrief:       strings.TrimSpace(stringArg(args, "wake_brief")),
		IntervalSeconds: mins * 60,
	}

	// Seed the change-baseline from a known-good probe so the first poll detects
	// a REAL change instead of firing on the initial content. Best-effort: if
	// the probe fails (credential not enabled yet, URL outside the allow-list),
	// save anyway — the first successful poll seeds the baseline.
	probeNote := ""
	if probe, perr := InvokeWatchTool(owner, m.WakeAgent, m.ToolName, m.ToolArgs); perr == nil {
		m.LastHash = HashWatcherBody(probe)
		probeNote = " Verified: the API responded and the change-baseline is seeded."
	} else {
		probeNote = fmt.Sprintf(" The initial probe didn't succeed (%v); the bridge will seed its baseline on the first successful poll.", perr)
	}

	SaveEventMonitor(RootDB, m)
	if err := ScheduleEventMonitor(RootDB, m); err != nil {
		return "", fmt.Errorf("saved but scheduling failed: %w", err)
	}
	got, _ := GetEventMonitor(RootDB, owner, name)
	return fmt.Sprintf(
		"Bridge %q created: every %d min I call %s (via credential %q) and wake agent %q only when the response changes. Next check: %s.%s%s%s",
		name, mins, url, cred, wakeAgent,
		got.NextCheck.Local().Format("Mon Jan 2 3:04 PM"), channelNote, credWarn, probeNote) + dupMonitorWarning(m) + bridgeWhereToManage, nil
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
		return "", fmt.Errorf("no channel %q for this user — create it in Agency first, or list the user's channels to see valid names/ids", channelID)
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
	cred := strings.TrimPrefix(m.ToolName, bridgeCredToolPrefix)
	var b strings.Builder
	fmt.Fprintf(&b, "Bridge %q:\n", m.Name)
	fmt.Fprintf(&b, "  credential:  %s\n", cred)
	fmt.Fprintf(&b, "  url:         %v\n", m.ToolArgs["url"])
	if v, ok := m.ToolArgs["method"]; ok {
		fmt.Fprintf(&b, "  method:      %v\n", v)
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
