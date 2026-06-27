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
			"url":              {Type: "string", Description: "The full URL to poll each interval. Must fall within the credential's allowed URL space."},
			"wake_agent":       {Type: "string", Description: "Name or id of the agent to wake when the response changes; its thread receives the change-alert. Omit to wake the agent creating the bridge (self-monitoring). Required when authoring a bridge FOR a different agent."},
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
	gt.AddAction("delete", &GroupedToolAction{
		Description: "Delete a bridge by name (stops its polling).",
		Params:      map[string]ToolParam{"name": {Type: "string", Description: "The bridge name."}},
		Required:    []string{"name"},
		Handler:     bridgeDelete,
	})
	return gt
}

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
		WakeBrief:       strings.TrimSpace(stringArg(args, "wake_brief")),
		IntervalSeconds: mins * 60,
	}

	// Seed the change-baseline from a known-good probe so the first poll detects
	// a REAL change instead of firing on the initial content. Best-effort: if
	// the probe fails (credential not enabled yet, URL outside the allow-list),
	// save anyway — the first successful poll seeds the baseline.
	probeNote := ""
	if probe, perr := InvokeWatchTool(owner, m.ToolName, m.ToolArgs); perr == nil {
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
		"Bridge %q created: every %d min I call %s (via credential %q) and wake agent %q only when the response changes. Next check: %s.%s%s",
		name, mins, url, cred, wakeAgent,
		got.NextCheck.Local().Format("Mon Jan 2 3:04 PM"), credWarn, probeNote), nil
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
		fmt.Fprintf(&b, "- %s [%s]: every %ds call %v (credential %q) → wake %q",
			m.Name, state, m.IntervalSeconds, m.ToolArgs["url"], cred, m.WakeAgent)
		if !m.LastFired.IsZero() {
			fmt.Fprintf(&b, "; last fired %s", m.LastFired.Local().Format("Jan 2 3:04 PM"))
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()), nil
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
	fmt.Fprintf(&b, "  wakes agent: %s\n", m.WakeAgent)
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
