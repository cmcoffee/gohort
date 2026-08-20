package bridges

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const configTable = "bridges_config"

// bridgesConfig is the deployment-wide transport switch. Enabled=false is the
// panic state: inbound is still recorded/deduped but nothing routes to an agent
// and nothing is delivered.
type bridgesConfig struct {
	Enabled bool `json:"enabled"`
	// SelfName labels the owner's own messages — those arrive over the bridge
	// with an empty handle (the daemon clears it for is_from_me), so without
	// this they resolve to "Someone". Set it and your messages read as you, in
	// group threads and to the agent.
	SelfName string `json:"self_name,omitempty"`
	// SelfHandle is the owner's OWN messaging handle (their phone/email), used so
	// the agent can text the owner directly (notify_me / self-notify) and resolve
	// "me" as a recipient. Bridges only knew SelfName (a label) before; this is the
	// addressable handle the MessagingLink owner-handle seam needs.
	SelfHandle string `json:"self_handle,omitempty"`
	// TagOverride is the GLOBAL layer of the outbound name tag: when set, it
	// replaces each agent's own name in the "[Name] …" prefix on outbound
	// messages — a deployment-wide label (e.g. a brand or assistant name) so
	// every agent signs with the same identity. A per-channel override still
	// wins over it, and it's inert on any agent that hasn't enabled tagging.
	TagOverride string `json:"tag_override,omitempty"`
}

func (T *Bridges) config() bridgesConfig {
	var c bridgesConfig
	if !T.DB.Get(configTable, "config", &c) {
		return bridgesConfig{Enabled: true} // default on
	}
	return c
}

func (T *Bridges) setConfig(c bridgesConfig) { T.DB.Set(configTable, "config", c) }

// RegisterRoutes wires the Bridges HTTP surface under its web prefix.
func (T *Bridges) RegisterRoutes(mux *http.ServeMux, prefix string) {
	// Connector endpoints authenticate with X-API-Key (not a session cookie),
	// so mark them public to bypass the login redirect.
	RegisterPublicPath(prefix + "/api/hook")
	RegisterPublicPath(prefix + "/api/poll")
	// Provider-native webhook receiver — a public, per-connector inbound route
	// (trailing "/" = prefix match). It authenticates each request itself via the
	// provider's signature scheme against the connector's stored signing secret.
	RegisterPublicPath(prefix + "/api/webhook/")

	sub := NewWebUI(T, prefix, AppUIAssets{})
	sub.HandleFunc("/", T.handleDashboard)
	sub.HandleFunc("/api/hook", T.handleHook)
	sub.HandleFunc("/api/poll", T.handlePoll)
	sub.HandleFunc("/api/webhook/", T.handleWebhook)
	sub.HandleFunc("/api/webhook-secret", T.handleWebhookSecret)
	sub.HandleFunc("/api/keys", T.handleKeys)
	sub.HandleFunc("/api/keys/", T.handleKeyOne)
	sub.HandleFunc("/api/bridges", T.handleBridgeList)
	sub.HandleFunc("/api/panic", T.handlePanic)
	sub.HandleFunc("/api/config", T.handleConfig)
	sub.HandleFunc("/api/conversations", T.handleConversations)
	sub.HandleFunc("/api/incoming-convos", T.handleIncomingConvos)
	sub.HandleFunc("/api/add-convo", T.handleAddConvo)
	sub.HandleFunc("/api/agent-channels", T.handleAgentChannels)
	sub.HandleFunc("/api/connect-channel", T.handleConnectChannel)
	sub.HandleFunc("/api/set-autoreply", T.handleSetAutoReply)
	sub.HandleFunc("/api/conv-info/", T.handleConvInfo)
	sub.HandleFunc("/api/conversation/", T.handleConvUpdate)
	sub.HandleFunc("/api/messages/", T.handleMessages)
	MountSubMux(mux, prefix, sub)

	// Expose Bridges' stored threads + outbound to orchestrate's channel-scoped
	// chat tools (list_chats / read_chat / send_message) without a cycle.
	RegisterChannelThreads(channelThreadsImpl{T})

	// Become the MessagingLink the Operator's tools (message_contact / notify_me /
	// console / operator_wake) call — the sole provider since phantom retired.
	// See phantomlink.go.
	RegisterMessagingLink(messagingLinkImpl{T})

	// Resolve bridge keys to their owner for userFromAPIKey / DesktopClientUser.
	// Phantom used to register this; when it retired, the ONLY surviving
	// API-key validator was the core desktop key — so the gohort-desktop
	// daemon authenticating its WS bridge (/api/desktop/ws) with a bridges-
	// minted X-API-Key got 401'd and never connected, and from_client.*
	// (filesystem, screenshot, …) calls failed with "desktop isn't connected"
	// even though the iMessage hook (which uses validateBridgeKey) worked.
	// Registered here, at route time, because T.DB must be live (not init()).
	RegisterAPIKeyValidator(T.bridgeKeyOwner)

	// The other half of that: a key resolving to an owner is only safe while
	// the owner exists. Deleting a user now reaches in here and destroys their
	// bridge keys. Registered at route time for the same reason as above —
	// T.DB has to be live.
	RegisterUserCredentialRevoker(bridgeKeyRevoker{app: T})

	// Server half of the messaging_bridge connector kind: ensure a routing
	// BridgeKey exists for a service and reflect the connector's enabled state.
	RegisterBridgeProvisioner(T.ensureServiceBridge)

	// Server-side poll loops for the rest_messaging connector kind (Teams/Slack/…
	// via a REST API + SecureAPI credential). Materialize starts a loop; Teardown
	// stops it. Registered here (store live) before ReloadApprovedConnectors runs at
	// startup, so approved pollers restart automatically. The probe backs the
	// connector `test` action (one poll + mapping preview).
	RegisterMessagingPoller(func(c Connector, start bool) error {
		if start {
			return T.startPoller(c)
		}
		T.stopPoller(c.Name)
		// Release provider-side resources (a Graph subscription) on unapprove/delete.
		T.teardownWebhook(c)
		return nil
	})
	RegisterMessagingProbe(T.probeMessaging)

	// Graph webhook subscriptions are expiring push subscriptions; the core
	// push-sub primitive owns their renewal + restart recovery, we supply the
	// provider create/renew/delete via this handler.
	RegisterPushSubHandler(graphPushKind, graphPushHandler{T})

	// Migrate the transcript to per-chat tables and start the daily expiry of
	// dedup keys + old messages. Here, not init(), because T.DB must be live.
	T.startRetention()
}

// ensureServiceBridge is the server half of a messaging_bridge connector: make
// sure the owner has a BridgeKey for the service and set its Enabled state.
// Reuses an existing key for the service if present (avoids the iMessage
// duplicate-key problem) rather than minting a second one; otherwise creates
// one. The daemon still auto-negotiates the real desktop key — this just
// guarantees a routing record exists and that approve/unapprove flips it
// on/off.
func (T *Bridges) ensureServiceBridge(owner, service string, enabled bool) error {
	for _, k := range T.listBridgeKeys(owner) {
		if k.Service == service {
			if k.Enabled != enabled {
				k.Enabled = enabled
				T.saveBridgeKey(k)
				Log("[bridges] %s bridge for %s set enabled=%v (via connector)", service, owner, enabled)
			}
			return nil
		}
	}
	if !enabled {
		return nil // nothing to disable
	}
	k := BridgeKey{
		ID:      newToken()[:12],
		Name:    ServiceDisplayName(service) + " bridge",
		Key:     newToken(),
		Owner:   owner,
		Service: service,
		Enabled: true,
		Created: now(),
	}
	T.saveBridgeKey(k)
	Log("[bridges] ensured %s bridge key for %s (via connector)", service, owner)
	return nil
}

// handleConfig gets/sets the transport switch (so the panic state can be turned
// back on from the dashboard toggle).
func (T *Bridges) handleConfig(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(T.config())
	case http.MethodPost, http.MethodPatch:
		// Merge into the existing config so a partial save (the master toggle
		// posts only {enabled}; the name field only {self_name}) doesn't reset
		// the other field — e.g. saving your name must not flip on panic mode.
		c := T.config()
		_ = json.NewDecoder(r.Body).Decode(&c)
		T.setConfig(c)
		_ = json.NewEncoder(w).Encode(c)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// hookRequest is the inbound contract a connector POSTs. Mirrors the existing
// bridge protocol so the gohort-desktop daemon needs only a URL change.
type hookRequest struct {
	ChatID           string   `json:"chat_id"`
	Handle           string   `json:"handle"`
	DisplayName      string   `json:"display_name"`                // the SENDER's name (person at Handle)
	ConversationName string   `json:"conversation_name,omitempty"` // the group/chat title — names the thread, distinct from the sender
	Text             string   `json:"text"`
	Images           []string `json:"images,omitempty"`
	Videos           []string `json:"videos,omitempty"` // base64 inbound video clips (e.g. an mp4 in a text); frames sampled for the vision model — connector must send them
	Audios           []string `json:"audios,omitempty"` // base64 inbound audio (a voice memo / m4a); transcribed for the agent — connector must send them on this field, not images
	MsgID            string   `json:"msg_id"`
	RowID            int64    `json:"row_id"`
	// Timestamp is when the message was SENT, per the connector (RFC3339). The
	// iMessage relay has always put this on the wire; this struct simply never
	// declared it, so encoding/json dropped it and the server had no idea when
	// anything was actually sent — every inbound looked like it happened now.
	// That is what let replayed history wake an agent as live conversation.
	Timestamp string `json:"timestamp,omitempty"`
}

// inboundMsgID returns the stable per-message id for an inbound, preferring the
// connector's own msg_id and falling back to its row_id.
//
// The iMessage relay has only ever sent row_id — its comment reads "DB ROWID for
// server-side deduplication" — and this server decoded that field and then used
// nothing but msg_id, which the relay does not send. So every iMessage inbound
// looked id-LESS: seenMessage bails on an empty id, so the persistent dedupe
// never ran, and everything fell through to the 2-minute content hash. That
// fallback cannot tell a re-delivery from two people saying "ok" in the same
// room, so ordinary messages were dropped as duplicates — worst in group rooms,
// where short identical replies are normal. storeMessage suffered the same way,
// minting a random id per message so nothing could be correlated afterward.
func inboundMsgID(req hookRequest) string {
	if id := strings.TrimSpace(req.MsgID); id != "" {
		return id
	}
	if req.RowID > 0 {
		// Namespaced: a row id is an integer from one machine's chat.db and must
		// never collide with a connector's own opaque msg_id.
		return "row:" + strconv.FormatInt(req.RowID, 10)
	}
	return ""
}

// staleInboundAge is how old a message may claim to be and still wake an agent.
// Past this it is history being replayed, not conversation: it gets recorded so
// nothing is lost from the thread, but it wakes nothing.
//
// Generous on purpose. A connector that was offline overnight, a phone that
// syncs on wake, a poller catching up after a restart — those deliver genuinely
// undelivered messages that deserve an answer, and the window has to clear them.
// A replay of old history is hours-to-months behind and never comes close.
const staleInboundAge = 6 * time.Hour

// inboundIsStale reports whether an inbound claims a send time far enough in the
// past to be replayed history, and returns the parsed time for the log.
//
// Fails OPEN in every uncertain case — no timestamp, an unparseable one, or a
// clock-skewed future one all count as current. A connector that doesn't send
// the field must keep working exactly as before, and the cost of being wrong
// here is a dropped real message, which is worse than an extra answered one.
func inboundIsStale(ts string) (time.Time, bool) {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return time.Time{}, false
	}
	sent, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}, false
	}
	return sent, time.Since(sent) > staleInboundAge
}

// handleHook is the inbound HTTP entry point: authenticate the connector, decode
// the message, and hand it to ingestInbound. Every ingest outcome (dedup,
// disabled, record-only, dispatched) is a 202 — the routing work happens
// in-process, so a slow agent run never blocks the connector's POST.
func (T *Bridges) handleHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key, ok := T.validateBridgeKey(r.Header.Get("X-API-Key"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req hookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	T.ingestInbound(key, req)
	w.WriteHeader(http.StatusAccepted)
}

// ingestInbound routes one inbound message to the bound Channel's agent (or
// records-only when nothing is bound / the transport is disabled). Shared by the
// HTTP hook handler AND the server-side messaging poller (messaging_poller.go),
// so it takes a decoded key + request and never touches an HTTP writer. No persona
// / engine here — the agent owns behavior.
func (T *Bridges) ingestInbound(key BridgeKey, req hookRequest) {
	svc := strings.TrimSpace(key.Service)
	if svc == "" {
		svc = "imessage"
	}
	activeChatID := strings.TrimSpace(req.ChatID)
	msgID := inboundMsgID(req)

	// Dedup — a connector may re-deliver; only act once.
	if T.seenMessage(activeChatID, msgID) {
		return
	}
	// The id dedupe above has nothing to key on when a delivery carries no
	// message id, so it lets every copy through as new. Fall back to content
	// for those: a duplicate delivery is what typically starts a self-thread
	// loop, because two identical inbounds produce two replies that arrive as
	// two more inbounds.
	//
	// This is a LAST resort, not the normal path. It cannot tell a re-delivery
	// from two people saying "ok" in the same room, so every connector that can
	// identify its messages must do so — see inboundMsgID.
	if msgID == "" && seenContent(activeChatID, req.Handle, req.Text) {
		Debug("[bridges] dropping id-less duplicate inbound on %s", activeChatID)
		return
	}

	// Record the conversation (identity + participant) and keep the message for
	// the thread view.
	T.upsertConvo(svc, activeChatID, req.Handle, req.DisplayName, req.ConversationName)
	// Attribute the message to a person (learns + resolves handle→name so group
	// chats read by who-said-it, not by phone number). Computed once here and
	// reused for the agent dispatch below — nothing between mutates the convo's
	// members, so a second resolve would return the same value.
	sender := T.resolveSender(activeChatID, req.Handle, req.DisplayName)
	// Record it at the time it was SENT, not the time it reached us. storeMessage
	// falls back to now() for a connector that supplies nothing, which is what
	// every inbound used to get — so replayed history read as having just arrived
	// in the thread view too, not only to the agent.
	T.storeMessage(StoredMessage{
		ID: firstNonEmpty(msgID, newToken()[:12]), ChatID: activeChatID, Role: "user",
		Handle: req.Handle, DisplayName: sender,
		Text: req.Text, Timestamp: strings.TrimSpace(req.Timestamp),
	})

	// Replayed history, not conversation. A connector can re-deliver old messages
	// for reasons the server can't see — a rebuilt local database handing back old
	// rows under new ids, a poller re-reading a feed from the start — and the
	// id/content dedupe only catches a repeat of something this server already
	// saw. It has nothing to say about old traffic arriving for the FIRST time,
	// which is exactly the case that wakes an agent to answer a months-old
	// message. The message's own send time is the one signal that settles it, so
	// this guard belongs here rather than in any single connector.
	if sent, stale := inboundIsStale(req.Timestamp); stale {
		Log("[bridges] inbound on %s was sent %s (%s ago) — recorded as history, not routed",
			activeChatID, sent.Format(time.RFC3339), time.Since(sent).Round(time.Minute))
		return
	}

	// Panic / disabled: recorded above (dedup), but don't route or reply —
	// either the global switch (panic) or this specific bridge being turned off.
	if !T.config().Enabled || !key.Enabled {
		Log("[bridges] %s disabled — inbound from %s recorded, not routed",
			map[bool]string{true: "transport", false: "bridge " + key.Name}[!T.config().Enabled], req.Handle)
		return
	}

	owner := key.Owner
	if owner == "" {
		owner = T.bridgeOwner()
	}
	if owner == "" || !ChannelAgentRunnerReady() {
		return
	}

	// Route to the bound channel. Match against the inbound's full identity
	// cluster — handle, chat id, and every alias-linked id in BOTH directions,
	// normalized across the chat-id ↔ raw-handle forms — so owner self-messages
	// (empty handle), groups, a chat's multiple ids, AND a contact aliased as
	// "this is also me" all resolve to the right channel regardless of which id
	// the message arrived on or which convo the alias was added to.
	candidates := T.inboundIdentities(svc, activeChatID, req.Handle)
	ch, found := ChannelForInbound(RootDB, owner, svc, candidates...)
	// Self-heal a stale group binding: a group connected before the group-aware
	// fix bound its channel to one member's handle (the old Handle-clobber), so
	// owner messages (handle="") never match the chat id. If a channel is bound
	// to a member handle of THIS group, migrate it to the stable chat id once —
	// then it routes for everyone, no manual reconnect.
	if !found && isGroupChat(activeChatID) {
		if c, ok := T.getConvo(activeChatID); ok && len(c.Members) > 0 {
			memberHandle := map[string]bool{}
			for _, m := range c.Members {
				if m.Handle != "" {
					memberHandle[m.Handle] = true
				}
				for _, a := range m.Aliases {
					if a != "" {
						memberHandle[a] = true
					}
				}
			}
			for _, cand := range ListChannels(RootDB, owner) {
				if cand.Service == svc && cand.Address != "" && memberHandle[cand.Address] {
					cand.Address = activeChatID
					SaveChannel(RootDB, cand)
					// Keep the FIRST match as the active channel for this message,
					// but don't stop — a group can have several channels stale-bound
					// to different member handles; re-stamp them all so they route.
					if !found {
						ch, found = cand, true
					}
					Log("[bridges] self-healed group binding: channel %q re-stamped from member handle to chat id %s", cand.Name, activeChatID)
				}
			}
		}
	}
	if !found || !ch.AutoReply || ChannelDirection(ch) == DirectionOutbound {
		Log("[bridges] no auto-reply channel for svc=%s handle=%q chat=%q — recorded only", svc, req.Handle, activeChatID)
		// Diagnostic: show the candidate ids vs every bound channel's address so
		// a mismatch (stale member-handle binding on a group, owner skew, wrong
		// service) is obvious in the log instead of a silent record-only.
		Debug("[bridges]   owner=%q candidates=%q", owner, candidates)
		for _, c := range ListChannels(RootDB, owner) {
			if c.Service == svc {
				Debug("[bridges]   channel %q addr=%q auto_reply=%v dir=%s", c.Name, c.Address, c.AutoReply, ChannelDirection(c))
			}
		}
		// A channel that IS bound to an agent but set not to wake (auto_reply off)
		// is a pure read-along: mirror the inbound into the agent's transcript so
		// it shows in the agent's chat, same as a gatekeeper-blocked message. Skip
		// when no channel matched (nothing to mirror to) or the channel is
		// outbound-only (an inbound doesn't belong in that thread). No-op if
		// orchestrate isn't loaded.
		if found && !ch.AutoReply && ChannelDirection(ch) != DirectionOutbound {
			RecordChannelSilent(ChannelInbound{
				Owner:            ch.Owner,
				AgentID:          ch.AgentID,
				SessionID:        ChannelSessionKey(ch, activeChatID),
				ChatID:           activeChatID,
				Handle:           req.Handle,
				SenderName:       sender,
				ConversationName: firstNonEmpty(req.ConversationName, sender),
				Text:             req.Text,
			})
		}
		return
	}

	// Our own reply coming back. In a SELF thread every message is is_from_me
	// (handle cleared), so an agent reply is indistinguishable by handle from
	// the owner typing — it routes to the channel that produced it and the
	// agent answers itself. The message stays in the transcript above; it just
	// doesn't wake anything.
	// Our own outbound tag on an inbound message is conclusive: that marker is
	// put there by us, on the way out, so anything wearing it is our message
	// returning. Checked FIRST because it holds where the others don't — it
	// survives the agent rephrasing, and it doesn't care which transport
	// carried the message back.
	if carriesOurTag(req.Text) {
		Log("[bridges] inbound on %s carries our own outbound tag — recorded, not routed", activeChatID)
		return
	}
	// Content fingerprint, for outbound that carried no tag. fromMe cannot be
	// read off an empty handle alone: over MMS the owner's own number arrives
	// as a RECEIVED message with the handle populated, which skipped this guard
	// entirely on the exact thread it was written for.
	if isOwnEcho(activeChatID, req.Handle, req.Text, T.isOwnerHandle(req.Handle)) {
		Log("[bridges] inbound on %s is our own message echoed back — recorded, not routed", activeChatID)
		return
	}
	// A conversation that already blew the reply budget stays cut until its
	// cooldown expires. Recording continues so nothing is lost from history.
	if loopTripped(activeChatID, req.Handle) {
		Log("[bridges] conversation %s is in loop cooldown — inbound recorded, not routed", activeChatID)
		return
	}

	sessionID := ChannelSessionKey(ch, activeChatID)
	replyHere := ChannelDirection(ch) != DirectionInbound
	// Does this service actually have a delivery path? A rest_messaging connector
	// without send_url (or a raw inbound webhook) has none — so an agent reply would
	// strand in an outbox nothing drains. When there's no output, overflow the reply
	// to the agent's cortex/session instead of enqueuing it.
	hasOutput := T.serviceHasOutput(owner, svc)
	chatID, handle, text, images, videos, audios := activeChatID, req.Handle, req.Text, req.Images, req.Videos, req.Audios

	Log("[bridges] channel %q (svc=%s agent=%s dir=%s) handling inbound from %s",
		ch.Name, svc, ch.AgentID, ChannelDirection(ch), handle)
	in := ChannelInbound{
		Owner:            ch.Owner,
		AgentID:          ch.AgentID,
		SessionID:        sessionID,
		ChatID:           chatID,
		Handle:           handle,
		SenderName:       sender,
		ConversationName: firstNonEmpty(req.ConversationName, sender),
		Roster:           T.rosterNames(activeChatID),
		Text:             text,
		Images:           images,
		Videos:           videos,
		Audios:           audios,
		StatusCallback: func(s string) {
			if !replyHere || !hasOutput {
				return // no interim status when we can't deliver it
			}
			if s = strings.TrimSpace(s); s != "" {
				T.enqueueOutbox(OutboxItem{ChatID: chatID, Handle: handle, Service: svc, Text: s, Type: "status"})
			}
		},
	}
	go func() {
		// Wake-rule gatekeeper: master (admin) + per-channel rules decide whether
		// this inbound wakes the agent. It was already recorded above for history;
		// on a block we simply don't run or reply. Fails open when no rules are set
		// or no evaluator is registered (see core.ChannelGatekeeperAllow). Runs in
		// the goroutine so the connector's POST returns 202 without waiting on the
		// gatekeeper's worker-LLM call. Short bound — this is one quick worker call.
		gctx, gcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		allowed := ChannelGatekeeperAllow(gctx, in)
		gcancel()
		if !allowed {
			Log("[bridges] gatekeeper blocked inbound from %s on channel %q — recorded only", sender, ch.Name)
			// Mirror the blocked message into the bound agent's own transcript so it
			// shows in the agent's chat and is in-context on its next wake — the
			// agent reads along even when it stays silent. No-op if orchestrate isn't
			// loaded. Store A already has it (storeMessage above); this is Store B.
			RecordChannelSilent(in)
			return
		}
		// Coalesce rapid same-session messages (a question, then an image posted
		// right after) into one turn instead of racing separate turns on the same
		// session. The winning caller returns the merged reply; a message that got
		// folded into another's batch returns an empty reply and sends nothing.
		// The dispatch context (with its generous timeout) is owned inside the
		// coalescer so a superseding message can cancel an in-flight turn. See
		// core.CoalesceChannelDispatch.
		reply, err := CoalesceChannelDispatch(sessionID, in, RunChannelAgent)
		if err != nil {
			Log("[bridges] channel agent run failed (chat=%s agent=%s): %v", chatID, ch.AgentID, err)
			return
		}
		if !replyHere {
			Log("[bridges] inbound-only channel %q — processed, reply not delivered here", ch.Name)
			return
		}
		if strings.TrimSpace(reply.Text) == "" && len(reply.Images) == 0 && len(reply.Videos) == 0 {
			return
		}
		// Count the reply against this conversation's budget. The echo guard
		// stops the ordinary self-thread loop, but it compares TEXT — an agent
		// that rephrases each round walks straight past it. This doesn't care
		// what was said, only how many times, so it terminates every loop shape
		// including that one. It runs after the agent has already answered, so
		// the reply about to be sent is the last one; the cut applies to the
		// inbound that would follow.
		if noteReply(chatID, handle, T.isSelfThread(chatID, handle)) {
			logLoopCut(chatID)
		}
		if hasOutput {
			T.enqueueOutbox(OutboxItem{ChatID: chatID, Handle: handle, Service: svc, Text: reply.Text, Images: reply.Images, Videos: reply.Videos, Agent: reply.AgentName, Owner: ch.Owner, Type: "reply"})
		} else if OverflowChannelReply(in, reply.Text) {
			// No delivery path for this service — surface the reply to the agent's
			// cortex/session instead of stranding it in an outbox nothing drains.
			Log("[bridges] channel %q reply overflowed to agent cortex/session (no output path for svc=%s)", ch.Name, svc)
		}
		T.storeMessage(StoredMessage{ID: newToken()[:12], ChatID: chatID, Role: "assistant", Text: reply.Text})
	}()
}

// serviceHasOutput reports whether a service can actually deliver outbound. A
// rest_messaging connector without send_url (a poll/webhook bridge configured
// inbound-only) has no delivery path; any other service — iMessage, or a connector
// WITH send_url — is assumed to have a drainer. Used to decide whether an agent's
// reply is enqueued for delivery or overflowed to the agent's cortex/session.
func (T *Bridges) serviceHasOutput(owner, svc string) bool {
	found := false
	for _, c := range ListConnectors(RootDB) {
		if c.Kind != RestMessagingConnectorKind || !c.Approved || c.Owner != owner {
			continue
		}
		var s RestMessagingSpec
		if json.Unmarshal(c.Spec, &s) != nil || s.Service != svc {
			continue
		}
		found = true
		if strings.TrimSpace(s.SendURL) != "" {
			return true
		}
	}
	// Connector(s) exist for this service but none can send → no output. No matching
	// connector (iMessage, etc.) → assume a drainer exists (existing behavior).
	return !found
}

// handlePoll hands a connector ONLY its own service's pending outbound.
func (T *Bridges) handlePoll(w http.ResponseWriter, r *http.Request) {
	key, ok := T.validateBridgeKey(r.Header.Get("X-API-Key"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	svc := strings.TrimSpace(key.Service)
	if svc == "" {
		svc = "imessage"
	}
	items := T.drainOutbox(svc)
	if items == nil {
		items = []OutboxItem{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

// handlePanic flips the transport off — the kill switch. Inbound keeps being
// recorded/deduped, but nothing routes to an agent and nothing is delivered.
func (T *Bridges) handlePanic(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	// Toggle only Enabled — a whole-struct replace here wiped SelfName/SelfHandle,
	// so after re-enable the owner attributed as "Owner" instead of the configured
	// name (and "me" recipient resolution broke).
	c := T.config()
	c.Enabled = false
	T.setConfig(c)
	Log("[bridges] PANIC — transport disabled; no inbound routes, no outbound delivers")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"enabled": false, "message": "Bridges disabled. Re-enable from Master switches."})
}
