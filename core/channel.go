package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
)

// Channel binds a messaging surface — a Service transport (imessage /
// telegram / slack) plus an address scope within it — to an orchestrate
// Agent. A message arriving on the channel runs the bound agent's loop;
// the reply goes back out the same surface. Phantom is the transport (it
// POSTs inbound and drains the outbox per Service); the binding here says
// WHICH agent handles a given channel. See docs/channels-and-agents.md.
//
// The binding is authoritative on the Channel (AgentID), so routing an
// inbound is a single lookup and "one channel, one agent" is an
// invariant rather than a convention. An agent's "attached channels" is
// the inverse view (ListChannelsForAgent), which is what the agent
// editor surfaces.
//
// Phase 1 (this) is data model + attach only — nothing routes inbound to
// the bound agent yet. Phase 2 wires ChannelForInbound into the inbound
// path. See [[project_channels_to_agents]].
type Channel struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`                 // gohort user who owns this channel
	Name        string `json:"name,omitempty"`        // friendly label
	Description string `json:"description,omitempty"` // what this interface is for
	// A Channel is the INTERFACE — the pipe to/from the bound agent's LLM. On
	// its own it's inert config (name/description/direction/auto-reply/
	// gatekeeper); it does nothing until a SOURCE is hooked in. Service +
	// Address are that hook: the transport the messages flow over. Empty
	// Service = no source hooked yet (inert) — nothing routes until one is.
	Service string `json:"service,omitempty"` // transport id: imessage / telegram / slack ("" = inert, no source)
	// Address scopes the binding within the service: "" means the WHOLE
	// service (every contact/room routes to AgentID); a specific value
	// (a handle / room id) binds just that conversation, overriding the
	// whole-service default.
	Address   string `json:"address,omitempty"`
	AgentID   string `json:"agent_id"`             // the orchestrate agent bound to this channel
	AutoReply bool   `json:"auto_reply,omitempty"` // channel-layer policy: answer inbound automatically (vs record-only)
	// Direction sets which way messages flow on this channel:
	//   "inbound"       — receives input; the agent processes it but does NOT
	//                     reply back out on this surface (a response, if any,
	//                     goes elsewhere — RespondsOn, a later slice).
	//   "bidirectional" — receives input and replies on the same surface. The
	//                     default; "" is treated as this.
	//   "outbound"      — the agent only SENDS here; inbound is not processed.
	Direction string `json:"direction,omitempty"`
	// AuthorizedSenders is an allow-list of agent IDs — OTHER than the bound
	// AgentID — permitted to deliver to this channel WITHOUT the proactive-send
	// approval queue. It's how a parent grants a sub-agent (a different AgentID)
	// the right to post to the parent's group channel: the sub-agent's send is
	// treated as pre-authorized for this conversation instead of queuing. Empty =
	// only the bound agent (and in-thread replies) deliver without approval.
	AuthorizedSenders []string `json:"authorized_senders,omitempty"`
	// Gatekeeper is this channel's own wake rule, layered on top of the
	// deployment-wide overall gatekeeper. A cheap pre-agent filter: the
	// transport evaluates it before spinning up the bound agent, and skips
	// the run on a no. Empty = no per-channel rule (the overall still applies).
	Gatekeeper string `json:"gatekeeper,omitempty"`
	// AgentBound marks a channel the bound AGENT created via its own
	// request_thread_binding (owner-approved), as opposed to an
	// owner-configured channel. Only AgentBound channels may be toggled
	// (set_thread_wake) or torn down (release_thread_binding) by the agent
	// itself — it can never touch its foundational channels or another agent's.
	AgentBound bool   `json:"agent_bound,omitempty"`
	// TagOverride / TagDisabled are the per-channel layer of the outbound name
	// tag (the "[Assistant] …" prefix an agent adds to distinguish its messages
	// from the owner's own). TagOverride, when set, replaces the agent/global
	// name for THIS channel only (most specific wins). TagDisabled turns the tag
	// off on this channel even when the bound agent opted in — e.g. a private
	// thread where the name prefix reads as noise. Both are inert unless the
	// bound agent enabled tagging (AgentRecord.TagName).
	TagOverride string `json:"tag_override,omitempty"`
	TagDisabled bool   `json:"tag_disabled,omitempty"`
	Created     string `json:"created,omitempty"`
}

// Channel flow directions (Channel.Direction).
const (
	DirectionInbound       = "inbound"       // input only; agent processes, no reply on this surface
	DirectionBidirectional = "bidirectional" // input + reply on the same surface (default)
	DirectionOutbound      = "outbound"      // output only; agent sends here, inbound not processed
)

// DefaultDMGatekeeperRule is the default per-channel wake ruleset seeded onto a
// new 1:1 DM channel — both an agent-initiated thread binding and an inbound DM
// connection the owner attaches. Two rules ship right out of the gate, each on
// its own line so the gatekeeper's rule enumerator numbers them as separate
// OR-triggers: (1) the agent is addressed by name, and (2) the message is a
// reply/continuation of something directed at the agent. The negative guardrail
// is folded into rule 2's line so it is not itself numbered as a wake trigger.
// (In a 1:1 every inbound is "directed at the agent", so rule 2 naturally admits
// ordinary 1:1 traffic; in a group it correctly narrows to messages aimed at the
// agent.)
const DefaultDMGatekeeperRule = "WAKE when the incoming message mentions or addresses the agent by name — including common nicknames or obvious typos of that name.\n" +
	"WAKE when the incoming message is a fragment, direct reply, or continuation of a message that was directed at the agent specifically — it answers or clearly follows up on something the agent said or was asked in this thread. Otherwise — unrelated topics, or an exchange between other people the agent isn't part of — stay silent (recorded only)."

// BridgeService describes a known transport. Adding a new bridge is, on the
// gohort side, ONE entry here: the routing id is the lowercase map key (stays
// lowercase everywhere internally); DisplayName is the brand label shown to the
// user; RendersMarkdown reports whether the surface renders markdown — when
// false the outbound chokepoint flattens **bold** / # / `code` to plain text so
// they don't leak as literal punctuation. The connector (the external daemon
// that speaks /api/hook + /api/poll) is the only other piece.
type BridgeService struct {
	DisplayName     string
	RendersMarkdown bool
}

// bridgeServices is the registry of known transports. Unknown ids fall back to
// the raw id for display and plain-text (RendersMarkdown=false) for delivery.
var bridgeServices = map[string]BridgeService{
	"imessage": {"iMessage", false},
	"sms":      {"SMS", false},
	// Telegram renders markdown, but only when the connector sends parse_mode
	// MarkdownV2 with proper escaping. Default false = gohort delivers plain
	// text (always safe); flip to true once a connector implements MarkdownV2.
	"telegram": {"Telegram", false},
	"slack":    {"Slack", true},
	"whatsapp": {"WhatsApp", false},
	"signal":   {"Signal", false},
	"discord":  {"Discord", true},
	"email":    {"Email", false},
}

// warnedUnknownServices dedupes the unknown-transport warning so a misspelled or
// not-yet-registered service id is surfaced once, not on every message.
var warnedUnknownServices sync.Map

// lookupBridgeService resolves a transport id to its registry entry. On an
// unknown (non-empty) id it warns ONCE — so a typo or an omission from
// bridgeServices is visible in the log instead of silently degrading to raw-id
// display and plain-text delivery.
func lookupBridgeService(service string) (BridgeService, bool) {
	id := strings.ToLower(strings.TrimSpace(service))
	if svc, ok := bridgeServices[id]; ok {
		return svc, true
	}
	if id != "" {
		if _, seen := warnedUnknownServices.LoadOrStore(id, true); !seen {
			Warn("[channel] unknown transport service %q — falling back to raw-id display + plain-text delivery; add it to bridgeServices if it should render markdown", id)
		}
	}
	return BridgeService{}, false
}

// ServiceDisplayName returns the brand-correct display label for a transport id
// (e.g. "imessage" → "iMessage"), falling back to the id unchanged for unknown
// services. Use anywhere a service is shown to a user.
func ServiceDisplayName(service string) string {
	if svc, ok := lookupBridgeService(service); ok {
		return svc.DisplayName
	}
	return strings.TrimSpace(service)
}

// ServiceRendersMarkdown reports whether a transport renders markdown formatting,
// so the outbound chokepoint keeps it instead of flattening to plain text.
// Conservative: unknown services get plain text (and a one-time warning).
func ServiceRendersMarkdown(service string) bool {
	svc, _ := lookupBridgeService(service)
	return svc.RendersMarkdown
}

// LooksLikeHandle reports whether a string is phone/email-shaped: an email
// (contains "@") or a phone-like token (only + - ( ) space and digits, with at
// least 5 digits). Used so a whole-service channel can address a brand-new
// recipient not yet in any thread. This is the CANONICAL handle test shared by
// the transport (bridges) and the agent tools (orchestrate) — keeping one
// definition here so both sides agree on what counts as a handle (two divergent
// copies previously disagreed, e.g. on "3 apples").
func LooksLikeHandle(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.Contains(s, "@") {
		return true // email
	}
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '+' || r == '-' || r == '(' || r == ')' || r == ' ':
		default:
			return false
		}
	}
	return digits >= 5
}

// ChannelDirection returns a channel's flow direction, defaulting to
// bidirectional when unset so existing bindings behave as before.
func ChannelDirection(ch Channel) string {
	if ch.Direction == "" {
		return DirectionBidirectional
	}
	return ch.Direction
}

const channelsTable = "channels"

func channelKey(owner, id string) string { return owner + ":" + id }

// NewChannelID returns a random, unguessable channel id.
func NewChannelID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SaveChannel upserts a channel binding (requires Owner + ID).
func SaveChannel(db Database, ch Channel) {
	if db == nil || ch.Owner == "" || ch.ID == "" {
		return
	}
	db.Set(channelsTable, channelKey(ch.Owner, ch.ID), ch)
}

// GetChannel fetches one channel, owner-scoped.
func GetChannel(db Database, owner, id string) (Channel, bool) {
	if db == nil || owner == "" || id == "" {
		return Channel{}, false
	}
	var ch Channel
	if !db.Get(channelsTable, channelKey(owner, id), &ch) {
		return Channel{}, false
	}
	return ch, true
}

// ListChannels returns all of the owner's channels.
func ListChannels(db Database, owner string) []Channel {
	if db == nil || owner == "" {
		return nil
	}
	prefix := owner + ":"
	var out []Channel
	for _, k := range db.Keys(channelsTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var ch Channel
		if db.Get(channelsTable, k, &ch) {
			out = append(out, ch)
		}
	}
	return out
}

// ListChannelsForAgent returns the owner's channels bound to a given agent —
// the inverse view that backs the agent editor's "attached channels".
func ListChannelsForAgent(db Database, owner, agentID string) []Channel {
	var out []Channel
	for _, ch := range ListChannels(db, owner) {
		if ch.AgentID == agentID {
			out = append(out, ch)
		}
	}
	return out
}

// ChannelAllowsSender reports whether agentID is authorized to deliver to the
// owner's channel identified by chatID or handle WITHOUT the proactive-send
// approval queue — either it's the bound agent, or it's in the channel's
// AuthorizedSenders grant. Matches an exact Address binding (per-conversation) or
// a whole-service channel (Address==""). Used by the send path to let a granted
// sub-agent post to a parent's channel.
func ChannelAllowsSender(db Database, owner, chatID, handle, agentID string) bool {
	if agentID == "" {
		return false
	}
	for _, ch := range ListChannels(db, owner) {
		if ch.Address != "" && ch.Address != chatID && ch.Address != handle {
			continue // a per-conversation channel that isn't this conversation
		}
		if ch.AgentID == agentID {
			return true
		}
		for _, s := range ch.AuthorizedSenders {
			if s == agentID {
				return true
			}
		}
	}
	return false
}

// DeleteChannel removes a channel binding.
func DeleteChannel(db Database, owner, id string) {
	if db == nil {
		return
	}
	db.Unset(channelsTable, channelKey(owner, id))
}

// ChannelSessionKey returns the orchestrate session id a channel's inbound runs
// under. A per-contact channel (Address set) keys by the channel itself, so the
// agent keeps one stable thread for that contact AND the rail can show the
// binding as a row before any message has arrived. A whole-service channel
// (Address == "") has no single thread, so it falls back to per-chat keying via
// the supplied chatID. Inbound routing and the session rail must agree on this,
// so they both call here.
func ChannelSessionKey(ch Channel, chatID string) string {
	if ch.Address != "" {
		return "chan:" + ch.Address
	}
	return "chan:" + chatID
}

// ChannelForInbound resolves which channel — and therefore which agent —
// handles an inbound message on a service for an owner. An exact Address
// binding wins over a whole-service binding (Address==""), so a per-contact
// channel overrides the service-wide default. Returns false when nothing is
// bound. Phase 2 routing uses this; Phase 1 just stores the bindings.
// addresses are the inbound's candidate identifiers (sender handle, chat id,
// and any conversation aliases). A channel bound to ANY non-empty one matches —
// owner self-chats have an empty handle and group rooms vary by sender, but the
// chat id is stable, so matching on the set is what makes routing robust.
func ChannelForInbound(db Database, owner, service string, addresses ...string) (Channel, bool) {
	service = strings.TrimSpace(service)
	want := map[string]bool{}
	for _, a := range addresses {
		if a = strings.TrimSpace(a); a != "" {
			want[a] = true
		}
	}
	var wholeService *Channel
	for _, ch := range ListChannels(db, owner) {
		if ch.Service != service {
			continue
		}
		if ch.Address != "" && want[ch.Address] {
			return ch, true // exact-address binding wins
		}
		if ch.Address == "" {
			c := ch
			wholeService = &c
		}
	}
	if wholeService != nil {
		return *wholeService, true
	}
	return Channel{}, false
}

// --- Phase 2: routing inbound to the bound agent -----------------------------

// ChannelInbound is one inbound message routed to a channel's bound agent.
type ChannelInbound struct {
	Owner     string // the channel owner — whose agent runs and under whose store
	AgentID   string // the bound agent (channel.AgentID)
	SessionID string // per-contact session id, stable per conversation, so each contact accumulates its own thread under the agent
	// ChatID + Handle identify the inbound conversation itself, so the agent's
	// messaging tools can recognize a reply BACK to it as in-thread (no approval
	// gate) versus a proactive reach-out to someone else (gated). ChatID is the
	// transport chat id; Handle the sender's handle (empty for the owner / some
	// groups). Either may be empty; the recipient key derives from whichever is set.
	ChatID           string
	Handle           string
	SenderName       string   // the inbound message's author display name (falls back to handle) — the per-message sender in the transcript
	ConversationName string   // the conversation/room display name (the title editable on the transport side) — names the session
	Roster           []string // known participant display names for a GROUP conversation, so the agent is handed who-is-here up front instead of having to call list_members. Empty for 1:1 chats or unknown rosters.
	Text             string   // the inbound message text
	Images           []string // base64 inbound attachments (a contact's photo) — delivered to the agent as multimodal content it can see this turn
	Videos           []string // base64 inbound video clips (e.g. an mp4 in a text) — the runner samples frames into the multimodal stream so the vision model can analyze them (it can't ingest raw mp4)
	Audios           []string // base64 inbound audio (a voice memo / m4a) — the runner transcribes it so the agent gets the spoken words (it can't ingest raw audio)
	// StatusCallback, when set, receives mid-turn status pings (the agent's
	// send_status / progress notes) so the transport can deliver them ahead
	// of the final reply. nil = no status (graceful).
	StatusCallback func(string)
}

// ChannelReply is the bound agent's response for the transport to deliver
// back out the channel.
type ChannelReply struct {
	Text   string
	Images []string // base64 image attachments the agent produced this turn
	Videos []string // base64 video attachments (kept separate so the transport delivers them as video, not mislabeled images)
	// AgentName is the display name of the bound agent that produced this reply,
	// so the transport can prefix an outbound name tag (opt-in) letting the
	// recipient tell an agent's message apart from the owner's own texts. Empty
	// when unresolved; the transport then leaves the message untagged.
	AgentName string
}

// ChannelAgentRunnerFunc runs a channel's bound agent on one inbound message
// and returns its reply. Registered by orchestrate (it owns the agent loop);
// the transport (phantom) calls it via RunChannelAgent when an inbound
// matches a bound Channel. Mirrors the standing-runner seam.
type ChannelAgentRunnerFunc func(ctx context.Context, in ChannelInbound) (ChannelReply, error)

var (
	channelAgentRunner   ChannelAgentRunnerFunc
	channelAgentRunnerMu sync.RWMutex
)

// RegisterChannelAgentRunner installs the agent-execution closure. Call once
// at startup from the agent-aware package (orchestrate).
func RegisterChannelAgentRunner(fn ChannelAgentRunnerFunc) {
	channelAgentRunnerMu.Lock()
	channelAgentRunner = fn
	channelAgentRunnerMu.Unlock()
}

// ChannelAgentRunnerReady reports whether an agent runner is registered, so
// the transport can fall back to its own engine when orchestrate isn't loaded.
func ChannelAgentRunnerReady() bool {
	channelAgentRunnerMu.RLock()
	defer channelAgentRunnerMu.RUnlock()
	return channelAgentRunner != nil
}

// RunChannelAgent runs the bound agent on an inbound message. Errors when no
// runner is registered.
func RunChannelAgent(ctx context.Context, in ChannelInbound) (ChannelReply, error) {
	channelAgentRunnerMu.RLock()
	fn := channelAgentRunner
	channelAgentRunnerMu.RUnlock()
	if fn == nil {
		return ChannelReply{}, errors.New("no channel agent runner registered")
	}
	return fn(ctx, in)
}

// ChannelGatekeeperFunc evaluates whether an inbound message should wake the
// bound agent. It returns true to ALLOW (run the agent) and false to BLOCK
// (record only, no run). The agent-aware package (orchestrate) owns the logic
// because the decision reads the agent's name and the worker LLM; the transport
// (bridges) calls it via ChannelGatekeeperAllow before dispatching. Mirrors the
// ChannelAgentRunnerFunc seam.
type ChannelGatekeeperFunc func(ctx context.Context, in ChannelInbound) bool

var (
	channelGatekeeper   ChannelGatekeeperFunc
	channelGatekeeperMu sync.RWMutex
)

// RegisterChannelGatekeeper installs the wake-rule evaluator. Call once at
// startup from orchestrate.
func RegisterChannelGatekeeper(fn ChannelGatekeeperFunc) {
	channelGatekeeperMu.Lock()
	channelGatekeeper = fn
	channelGatekeeperMu.Unlock()
}

// ChannelGatekeeperAllow reports whether an inbound may wake its bound agent.
// Fails OPEN (allow) when no evaluator is registered — orchestrate not loaded
// means there are no channel agents to gate anyway. The evaluator itself fails
// open when no rules are configured and fails closed only when rules exist but
// could not be evaluated (see orchestrate channel_gatekeeper.go).
func ChannelGatekeeperAllow(ctx context.Context, in ChannelInbound) bool {
	channelGatekeeperMu.RLock()
	fn := channelGatekeeper
	channelGatekeeperMu.RUnlock()
	if fn == nil {
		return true
	}
	return fn(ctx, in)
}

// ChannelSilentRecorderFunc mirrors an inbound that the gatekeeper BLOCKED
// (recorded-only, no wake) into the bound agent's own conversation transcript,
// so the message shows in the agent's chat and is in-context on its next wake —
// the agent reads along even when it stays silent, instead of waking amnesiac.
// The agent-aware package (orchestrate) owns it because the transcript lives in
// the agent's per-user store; the transport (bridges) calls it via
// RecordChannelSilent at the block point. Mirrors the ChannelGatekeeperFunc seam.
type ChannelSilentRecorderFunc func(in ChannelInbound)

var (
	channelSilentRecorder   ChannelSilentRecorderFunc
	channelSilentRecorderMu sync.RWMutex
)

// RegisterChannelSilentRecorder installs the recorded-only mirror. Call once at
// startup from orchestrate.
func RegisterChannelSilentRecorder(fn ChannelSilentRecorderFunc) {
	channelSilentRecorderMu.Lock()
	channelSilentRecorder = fn
	channelSilentRecorderMu.Unlock()
}

// RecordChannelSilent mirrors a blocked inbound into the bound agent's
// transcript. No-op when no recorder is registered (orchestrate not loaded), so
// the transport can call it unconditionally at the block point.
func RecordChannelSilent(in ChannelInbound) {
	channelSilentRecorderMu.RLock()
	fn := channelSilentRecorder
	channelSilentRecorderMu.RUnlock()
	if fn == nil {
		return
	}
	fn(in)
}

// ChannelOverflowFunc absorbs an agent's reply that a transport COULDN'T deliver
// (a bidirectional channel bound to a service with no output path) into the
// agent's cortex feed instead of stranding it in an undeliverable outbox. The
// cortex is a non-triggering awareness surface, so this never re-runs the agent —
// no reply→observe→reply loop. Owned by orchestrate (the cortex lives in the
// agent's per-user store); the transport calls OverflowChannelReply at the point
// it would otherwise enqueue an undeliverable reply.
type ChannelOverflowFunc func(in ChannelInbound, replyText string)

var (
	channelOverflow   ChannelOverflowFunc
	channelOverflowMu sync.RWMutex
)

// RegisterChannelOverflow installs the reply-overflow sink. Call once at startup
// from orchestrate.
func RegisterChannelOverflow(fn ChannelOverflowFunc) {
	channelOverflowMu.Lock()
	channelOverflow = fn
	channelOverflowMu.Unlock()
}

// OverflowChannelReply routes an undeliverable reply to the agent's cortex.
// Reports whether a sink handled it (false when orchestrate isn't loaded, so the
// transport can fall back to its previous behavior).
func OverflowChannelReply(in ChannelInbound, replyText string) bool {
	channelOverflowMu.RLock()
	fn := channelOverflow
	channelOverflowMu.RUnlock()
	if fn == nil {
		return false
	}
	fn(in, replyText)
	return true
}
