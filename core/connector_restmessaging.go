// The rest_messaging connector kind: a GENERIC, server-side-polled, TWO-SIDED
// messaging bridge for any REST-pollable chat service (Microsoft Teams, Slack,
// Discord, …) declared entirely from a spec — no Go per service.
//
// Where messaging_bridge (connector_bridge.go) pairs a server key with a relay
// COMPILED INTO the desktop daemon (iMessage), rest_messaging is SERVER-ONLY: a
// scheduled poll loop (materialized in the bridges app) reads a service's REST
// API through an already-governed SecureAPI credential, maps each new message
// onto the existing Bridges inbound contract (hookRequest), and delivers the
// bound agent's replies back out the same API. To the rest of Bridges it looks
// identical to any other connector speaking the /api/hook + /api/poll contract —
// so Channels binding, threads, the dashboard, and reply delivery all work for
// free.
//
// This is the unified-connector mission (a new bridge type with no code change)
// realized for messaging: the Builder authors the spec (or picks a preset); core
// owns the kind + lifecycle; the bridges app owns the poll/deliver behavior via
// the registered seams below.
//
// Approval is MANDATORY (no ConnectorAutoApprover): a messaging bridge routes
// external messages INTO an agent and sends replies OUT, so it stays pending
// until an admin approves — same gate as messaging_bridge, unlike the inert
// rest_poll.
package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RestMessagingConnectorKind is the Kind value for a server-polled messaging bridge.
const RestMessagingConnectorKind = "rest_messaging"

// RestMessagingFieldMap holds DECLARATIVE dot-paths, each resolved against ONE
// element of the polled message list, that pull a normalized message out of an
// arbitrary JSON shape. Paths are element-relative (e.g. "from.user.displayName",
// "body.content", "id"). A key that itself contains dots (Graph's
// "@odata.deltaLink") is matched literally. No script/transform hook — the mapper
// is fully sandboxed; a service whose JSON can't be dot-path-mapped is simply not
// supported by this kind.
type RestMessagingFieldMap struct {
	ChatID     string `json:"chat_id,omitempty"`     // REQUIRED — stable conversation id (what a Channel binds to)
	MsgID      string `json:"msg_id,omitempty"`      // per-message id (dedup key)
	Sender     string `json:"sender,omitempty"`      // sender handle/id
	SenderName string `json:"sender_name,omitempty"` // sender display name
	Text       string `json:"text,omitempty"`        // REQUIRED — message body
	ConvName   string `json:"conv_name,omitempty"`   // conversation/thread title
	Timestamp  string `json:"timestamp,omitempty"`   // message time (optional, informational)
}

// RestMessagingSpec is the Spec payload for a rest_messaging connector.
type RestMessagingSpec struct {
	Service      string `json:"service"`                 // namespaces the BridgeKey + outbox (e.g. "teams")
	Credential   string `json:"credential"`              // registered SecureAPI (OAuth2) credential name
	PollURL      string `json:"poll_url"`                // absolute list endpoint hit each interval
	Method       string `json:"method,omitempty"`        // poll HTTP method (default GET)
	Body         string `json:"body,omitempty"`          // poll request body (rare)
	IntervalSecs int    `json:"interval_secs,omitempty"` // poll cadence (default 30, min 5)
	ListPath     string `json:"list_path,omitempty"`     // dot-path to the message array (empty = response root is the array)
	Map          RestMessagingFieldMap `json:"map"`

	// ChatIDConst is a fixed chat id for EVERY message, for services whose message
	// objects don't echo their conversation id (Slack conversations.history omits
	// the channel). Takes precedence over Map.ChatID; a preset var like {channel_id}
	// substitutes into it. One connector = one conversation, so a constant is right.
	ChatIDConst string `json:"chat_id_const,omitempty"`

	// MoreURLPath is a response dot-path to a COMPLETE next-page URL, followed
	// WITHIN a single tick until absent (Microsoft Graph's "@odata.nextLink") —
	// distinct from the cross-tick cursor below. Without it a paginated first
	// response (any channel with history) never reaches the deltaLink, so the
	// cursor never advances.
	MoreURLPath string `json:"more_url_path,omitempty"`

	// Cursor / pagination — pick ONE mode per service:
	//   - NextURLPath: a response dot-path whose value REPLACES PollURL on the next
	//     poll (Microsoft Graph's "@odata.deltaLink").
	//   - CursorPath + CursorParam: a response dot-path whose value is injected as a
	//     query param named CursorParam on the next poll (Slack "cursor", Discord
	//     "after").
	NextURLPath string `json:"next_url_path,omitempty"`
	CursorPath  string `json:"cursor_path,omitempty"`
	CursorParam string `json:"cursor_param,omitempty"`

	// Outbound: how a bound agent's reply is delivered back to the service.
	SendURL    string `json:"send_url,omitempty"`    // absolute send endpoint; may contain {chat_id}
	SendMethod string `json:"send_method,omitempty"` // send HTTP method (default POST)
	SendBody   string `json:"send_body,omitempty"`   // JSON template; {text}/{chat_id} substituted + JSON-escaped

	// StripHTML runs a light tag-strip + entity-unescape on the mapped Text before
	// it reaches the agent (Graph message bodies are HTML). Set by the teams preset.
	StripHTML bool `json:"strip_html,omitempty"`

	// WebhookProvider switches inbound from POLL to real-time PUSH: the service
	// posts events to /bridges/api/webhook/<name> and the named provider ("slack"
	// or "graph") handles the URL-verification handshake, signature check, and
	// event → message mapping. The outbound half (SendURL/credential) is unchanged.
	// Empty = poll mode. The provider's signing secret lives in an encrypted
	// bridges table, never in this spec.
	WebhookProvider string `json:"webhook_provider,omitempty"`
}

// knownWebhookProviders gates spec.WebhookProvider at validate time. The provider
// implementations live in the bridges app; core only needs the names to reject a
// typo early. Keep in sync with apps/bridges/webhook.go's webhookProviders.
var knownWebhookProviders = map[string]bool{"slack": true, "graph": true}

func init() { RegisterConnectorKind(RestMessagingConnectorKind, restMessagingHandler{}) }

type restMessagingHandler struct{}

func (restMessagingHandler) parse(c Connector) (RestMessagingSpec, error) {
	var s RestMessagingSpec
	if len(c.Spec) > 0 {
		if err := json.Unmarshal(c.Spec, &s); err != nil {
			return s, fmt.Errorf("bad rest_messaging spec: %w", err)
		}
	}
	s.Service = strings.ToLower(strings.TrimSpace(s.Service))
	s.Credential = strings.TrimSpace(s.Credential)
	s.PollURL = strings.TrimSpace(s.PollURL)
	return s, nil
}

func (h restMessagingHandler) Validate(c Connector) error {
	if strings.TrimSpace(c.Owner) == "" {
		return fmt.Errorf("rest_messaging requires an owner (the user whose channel agents handle the messages)")
	}
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	if s.Service == "" {
		return fmt.Errorf("service is required (e.g. \"teams\") — it namespaces the bridge")
	}
	if !connectorNameRE.MatchString(s.Service) {
		return fmt.Errorf("service %q must be tool-namespace-safe (letters, digits, underscore)", s.Service)
	}
	if s.Credential == "" {
		return fmt.Errorf("credential is required (a registered SecureAPI credential name)")
	}
	if exists, _, _ := Secure().CredentialStatus(s.Credential); !exists {
		return fmt.Errorf("no credential named %q — draft it first (draft_oauth_credential) and have the admin enable it", s.Credential)
	}
	if s.WebhookProvider != "" {
		// PUSH mode: the provider owns inbound (handshake + verify + extraction), so
		// poll_url and the poll-side map aren't required. Outbound (send_url) is.
		if !knownWebhookProviders[s.WebhookProvider] {
			return fmt.Errorf("unknown webhook_provider %q (supported: graph, slack)", s.WebhookProvider)
		}
	} else {
		if !strings.HasPrefix(s.PollURL, "https://") && !strings.HasPrefix(s.PollURL, "http://") {
			return fmt.Errorf("poll_url must be http(s) — got %q (did you fill the preset vars, e.g. team_id/channel_id?)", s.PollURL)
		}
		if s.Map.ChatID == "" && s.ChatIDConst == "" {
			return fmt.Errorf("map.chat_id (a dot-path) or chat_id_const (a fixed value) is required")
		}
		if s.Map.Text == "" {
			return fmt.Errorf("map.text is required (a dot-path into each message)")
		}
	}
	if s.SendURL != "" && !strings.HasPrefix(s.SendURL, "https://") && !strings.HasPrefix(s.SendURL, "http://") {
		return fmt.Errorf("send_url must be http(s)")
	}
	return nil
}

// Materialize provisions the server-side routing key (so the service shows in the
// Bridges dashboard and owner resolution works) then starts the poll loop. Both
// halves live in the bridges app, registered via the seams below; core can't
// import bridges. Idempotent — the poller stops any prior loop for this connector
// before starting, so a re-materialize (admin re-approve, startup reload) is safe.
func (h restMessagingHandler) Materialize(c Connector) error {
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	if err := provisionServiceBridge(c.Owner, s.Service, true); err != nil {
		return fmt.Errorf("server bridge key: %w", err)
	}
	return startMessagingPoller(c, true)
}

// Teardown stops the poll loop. It deliberately leaves the service's BridgeKey in
// place (a sibling connector on the same service may still need it; an admin can
// delete the key from the dashboard).
func (h restMessagingHandler) Teardown(c Connector) error {
	return startMessagingPoller(c, false)
}

func (h restMessagingHandler) Summary(c Connector) string {
	s, _ := h.parse(c)
	secs := s.IntervalSecs
	if secs < 5 {
		secs = 30
	}
	reply := "reply out"
	if s.SendURL == "" {
		reply = "inbound only"
	}
	if s.WebhookProvider != "" {
		return fmt.Sprintf("%s webhook → route to %s channel agents (%s) at /bridges/api/webhook/%s",
			s.WebhookProvider, s.Service, reply, c.Name)
	}
	return fmt.Sprintf("poll %s messages via credential %s every %ds → route to %s channel agents (%s)",
		s.Service, s.Credential, secs, s.Service, reply)
}

// --- bridges-side poller seam ------------------------------------------------
//
// Same shape as RegisterBridgeProvisioner: core owns the connector lifecycle, the
// bridges app supplies the actual poll/deliver loop and registers it at route
// time (when its store is live). When no poller is registered (bridges app not
// loaded), rest_messaging validates + provisions its key but stays inert.

// MessagingPollerFn starts (start=true) or stops (start=false) the poll loop for a
// rest_messaging connector.
type MessagingPollerFn func(c Connector, start bool) error

var messagingPoller MessagingPollerFn

// RegisterMessagingPoller installs the bridges-side poll loop. Call once at
// route-registration time.
func RegisterMessagingPoller(fn MessagingPollerFn) { messagingPoller = fn }

func startMessagingPoller(c Connector, start bool) error {
	if messagingPoller == nil {
		if start {
			Warn("[connector] no messaging poller registered — rest_messaging %q inert until the bridges app loads", c.Name)
		}
		return nil
	}
	return messagingPoller(c, start)
}

// MessagingProbeFn does ONE poll + maps the first message and returns a
// human-readable preview, without ingesting — so the Builder can verify a mapping
// before going live. Registered by the bridges app; needs the credential enabled.
type MessagingProbeFn func(c Connector) (string, error)

var messagingProbe MessagingProbeFn

// RegisterMessagingProbe installs the bridges-side mapping probe.
func RegisterMessagingProbe(fn MessagingProbeFn) { messagingProbe = fn }

// ProbeMessagingConnector runs the registered probe, or reports it's unavailable.
func ProbeMessagingConnector(c Connector) (string, error) {
	if messagingProbe == nil {
		return "", fmt.Errorf("messaging probe unavailable (bridges app not loaded)")
	}
	return messagingProbe(c)
}

// --- service presets ---------------------------------------------------------
//
// A preset fills the fiddly service-specific fields (endpoints, dot-paths, cursor
// mode) so a Builder supplies only the credential + per-instance vars. An explicit
// spec field always overrides its preset value — a preset is defaults, not a lock.

var restMessagingPresets = map[string]RestMessagingSpec{
	// Microsoft Teams via Graph channel-message delta. Vars: team_id, channel_id.
	// Requires a Graph OAuth2 credential (client_credentials) with the protected
	// ChannelMessage.Read.All permission granted by the tenant admin.
	"teams": {
		Service:      "teams",
		PollURL:      "https://graph.microsoft.com/v1.0/teams/{team_id}/channels/{channel_id}/messages/delta",
		Method:       "GET",
		IntervalSecs: 30,
		ListPath:     "value",
		Map: RestMessagingFieldMap{
			ChatID:     "channelIdentity.channelId",
			MsgID:      "id",
			Sender:     "from.user.id",
			SenderName: "from.user.displayName",
			Text:       "body.content",
			Timestamp:  "createdDateTime",
		},
		MoreURLPath: "@odata.nextLink",
		NextURLPath: "@odata.deltaLink",
		SendURL:     "https://graph.microsoft.com/v1.0/teams/{team_id}/channels/{channel_id}/messages",
		SendMethod:  "POST",
		SendBody:    `{"body":{"contentType":"html","content":"{text}"}}`,
		StripHTML:   true,
	},

	// Slack via the Web API. Var: channel_id (a "C…" channel id). Requires a
	// SecureAPI BEARER credential holding a bot token (xoxb-…) with scopes
	// channels:history (read) and chat:write (reply); the bot must be a member of
	// the channel. conversations.history returns newest-first and omits the channel
	// on each message, so chat_id is fixed and the poll-forward cursor is the newest
	// ts fed back as `oldest`. (No within-tick pagination: a poll returns only
	// messages since the last ts — normally under one page.)
	"slack": {
		Service:      "slack",
		PollURL:      "https://slack.com/api/conversations.history?channel={channel_id}",
		Method:       "GET",
		IntervalSecs: 15,
		ListPath:     "messages",
		ChatIDConst:  "{channel_id}",
		Map: RestMessagingFieldMap{
			MsgID:  "ts",
			Sender: "user",
			Text:   "text",
		},
		CursorPath:  "messages.0.ts",
		CursorParam: "oldest",
		SendURL:     "https://slack.com/api/chat.postMessage",
		SendMethod:  "POST",
		SendBody:    `{"channel":"{chat_id}","text":"{text}"}`,
	},
}

// RestMessagingPreset returns a preset spec by name.
func RestMessagingPreset(name string) (RestMessagingSpec, bool) {
	p, ok := restMessagingPresets[strings.ToLower(strings.TrimSpace(name))]
	return p, ok
}

// RestMessagingPresetNames lists the available preset names, sorted-ish (small
// map; a stable-enough order for an error message).
func RestMessagingPresetNames() []string {
	out := make([]string, 0, len(restMessagingPresets))
	for k := range restMessagingPresets {
		out = append(out, k)
	}
	return out
}

// ApplyRestMessagingPreset overlays `over` (the explicit args) onto the named
// preset (empty preset = no defaults), then substitutes {var} tokens in the poll
// and send URLs from `vars` (e.g. team_id, channel_id). The returned spec is fully
// resolved and ready to persist.
func ApplyRestMessagingPreset(preset string, over RestMessagingSpec, vars map[string]string) (RestMessagingSpec, error) {
	out := over
	if p := strings.TrimSpace(preset); p != "" {
		base, ok := RestMessagingPreset(p)
		if !ok {
			return out, fmt.Errorf("unknown rest_messaging preset %q (known: %s)", p, strings.Join(RestMessagingPresetNames(), ", "))
		}
		out = MergeRestMessagingSpec(base, over)
	}
	out.PollURL = substituteTokens(out.PollURL, vars)
	out.SendURL = substituteTokens(out.SendURL, vars)
	out.ChatIDConst = substituteTokens(out.ChatIDConst, vars)
	out.Service = strings.ToLower(strings.TrimSpace(out.Service))
	return out, nil
}

// MergeRestMessagingSpec returns `over` with any empty field filled from `base`.
// Explicit (`over`) values win; `base` supplies the rest. Used both to apply a
// preset's defaults (base=preset) and to partial-patch an existing connector on
// update (base=the stored spec). Note: an empty `over` field means "keep base",
// so a field can't be CLEARED through this merge — StripHTML likewise only turns
// on. Callers wanting to clear a field re-author the connector.
func MergeRestMessagingSpec(base, over RestMessagingSpec) RestMessagingSpec {
	out := over
	fill := func(dst *string, src string) {
		if strings.TrimSpace(*dst) == "" {
			*dst = src
		}
	}
	fill(&out.Service, base.Service)
	fill(&out.PollURL, base.PollURL)
	fill(&out.Method, base.Method)
	fill(&out.Body, base.Body)
	fill(&out.ListPath, base.ListPath)
	fill(&out.ChatIDConst, base.ChatIDConst)
	fill(&out.MoreURLPath, base.MoreURLPath)
	fill(&out.NextURLPath, base.NextURLPath)
	fill(&out.CursorPath, base.CursorPath)
	fill(&out.CursorParam, base.CursorParam)
	fill(&out.SendURL, base.SendURL)
	fill(&out.SendMethod, base.SendMethod)
	fill(&out.SendBody, base.SendBody)
	fill(&out.WebhookProvider, base.WebhookProvider)
	if out.IntervalSecs == 0 {
		out.IntervalSecs = base.IntervalSecs
	}
	if !out.StripHTML {
		out.StripHTML = base.StripHTML
	}
	fill(&out.Map.ChatID, base.Map.ChatID)
	fill(&out.Map.MsgID, base.Map.MsgID)
	fill(&out.Map.Sender, base.Map.Sender)
	fill(&out.Map.SenderName, base.Map.SenderName)
	fill(&out.Map.Text, base.Map.Text)
	fill(&out.Map.ConvName, base.Map.ConvName)
	fill(&out.Map.Timestamp, base.Map.Timestamp)
	return out
}

// substituteTokens replaces each {key} in s with vars[key]. Unknown tokens (e.g.
// the runtime {chat_id}/{text}) are left untouched for later substitution.
func substituteTokens(s string, vars map[string]string) string {
	if s == "" || len(vars) == 0 {
		return s
	}
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}
