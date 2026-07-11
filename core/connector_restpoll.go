// The rest_poll connector kind: materializes a "poll an authenticated URL every
// N minutes and WAKE an agent when the response changes" capability — the same
// shape as the `bridge` tool, expressed as a Connector so it lives in the
// unified surface (visible + governable + tearable in Admin > Connectors).
//
// It reaches out only through an already-enabled SecureAPI credential, so it
// AUTO-APPROVES on create (parity with the ungated bridge tool); an admin can
// still Unapprove/Delete it. Materializes into a watch-kind EventMonitor whose
// captured tool is call_<credential> — identical to what apps/orchestrate's
// bridge tool builds.
package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RestPollConnectorKind is the Kind value for a poll-and-wake connector.
const RestPollConnectorKind = "rest_poll"

// restPollCredToolPrefix is the watch-tool prefix that routes a poll through
// SecureAPI at fire time (mirrors the bridge tool's bridgeCredToolPrefix).
const restPollCredToolPrefix = "call_"

// RestPollSpec is the Spec payload for a rest_poll connector. WakeAgent holds a
// RESOLVED agent id (the tool resolves the user-facing name at create time).
type RestPollSpec struct {
	Credential      string `json:"credential"` // SecureAPI credential name → call_<name>
	URL             string `json:"url"`
	Method          string `json:"method,omitempty"`
	Body            string `json:"body,omitempty"`
	WakeAgent       string `json:"wake_agent"` // resolved agent id
	WakeBrief       string `json:"wake_brief,omitempty"`
	IntervalMinutes int    `json:"interval_minutes"`
}

func init() { RegisterConnectorKind(RestPollConnectorKind, restPollHandler{}) }

type restPollHandler struct{}

// AutoApprove: rest_poll reaches out only via an already-governed credential, so
// it goes live on create like the bridge tool.
func (restPollHandler) AutoApprove() bool { return true }

func (restPollHandler) parse(c Connector) (RestPollSpec, error) {
	var s RestPollSpec
	if len(c.Spec) > 0 {
		if err := json.Unmarshal(c.Spec, &s); err != nil {
			return s, fmt.Errorf("bad rest_poll spec: %w", err)
		}
	}
	s.Credential = strings.TrimSpace(s.Credential)
	s.URL = strings.TrimSpace(s.URL)
	s.WakeAgent = strings.TrimSpace(s.WakeAgent)
	return s, nil
}

func (h restPollHandler) Validate(c Connector) error {
	if strings.TrimSpace(c.Owner) == "" {
		return fmt.Errorf("rest_poll requires an owner (the user whose agent is woken)")
	}
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	if s.Credential == "" {
		return fmt.Errorf("credential is required (a registered SecureAPI credential name)")
	}
	if exists, _, _ := Secure().CredentialStatus(s.Credential); !exists {
		return fmt.Errorf("no credential named %q — draft it first (draft_api_credential / draft_oauth_credential) and have the admin enable it", s.Credential)
	}
	if !strings.HasPrefix(s.URL, "https://") && !strings.HasPrefix(s.URL, "http://") {
		return fmt.Errorf("url must be http(s)")
	}
	if s.WakeAgent == "" {
		return fmt.Errorf("wake_agent is required (the agent to wake on change)")
	}
	return nil
}

// monitor builds the watch-kind EventMonitor this connector materializes into.
func (restPollHandler) monitor(c Connector, s RestPollSpec) EventMonitor {
	mins := s.IntervalMinutes
	if mins < 1 {
		mins = 1
	}
	toolArgs := map[string]any{"url": s.URL}
	if strings.TrimSpace(s.Method) != "" {
		toolArgs["method"] = s.Method
	}
	if strings.TrimSpace(s.Body) != "" {
		toolArgs["body"] = s.Body
	}
	return EventMonitor{
		Name:            c.Name,
		Owner:           c.Owner,
		Kind:            EventKindWatch,
		Notify:          EventNotifyChannel,
		ToolName:        restPollCredToolPrefix + s.Credential,
		ToolArgs:        toolArgs,
		WakeAgent:       s.WakeAgent,
		WakeBrief:       s.WakeBrief,
		IntervalSeconds: mins * 60,
	}
}

func (h restPollHandler) Materialize(c Connector) error {
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	m := h.monitor(c, s)
	// Seed the change-baseline from a probe (best-effort) so the first poll
	// detects a REAL change, not the initial content — same as the bridge tool.
	if probe, perr := InvokeWatchTool(c.Owner, m.WakeAgent, m.ToolName, m.ToolArgs); perr == nil {
		m.LastHash = HashWatcherBody(probe)
	}
	SaveEventMonitor(RootDB, m)
	return ScheduleEventMonitor(RootDB, m)
}

func (restPollHandler) Teardown(c Connector) error {
	DeleteEventMonitor(RootDB, c.Owner, c.Name)
	return nil
}

func (h restPollHandler) Summary(c Connector) string {
	s, _ := h.parse(c)
	mins := s.IntervalMinutes
	if mins < 1 {
		mins = 1
	}
	url := s.URL
	if url == "" {
		url = "(no url)"
	}
	return fmt.Sprintf("poll %s (via credential %s) every %dm → wake %s on change", url, s.Credential, mins, s.WakeAgent)
}
