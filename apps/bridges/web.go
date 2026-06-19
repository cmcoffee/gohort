package bridges

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

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

	sub := NewWebUI(T, prefix, AppUIAssets{})
	sub.HandleFunc("/", T.handleDashboard)
	sub.HandleFunc("/api/hook", T.handleHook)
	sub.HandleFunc("/api/poll", T.handlePoll)
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
	ChatID      string   `json:"chat_id"`
	Handle      string   `json:"handle"`
	DisplayName string   `json:"display_name"`
	Text        string   `json:"text"`
	Images      []string `json:"images,omitempty"`
	MsgID       string   `json:"msg_id"`
	RowID       int64    `json:"row_id"`
}

// handleHook is the inbound entry point: authenticate the connector, dedup,
// then route to the bound Channel's agent (or record-only when nothing is
// bound). No persona / engine here — the agent owns behavior.
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
	svc := strings.TrimSpace(key.Service)
	if svc == "" {
		svc = "imessage"
	}
	var req hookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	activeChatID := strings.TrimSpace(req.ChatID)

	// Dedup — a connector may re-deliver; only act once.
	if T.seenMessage(activeChatID, req.MsgID) {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Record the conversation (identity + participant) and keep the message for
	// the thread view.
	T.upsertConvo(svc, activeChatID, req.Handle, req.DisplayName)
	T.storeMessage(StoredMessage{
		ID: firstNonEmpty(req.MsgID, newToken()[:12]), ChatID: activeChatID, Role: "user",
		Handle: req.Handle, DisplayName: T.resolveSender(activeChatID, req.Handle, req.DisplayName),
		Text: req.Text,
	})

	// Panic / disabled: recorded above (dedup), but don't route or reply —
	// either the global switch (panic) or this specific bridge being turned off.
	if !T.config().Enabled || !key.Enabled {
		Log("[bridges] %s disabled — inbound from %s recorded, not routed",
			map[bool]string{true: "transport", false: "bridge " + key.Name}[!T.config().Enabled], req.Handle)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	owner := key.Owner
	if owner == "" {
		owner = T.bridgeOwner()
	}
	if owner == "" || !ChannelAgentRunnerReady() {
		w.WriteHeader(http.StatusAccepted)
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
					ch, found = cand, true
					Log("[bridges] self-healed group binding: channel %q re-stamped from member handle to chat id %s", cand.Name, activeChatID)
					break
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
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Attribute the message to a person (learns + resolves handle→name so group
	// chats read by who-said-it, not by phone number).
	sender := T.resolveSender(activeChatID, req.Handle, req.DisplayName)
	sessionID := ChannelSessionKey(ch, activeChatID)
	replyHere := ChannelDirection(ch) != DirectionInbound
	chatID, handle, text, images := activeChatID, req.Handle, req.Text, req.Images

	Log("[bridges] channel %q (svc=%s agent=%s dir=%s) handling inbound from %s",
		ch.Name, svc, ch.AgentID, ChannelDirection(ch), handle)
	go func() {
		reply, err := RunChannelAgent(context.Background(), ChannelInbound{
			Owner:            ch.Owner,
			AgentID:          ch.AgentID,
			SessionID:        sessionID,
			ChatID:           chatID,
			Handle:           handle,
			SenderName:       sender,
			ConversationName: sender,
			Text:             text,
			Images:           images,
			StatusCallback: func(s string) {
				if !replyHere {
					return
				}
				if s = strings.TrimSpace(s); s != "" {
					T.enqueueOutbox(OutboxItem{ChatID: chatID, Handle: handle, Service: svc, Text: s, Type: "status"})
				}
			},
		})
		if err != nil {
			Log("[bridges] channel agent run failed (chat=%s agent=%s): %v", chatID, ch.AgentID, err)
			return
		}
		if !replyHere {
			Log("[bridges] inbound-only channel %q — processed, reply not delivered here", ch.Name)
			return
		}
		if strings.TrimSpace(reply.Text) == "" && len(reply.Images) == 0 {
			return
		}
		T.enqueueOutbox(OutboxItem{ChatID: chatID, Handle: handle, Service: svc, Text: reply.Text, Images: reply.Images, Type: "reply"})
		T.storeMessage(StoredMessage{ID: newToken()[:12], ChatID: chatID, Role: "assistant", Text: reply.Text})
	}()
	w.WriteHeader(http.StatusAccepted)
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
	T.setConfig(bridgesConfig{Enabled: false})
	Log("[bridges] PANIC — transport disabled; no inbound routes, no outbound delivers")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"enabled": false, "message": "Bridges disabled. Re-enable from Master switches."})
}
