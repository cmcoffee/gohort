// Bridges is the messaging-transport layer: it routes inbound from a messaging
// service (iMessage, Telegram, Slack, …) to the agent bound on a Channel, and
// delivers the agent's reply back out. It is PURE TRANSPORT — no persona, no
// own LLM engine, no tools. The agent (orchestrate) owns all of that; a Channel
// (core) is the interface to it; a Bridge is the connector for one service.
//
// A Bridge is defined by its SERVICE and the contract it speaks to the server:
// POST inbound to /bridges/api/hook, poll /bridges/api/poll for outbound, and
// authenticate with a bridge key (which declares the service). HOW a bridge
// sources messages varies — iMessage runs as the gohort-desktop daemon
// (device-side, Mac-only); Telegram/Slack would be server-side pollers/webhooks
// — but to Bridges they're all just connectors speaking the same contract.
//
// Replaces phantom's transport slice; phantom's own-engine is left behind to
// retire. See [[project_channels_to_agents]].
package bridges

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterApp(new(Bridges))
	// A gatekeeper pre-filter (later) makes a worker LLM call; register a route
	// stage so it's tunable alongside the other apps.
	RegisterRouteStage(RouteStage{
		Key:     "app.bridges",
		Label:   "Bridges",
		Default: "worker (thinking)",
		Group:   "Apps",
		App:     "/bridges",
	})
}

// Bridges — the transport app. Dashboard-only (no CLI surface).
type Bridges struct {
	AppCore
}

func (T *Bridges) Name() string    { return "bridges" }
func (T *Bridges) WebPath() string { return "/bridges" }

// HubTab makes Bridges a member of the shared top-nav hub.
func (T *Bridges) HubTab() (string, int) { return "Bridges", 20 }
func (T *Bridges) WebName() string { return "Bridges" }
func (T *Bridges) WebDesc() string {
	return "Connect messaging services (iMessage, Telegram, …) to your channel agents."
}
func (T *Bridges) Desc() string { return "Apps: messaging transport — services → channels." }

// WebOrder places Bridges right after Agency on the dashboard (Agency is
// -1000, Knowledge is -800), ahead of the default-50 app grid.
func (T *Bridges) WebOrder() int { return -900 }

func (T *Bridges) Init() error { return T.Flags.Parse() }

func (T *Bridges) Main() error {
	Log("Bridges is a dashboard app. Start with:\n  gohort serve :8080")
	return nil
}

// --- storage tables ----------------------------------------------------------

const (
	bridgeKeysTable = "bridges_keys"   // BridgeKey records (auth + service declaration)
	outboxTable     = "bridges_outbox" // pending outbound items, drained by poll
	convosTable     = "bridges_convos" // chats seen — identity + members + aliases
	messagesTable   = "bridges_messages"

	// seenMsgTable is the inbound dedup set: chatID:msgID → RFC3339 first-seen
	// time, so the retention sweep has an age to expire on.
	//
	// A SEPARATE table from the original, which stored a bare int. kvlite decodes
	// through gob and DBase.Get routes a decode error to Critical — so reading a
	// legacy int into a string doesn't fail softly, it takes the process down.
	// A value type can therefore never be changed in place; the old table is
	// dropped once and the new one only ever holds the new type.
	seenMsgTable = "bridges_seen_msgs_v2"

	// legacySeenMsgTable is the pre-timestamp dedup set (chatID:msgID → 1),
	// dropped wholesale on first start. Its entries only answer "seen before",
	// and the staleness guard covers the one risk losing them creates.
	legacySeenMsgTable = "bridges_seen_msgs"
)

// ConvMember is one participant — the identity Bridges learns, the name the user
// can set, and alternate handles for the same person (so a contact reachable via
// phone + email resolves to one member).
type ConvMember struct {
	Handle  string   `json:"handle"`
	Name    string   `json:"name,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
}

// Convo is a chat Bridges has seen: identity, participants, and conversation-
// level alias handles (alternate ids for the SAME chat). No persona/engine.
type Convo struct {
	ChatID       string       `json:"chat_id"`
	Service      string       `json:"service"`
	Handle       string       `json:"handle,omitempty"`
	DisplayName  string       `json:"display_name,omitempty"`
	Members      []ConvMember `json:"members,omitempty"`
	AliasHandles []string     `json:"alias_handles,omitempty"`
	// Added marks a conversation the operator has explicitly curated — picked
	// from the incoming list or entered by number. The dashboard's managed
	// list shows only these; raw inbound stays in the "Add" picker until added.
	Added  bool   `json:"added,omitempty"`
	LastAt string `json:"last_at,omitempty"`
}

func (T *Bridges) getConvo(chatID string) (Convo, bool) {
	var c Convo
	ok := T.DB.Get(convosTable, chatID, &c)
	return c, ok
}

func (T *Bridges) saveConvo(c Convo) {
	if c.ChatID != "" {
		T.DB.Set(convosTable, c.ChatID, c)
	}
}

// deleteConvo removes a conversation and its stored thread — used when folding a
// duplicate chat into another (its id added as an alias on the keeper).
func (T *Bridges) deleteConvo(chatID string) {
	if chatID == "" {
		return
	}
	T.DB.Unset(convosTable, chatID)
	// One Drop instead of a walk over every message in every conversation.
	T.DB.Drop(chatMessagesTable(chatID))
}

// isGroupChat reports whether a chat id is a group room. iMessage marks the
// chat type in the id: ";+;" is a group (many members), ";-;" is a 1:1. A group
// has no single canonical handle — its identity is the stable chat id — so we
// must never treat a member's handle as the group's address.
func isGroupChat(chatID string) bool {
	return strings.Contains(chatID, ";+;")
}

// chatHandle extracts the raw handle from a 1:1 chat id. iMessage chat ids are
// "<account>;<type>;<handle>" where type "-" is a 1:1 (the trailing segment is
// the contact's handle) and "+" is a group (no single handle). Returns "" for
// groups. Lets a raw-handle alias the user typed ("+1650…") link to the chat-id
// form ("any;-;+1650…") so the two are recognized as the same person.
func chatHandle(chatID string) string {
	if chatID == "" || isGroupChat(chatID) {
		return ""
	}
	if i := strings.LastIndex(chatID, ";"); i >= 0 {
		return strings.TrimSpace(chatID[i+1:])
	}
	return strings.TrimSpace(chatID)
}

// inboundIdentities resolves every id/handle equivalent to an inbound message,
// following user-set aliases in BOTH directions and normalizing the chat-id ↔
// raw-handle forms. A channel bound to ANY of the person's ids then matches,
// regardless of which id the message arrived on or which convo the alias was
// added to. Without this: an alias added on the OTHER convo (the natural "this
// is also me" gesture) silently fails to route, and a raw-handle alias never
// matches a chat-id-form channel address. Only 1:1 convos cluster — a group is
// identified by its own chat id, never by a member, so it never joins a person.
func (T *Bridges) inboundIdentities(svc, chatID, handle string) []string {
	seen := map[string]bool{}
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			seen[s] = true
		}
	}
	add(handle)
	add(chatID)
	add(chatHandle(chatID))
	// A group inbound never clusters by member — match it by its own chat id.
	if !isGroupChat(chatID) {
		// Alias closure over 1:1 conversations. Repeat until the cluster stops
		// growing so transitive links (A↔B, B↔C) all collapse. Bounded by the
		// convo count; trivial at personal-assistant scale.
		for grew := true; grew; {
			grew = false
			for _, c := range T.listConvos() {
				if isGroupChat(c.ChatID) {
					continue
				}
				if svc != "" && c.Service != "" && c.Service != svc {
					continue
				}
				ids := append([]string{c.ChatID, c.Handle, chatHandle(c.ChatID)}, c.AliasHandles...)
				linked := false
				for _, id := range ids {
					if id = strings.TrimSpace(id); id != "" && seen[id] {
						linked = true
						break
					}
				}
				if !linked {
					continue
				}
				for _, id := range ids {
					if id = strings.TrimSpace(id); id != "" && !seen[id] {
						seen[id], grew = true, true
					}
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out
}

// upsertConvo records a conversation + learns the sender as a participant.
// senderName names the PERSON at handle (drives the handle→name roster a
// group-bound agent reads); convoName is the group/chat title and names the
// thread. They are distinct: in a named group every message shares one
// convoName but each has its own senderName — conflating them (the old single
// arg) stamped the group's name onto every participant.
func (T *Bridges) upsertConvo(service, chatID, handle, senderName, convoName string) {
	if strings.TrimSpace(chatID) == "" {
		return
	}
	c, _ := T.getConvo(chatID)
	c.ChatID = chatID
	c.Service = service
	// A group has no canonical handle — its identity is the stable chat id.
	// Clear any handle a pre-fix inbound clobbered in, so the address derives
	// from the chat id and the dashboard stops showing a stale member binding
	// as "connected".
	if isGroupChat(chatID) {
		c.Handle = ""
	}
	if h := strings.TrimSpace(handle); h != "" {
		c.Members = upsertMember(c.Members, h, senderName) // learn the participant by their OWN name
		if !isGroupChat(chatID) {
			c.Handle = h // only a 1:1 chat has a single canonical handle
		}
	}
	// Title the thread: a group's title is convoName; a 1:1 is named for its one
	// contact (the sender). Never let a group inbound rename the thread after the
	// last person who spoke.
	if title := strings.TrimSpace(convoName); title != "" {
		c.DisplayName = title
	} else if s := strings.TrimSpace(senderName); s != "" && !isGroupChat(chatID) {
		c.DisplayName = s
	}
	c.LastAt = now()
	T.saveConvo(c)
}

// upsertMember adds/updates a participant by handle, learning a name when one is
// supplied. Matches on the handle OR any of its aliases.
func upsertMember(members []ConvMember, handle, name string) []ConvMember {
	handle, name = strings.TrimSpace(handle), strings.TrimSpace(name)
	if handle == "" {
		return members
	}
	for i := range members {
		// Case-insensitive match, symmetric with resolveSender's fold lookup —
		// otherwise "Alex" arriving for a stored "alex" appends a DUPLICATE member
		// instead of updating the existing one.
		if strings.EqualFold(members[i].Handle, handle) || containsFold(members[i].Aliases, handle) {
			// Fill the name only when we don't have one yet — first real name
			// wins. Never overwrite a learned or dashboard-edited name with a
			// later, possibly-inconsistent inbound display name (that clobbered
			// user corrections and made one person read as two).
			if members[i].Name == "" && name != "" && name != handle {
				members[i].Name = name
			}
			return members
		}
	}
	m := ConvMember{Handle: handle}
	if name != "" && name != handle {
		m.Name = name
	}
	return append(members, m)
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// syncMembersFromHistory harvests participants from the stored thread — every
// handle that's ever sent becomes a member (carrying any name seen) — so the
// roster is complete even for senders we didn't catch live. Derive-on-read,
// mirroring phantom. Returns the (possibly updated, persisted) Convo.
func (T *Bridges) syncMembersFromHistory(chatID string) Convo {
	c, _ := T.getConvo(chatID)
	c.ChatID = chatID
	// A CONVERSATION ALIAS handle is an alternate id for the CHAT itself (a
	// folded-in duplicate reachable via another phone/email), NOT a person — so
	// it must never enter the participant roster. Skip alias handles when
	// harvesting senders, and prune any that were harvested as a member before
	// the user marked them an alias. Without this, folding a chat in by its email
	// makes that email show up as a bogus participant.
	isAlias := func(h string) bool {
		if h = strings.TrimSpace(h); h == "" {
			return false
		}
		for _, a := range c.AliasHandles {
			if strings.EqualFold(strings.TrimSpace(a), h) {
				return true
			}
		}
		return false
	}
	changed := false
	kept := c.Members[:0]
	for _, m := range c.Members {
		if isAlias(m.Handle) {
			changed = true // drop a member that's really a conversation alias
			continue
		}
		kept = append(kept, m)
	}
	c.Members = kept
	before := len(c.Members)
	for _, m := range T.recentMessages(chatID, 0) { // 0 = entire thread
		if m.Role == "user" && strings.TrimSpace(m.Handle) != "" && !isAlias(m.Handle) {
			c.Members = upsertMember(c.Members, m.Handle, m.DisplayName)
		}
	}
	if changed || len(c.Members) != before {
		T.saveConvo(c)
	}
	return c
}

// --- message thread (for the dashboard view) ---------------------------------

// StoredMessage is one inbound/outbound message kept for the thread view.
type StoredMessage struct {
	ID          string `json:"id"`
	ChatID      string `json:"chat_id"`
	Role        string `json:"role"` // "user" | "assistant"
	Handle      string `json:"handle,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Text        string `json:"text"`
	Timestamp   string `json:"timestamp"`
}

func (T *Bridges) storeMessage(m StoredMessage) {
	if m.ChatID == "" || m.ID == "" || strings.TrimSpace(m.Text) == "" {
		return
	}
	if m.Timestamp == "" {
		m.Timestamp = now()
	}
	T.DB.Set(chatMessagesTable(m.ChatID), m.ID, m)
}

// recentMessages returns a conversation's last n messages, oldest first (n<=0
// for the whole thread). Reads only this chat's table — see retention.go for why
// the transcript is stored per chat rather than in one flat table.
func (T *Bridges) recentMessages(chatID string, n int) []StoredMessage {
	var out []StoredMessage
	table := chatMessagesTable(chatID)
	for _, k := range T.DB.Keys(table) {
		var m StoredMessage
		if T.DB.Get(table, k, &m) {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp < out[j].Timestamp })
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

func (T *Bridges) listConvos() []Convo {
	var out []Convo
	for _, k := range T.DB.Keys(convosTable) {
		var c Convo
		if T.DB.Get(convosTable, k, &c) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt > out[j].LastAt })
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// --- Bridge key: a connector's credential + the service it speaks -------------

// BridgeKey authenticates one connector (e.g. the gohort-desktop iMessage
// daemon) and declares which Service it bridges. The key's LastSeen drives the
// dashboard's connection status.
type BridgeKey struct {
	ID       string `json:"id"`
	Name     string `json:"name"`    // friendly label, e.g. "Craig's MacBook"
	Key      string `json:"key"`     // the secret; shown once on creation
	Owner    string `json:"owner"`   // gohort user this bridge belongs to
	Service  string `json:"service"` // "imessage", "telegram", … (the bridge's service id)
	Enabled  bool   `json:"enabled"` // per-bridge switch; create sets true. Disabled = inbound recorded, not routed/delivered
	Created  string `json:"created"`
	LastSeen string `json:"last_seen,omitempty"`
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// saveBridgeKey upserts a key.
func (T *Bridges) saveBridgeKey(k BridgeKey) { T.DB.Set(bridgeKeysTable, k.ID, k) }

// listBridgeKeys returns the owner's bridge keys (newest first).
func (T *Bridges) listBridgeKeys(owner string) []BridgeKey {
	var out []BridgeKey
	for _, id := range T.DB.Keys(bridgeKeysTable) {
		var k BridgeKey
		if T.DB.Get(bridgeKeysTable, id, &k) && k.Owner == owner {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out
}

// bridgeKeyOwner resolves a bridge-key secret to its owner username, READ-ONLY
// — no LastSeen stamp, no desktop-record creation. Registered as a core
// API-key validator (RegisterAPIKeyValidator) so userFromAPIKey and
// DesktopClientUser resolve bridge keys; those run on the hot path (every
// desktop-authed chat request consults DesktopClientUser), so this must not
// write. The core desktop key is resolved by its own validator, so this only
// needs to cover the bridges_keys store. Returns ("", false) on no match.
func (T *Bridges) bridgeKeyOwner(secret string) (string, bool) {
	secret = strings.TrimSpace(secret)
	if secret == "" || T.DB == nil {
		return "", false
	}
	// Constant-time, and every record is checked. Returning on the first
	// match leaks through timing how much of a guessed key was right, and the
	// scan is over the operator's own short list, so the cost is nothing next
	// to the property. Same reasoning core/peer_key.go's LookupPeerKey writes
	// down; bridge keys authenticate the same kind of caller and had been
	// comparing with ==.
	owner, found := "", false
	for _, id := range T.DB.Keys(bridgeKeysTable) {
		var k BridgeKey
		if !T.DB.Get(bridgeKeysTable, id, &k) || k.Owner == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(k.Key), []byte(secret)) == 1 {
			owner, found = k.Owner, true
		}
	}
	return owner, found
}

// bridgeKeyRevoker destroys a departing user's bridge keys. Core cannot know
// what a bridge key is, so deletion reaches this through the revoker registry
// (see core/revoke.go). Without it a removed account's keys kept authenticating
// the iMessage hook and the desktop WS bridge indefinitely.
type bridgeKeyRevoker struct{ app *Bridges }

func (bridgeKeyRevoker) CredentialKind() string { return "bridge keys" }

func (r bridgeKeyRevoker) RevokeUserCredentials(user string) int {
	if r.app == nil || r.app.DB == nil || strings.TrimSpace(user) == "" {
		return 0
	}
	n := 0
	for _, id := range r.app.DB.Keys(bridgeKeysTable) {
		var k BridgeKey
		if r.app.DB.Get(bridgeKeysTable, id, &k) && k.Owner == user {
			r.app.DB.Unset(bridgeKeysTable, id)
			n++
		}
	}
	return n
}

// validateBridgeKey resolves a secret to its key record, stamping LastSeen so
// the dashboard can show "connected N ago".
func (T *Bridges) validateBridgeKey(secret string) (BridgeKey, bool) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return BridgeKey{}, false
	}
	// Constant-time and no early exit, as in bridgeKeyOwner above. The
	// LastSeen write is deferred out of the loop so the matching branch does
	// no more work than a non-matching one.
	var match BridgeKey
	found := false
	for _, id := range T.DB.Keys(bridgeKeysTable) {
		var k BridgeKey
		if !T.DB.Get(bridgeKeysTable, id, &k) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(k.Key), []byte(secret)) == 1 {
			match, found = k, true
		}
	}
	if found {
		match.LastSeen = now()
		T.DB.Set(bridgeKeysTable, match.ID, match)
		return match, true
	}
	// Accept the core desktop key — the gohort-desktop daemon's auto-negotiated
	// credential — as the iMessage bridge, so the existing daemon authenticates
	// without minting a separate key. Backed by a PERSISTENT record (created on
	// first sight) so the desktop bridge shows in the dashboard and has its own
	// enable switch like any other bridge.
	if user, ok := LookupDesktopKey(secret); ok && user != "" {
		return T.desktopServiceBridge(user), true
	}
	return BridgeKey{}, false
}

// desktopServiceBridge returns the routing record for a user's desktop
// (iMessage) bridge, stamping LastSeen, creating it on first sight.
//
// ADOPTS an existing record for the service rather than always minting its own.
// Two paths create this row — the connector approve path (ensureServiceBridge)
// and this one — and only the former checked for an existing key first, so
// approving the messaging connector BEFORE the daemon ever connected left the
// dashboard with two iMessage bridges for one person: the daemon's live
// "Desktop" row and the connector's orphan, stuck at "never connected" because
// nothing ever authenticates with a secret that was minted server-side and shown
// to no one. One bridge, one status, one enable switch.
func (T *Bridges) desktopServiceBridge(user string) BridgeKey {
	const service = "imessage"
	id := "desktop:" + user
	var k BridgeKey
	switch {
	case T.DB.Get(bridgeKeysTable, id, &k):
		if k.Name == "Desktop (iMessage)" {
			k.Name = "Desktop" // migrate the old doubled-suffix name in place
		}
		// Prune orphans left by the ordering bug above: another record for the
		// same owner + service that has NEVER been seen. Never-seen means its
		// secret has authenticated nothing, so nothing breaks by dropping it —
		// and leaving it means the dashboard shows a permanent phantom
		// disconnect. A sibling that HAS been seen is a real second connector
		// (a second Mac with its own minted key); that one stays.
		for _, other := range T.listBridgeKeys(user) {
			if other.ID != id && other.Service == service && other.LastSeen == "" {
				T.DB.Unset(bridgeKeysTable, other.ID)
				Log("[bridges] dropped duplicate never-connected %s bridge %q for %s (superseded by the desktop bridge)", service, other.Name, user)
			}
		}
	default:
		// Adopt a record the connector path already created for this service so
		// the two stay one row; only mint when there's nothing to adopt. Name
		// carries no service suffix — the dashboard appends "(iMessage)" when it
		// renders the row, so "Desktop" shows as "Desktop (iMessage)".
		k = BridgeKey{ID: id, Name: "Desktop", Owner: user, Service: service, Enabled: true, Created: now()}
		for _, other := range T.listBridgeKeys(user) {
			if other.Service == service {
				k = other
				break
			}
		}
	}
	k.LastSeen = now()
	T.DB.Set(bridgeKeysTable, k.ID, k)
	return k
}

// --- Outbox: pending outbound, drained per-service by the poll ---------------

// OutboxItem is one message queued for a connector to deliver.
type OutboxItem struct {
	ID      string   `json:"id"`
	ChatID  string   `json:"chat_id"`
	Handle  string   `json:"handle,omitempty"`
	Service string   `json:"service"`
	Text    string   `json:"text,omitempty"`
	Images  []string `json:"images,omitempty"` // base64
	Videos  []string `json:"videos,omitempty"` // base64 video attachments — connector delivers as video, not image
	Type    string   `json:"type,omitempty"`   // "reply" | "status"
	// Agent is the display name of the agent that composed this outbound (the
	// bound channel agent for a reply, the calling agent for a proactive send),
	// set ONLY when that agent opted into signing its messages
	// (AgentRecord.TagName). enqueueOutbox prefixes it as a "[Agent] " name tag
	// so recipients can tell an agent's message from the owner's own texts.
	// Empty = the agent didn't opt in (or is unknown) → untagged.
	Agent   string   `json:"agent,omitempty"`
	// Owner is the gohort user this outbound belongs to, carried ONLY so
	// enqueueOutbox can resolve the bound channel for per-channel tag overrides.
	// Cleared before the item is stored/drained, so it never reaches a connector.
	Owner   string   `json:"-"`
	Created string   `json:"created"`
	// Seq is a per-process monotonic enqueue counter used ONLY to break ties when
	// drainOutbox sorts: Created is second-resolution, so two items enqueued in
	// the same second would otherwise have unspecified relative order. Not stable
	// across process restarts, but Created separates different seconds, so a
	// same-second collision across a restart is the only (astronomically rare) gap.
	Seq int64 `json:"seq,omitempty"`
}

// outboxSeq is the monotonic enqueue counter feeding OutboxItem.Seq.
var outboxSeq int64

func (T *Bridges) enqueueOutbox(it OutboxItem) {
	if it.ID == "" {
		it.ID = newToken()[:16]
	}
	if it.Created == "" {
		it.Created = now()
	}
	if it.Seq == 0 {
		it.Seq = atomic.AddInt64(&outboxSeq, 1)
	}
	// De-markdown at the single outbound chokepoint. A plain-text transport
	// (iMessage, SMS) renders literal **bold**, `code`, # headers and [text](url)
	// as punctuation, so every reply / send / status item is flattened to plain
	// text here — covering channel replies, send_message, and approved sends in
	// one place (phantom did the same on its outbox). Services that DO render
	// markdown (Slack, Discord) keep their formatting.
	if it.Text != "" && !ServiceRendersMarkdown(it.Service) {
		it.Text = MarkdownToPlain(it.Text)
	}
	// Outbound name tag: prefix "[Name] " so the recipient can tell an agent's
	// message apart from the owner's own texts in the same thread. Done here at
	// the single outbound chokepoint so it covers channel replies, send_message,
	// and notify_me alike. Three layers resolve here (most specific wins):
	//   enabled  = the bound agent opted in (it.Agent set) AND the channel
	//              didn't disable the tag.
	//   name     = per-channel override → global override → the agent's own name.
	// The agent-aware send paths stamp it.Agent with the agent's name only when
	// that agent opted in, so a non-empty Agent means "the base case is on".
	// Stored transcripts keep the untagged text — this is a wire-only concern.
	if it.Agent != "" && it.Text != "" {
		name := it.Agent
		if g := strings.TrimSpace(T.config().TagOverride); g != "" {
			name = g
		}
		if it.Owner != "" {
			if ch, ok := ChannelForInbound(RootDB, it.Owner, it.Service, it.ChatID, it.Handle); ok {
				switch {
				case ch.TagDisabled:
					name = ""
				case strings.TrimSpace(ch.TagOverride) != "":
					name = strings.TrimSpace(ch.TagOverride)
				}
			}
		}
		if name != "" {
			prefix := "[" + name + "] "
			it.Text = prefix + it.Text
			// Remember the marker so the copy that comes back is recognizable
			// as ours no matter how it was worded or which transport carried it.
			noteOutboundTag(prefix)
		}
	}
	it.Owner = "" // transient — never persist/leak the owner to a connector
	// Fingerprint what we are about to send, AFTER the tag + markdown
	// flattening, since that is the form that comes back. In a self thread the
	// copy that returns is is_from_me exactly like the owner's own typing, so
	// this is the only thing that tells the two apart. A status item is not an
	// agent turn, but it echoes the same way, so it is recorded too.
	noteOutbound(it.ChatID, it.Handle, it.Text)
	T.DB.Set(outboxTable, it.ID, it)
	// Delivery-leg visibility: the outbound path (enqueue → connector /api/poll →
	// drainOutbox) was previously unlogged, so a dropped reply was invisible.
	// Log image byte totals too — an oversized attachment payload is the prime
	// suspect for a reply that generates fine but never reaches the chat.
	imgBytes := 0
	for _, im := range it.Images {
		imgBytes += len(im)
	}
	vidBytes := 0
	for _, v := range it.Videos {
		vidBytes += len(v)
	}
	Log("[bridges.outbox] enqueued id=%s chat=%s svc=%q type=%s text=%dch images=%d (%d img bytes) videos=%d (%d vid bytes)",
		it.ID, it.ChatID, it.Service, it.Type, len(it.Text), len(it.Images), imgBytes, len(it.Videos), vidBytes)
}

// drainOutbox returns and removes every pending item for one service, oldest
// first, so a connector only ever gets its own service's traffic.
func (T *Bridges) drainOutbox(service string) []OutboxItem {
	var out []OutboxItem
	for _, id := range T.DB.Keys(outboxTable) {
		var it OutboxItem
		if T.DB.Get(outboxTable, id, &it) && it.Service == service {
			out = append(out, it)
			T.DB.Unset(outboxTable, id)
		}
	}
	// Oldest first. Created is second-resolution, so tie-break on the monotonic
	// enqueue Seq to keep same-second items in enqueue order (a total order, so a
	// plain non-stable sort is fine).
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created < out[j].Created
		}
		return out[i].Seq < out[j].Seq
	})
	// Only log a non-empty drain (poll fires constantly and mostly drains nothing).
	// The total byte size is the tell for an oversized /api/poll payload the
	// connector may fail to fetch.
	if len(out) > 0 {
		total := 0
		for _, it := range out {
			total += len(it.Text)
			for _, im := range it.Images {
				total += len(im)
			}
			for _, v := range it.Videos {
				total += len(v)
			}
		}
		Log("[bridges.outbox] drained %d item(s) for svc=%q (%d bytes total) — handed to connector", len(out), service, total)
	}
	return out
}

// --- inbound dedup -----------------------------------------------------------

// seenMessage reports whether this inbound was already processed, recording it
// if not. Keeps a connector's at-least-once delivery from double-firing agents.
func (T *Bridges) seenMessage(chatID, msgID string) bool {
	if msgID == "" {
		return false
	}
	key := chatID + ":" + msgID
	var at string
	if T.DB.Get(seenMsgTable, key, &at) {
		return true
	}
	// Stamped, not flagged: the retention sweep needs an age to expire on, and a
	// bare 1 gave it nothing to work with. Written to the v2 table — the old one
	// holds ints, and reading one of those into this string is fatal, not a miss.
	T.DB.Set(seenMsgTable, key, now())
	return false
}

// --- group identity ----------------------------------------------------------

// resolveSender returns who to attribute a message to: the name on THIS message
// if present, else a member name remembered for the handle (or one of its
// aliases), else the raw handle. upsertConvo learns names onto the Convo's
// Members, and the user can override them in the member editor — so group
// transcripts read by person, not by phone number.
func (T *Bridges) resolveSender(chatID, handle, fresh string) string {
	fresh = strings.TrimSpace(fresh)
	if handle = strings.TrimSpace(handle); handle == "" {
		// Empty handle = the owner's own message (the daemon clears it for
		// is_from_me). Prefer the configured self name, then a per-message
		// name, then a clear "Owner" label — never the anonymous "Someone",
		// which lost the owner's identity in a group transcript.
		if n := strings.TrimSpace(T.config().SelfName); n != "" {
			return n
		}
		if fresh != "" {
			return fresh
		}
		return "Owner"
	}
	// Prefer the remembered (learned or dashboard-edited) roster name over the
	// per-message display name, so one handle reads as ONE name across messages
	// even when the connector sends inconsistent display names. The fresh name
	// and raw handle are fallbacks only.
	if c, ok := T.getConvo(chatID); ok {
		for _, m := range c.Members {
			// Case-insensitive match, symmetric with the recipient side
			// (ResolveRecipient/chatIDForHandle use containsFold/EqualFold): an
			// alias stored "alex" arriving as "Alex" must attribute to the same
			// member, not fall through to the raw handle.
			if (strings.EqualFold(m.Handle, handle) || containsFold(m.Aliases, handle)) && m.Name != "" {
				return m.Name
			}
		}
	}
	if fresh != "" {
		return fresh
	}
	return handle
}

// rosterNames returns the display names of a GROUP conversation's known
// participants, to hand the agent as the up-front roster (see ChannelInbound.
// Roster). Names fall back to the handle; duplicates are dropped. Returns nil for
// 1:1 chats (the single other party is already the sender) or an unknown convo.
func (T *Bridges) rosterNames(chatID string) []string {
	if !isGroupChat(chatID) {
		return nil
	}
	c, ok := T.getConvo(chatID)
	if !ok {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for _, m := range c.Members {
		n := strings.TrimSpace(m.Name)
		if n == "" {
			n = strings.TrimSpace(m.Handle)
		}
		if n == "" || seen[strings.ToLower(n)] {
			continue
		}
		seen[strings.ToLower(n)] = true
		names = append(names, n)
	}
	return names
}

// bridgeOwner returns the single owner Bridges operates for — the deployment
// admin (one phone / one admin, mirroring phantom's single-tenant posture).
func (T *Bridges) bridgeOwner() string {
	src := RootDB
	if src == nil {
		src = T.DB
	}
	if src == nil {
		return ""
	}
	for _, u := range AuthListUsers(src) {
		if u.Admin {
			return u.Username
		}
	}
	return ""
}

var _ = http.MethodGet // placeholder import use; routes live in web.go
