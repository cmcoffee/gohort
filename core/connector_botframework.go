// The bot_framework connector kind: gohort as a first-class Microsoft Teams
// participant, rather than a reader of one channel's message list.
//
// Where rest_messaging polls a service's REST API (or receives its native
// webhook) and maps an arbitrary JSON shape with declarative dot-paths, this
// kind speaks ONE known protocol -- Bot Framework Activities -- and that is why
// it is a sibling kind instead of a third rest_messaging webhook_provider:
//
//   - Inbound authenticates with a JWKS-validated RS256 token, not a shared
//     secret. rest_messaging's provider seam hands its verifier a stored secret
//     string, which an app id is not.
//   - Outbound goes to a serviceUrl LEARNED from each inbound activity and
//     regional per conversation, not to a URL declared in the spec. There is no
//     field in RestMessagingSpec that can hold it.
//   - Poll url, list path, field map and cursor are all meaningless here, so
//     folding it in would turn that spec into a union type.
//
// What it does reuse is everything past the front door: the bridges app maps an
// activity onto the same hookRequest contract, so channel binding, dedup,
// dispatch, threads, retention and the loop guard all apply unchanged.
//
// Approval is MANDATORY (no ConnectorAutoApprover): it routes a tenant's
// conversations into an agent and posts back under the bot's own identity.
//
// WHAT IT BUYS OVER THE GRAPH PATH. Graph's app-only permissions can read
// channel messages (behind Microsoft's protected-API gate) but cannot SEND
// them, and reach chats barely at all. A Bot Framework bot answers in channels,
// group chats and 1:1 DMs, and can open a conversation it was not spoken to
// first -- which is the capability an agent that reports on its own schedule
// actually needs.
package core

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cmcoffee/gohort/core/jwks"
)

// BotFrameworkConnectorKind is the Kind value for a Bot Framework bridge.
const BotFrameworkConnectorKind = "bot_framework"

const (
	// botFrameworkIssuer is the iss every inbound activity token carries.
	botFrameworkIssuer = "https://api.botframework.com"

	// botFrameworkMetadataURL is the OIDC document naming the key set those
	// tokens are signed with.
	botFrameworkMetadataURL = "https://login.botframework.com/v1/.well-known/openidconfiguration"

	// botFrameworkServiceHost is the public-cloud host every regional serviceUrl
	// sits under (the region is a path segment: /amer/, /emea/, /apac/). Sovereign
	// clouds differ, which is what BotFrameworkSpec.ServiceHost is for.
	botFrameworkServiceHost = "https://smba.trafficmanager.net"

	// botFrameworkDefaultService namespaces the BridgeKey and outbox.
	botFrameworkDefaultService = "teams"
)

// botFrameworkChannelTypes is the set Teams puts on conversation.conversationType.
var botFrameworkChannelTypes = map[string]bool{
	"personal":  true, // 1:1 DM with the bot
	"channel":   true, // a team channel
	"groupChat": true, // a group or meeting chat
}

// BotFrameworkSpec is the Spec payload for a bot_framework connector. Nothing
// here is secret: the app id is public (it is the token audience), and the
// client secret lives in the SecureAPI credential, referenced by name.
type BotFrameworkSpec struct {
	Service string `json:"service,omitempty"` // namespaces BridgeKey + outbox (default "teams")

	// AppID is the Entra application (client) id registered as the bot. Inbound
	// tokens must carry it as their audience, which is what separates this
	// deployment's traffic from every other bot the same issuer signs for.
	AppID string `json:"app_id"`

	// TenantID pins a single-tenant bot. Empty means multi-tenant.
	TenantID string `json:"tenant_id,omitempty"`

	// Credential is a registered SecureAPI oauth2/client_credentials credential
	// used for OUTBOUND only. Its token endpoint is the botframework.com tenant
	// on login.microsoftonline.com, and its URL allow-list must cover ServiceHost.
	Credential string `json:"credential"`

	// ServiceHost bounds where replies may be sent. It is checked against the
	// credential's allow-list at approval time, and it exists as a field so a
	// sovereign cloud can point somewhere other than the public one.
	ServiceHost string `json:"service_host,omitempty"`

	// ReplyInThread threads a channel reply under the message that triggered it
	// (replyToId) rather than posting at the conversation root.
	ReplyInThread bool `json:"reply_in_thread,omitempty"`

	// AcceptChannelTypes limits which Teams surfaces route inbound: personal,
	// channel, groupChat. Empty accepts all three. The Teams app manifest is the
	// real gate; this is a second one an admin can tighten without asking anyone
	// to re-upload a manifest.
	AcceptChannelTypes []string `json:"accept_channel_types,omitempty"`
}

// botFrameworkVerifier is shared by every bot_framework connector: one issuer,
// one key set, so one cache. Package-level rather than per-connector so a
// deployment with several bots does not fetch the same keys several times.
var botFrameworkVerifier = &jwks.Verifier{
	MetadataURL: botFrameworkMetadataURL,
	Issuer:      botFrameworkIssuer,
	Name:        "bot framework",
}

// BotFrameworkVerifier returns the shared verifier for inbound activity tokens.
// The bridges app calls Verify(ctx, token, spec.AppID) on every request that
// arrives at a bot_framework route, and may read a verifying key's
// "endorsements" member (via KeyByID) to insist the key is endorsed for the
// msteams channel.
func BotFrameworkVerifier() *jwks.Verifier { return botFrameworkVerifier }

func init() { RegisterConnectorKind(BotFrameworkConnectorKind, botFrameworkHandler{}) }

type botFrameworkHandler struct{}

func (botFrameworkHandler) parse(c Connector) (BotFrameworkSpec, error) {
	var s BotFrameworkSpec
	if len(c.Spec) > 0 {
		if err := json.Unmarshal(c.Spec, &s); err != nil {
			return s, fmt.Errorf("bad bot_framework spec: %w", err)
		}
	}
	s.Service = strings.ToLower(strings.TrimSpace(s.Service))
	if s.Service == "" {
		s.Service = botFrameworkDefaultService
	}
	s.AppID = strings.TrimSpace(s.AppID)
	s.TenantID = strings.TrimSpace(s.TenantID)
	s.Credential = strings.TrimSpace(s.Credential)
	s.ServiceHost = strings.TrimRight(strings.TrimSpace(s.ServiceHost), "/")
	if s.ServiceHost == "" {
		s.ServiceHost = botFrameworkServiceHost
	}
	return s, nil
}

func (h botFrameworkHandler) Validate(c Connector) error {
	if strings.TrimSpace(c.Owner) == "" {
		return fmt.Errorf("bot_framework requires an owner (the user whose channel agents handle the conversations)")
	}
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	if !connectorNameRE.MatchString(s.Service) {
		return fmt.Errorf("service %q must be tool-namespace-safe (letters, digits, underscore)", s.Service)
	}
	if s.AppID == "" {
		return fmt.Errorf("app_id is required (the Entra application id of the Azure Bot registration; it is the token audience, not a secret)")
	}
	if s.Credential == "" {
		return fmt.Errorf("credential is required (a registered SecureAPI oauth2 credential for outbound replies)")
	}
	for _, ct := range s.AcceptChannelTypes {
		if !botFrameworkChannelTypes[strings.TrimSpace(ct)] {
			return fmt.Errorf("unknown accept_channel_types value %q (personal, channel, groupChat)", ct)
		}
	}
	if !strings.HasPrefix(s.ServiceHost, "https://") {
		return fmt.Errorf("service_host must be https (got %q)", s.ServiceHost)
	}

	// Everything above is structural: it can be judged from the spec alone. Only
	// now do the checks that depend on what happens to be in the store, so an
	// author with a typo hears about the typo instead of being sent off to
	// create a credential first and told about it on the way back.
	if exists, _, _ := Secure().CredentialStatus(s.Credential); !exists {
		return fmt.Errorf("no credential named %q — draft it first (draft_oauth_credential) and have the admin enable it", s.Credential)
	}
	return h.checkOutboundReach(s)
}

// checkOutboundReach verifies the credential is actually ALLOWED to post to a
// Bot Framework serviceUrl.
//
// Worth failing an approval over, because the failure it prevents is the
// confusing kind: the bridge comes up, receives messages, dispatches them, and
// then every reply is refused by our own allow-list rather than by Microsoft.
// The region is a path segment under one host, so a BaseURL of the host with no
// endpoint list covers every region at once.
func (botFrameworkHandler) checkOutboundReach(s BotFrameworkSpec) error {
	cred, ok := Secure().Load(s.Credential)
	if !ok {
		return nil // CredentialStatus already reported existence; nothing to check against
	}
	if botFrameworkOutboundReachable(cred, s.ServiceHost) {
		return nil
	}
	return fmt.Errorf("credential %q is not allowed to reach %s — set its base URL to %q and leave its endpoint list empty, so every region (/amer/, /emea/, /apac/, …) is covered",
		s.Credential, botFrameworkProbeURL(s.ServiceHost), s.ServiceHost)
}

// botFrameworkProbeURL is a representative outbound target: the activities
// endpoint of one region. Any real serviceUrl differs only in the region
// segment and the conversation id, both of which sit under the same host.
func botFrameworkProbeURL(serviceHost string) string {
	return strings.TrimRight(serviceHost, "/") + "/amer/v3/conversations/probe/activities"
}

// botFrameworkOutboundReachable reports whether the credential's own allow-list
// would pass a reply through.
func botFrameworkOutboundReachable(cred SecureCredential, serviceHost string) bool {
	return urlAllowedByCredential(cred, botFrameworkProbeURL(serviceHost))
}

// Materialize provisions the server-side routing key so the service appears in
// the Bridges dashboard and owner resolution works, then starts the bridge's
// outbound loop and route binding. Idempotent: the bridges side stops any prior
// run for this connector first, so a re-approve or a startup reload is safe.
//
// Unlike the graph webhook path there is no subscription to create or renew --
// Bot Framework routing is configured once in Azure and does not expire -- so
// nothing here can fail because of a clock.
func (h botFrameworkHandler) Materialize(c Connector) error {
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	if err := provisionServiceBridge(c.Owner, s.Service, true); err != nil {
		return fmt.Errorf("server bridge key: %w", err)
	}
	return startBotBridge(c, true)
}

// Teardown stops the bridge. The service's BridgeKey deliberately stays: a
// sibling connector on the same service may still need it, and an admin can
// delete the key from the dashboard.
func (h botFrameworkHandler) Teardown(c Connector) error {
	return startBotBridge(c, false)
}

func (h botFrameworkHandler) Summary(c Connector) string {
	s, err := h.parse(c)
	if err != nil {
		return "bot framework bridge (unreadable spec)"
	}
	scope := "personal, channel, groupChat"
	if len(s.AcceptChannelTypes) > 0 {
		scope = strings.Join(s.AcceptChannelTypes, ", ")
	}
	tenancy := "multi-tenant"
	if s.TenantID != "" {
		tenancy = "tenant " + s.TenantID
	}
	return fmt.Sprintf("bot framework (%s, %s): %s activities → %s channel agents, replies via credential %s at /bridges/api/bot/%s",
		s.AppID, tenancy, scope, s.Service, s.Credential, c.Name)
}

// --- bridges-side runner seam ------------------------------------------------
//
// Same shape as RegisterMessagingPoller: core owns the connector lifecycle, the
// bridges app owns the route binding and the outbound delivery loop, and
// registers itself at route time when its store is live. With nothing
// registered (bridges app not loaded) the kind validates and provisions its key
// but stays inert.

// botBridgeFn starts (start=true) or stops (start=false) the bridges-side half
// of a bot_framework connector. Unexported deliberately: every caller passes a
// func literal, so naming the type in core's namespace buys nothing and costs
// a symbol in 548 dot-importing files.
type botBridgeFn func(c Connector, start bool) error

var botBridge botBridgeFn

// RegisterBotBridge installs the bridges-side runner. Call once at
// route-registration time.
func RegisterBotBridge(fn func(c Connector, start bool) error) { botBridge = fn }

func startBotBridge(c Connector, start bool) error {
	if botBridge == nil {
		if start {
			Warn("[connector] no bot bridge registered — bot_framework %q inert until the bridges app loads", c.Name)
		}
		return nil
	}
	return botBridge(c, start)
}

// Accepts reports whether an activity from the given Teams
// conversationType should route. Lives here with the spec that decides it so
// the bridges app does not restate the rule. An unset list accepts everything,
// which is what a connector authored before the field existed means.
func (s BotFrameworkSpec) Accepts(conversationType string) bool {
	if len(s.AcceptChannelTypes) == 0 {
		return true
	}
	for _, ct := range s.AcceptChannelTypes {
		if strings.EqualFold(strings.TrimSpace(ct), strings.TrimSpace(conversationType)) {
			return true
		}
	}
	return false
}
