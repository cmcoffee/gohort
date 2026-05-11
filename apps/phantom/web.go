package phantom

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
)

func (T *Phantom) RegisterRoutes(mux *http.ServeMux, prefix string) {
	migrateFromRelay(T.DB)

	// Agent endpoints authenticate with X-API-Key, not a session cookie.
	// Register them as public so AuthMiddleware doesn't redirect to /login.
	RegisterPublicPath(prefix + "/api/hook")
	RegisterPublicPath(prefix + "/api/poll")

	// Phantom now uses the core/ui declarative framework on both
	// desktop and mobile. The same handler renders both — the page is
	// laid out responsively (toggle group, persona form, conversations
	// table) and the framework handles drawer behavior on small
	// viewports.
	sub := NewWebUI(T, prefix, AppUIAssets{})
	sub.HandleFunc("/", T.handleDashboard)

	// Web UI endpoints (session auth).
	sub.HandleFunc("/api/keys", T.handleKeys)
	sub.HandleFunc("/api/keys/", T.handleKeyDelete)
	sub.HandleFunc("/api/conversations", T.handleConversations)
	sub.HandleFunc("/api/conversation/", T.handleConversation)
	sub.HandleFunc("/api/config", T.handleConfig)
	sub.HandleFunc("/api/proactive/test", T.handleProactiveTest)
	sub.HandleFunc("/api/proactive-next", T.handleProactiveNext)
	sub.HandleFunc("/api/tools", T.handleToolList)
	sub.HandleFunc("/api/announce", T.handleAnnounce)
	sub.HandleFunc("/api/conv-info/", T.handleConvInfo)
	sub.HandleFunc("/api/memory/", T.handleMemory)
	sub.HandleFunc("/api/conversation-clear/", T.handleConversationClear)
	sub.HandleFunc("/api/personas", T.handlePersonas)
	sub.HandleFunc("/api/personas/", T.handlePersonas)
	sub.HandleFunc("/api/persona-assist", T.handlePersonaAssist)

	// Legacy /phantom/mobile alias — same dashboard, kept so any
	// existing bookmarks keep working.
	sub.HandleFunc("/mobile", T.handleDashboard)
	sub.HandleFunc("/mobile/", T.handleDashboard)
	sub.HandleFunc("/api/mobile/panic", T.handleMobilePanic)

	// Agent endpoints (API key auth, no session needed).
	sub.HandleFunc("/api/hook", T.handleHook)
	sub.HandleFunc("/api/poll", T.handlePoll)

	T.registerSchedulerHandler()
	MountSubMux(mux, prefix, sub)
}

// --- API key management ---

func (T *Phantom) handleKeys(w http.ResponseWriter, r *http.Request) {
	_, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		var keys []APIKey
		for _, k := range T.DB.Keys(apiKeyTable) {
			var ak APIKey
			if T.DB.Get(apiKeyTable, k, &ak) {
				ak.Key = "••••••••" // never expose secret over UI
				keys = append(keys, ak)
			}
		}
		if keys == nil {
			keys = []APIKey{}
		}
		jsonOK(w, keys)

	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		secret := make([]byte, 24)
		rand.Read(secret)
		ak := APIKey{
			ID:      newID(),
			Name:    strings.TrimSpace(req.Name),
			Key:     fmt.Sprintf("%x", secret),
			Created: now(),
		}
		T.DB.Set(apiKeyTable, ak.ID, ak)
		jsonOK(w, ak) // key shown once in full

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *Phantom) handleKeyDelete(w http.ResponseWriter, r *http.Request) {
	_, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/keys/")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	T.DB.Unset(apiKeyTable, id)
	w.WriteHeader(http.StatusNoContent)
}

// --- Persona config ---

func (T *Phantom) handleConfig(w http.ResponseWriter, r *http.Request) {
	_, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		jsonOK(w, defaultConfig(T.DB))
	case http.MethodPost:
		var cfg PhantomConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		var prev PhantomConfig
		T.DB.Get(configTable, configKey, &prev)
		T.DB.Set(configTable, configKey, cfg)
		proactiveChanged := cfg.ProactiveEnabled != prev.ProactiveEnabled ||
			cfg.ProactiveWindow != prev.ProactiveWindow ||
			cfg.ProactivePrompt != prev.ProactivePrompt ||
			cfg.ProactiveMaxPerDay != prev.ProactiveMaxPerDay
		if proactiveChanged {
			go T.syncProactiveTasks(cfg)
		}
		jsonOK(w, cfg)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleProactiveTest schedules a one-shot proactive message for all opted-in
// conversations at a specified time (or in 10 seconds if none given).
func (T *Phantom) handleProactiveTest(w http.ResponseWriter, r *http.Request) {
	_, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		FireAt string `json:"fire_at"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	fireAt := time.Now().Add(10 * time.Second)
	if req.FireAt != "" {
		if t, err := time.Parse(time.RFC3339, req.FireAt); err == nil {
			fireAt = t
		}
	}

	cfg := defaultConfig(T.DB)
	var count int
	for _, k := range T.DB.Keys(conversationTable) {
		var conv Conversation
		if !T.DB.Get(conversationTable, k, &conv) || !conv.ProactiveEnabled {
			continue
		}
		payload := phantomCallPayload{
			ChatID:      conv.ChatID,
			Handle:      conv.Handle,
			Prompt:      cfg.ProactivePrompt,
			IsProactive: true,
		}
		if _, err := ScheduleTask(phantomTaskKind, payload, fireAt); err != nil {
			Log("[phantom/proactive] test schedule error for %s: %v", conv.ChatID, err)
			continue
		}
		count++
	}
	jsonOK(w, map[string]any{
		"message": fmt.Sprintf("Test scheduled for %d conversation(s) at %s", count, fireAt.Local().Format("3:04:05 PM")),
	})
}

// handleProactiveNext returns the next scheduled proactive fire time for each
// opted-in conversation. The UI displays these in the conversation detail panel.
func (T *Phantom) handleProactiveNext(w http.ResponseWriter, r *http.Request) {
	_, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	type nextFire struct {
		ChatID  string `json:"chat_id"`
		NextFire string `json:"next_fire"` // RFC3339 or empty
	}

	var result []nextFire
	for _, k := range T.DB.Keys(conversationTable) {
		var conv Conversation
		if !T.DB.Get(conversationTable, k, &conv) || !conv.ProactiveEnabled {
			continue
		}
		var sid string
		if !T.DB.Get(proactiveIDsTable, conv.ChatID, &sid) || sid == "" {
			continue
		}
		// Look up the scheduled task in the global scheduler.
		for _, task := range ListScheduledTasks(phantomTaskKind) {
			if task.ID == sid {
				result = append(result, nextFire{
					ChatID:   conv.ChatID,
					NextFire: task.RunAt,
				})
				break
			}
		}
	}
	jsonOK(w, result)
}

// handleToolList returns all tools available to phantom — both registry tools
// and session-scoped built-ins — so the UI can render a complete toggleable picker.
func (T *Phantom) handleToolList(w http.ResponseWriter, r *http.Request) {
	authUser, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Disable browser caching — the persistent-tool list mutates as
	// the operator approves new tools in admin, and a stale cached
	// response would hide them from phantom's per-conversation picker.
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	type toolInfo struct {
		Name string `json:"name"`
		Desc string `json:"desc"`
	}
	// skip always-on tools and conditionally-available ones — handled below
	skip := map[string]bool{"stay_silent": true, "keep_going": true, "generate_image": true}

	// Persistent temp tools FIRST so they're visible at the top of
	// the chip picker without scrolling — these are the operator's
	// custom wrappers and should be easy to find/toggle.
	var out []toolInfo
	if authUser != "" {
		for _, p := range LoadPersistentTempTools(T.DB, authUser) {
			desc := strings.TrimSpace(p.Tool.Description)
			tag := "[wrapper]"
			if p.Tool.Mode == TempToolModeShell {
				tag = "[shell]"
			}
			if desc == "" {
				desc = tag + " persistent temp tool"
			} else {
				desc = tag + " " + desc
			}
			out = append(out, toolInfo{Name: p.Tool.Name, Desc: desc})
		}
	}

	// Built-in chat tools (sorted alphabetically).
	var builtIn []toolInfo
	for _, t := range RegisteredChatTools() {
		if skip[t.Name()] {
			continue
		}
		builtIn = append(builtIn, toolInfo{Name: t.Name(), Desc: t.Desc()})
	}
	sort.Slice(builtIn, func(i, j int) bool { return builtIn[i].Name < builtIn[j].Name })
	out = append(out, builtIn...)

	// Phantom-only tools and conditionally-available ones.
	out = append(out,
		toolInfo{Name: "memory", Desc: "Manage per-conversation memory: save / list / delete saved facts about the person. Call with action=help for usage."},
		toolInfo{Name: "schedule_callback", Desc: "Schedule a follow-up message at a specified time."},
		toolInfo{Name: "follow_up", Desc: "Send a brief follow-up message after a short delay (1–5 seconds)."},
	)
	if ImageGenerationAvailable() {
		out = append(out, toolInfo{Name: "generate_image", Desc: "Generate an AI image from a description and send it as an attachment."})
	}
	// Secure-API tools (call_<credname>) — auto-generated from registered
	// credentials. Surface them in the phantom tools picker so the operator
	// can opt a conv into them via checkbox like any other tool. Hidden
	// when the master switch is off so the picker doesn't tease tools
	// that wouldn't fire anyway.
	cfg := defaultConfig(T.DB)
	if cfg.SecureAPIEnabled {
		// nil session — this endpoint enumerates the catalog for the
		// admin UI, no save_to dispatch happens here. Restricted
		// credentials (Restricted=true) are deliberately omitted so
		// the phantom picker can't expose direct call_<name> for
		// anything the operator marked Restricted in admin. Wrapped
		// dispatch through approved api-mode temp tools is unaffected.
		for _, td := range Secure().BuildTools(nil) {
			out = append(out, toolInfo{Name: td.Tool.Name, Desc: td.Tool.Description})
		}
	}
	jsonOK(w, out)
}

// --- Conversations ---

func (T *Phantom) handleConversations(w http.ResponseWriter, r *http.Request) {
	_, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var convs []Conversation
	for _, k := range T.DB.Keys(conversationTable) {
		var c Conversation
		if T.DB.Get(conversationTable, k, &c) && c.AliasOf == "" {
			// Fall back to handle when DisplayName is unset so the
			// mobile conversations table doesn't show blank rows for
			// numbered-only contacts. The mobile Table component
			// renders the display_name column verbatim.
			if strings.TrimSpace(c.DisplayName) == "" {
				c.DisplayName = strings.TrimSpace(c.Handle)
				if c.DisplayName == "" {
					c.DisplayName = c.ChatID
				}
			}
			convs = append(convs, c)
		}
	}
	if convs == nil {
		convs = []Conversation{}
	}
	jsonOK(w, convs)
}

// syncMembersFromHistory scans all stored messages for chatID and ensures every
// unique user-role sender handle is present in the conversation's member list.
// This retroactively fills in members from messages received before the member-tracking
// feature was added, and keeps the list consistent without requiring a roster from the relay.
func syncMembersFromHistory(db Database, chatID string) Conversation {
	var conv Conversation
	db.Get(conversationTable, chatID, &conv)
	conv.ChatID = chatID

	prefix := chatID + ":"
	for _, k := range db.Keys(messageTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var m PhantomMessage
		if !db.Get(messageTable, k, &m) || m.Role != "user" || m.Handle == "" {
			continue
		}
		conv.Members = upsertMember(conv.Members, m.Handle, m.DisplayName)
	}

	db.Set(conversationTable, chatID, conv)
	return conv
}

// handleConvInfo returns the full Conversation record for a single chat_id,
// running a member-sync from message history first so the persona panel always
// shows an up-to-date member list without requiring a relay agent update.
func (T *Phantom) handleConvInfo(w http.ResponseWriter, r *http.Request) {
	_, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	chatID := strings.TrimPrefix(r.URL.Path, "/api/conv-info/")
	if chatID == "" {
		http.Error(w, "chat_id required", http.StatusBadRequest)
		return
	}
	conv := syncMembersFromHistory(T.DB, chatID)
	jsonOK(w, conv)
}

// handleMemory handles GET (list) and DELETE (single) for per-conversation memories.
// GET  /api/memory/{chatID}        → [{id, note, created_at}, ...]
// DELETE /api/memory/{chatID}/{id} → 204
func (T *Phantom) handleMemory(w http.ResponseWriter, r *http.Request) {
	_, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/memory/")
	parts := strings.SplitN(path, "/", 2)
	chatID := parts[0]
	if chatID == "" {
		http.Error(w, "chat_id required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		type memEntry struct {
			ID        string `json:"id"`
			Note      string `json:"note"`
			CreatedAt string `json:"created_at"`
		}
		prefix := chatID + ":"
		var entries []memEntry
		for _, k := range T.DB.Keys(memoryTable) {
			if strings.HasPrefix(k, prefix) {
				var m phantomMemory
				if T.DB.Get(memoryTable, k, &m) {
					entries = append(entries, memEntry{
						ID:        strings.TrimPrefix(k, prefix),
						Note:      m.Note,
						CreatedAt: m.CreatedAt,
					})
				}
			}
		}
		if entries == nil {
			entries = []memEntry{}
		}
		jsonOK(w, entries)

	case http.MethodDelete:
		if len(parts) < 2 || parts[1] == "" {
			http.Error(w, "memory id required", http.StatusBadRequest)
			return
		}
		key := chatID + ":" + parts[1]
		T.DB.Unset(memoryTable, key)
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *Phantom) handleConversation(w http.ResponseWriter, r *http.Request) {
	_, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	chatID := strings.TrimPrefix(r.URL.Path, "/api/conversation/")
	if chatID == "" {
		http.Error(w, "chat_id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		// ?record=1 returns the Conversation record (settings, tools,
		// persona, etc.) instead of the message history. Used by the
		// mobile chip-picker so it can read the live enabled_tools list
		// without going through the conversations index.
		if r.URL.Query().Get("record") == "1" {
			var conv Conversation
			T.DB.Get(conversationTable, chatID, &conv)
			conv.ChatID = chatID
			jsonOK(w, conv)
			return
		}
		msgs := recentMessages(T.DB, chatID, 50)
		if msgs == nil {
			msgs = []PhantomMessage{}
		}
		// Strip NSKeyedArchiver metadata from stored text for UI display.
		for i := range msgs {
			msgs[i].Text = cleanMessageText(msgs[i].Text)
		}
		jsonOK(w, msgs)
	case http.MethodPatch:
		var req struct {
			AutoReply          *bool          `json:"auto_reply"`
			DisplayName        *string        `json:"display_name"`
			PersonaName        *string        `json:"persona_name"`
			Personality        *string        `json:"personality"`
			SystemPrompt       *string        `json:"system_prompt"`
			EnabledTools       *[]string      `json:"enabled_tools"`
			GatekeeperPrompt   *string        `json:"gatekeeper_prompt"`
			Members            *[]ConvMember  `json:"members"`
			AliasHandles       *[]string      `json:"alias_handles"`
			ProactiveEnabled   *bool          `json:"proactive_enabled"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var conv Conversation
		T.DB.Get(conversationTable, chatID, &conv)
		conv.ChatID = chatID
		if req.AutoReply != nil {
			conv.AutoReply = *req.AutoReply
		}
		if req.DisplayName != nil {
			conv.DisplayName = *req.DisplayName
		}
		if req.PersonaName != nil {
			conv.PersonaName = *req.PersonaName
		}
		if req.Personality != nil {
			conv.Personality = *req.Personality
		}
		if req.SystemPrompt != nil {
			conv.SystemPrompt = *req.SystemPrompt
		}
		if req.EnabledTools != nil {
			conv.EnabledTools = *req.EnabledTools
		}
		if req.GatekeeperPrompt != nil {
			conv.GatekeeperPrompt = *req.GatekeeperPrompt
		}
		if req.Members != nil {
			conv.Members = *req.Members
		}
		if req.AliasHandles != nil {
			// Trim whitespace from each handle; drop empties.
			handles := (*req.AliasHandles)[:0:len(*req.AliasHandles)]
			for _, h := range *req.AliasHandles {
				if h = strings.TrimSpace(h); h != "" {
					handles = append(handles, h)
				}
			}
			conv.AliasHandles = handles
			Log("[phantom] PATCH alias_handles for %s: %v", chatID, handles)

			// Eagerly propagate AliasOf on any existing conversations that match,
			// so alias records are immediately hidden from the list without waiting
			// for the next incoming message to trigger the lazy cache write.
			handleSet := make(map[string]bool, len(handles))
			for _, h := range handles {
				handleSet[h] = true
			}
			for _, k := range T.DB.Keys(conversationTable) {
				if k == chatID {
					continue
				}
				var aliasConv Conversation
				if !T.DB.Get(conversationTable, k, &aliasConv) {
					continue
				}
				matched := handleSet[aliasConv.Handle] || handleSet[aliasConv.ChatID]
				if matched && aliasConv.AliasOf != chatID {
					aliasConv.AliasOf = chatID
					T.DB.Set(conversationTable, k, aliasConv)
					Log("[phantom] alias pointer set %s → %s", k, chatID)
				} else if !matched && aliasConv.AliasOf == chatID {
					// Handle removed from AliasHandles — clear the stale pointer.
					aliasConv.AliasOf = ""
					T.DB.Set(conversationTable, k, aliasConv)
					Log("[phantom] alias pointer cleared for %s", k)
				}
			}
		}
		if req.ProactiveEnabled != nil {
			conv.ProactiveEnabled = *req.ProactiveEnabled
		}
		T.DB.Set(conversationTable, chatID, conv)
		jsonOK(w, conv)
	case http.MethodDelete:
		T.DB.Unset(conversationTable, chatID)
		prefix := chatID + ":"
		for _, k := range T.DB.Keys(messageTable) {
			if strings.HasPrefix(k, prefix) {
				T.DB.Unset(messageTable, k)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleConversationClear deletes all messages for a conversation without
// removing the conversation record or its settings.
func (T *Phantom) handleConversationClear(w http.ResponseWriter, r *http.Request) {
	_, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	chatID := strings.TrimPrefix(r.URL.Path, "/api/conversation-clear/")
	if chatID == "" {
		http.Error(w, "chat_id required", http.StatusBadRequest)
		return
	}
	prefix := chatID + ":"
	for _, k := range T.DB.Keys(messageTable) {
		if strings.HasPrefix(k, prefix) {
			T.DB.Unset(messageTable, k)
		}
	}
	Log("[phantom] cleared message history for %s", chatID)
	w.WriteHeader(http.StatusNoContent)
}

// --- Announcements ---

func (T *Phantom) handleAnnounce(w http.ResponseWriter, r *http.Request) {
	_, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Text    string   `json:"text"`
		ChatIDs []string `json:"chat_ids"` // empty = all enabled conversations
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	targets := req.ChatIDs
	if len(targets) == 0 {
		for _, k := range T.DB.Keys(conversationTable) {
			var c Conversation
			if T.DB.Get(conversationTable, k, &c) && c.AutoReply {
				targets = append(targets, c.ChatID)
			}
		}
	}
	count := 0
	for _, chatID := range targets {
		var conv Conversation
		if !T.DB.Get(conversationTable, chatID, &conv) || conv.Handle == "" {
			continue
		}
		enqueueOutbox(T.DB, OutboxItem{
			ID:      newID(),
			ChatID:  chatID,
			Handle:  conv.Handle,
			Text:    req.Text,
			Type:    "announce",
			Created: now(),
		})
		count++
	}
	jsonOK(w, map[string]int{"queued": count})
}

// --- Agent endpoints (API key auth) ---

// handleHook receives an incoming message from the relay agent on the Mac.
func (T *Phantom) handleHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := validateAPIKey(T.DB, r.Header.Get("X-API-Key")); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		RowID       int64    `json:"row_id,omitempty"`
		ChatID      string   `json:"chat_id"`
		Handle      string   `json:"handle"`
		DisplayName string   `json:"display_name"`
		Text        string   `json:"text"`
		Timestamp   string   `json:"timestamp"`
		Images      []string `json:"images,omitempty"` // base64-encoded image data
		Videos      []string `json:"videos,omitempty"` // base64-encoded video data
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ChatID == "" || (req.Text == "" && len(req.Images) == 0 && len(req.Videos) == 0) {
		http.Error(w, "chat_id and text or image/video required", http.StatusBadRequest)
		return
	}
	req.Text = stripLeadingArtifact(req.Text)

	// Loop-back guard: if the bridge's ROWID + sentText skip both missed
	// (slow chat.db commit, bridge restart), our own reply text could
	// arrive here as if the user typed it. Apply cleanMessageText first
	// since the body extractor may prepend typedstream prefixes that
	// would otherwise prevent the comparison from matching. Empty handle
	// is the only path that can be a loop-back; user messages from
	// another device also use empty handle (is_from_me=1) but won't
	// match the recent-reply set.
	if req.Handle == "" && req.Text != "" {
		clean := cleanMessageText(req.Text)
		if T.matchesRecentReply(clean) {
			Log("[phantom] dropping loop-back of our own reply (chat_id=%s, %d chars)", req.ChatID, len(clean))
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}

	// Normalize from_me messages: the relay sends the owner's phone number as the
	// handle for messages sent from this device. Zero it out so ownerLabel() is used
	// instead of the raw number (which confuses the gatekeeper's wake-word check).
	cfg := defaultConfig(T.DB)
	if cfg.OwnerHandle != "" && req.Handle == cfg.OwnerHandle {
		req.Handle = ""
	}
	// NOTE: there used to be a fallback here that zeroed req.Handle
	// when the chat ID's suffix matched req.Handle. That was BACKWARDS
	// for iMessage 1:1 chats — the chat ID is named after the OTHER
	// party (chat_id "iMessage;-;+15551234567" where +1555... is the
	// contact, not the owner). So every message from the contact got
	// flagged as "from_me", causing the bot to think the other party's
	// messages were the owner's own. Removed. If OwnerHandle isn't
	// configured, owner-typed messages will appear with the owner's
	// raw handle in history rather than normalized to ownerLabel —
	// minor cosmetic issue, but vastly better than misattributing
	// every incoming message.

	// Upsert the incoming conversation record (may be an alias).
	var incomingConv Conversation
	knownConv := T.DB.Get(conversationTable, req.ChatID, &incomingConv)
	incomingConv.ChatID = req.ChatID
	incomingConv.Handle = req.Handle
	if req.DisplayName != "" {
		incomingConv.DisplayName = req.DisplayName
	}
	incomingConv.Updated = now()
	T.DB.Set(conversationTable, req.ChatID, incomingConv)

	// Resolve the active conversation: route to a primary if this is an alias.
	// Messages are stored and processed under the primary chat_id; replies go to
	// the original sender address so iMessage delivers them correctly.
	//
	// routingResolved tracks whether we know how to route this conversation —
	// either it's been linked to a primary via an alias (isAlias=true) or
	// it's a primary in its own right (isAlias=false, but routingResolved=true
	// because the conv already exists in the DB and isn't aliased to anyone).
	// The two used to share one variable named `aliasResolved`, which made
	// the log line `alias=true` ambiguous: it fired for both "real alias
	// resolved" and "this is its own primary, no alias involved." Splitting
	// makes the debug story honest.
	activeChatID := req.ChatID
	var conv Conversation
	routingResolved := false
	isAlias := false
	// stalePointerCleared is true when we just nuked an AliasOf pointing
	// at a missing primary. In that case we MUST run the scan rather than
	// auto-promote to primary — the AliasHandles on the real primary
	// still likely lists this chat ID, so re-resolution will find it.
	// Without this flag, the auto-promote path silently turns a known
	// alias into a brand-new primary the first time the gmail record is
	// momentarily missing (server restart, DB blip), and from then on
	// the conversation is split.
	stalePointerCleared := false

	// Fast path: this conversation already has a cached alias pointer.
	if incomingConv.AliasOf != "" {
		var primary Conversation
		if T.DB.Get(conversationTable, incomingConv.AliasOf, &primary) {
			activeChatID = incomingConv.AliasOf
			conv = primary
			routingResolved = true
			isAlias = true
			Log("[phantom] alias (cached) %s → %s", req.ChatID, activeChatID)
		} else {
			// Stale pointer — clear it and force a re-scan below.
			incomingConv.AliasOf = ""
			T.DB.Set(conversationTable, req.ChatID, incomingConv)
			stalePointerCleared = true
			Log("[phantom] stale alias pointer cleared for %s — re-scanning AliasHandles", req.ChatID)
		}
	}

	// Always scan AliasHandles when no cached alias pointer is set, even
	// for already-known convs. The previous behavior fast-pathed any
	// known conv with empty AliasOf as "confirmed primary, skip scan,"
	// which permanently locked in routing decisions made before the
	// alias relationship was configured. If +14155551234 was first seen
	// as its own conv and the user later added it to gmail's
	// AliasHandles, the fast-path kept routing it as a separate primary
	// forever. Running the scan here lets the cmcoffee@gmail.com primary
	// reclaim it via its AliasHandles entry, self-healing the routing
	// without manual DB surgery.
	//
	// The scan is O(convs * aliases-per-conv) which is negligible for
	// any plausible volume (hundreds of convs at most), so we just
	// always pay the cost rather than maintaining a reverse index.
	//
	// If the scan finds no match, the post-scan fallback below treats
	// the conv as its own primary — same outcome as the old fast path.
	if !routingResolved {
		var aliasConvsChecked int
		for _, k := range T.DB.Keys(conversationTable) {
			if k == req.ChatID {
				continue
			}
			var c Conversation
			if !T.DB.Get(conversationTable, k, &c) || len(c.AliasHandles) == 0 {
				continue
			}
			aliasConvsChecked++
			for _, ah := range c.AliasHandles {
				match := ah == req.Handle || ah == req.ChatID
				// When handle is empty (e.g. owner message or service doesn't
				// populate it), check if the alias handle is embedded in the
				// chat ID — same addresses that appear as the suffix of
				// "service;-;+14155551234" style chat IDs.
				if !match && req.Handle == "" {
					match = strings.HasSuffix(req.ChatID, ah)
				}
				if match {
					activeChatID = k
					conv = c
					routingResolved = true
					isAlias = true
					// Cache the alias pointer so future messages skip this scan.
					if incomingConv.AliasOf != k {
						incomingConv.AliasOf = k
						T.DB.Set(conversationTable, req.ChatID, incomingConv)
					}
					Log("[phantom] alias (handle match) %s → %s (matched %q)", req.ChatID, activeChatID, ah)
					break
				}
			}
			if routingResolved {
				break
			}
		}
		if !routingResolved {
			if aliasConvsChecked > 0 {
				Debug("[phantom] alias scan: no match for handle=%q chatID=%q — checked %d convs with alias_handles", req.Handle, req.ChatID, aliasConvsChecked)
			} else {
				Debug("[phantom] alias scan: no convs have alias_handles configured (handle=%q chatID=%q)", req.Handle, req.ChatID)
			}
			// Post-scan fallback: an already-known conv that nobody
			// claims as an alias is its own primary. This restores the
			// behavior the old fast path provided, but only AFTER the
			// scan has had a chance to redirect to a real primary.
			if knownConv {
				routingResolved = true
				// isAlias stays false — this conv is the primary.
			}
		}
	}
	_ = stalePointerCleared // retained for future use; the always-scan path makes it incidental

	// Sync members from history so every sender is captured.
	if !routingResolved {
		conv = syncMembersFromHistory(T.DB, req.ChatID)
	} else {
		conv = syncMembersFromHistory(T.DB, activeChatID)
	}

	// Store incoming message under the active (primary) chat_id.
	ts := req.Timestamp
	if ts == "" {
		ts = now()
	}
	var msgID string
	if req.RowID > 0 {
		msgID = fmt.Sprintf("%s-%d", ts, req.RowID)
	} else {
		msgID = ts + "-" + newID()
	}

	// Deduplication: if this exact message is already stored, skip LLM processing.
	msgKey := activeChatID + ":" + msgID
	var existingMsg PhantomMessage
	alreadyProcessed := T.DB.Get(messageTable, msgKey, &existingMsg)

	storeMessage(T.DB, PhantomMessage{
		ID:          msgID,
		ChatID:      activeChatID,
		Role:        "user",
		Handle:      req.Handle,
		DisplayName: req.DisplayName,
		Text:        req.Text,
		Images:      req.Images,
		Videos:      req.Videos,
		Timestamp:   ts,
	})

	if alreadyProcessed {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Process through LLM if relay is enabled for this conversation.
	// Replies are sent to req.ChatID (the actual sender address), not activeChatID.
	// For real aliases, honor auto-reply on either the incoming address or
	// the primary — that lets a user opt in on either side. For non-alias
	// primaries the incomingConv IS the conv, so checking it twice is just
	// noise; gate on isAlias to keep the intent obvious.
	autoReply := conv.AutoReply || (isAlias && incomingConv.AutoReply)
	Log("[phantom] hook from %s — enabled=%v auto_reply_all=%v conv_auto_reply=%v alias=%v primary=%v active=%s",
		req.Handle, cfg.Enabled, cfg.AutoReplyAll, autoReply, isAlias, routingResolved && !isAlias, activeChatID)
	if cfg.Enabled {
		// Gatekeeper applies to ALL incoming messages including the
		// owner's own — gatekeeperAllow's senderLabel resolution
		// already labels owner-handle messages as the owner, so the
		// rule can be written to "always allow if from owner" if
		// that's the desired behavior. Letting the rule decide gives
		// operators the option to wake-word-gate their own messages
		// (the original bypass forced an unconditional always-allow
		// for the owner, which broke wake-word setups).
		if !T.gatekeeperAllow(cfg, conv, activeChatID, req.Handle, req.DisplayName, req.Text, len(req.Images)+len(req.Videos)) {
			senderTag := req.Handle
			if senderTag == "" {
				senderTag = "owner"
			}
			Log("[phantom] gatekeeper blocked message from %s", senderTag)
			return
		}
		if cfg.AutoReplyAll || autoReply {
			// For self-messages (empty handle), reply to the original sender
			// address rather than the resolved primary — unless the incoming
			// chat ID uses an unresolvable service (e.g. "any;-;..." for
			// cross-service threads like Gmail), in which case fall back to
			// the primary so the reply can actually be delivered.
			deliverChatID := activeChatID
			if req.Handle == "" && !strings.HasPrefix(req.ChatID, "any;-;") {
				deliverChatID = req.ChatID
			}
			go func() {
				T.processCoalesced(activeChatID, deliverChatID, req.Handle, req.Text, conv)
			}()
		}
	}

	w.WriteHeader(http.StatusAccepted)
}

// handlePoll returns and removes all pending outbox items.
// The agent's in-memory retry queue handles re-delivery if osascript fails.
func (T *Phantom) handlePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := validateAPIKey(T.DB, r.Header.Get("X-API-Key")); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	items := drainOutbox(T.DB)
	if items == nil {
		items = []OutboxItem{}
	}
	jsonOK(w, items)
}

// gatekeeperAllow runs the configured gatekeeper prompt (if any) and returns
// true if the message should be processed. No gatekeeper = always allow.
func (T *Phantom) gatekeeperAllow(cfg PhantomConfig, conv Conversation, chatID, handle, displayName, text string, imageCount int) bool {
	// Combine the global gatekeeper rule with the per-conversation
	// rule. Both must be satisfied — the conv-level rule narrows but
	// never widens what global allows. This matches operators' mental
	// model: "global is what I want everywhere; per-conv is what I
	// want EXTRA for this specific chat."
	// Merge the master (global) ruleset with the per-conversation
	// rules into one numbered list. Rules are TRIGGERS for "respond"
	// — any single rule matching the message is enough to allow it.
	// Each line of each ruleset becomes its own numbered entry so
	// the model evaluates them independently and reports which one
	// (if any) tripped, rather than collapsing the whole list into
	// one fuzzy criterion.
	// Section-labeled merge — the rules are a single OR list (any
	// one match → YES), but rules from each source are grouped under
	// their own header so the LLM can't skim past the conversation
	// rules. Numbering continues across sections so each rule has a
	// unique number the LLM can name in the reason field.
	enumerateInto := func(b *strings.Builder, body string, startIdx int) int {
		idx := startIdx
		for _, ln := range strings.Split(body, "\n") {
			t := strings.TrimSpace(ln)
			if t == "" {
				continue
			}
			idx++
			fmt.Fprintf(b, "  %d. %s\n", idx, t)
		}
		return idx
	}
	var prompt string
	if cfg.GatekeeperPrompt != "" || conv.GatekeeperPrompt != "" {
		var b strings.Builder
		b.WriteString("Rules — answer YES if the message matches ANY single rule below (rules are alternatives, joined by OR). You MUST evaluate every rule in every section before deciding; do not stop at the first match.\n\n")
		idx := 0
		if strings.TrimSpace(cfg.GatekeeperPrompt) != "" {
			b.WriteString("Master rules (apply to every conversation):\n")
			idx = enumerateInto(&b, cfg.GatekeeperPrompt, idx)
			b.WriteString("\n")
		}
		if strings.TrimSpace(conv.GatekeeperPrompt) != "" {
			b.WriteString("Conversation rules (apply only to this conversation — evaluate each one fully):\n")
			idx = enumerateInto(&b, conv.GatekeeperPrompt, idx)
			b.WriteString("\n")
		}
		if idx > 0 {
			prompt = b.String()
		}
	}
	if prompt == "" {
		return true
	}
	if T.LLM == nil {
		return true
	}

	// Resolve the sender's display name from the member list, falling back to
	// displayName from the relay, then the raw handle. An empty handle means the
	// message came from the phone owner — use the configured owner name.
	senderLabel := handle
	if handle == "" {
		senderLabel = cfg.ownerLabel()
	} else if displayName != "" {
		senderLabel = displayName
	}
	for _, m := range conv.Members {
		matched := m.Handle == handle
		if !matched {
			for _, a := range m.Aliases {
				if a == handle {
					matched = true
					break
				}
			}
		}
		if matched && m.Name != "" {
			senderLabel = m.Name
			break
		}
	}

	// Build a clear description of what arrived so the LLM can evaluate it.
	var msgDesc string
	switch {
	case imageCount > 0 && text != "" && text != "[Image]":
		msgDesc = fmt.Sprintf("[image with caption: %s]", text)
	case imageCount > 0:
		msgDesc = fmt.Sprintf("[image, no text — %d image(s)]", imageCount)
	default:
		msgDesc = text
	}

	// Only inject recent context if the AI was active recently — this lets the
	// gatekeeper recognize follow-up messages without drowning in unrelated
	// human-to-human chat. Limit to 4 messages to keep the signal clean.
	var contextBlock string
	if chatID != "" {
		recent := recentMessages(T.DB, chatID, 4)
		hasAI := false
		for _, m := range recent {
			if m.Role == "assistant" {
				hasAI = true
				break
			}
		}
		if hasAI {
			personaName := cfg.PersonaName
			if conv.PersonaName != "" {
				personaName = conv.PersonaName
			}
			if personaName == "" {
				personaName = "assistant"
			}
			var b strings.Builder
			b.WriteString("\nRecent exchange:\n")
			for _, m := range recent {
				label := personaName
				if m.Role != "assistant" {
					label = m.Handle
					if label == "" {
						label = cfg.ownerLabel()
					}
					for _, mem := range conv.Members {
						if mem.Handle == m.Handle && mem.Name != "" {
							label = mem.Name
							break
						}
					}
				}
				b.WriteString(fmt.Sprintf("[%s] %s\n", label, m.Text))
			}
			contextBlock = b.String()
		}
	}

	sysPrompt := `You are a message filter. Reply with ONLY a JSON object — no other text:
{"answer": "YES", "reason": "one sentence"}

The rules are TRIGGERS connected by OR — each numbered rule describes a condition under which the AI should respond. answer is YES if the message satisfies AT LEAST ONE rule, NO if it satisfies NONE.

The rules may be split into "Master rules" and "Conversation rules" sections. EVERY rule in EVERY section must be evaluated against the message before you decide. Walk the list from rule 1 to the last rule explicitly — do not stop early, do not skip the Conversation rules, do not collapse multiple rules into a single criterion. The reason field should name the rule number that actually fired (or, if none fire, identify what was missing).

Apply each rule literally to every message, regardless of who sent it — including messages from the phone owner themselves. Do not grant any sender an implicit exception based on identity, role, or familiarity. If a rule wants the owner auto-allowed, it will say so explicitly.`
	// Tag the owner explicitly so a rule that wants to differentiate
	// "the owner vs. an outside contact" has the signal to do so. The
	// strong sysPrompt above prevents the model from implicitly
	// allowing owner messages without the rule actually saying it.
	displaySender := senderLabel
	if handle == "" {
		displaySender = senderLabel + " (phone owner)"
	}
	// Resolve the persona's display name so rules like "answer only
	// when the user calls me by name" actually work. Per-conversation
	// PersonaName overrides the global cfg.PersonaName; falls back to
	// "the assistant" so rules don't get a literal "you" placeholder.
	gkPersonaName := cfg.PersonaName
	if conv.PersonaName != "" {
		gkPersonaName = conv.PersonaName
	}
	if gkPersonaName == "" {
		gkPersonaName = "the assistant"
	}
	identity := fmt.Sprintf("Your name in this conversation is \"%s\". When a rule refers to \"you\", \"the AI\", \"the assistant\", or asks whether the sender mentioned you by name, treat that as referring to \"%s\" — including common nicknames or obvious typos of that name.\n\n", gkPersonaName, gkPersonaName)
	var userMsg string
	if contextBlock != "" {
		userMsg = fmt.Sprintf("%sRules:\n%s\n\nRecent exchange (context only):\n%s\n\nNew message to evaluate:\nFrom: %s\nText: %s\n\nDoes the new message satisfy every rule on its own, OR is it a natural follow-up to the recent AI exchange above?", identity, prompt, strings.TrimSpace(contextBlock), displaySender, msgDesc)
	} else {
		userMsg = fmt.Sprintf("%sRules:\n%s\n\nNew message to evaluate:\nFrom: %s\nText: %s", identity, prompt, displaySender, msgDesc)
	}

	// Run cleanMessageText on the log preview only — the gatekeeper LLM
	// still sees the raw msgDesc (with whatever typedstream prefix bytes
	// the bridge surfaced) so nothing about its evaluation changes;
	// just tidies the log output so a reader scanning gohort.log
	// isn't visually drowned by `streamtyped NSAttributedString iI ...`
	// noise alongside the real text.
	Log("[phantom] gatekeeper eval — from=%s msg=%q", senderLabel, truncateStr(cleanMessageText(msgDesc), 120))

	resp, err := T.LLM.Chat(context.Background(),
		[]Message{{Role: "user", Content: userMsg}},
		WithSystemPrompt(sysPrompt), WithJSONMode(), WithRouteKey("app.phantom"), WithThink(false),
	)
	if err != nil {
		Log("[phantom] gatekeeper LLM error: %v — blocking", err)
		return false
	}

	var gkResp struct {
		Answer string `json:"answer"`
		Reason string `json:"reason"`
	}
	if err := DecodeJSON(resp.Content, &gkResp); err != nil {
		// Fallback: scan raw text for YES/NO.
		answer := strings.ToUpper(strings.TrimSpace(resp.Content))
		yesIdx := wordIndex(answer, "YES")
		noIdx := wordIndex(answer, "NO")
		if yesIdx >= 0 && (noIdx < 0 || yesIdx < noIdx) {
			Log("[phantom] gatekeeper: ALLOW (raw) — %q", truncateStr(resp.Content, 80))
			return true
		}
		Log("[phantom] gatekeeper: BLOCK (raw/ambiguous) — %q", truncateStr(resp.Content, 80))
		return false
	}

	answer := strings.ToUpper(strings.TrimSpace(gkResp.Answer))
	if strings.HasPrefix(answer, "YES") {
		Log("[phantom] gatekeeper: ALLOW — %s", gkResp.Reason)
		return true
	}
	Log("[phantom] gatekeeper: BLOCK — %s", gkResp.Reason)
	return false
}

// wordIndex returns the position of word in s (uppercase), or -1 if not found.
// It checks that the word is not embedded inside a longer word (e.g. "NO" in "KNOWN").
func wordIndex(s, word string) int {
	idx := 0
	for {
		i := strings.Index(s[idx:], word)
		if i < 0 {
			return -1
		}
		abs := idx + i
		before := abs == 0 || !isLetter(rune(s[abs-1]))
		after := abs+len(word) >= len(s) || !isLetter(rune(s[abs+len(word)]))
		if before && after {
			return abs
		}
		idx = abs + 1
	}
}

func isLetter(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

// processCoalesced acquires the per-chat slot and runs processMessage, coalescing
// any messages that arrive while the LLM is working into a single follow-up reply.
// If another goroutine is already running for this convChatID, the new message
// is queued and the running goroutine will pick it up when it finishes.
//
// convChatID is the alias-resolved primary chat_id used for storage, history,
// and the per-chat slot key — every conversation participant routes through
// the same primary so the LLM sees one continuous thread regardless of which
// alias address the bridge delivered the hook from.
//
// deliverChatID is the iMessage address replies are sent to. For aliased
// inbound (Craig typed from his iPhone, hook came in via iMessage;-;phone#)
// we want to reply to the same address so it lands in the right thread on
// the user's device, NOT to the resolved primary. Decoupling the two
// closes the bug where storage and history were under different IDs.
func (T *Phantom) processCoalesced(convChatID, deliverChatID, handle, text string, conv Conversation) {
	slotVal, _ := T.chatSlots.LoadOrStore(convChatID, &chatPending{})
	slot := slotVal.(*chatPending)

	slot.mu.Lock()
	if slot.active {
		// Already processing — queue this message for the next pass.
		slot.handle = handle
		slot.text = text
		slot.conv = conv
		slot.deliverChatID = deliverChatID
		slot.queued = true
		slot.generation++
		slot.mu.Unlock()
		Log("[phantom] coalescing message from %s into in-flight reply for %s", handle, convChatID)
		return
	}
	slot.active = true
	slot.mu.Unlock()

	for {
		// Capture the current generation so processMessage can check whether a
		// newer message arrived before it sends its reply.
		slot.mu.Lock()
		gen := slot.generation
		slot.mu.Unlock()

		shouldSend := func() bool {
			slot.mu.Lock()
			defer slot.mu.Unlock()
			return slot.generation == gen
		}

		T.processMessage(convChatID, deliverChatID, handle, text, conv, shouldSend)

		slot.mu.Lock()
		if !slot.queued {
			slot.active = false
			slot.mu.Unlock()
			return
		}
		// Pick up the accumulated message and loop.
		handle = slot.handle
		text = slot.text
		conv = slot.conv
		deliverChatID = slot.deliverChatID
		slot.queued = false
		slot.mu.Unlock()
		Log("[phantom] running coalesced reply for %s", convChatID)
	}
}

// buildConvTools assembles the tool set for a conversation.
// When includeScheduler is false the scheduling tools are omitted — used for
// proactive/scheduled calls that must not be able to create or cancel tasks.
func (T *Phantom) buildConvTools(chatID, handle string, conv Conversation, cfg PhantomConfig, sess *ToolSession, includeScheduler bool) []AgentToolDef {
	toolNames := cfg.EnabledTools
	if conv.EnabledTools != nil {
		toolNames = conv.EnabledTools
	}
	toolEnabled := func(name string) bool {
		if len(toolNames) == 0 {
			return true
		}
		for _, n := range toolNames {
			if n == name {
				return true
			}
		}
		return false
	}

	var tools []AgentToolDef
	// Control-flow tools that are always available regardless of
	// EnabledTools — they're prompt-shape essentials, not capability
	// grants. stay_silent lets the LLM decline to reply; keep_going
	// lets it request another round when it needs to plan more before
	// acting (without that, "let me think" lands as visible text and
	// the round ends).
	if st, err := GetAgentToolsWithSession(sess, "stay_silent", "keep_going"); err == nil {
		tools = append(tools, st...)
	}

	if len(toolNames) > 0 {
		// Pre-build the secure-API tool catalog so we can match
		// `call_<credname>` entries against it. Three independent
		// gates apply:
		//   1. Per-credential Disabled flag (handled inside
		//      BuildSecureAPITools).
		//   2. Phantom-app master switch cfg.SecureAPIEnabled —
		//      when off, BuildSecureAPITools isn't called at all so
		//      no credential is reachable via phantom regardless of
		//      individual credential or per-conv state.
		//   3. Per-conv EnabledTools opt-in — only call_<credname>
		//      entries listed in the current conv's EnabledTools
		//      are exposed.
		var secureAPIByName map[string]AgentToolDef
		if cfg.SecureAPIEnabled {
			// BuildTools respects Restricted — when the operator
			// flips a credential to "Restricted" in admin, it drops
			// out of phantom's resolvable set here too. Even if a
			// conv's EnabledTools still names call_<credname>, the
			// lookup misses and the entry is silently ignored.
			// LLM-defined api-mode temp tools wrapping the same
			// credential keep working — admin approval is the trust
			// gate for those.
			secureAPI := Secure().BuildTools(sess)
			secureAPIByName = make(map[string]AgentToolDef, len(secureAPI))
			for _, td := range secureAPI {
				secureAPIByName[td.Tool.Name] = td
			}
			var availNames []string
			for n := range secureAPIByName {
				availNames = append(availNames, n)
			}
			Debug("[phantom] secure-api: master ON, %d available (%v), enabled tools in conv: %v", len(secureAPIByName), availNames, toolNames)
		} else {
			secureAPIByName = map[string]AgentToolDef{}
			Debug("[phantom] secure-api: master switch OFF in PhantomConfig.SecureAPIEnabled — no credentials reachable")
		}

		var registryNames []string
		for _, n := range toolNames {
			switch n {
			case "memory", "save_memory", "schedule_callback", "follow_up":
				// phantom-specific — handled below
			case "generate_image":
				if ImageGenerationAvailable() {
					registryNames = append(registryNames, n)
				}
			default:
				// Secure-API tools (call_<credname>) live outside the
				// global RegisteredChatTools registry — they're built
				// per-session from the encrypted credential store.
				// Match those first so they don't fall through to
				// GetAgentToolsWithSession (which would log "not
				// registered" and skip).
				if td, ok := secureAPIByName[n]; ok {
					tools = append(tools, td)
					continue
				}
				// Persistent temp tools also aren't in the global
				// registry — they're loaded later from sess.TempTools.
				// Skip names that don't resolve here so a single
				// unknown entry can't cause GetAgentToolsWithSession
				// to fail-fast and drop the whole registry batch.
				if _, ok := FindChatTool(n); !ok {
					continue
				}
				registryNames = append(registryNames, n)
			}
		}
		if len(registryNames) > 0 {
			if rt, err := GetAgentToolsWithSession(sess, registryNames...); err != nil {
				Log("[phantom] tool resolve error: %v", err)
			} else {
				tools = append(tools, rt...)
			}
		}
	}
	// Persistent temp tools: tools defined in chat with persist=true
	// and approved by the admin. They live in sess.TempTools (loaded
	// at session creation in processReply). Surface them through the
	// per-conv picker like any other tool — same rule as the rest of
	// buildConvTools: when EnabledTools is empty (no curation), all
	// api-mode temp tools fire and all shell-mode ones are skipped
	// (run_local-style default-deny for arbitrary exec). When
	// EnabledTools IS curated, the operator's list is authoritative
	// for both modes — listing a shell-mode temp tool there is the
	// explicit opt-in for that conv, mirroring how run_local works.
	dyn := temptool.BuildAgentToolDefs(sess)
	var dynNames []string
	for _, d := range dyn {
		dynNames = append(dynNames, d.Tool.Name)
	}
	Log("[phantom] buildConvTools: sess.Username=%q, BuildAgentToolDefs returned %d (%v), conv.EnabledTools curated=%v", sess.Username, len(dyn), dynNames, len(toolNames) > 0)
	if len(dyn) > 0 {
		var added []string
		var skippedNotEnabled []string
		var skippedShell []string
		for _, td := range dyn {
			isShell := false
			for _, cap := range td.Tool.Caps {
				if cap == CapExecute {
					isShell = true
					break
				}
			}
			if len(toolNames) > 0 {
				if !toolEnabled(td.Tool.Name) {
					skippedNotEnabled = append(skippedNotEnabled, td.Tool.Name)
					continue
				}
			} else if isShell {
				skippedShell = append(skippedShell, td.Tool.Name)
				continue
			}
			tools = append(tools, td)
			added = append(added, td.Tool.Name)
		}
		Log("[phantom] persistent temp tools: added=%v, skipped_not_enabled=%v, skipped_shell_default_deny=%v", added, skippedNotEnabled, skippedShell)
	}

	// Memory tool: accept either the new "memory" name or the legacy
	// "save_memory" entry for backward compat with existing conv
	// EnabledTools lists. Both expose the same grouped tool now.
	if toolEnabled("memory") || toolEnabled("save_memory") {
		tools = append(tools, memoryGroupedToolDef(T.DB, chatID))
	}
	if toolEnabled("follow_up") {
		tools = append(tools, followUpToolDef(T.DB, chatID, handle))
	}
	if includeScheduler && toolEnabled("schedule_callback") {
		tools = append(tools, T.schedulerToolDef(chatID, handle))
		tools = append(tools, T.listScheduledToolDef(chatID))
		tools = append(tools, T.cancelScheduledToolDef(chatID))
	}
	// look_at_attachment: lazy-load any prior conversation attachment
	// by reference ID (image-N / video-N from history annotations).
	// Always-on; the model uses it only when a follow-up question
	// requires re-examining an earlier image. fetchHistory captures
	// the DB at call time so newly-arrived attachments are visible.
	tools = append(tools, buildLookAtAttachmentTool(
		func() []PhantomMessage { return recentMessages(T.DB, chatID, 100) },
		func(b []byte) { sess.AppendViewImage(b) },
		func(b []byte) {
			// Video lookup not yet supported — needs frame sampling.
			// Stash as a single-frame view image so the model at least
			// gets the first frame; better than nothing for "look at that
			// video" follow-ups. Future: wire up the same yt-dlp +
			// sample-frames pipeline used by view_video.
			sess.AppendViewImage(b)
		},
	))
	return tools
}

// processMessage runs an incoming message through the LLM and queues a reply.
// shouldSend, if non-nil, is called before the LLM call and again before
// enqueueing the reply; returning false aborts to let a coalesced re-run take
// over with the full updated history.
//
// convChatID is the alias-resolved primary used for storage and history fetch;
// deliverChatID is the address the outbound reply is delivered to (may equal
// convChatID, but for aliased inbound it's the original incoming address so
// the reply lands in the user's actual thread).
func (T *Phantom) processMessage(convChatID, deliverChatID, handle, text string, conv Conversation, shouldSend func() bool) {
	chatID := convChatID // legacy local name used throughout the body for storage/history
	if T.LLM == nil {
		Log("[phantom] processMessage: LLM not configured")
		return
	}
	cfg := defaultConfig(T.DB)
	isGroup := len(conv.Members) > 1

	// labelHandle wraps a raw iMessage handle with a kind hint so the
	// LLM knows what it's looking at. Phone numbers (+15551234567)
	// become "phone: +15551234567"; emails become "email: x@y". Without
	// the label the model treats the parenthetical as opaque metadata
	// and won't recall it when asked "what's their phone number?".
	labelHandle := func(h string) string {
		h = strings.TrimSpace(h)
		if h == "" {
			return ""
		}
		if strings.Contains(h, "@") {
			return "email: " + h
		}
		if strings.HasPrefix(h, "+") || (len(h) > 0 && h[0] >= '0' && h[0] <= '9') {
			return "phone: " + h
		}
		return h
	}

	var senderDesc string
	if handle == "" {
		senderDesc = cfg.ownerLabel()
	} else {
		labeled := labelHandle(handle)
		senderDesc = labeled
		if conv.DisplayName != "" && !isGroup {
			senderDesc = fmt.Sprintf("%s (%s)", conv.DisplayName, labeled)
		}
		for _, m := range conv.Members {
			matched := m.Handle == handle
			if !matched {
				for _, a := range m.Aliases {
					if a == handle {
						matched = true
						break
					}
				}
			}
			if matched && m.Name != "" {
				senderDesc = fmt.Sprintf("%s (%s)", m.Name, labeled)
				break
			}
		}
	}

	// Build conversation history as LLM messages.
	history := recentMessages(T.DB, chatID, 20)
	// labelForMsg resolves a display name for a stored message's sender.
	labelForMsg := func(m PhantomMessage) string {
		if m.Handle == "" {
			return cfg.ownerLabel()
		}
		for _, mem := range conv.Members {
			matched := mem.Handle == m.Handle
			if !matched {
				for _, a := range mem.Aliases {
					if a == m.Handle {
						matched = true
						break
					}
				}
			}
			if matched && mem.Name != "" {
				return mem.Name
			}
		}
		if m.DisplayName != "" {
			return m.DisplayName
		}
		return m.Handle
	}
	// fmtMsgTime parses an RFC3339 timestamp and returns a short bracket
	// prefix in relative form: "[2h ago] ", "[just now] ", "[3d ago] ".
	// Relative beats absolute for an LLM — the model can reason about
	// "how long has it been since the last reply" without doing date
	// math, which is what actually matters for messaging-conversation
	// pacing decisions.
	fmtMsgTime := func(ts string) string {
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil || ts == "" {
			return ""
		}
		return "[" + relTimeShort(t) + "] "
	}
	var msgs []Message
	for _, m := range history {
		role := "user"
		var content string
		if m.Role == "assistant" {
			role = "assistant"
			// NO timestamp prefix on the assistant's own messages —
			// the model is a strong format-mimic and starts emitting
			// "[just now] ..." on every new reply if its prior turns
			// show that pattern. The gap signal is already encoded
			// on the user-side timestamps, which is what actually
			// matters for messaging-conversation pacing decisions.
			content = m.Text
		} else {
			content = fmtMsgTime(m.Timestamp) + labelForMsg(m) + ": " + cleanMessageText(m.Text)
		}
		msgs = append(msgs, Message{Role: role, Content: content})
	}
	// Annotate history user messages that had attachments with stable
	// per-conversation reference IDs (image-1, video-2, etc) so the
	// model can call look_at_attachment to re-fetch the bytes when a
	// follow-up question requires visual detail beyond what its
	// in-context multimodal recall covers. Counter walks forward
	// chronologically so IDs match the order things appeared in the
	// conversation. Attachment metadata (type, size, dimensions if
	// extractable) goes alongside the ID so the model has enough
	// signal to decide whether it needs to look or not.
	imgCounter, vidCounter := 0, 0
	for i, m := range history {
		if m.Role != "user" {
			continue
		}
		if len(m.Images) == 0 && len(m.Videos) == 0 {
			continue
		}
		var tags []string
		for _, b64 := range m.Images {
			imgCounter++
			tags = append(tags, fmt.Sprintf("[image-%d | %s]", imgCounter, attachmentMetadata(b64, "image")))
		}
		for _, b64 := range m.Videos {
			vidCounter++
			tags = append(tags, fmt.Sprintf("[video-%d | %s]", vidCounter, attachmentMetadata(b64, "video")))
		}
		if len(tags) > 0 {
			msgs[i].Content = msgs[i].Content + "\n" + strings.Join(tags, " ")
		}
	}
	// Strip the current message from history (matched by raw text) and re-inject
	// it as a clearly labelled final turn so the model doesn't treat it as pending.
	if len(history) > 0 && history[len(history)-1].Role == "user" && history[len(history)-1].Text == text {
		msgs = msgs[:len(msgs)-1]
	}
	// Append the new message with a header that separates it from history.
	cleaned := cleanMessageText(text)
	newMsg := Message{Role: "user", Content: "--- NEW MESSAGE (respond to this) ---\n" + senderDesc + ": " + cleaned}
	// Carry images and videos on the new message if present.
	if len(history) > 0 {
		last := history[len(history)-1]
		if last.Role == "user" && last.Text == text {
			for _, b64 := range last.Images {
				data, err := base64.StdEncoding.DecodeString(b64)
				if err == nil {
					newMsg.Images = append(newMsg.Images, data)
				}
			}
			for _, b64 := range last.Videos {
				data, err := base64.StdEncoding.DecodeString(b64)
				if err == nil {
					newMsg.Videos = append(newMsg.Videos, data)
				}
			}
		}
	}
	// Annotate the new message text with what's actually attached on
	// THIS turn so the model doesn't infer attachment type from earlier
	// conversation context. Without this, history that referenced a
	// prior video can prime the model to call download_video on a
	// freshly-arrived image. The tag goes after the header so it's the
	// first thing the model sees alongside the sender + text.
	if n := len(newMsg.Images); n > 0 {
		tag := fmt.Sprintf("\n[CURRENT ATTACHMENT: %d image(s) — already in your context as multimodal content; do NOT call download_video, fetch_image, or find_image for these]\n", n)
		newMsg.Content = "--- NEW MESSAGE (respond to this) ---" + tag + senderDesc + ": " + cleaned
	}
	if n := len(newMsg.Videos); n > 0 {
		tag := fmt.Sprintf("\n[CURRENT ATTACHMENT: %d video(s) — already sampled into frames in your context; do NOT call download_video for these, the file is already attached]\n", n)
		newMsg.Content = "--- NEW MESSAGE (respond to this) ---" + tag + senderDesc + ": " + cleaned
	}
	msgs = append(msgs, newMsg)

	personaName := cfg.PersonaName
	if conv.PersonaName != "" {
		personaName = conv.PersonaName
	}
	personality := cfg.Personality
	if conv.Personality != "" {
		personality = conv.Personality
	}
	// Conversation rules: global rules apply to ALL conversations as a
	// baseline; per-conv rules add on top of (don't replace) the global
	// set. This lets an operator put universal expectations ("always
	// reply in plain text", "never reveal system info") in the master
	// config and add per-relationship tweaks ("with Mom, keep replies
	// short and warm") in the per-conv override without having to
	// repeat the universal rules each time.
	convRules := cfg.SystemPrompt
	if conv.SystemPrompt != "" {
		if convRules != "" {
			convRules = convRules + "\n\n" + conv.SystemPrompt
		} else {
			convRules = conv.SystemPrompt
		}
	}
	systemPrompt := buildSystemPrompt(personality, convRules)

	var membersNote string
	if len(conv.Members) > 1 {
		ownerLabel := cfg.ownerLabel()
		memberList := fmt.Sprintf("%s (phone owner — their messages are not from an outside contact)", ownerLabel)
		if others := formatMembers(conv.Members); others != "" {
			memberList += ", " + others
		}
		membersNote = fmt.Sprintf("\n\nThis is a group conversation. Participants: %s.", memberList)
	} else {
		// 1:1 chat — spell out who is who. Without this the model sees
		// alternating "Mom: ..." / "Craig: ..." / "Mom: ..." in user-role
		// messages and gets confused about its own identity (it IS
		// Craig the phone owner, replying as Craig). assistant-role
		// messages are messages this bot already sent; "Craig: ..."
		// user-role messages are the human owner typing manually.
		ownerLabel := cfg.ownerLabel()
		membersNote = fmt.Sprintf(
			"\n\nThis is a one-on-one conversation between %s (the phone owner — that's YOU) and %s. "+
				"Messages prefixed with \"%s:\" are from %s, the other party. "+
				"Messages prefixed with \"%s:\" with role=user are from %s typing directly on their phone — those are NOT from you (the assistant); treat them as additional context the user typed alongside the bot. "+
				"Messages with role=assistant are your own prior replies. Never confuse the two.",
			ownerLabel, senderDesc,
			senderDesc, senderDesc,
			ownerLabel, ownerLabel,
		)
	}

	// Bail out before the expensive LLM call if a newer message has already arrived.
	if shouldSend != nil && !shouldSend() {
		Log("[phantom] newer message pending for %s — skipping stale LLM call", chatID)
		return
	}

	sysPrompt := fmt.Sprintf(
		"Current date and time: %s\n\nYour name is %s. The person messaging you is %s.\n\n%s%s%s\n\n"+
			"When you know someone's name, use it naturally in conversation. Never use more than one emoji in a message, and use them sparingly. "+
			"Keep responses varied — avoid falling into repetitive patterns of jokes or phrases even across a long conversation. "+
			"When you learn something worth remembering about the person — their name, preferences, relationships, or important facts — call memory(action=\"save\", note=\"...\") before replying. "+
			"When asked about something you can look up or do with a tool, use the tool — never say you can't do something if a tool can do it. "+
			"PARTICIPANT CONTACT INFO: The phone numbers and emails of the people in this conversation are listed above as labeled handles (\"phone: +15551234567\", \"email: x@y\"). When the phone owner asks for a participant's number, email, or how to reach them, share the labeled handle directly — that information is theirs to recall, not private data to refuse. \"I don't have their number\" is wrong when the handle is right there in your context. "+
			"Your text replies must be plain text only — no markdown, no bullet points, no headers. This is a text message conversation.",
		time.Now().Format("Monday, January 2, 2006 3:04 PM MST"),
		personaName, senderDesc, systemPrompt, membersNote, memoryBlock(T.DB, chatID),
	)
	// Surface the model's last few assistant replies as an explicit
	// "do NOT repeat these" callout. The history already contains
	// these messages, but Qwen-class models pattern-match heavily and
	// will re-use the same joke or phrasing turn-after-turn unless
	// told otherwise. Calling them out near the end of the system
	// prompt (closest to generation) raises the signal.
	if dontRepeat := recentAssistantReplies(history, 4); dontRepeat != "" {
		sysPrompt += dontRepeat
	}

	sess := &ToolSession{
		LLM:          T.LLM,
		LeadLLM:      T.LeadLLM,
		WorkspaceDir: ensurePhantomWorkspace(cfg),
		DB:           T.DB,
		// ChatSessionID anchors session-scoped temp tools to this
		// phantom conversation. Without it, a tool the LLM creates
		// with persist=false in turn N is gone in turn N+1 — phantom
		// rebuilds this ToolSession every incoming message, so
		// session-scoped state has to round-trip through the DB.
		// Using convChatID keeps a tool the model created in this
		// conversation visible only to subsequent messages in this
		// same conversation (won't bleed into other phantom convs).
		ChatSessionID: convChatID,
		// Username scopes persistent temp tools. Use the first admin
		// account — that's the identity that originally approved
		// pending temp tools via the admin UI, so loading under the
		// same name surfaces them here. OwnerHandle (a phone number)
		// is wrong for this; it's a messaging identity, not a tool
		// owner. Falls back to empty when no admin exists yet, which
		// just disables persistence loading.
		Username: phantomToolOwner(T.DB),
		// RoutingTarget tells generic tools (e.g. watcher) where to
		// dispatch follow-up payloads back to. Mirrors what the normal
		// reply path uses: deliverChatID for ChatID + a recipient
		// handle. We try the per-message handle (most accurate),
		// then conv.Handle (set on the conv record), then leave the
		// target empty so RoutingTarget falls back to "log" — better
		// to drop the watcher's iMessage delivery than queue a
		// broken outbox item the bridge can't route.
		RoutingTarget: phantomRoutingTarget(deliverChatID, handle, conv.Handle),
	}
	// Load any persistent temp tools approved for this owner so the
	// LLM can use them in phantom too. Same pool the chat agent reads
	// when the admin is logged in as the same email.
	loaded := LoadPersistentTempTools(sess.DB, sess.Username)
	loadedNames := make([]string, 0, len(loaded))
	for _, p := range loaded {
		t := p.Tool
		if err := sess.AppendTempTool(&t); err != nil {
			Log("[phantom] AppendTempTool(%q) failed for owner %q: %v", t.Name, sess.Username, err)
			continue
		}
		loadedNames = append(loadedNames, t.Name)
	}
	// Session-scoped temp tools — ones the LLM created in a prior
	// message of THIS phantom conversation via persist=false. Without
	// this load the model forgets its own tools between user turns
	// (each processMessage rebuilds the ToolSession), defeating the
	// create-then-use flow and making list_temp_tools incorrectly
	// report no session tools after the conversation continues.
	sessionLoaded := LoadSessionTempTools(sess.DB, sess.ChatSessionID)
	sessionLoadedNames := make([]string, 0, len(sessionLoaded))
	for _, t := range sessionLoaded {
		tool := t
		if err := sess.AppendTempTool(&tool); err != nil {
			Log("[phantom] AppendTempTool(%q) failed for conv %q (session-scoped): %v", tool.Name, sess.ChatSessionID, err)
			continue
		}
		sessionLoadedNames = append(sessionLoadedNames, tool.Name)
	}
	if len(sessionLoadedNames) > 0 {
		Log("[phantom] loaded %d session-scoped temp tool(s) for conv %s: %v", len(sessionLoadedNames), sess.ChatSessionID, sessionLoadedNames)
	}
	Log("[phantom] persistent temp tools for owner %q: %d loaded → %v", sess.Username, len(loadedNames), loadedNames)
	// send_status: enqueue an immediate outbox item so the user receives
	// the status as its own iMessage before the eventual reply. The
	// outbox is FIFO so order is preserved. We also persist it as an
	// assistant message in chat history for transcript parity.
	sess.StatusCallback = func(text string) {
		text = strings.TrimSpace(stripEmojis(text))
		if text == "" {
			return
		}
		Log("[phantom] send_status for %s: %q", handle, text)
		T.rememberRecentReply(text)
		storeMessage(T.DB, PhantomMessage{
			ID:        now() + "-" + newID(),
			ChatID:    chatID,
			Role:      "assistant",
			Text:      text,
			Timestamp: now(),
		})
		enqueueOutbox(T.DB, OutboxItem{
			ID:      newID(),
			ChatID:  deliverChatID,
			Handle:  handle,
			Text:    text,
			Type:    "status",
			Created: now(),
		})
	}
	tools := T.buildConvTools(chatID, handle, conv, cfg, sess, true)

	Log("[phantom] processing reply for %s (%d history msgs, %d tools)", senderDesc, len(msgs), len(tools))

	phantomChatOpts := buildThinkOpts("app.phantom")
	resp, _, err := T.RunAgentLoop(context.Background(), msgs, AgentLoopConfig{
		SystemPrompt: sysPrompt,
		Tools:        tools,
		MaxRounds:    15,
		RouteKey:     "app.phantom",
		PromptTools:  T.PromptTools,
		ChatOptions:  phantomChatOpts,
		Confirm:      func(string, string) bool { return true },
		// Drain any view-images deposited by tools (view_video,
		// download_video frame sampling) into a follow-up user message
		// at the next round so the LLM sees them. Images go to the LLM
		// only — not delivered to the iMessage user via sess.Images.
		OnRoundStart: func() []Message {
			imgs := sess.DrainViewImages()
			if len(imgs) == 0 {
				return nil
			}
			return []Message{{
				Role:    "user",
				Content: "Here are the sampled frames for visual analysis:",
				Images:  imgs,
			}}
		},
	})
	if err != nil {
		Log("[phantom] chat error for %s: %v", handle, err)
		return
	}

	var reply, reasoning string
	if resp != nil {
		reply = strings.TrimSpace(stripEmojis(resp.Content))
		reasoning = resp.Reasoning
	}

	sessionImages := filterNewImages(sess.Images)
	sessionVideos := filterNewVideos(sess.Videos)
	// Audio normalization: anything in sessionVideos that's actually
	// non-AAC audio (MP3 from ElevenLabs, WAV/OGG from other tools) gets
	// transcoded to M4A/AAC server-side. Real video clips and audio
	// already in AAC pass through unchanged. iMessage renders M4A as a
	// clean audio bubble that survives MMS fallback.
	sessionVideos = normalizeAudioForDelivery(sessionVideos)

	// stay_silent suppresses the LLM's text reply but lets gathered
	// attachments through. Pattern: LLM calls download_video, then
	// stay_silent — user receives the file with no caption. If nothing
	// was gathered, silence collapses to "send nothing at all" (the
	// classic stay_silent semantic).
	if sess.Silenced {
		if len(sessionImages) == 0 && len(sessionVideos) == 0 {
			Log("[phantom] stay_silent called for %s — no reply sent", handle)
			return
		}
		Log("[phantom] stay_silent called for %s — text suppressed, %d images / %d videos still delivered", handle, len(sessionImages), len(sessionVideos))
		reply = ""
	}

	if reply == "" && len(sessionImages) == 0 && len(sessionVideos) == 0 {
		// Capture diagnostic context so we can tell why the LLM came back empty.
		if resp == nil {
			Log("[phantom] empty LLM response for %s — nil response from RunAgentLoop", handle)
		} else {
			Log("[phantom] empty LLM response for %s — content=%d chars, reasoning=%d chars, tool_calls=%d",
				handle, len(resp.Content), len(resp.Reasoning), len(resp.ToolCalls))
		}
		return
	}

	// Final check: if a newer message arrived while the LLM was working, discard
	// this reply — the coalesced re-run will produce a reply covering everything.
	if shouldSend != nil && !shouldSend() {
		Log("[phantom] newer message arrived during LLM call for %s — discarding reply", chatID)
		return
	}

	Log("[phantom] reply generated for %s (%d chars, %d images, %d videos, %d files), queuing outbox", handle, len(reply), len(sessionImages), len(sessionVideos), len(sess.Files))
	// Phantom doesn't currently deliver generic file attachments through
	// the bridge (only images and videos). If the LLM called attach_file,
	// surface that loudly so the operator notices the silent drop and
	// either (a) adds bridge file-attachment support or (b) tells the
	// LLM not to attach_file in phantom contexts.
	if len(sess.Files) > 0 {
		var names []string
		for _, f := range sess.Files {
			names = append(names, f.Name)
		}
		Log("[phantom] WARNING: %d file attachment(s) discarded — phantom doesn't deliver generic files yet (got: %s)", len(sess.Files), strings.Join(names, ", "))
	}

	// Store assistant reply.
	storeMessage(T.DB, PhantomMessage{
		ID:        now() + "-" + newID(),
		ChatID:    chatID,
		Role:      "assistant",
		Text:      reply,
		Reasoning: reasoning,
		Timestamp: now(),
	})

	// Replay guard: if this exact reply was already enqueued recently,
	// drop it. Catches two failure modes that both produce a confused
	// user experience:
	//   1. The agent loop's empty-response rescue (agent_loop.go) pulled
	//      a stale earlier assistant turn after MaxRounds was hit — the
	//      model would have re-emitted a turn the user already received.
	//   2. A coalesced re-run that produced identical output to a prior
	//      pass (rare but possible on deterministic small models).
	// The recentReplies map is shared with the loop-back guard from
	// hookHandler — same TTL, same exact-match semantics.
	if T.matchesRecentReply(reply) {
		Log("[phantom] dropping duplicate reply (matches recently-sent text) for %s", handle)
		return
	}

	// Remember the reply text so the hook handler can drop a loop-back of
	// our own outbound if the bridge's skip mechanisms miss it, AND so
	// the replay guard above can catch repeats. Belt and suspenders
	// alongside the bridge-side ROWID + sentText filtering.
	T.rememberRecentReply(reply)

	// Queue for delivery. ChatID is the iMessage destination address —
	// for aliased inbound this is the original sender thread, NOT the
	// internal storage convChatID.
	enqueueOutbox(T.DB, OutboxItem{
		ID:      newID(),
		ChatID:  deliverChatID,
		Handle:  handle,
		Text:    reply,
		Images:  sessionImages,
		Videos:  sessionVideos,
		Type:    "reply",
		Created: now(),
	})
}

// upsertMember adds or updates handle in the member list. It checks the primary
// handle and all aliases, so a known contact is recognized regardless of which
// address iMessage delivers from. The existing name is preserved when name is empty.
func upsertMember(members []ConvMember, handle, name string) []ConvMember {
	for i, m := range members {
		if m.Handle == handle {
			if name != "" {
				members[i].Name = name
			}
			return members
		}
		for _, a := range m.Aliases {
			if a == handle {
				if name != "" {
					members[i].Name = name
				}
				return members
			}
		}
	}
	return append(members, ConvMember{Handle: handle, Name: name})
}

// relTimeShort renders a time as a compact relative phrase tuned for
// LLM context — "just now", "5m ago", "3h ago", "2d ago", or an
// absolute "Mon Jan 2" past 7 days. Compact format keeps token cost
// low while giving the model the only thing it actually needs:
// roughly how long since the message went out.
func relTimeShort(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Local().Format("Mon Jan 2")
	}
}

// recentAssistantReplies returns a "DO NOT REPEAT" block built from the
// last n non-empty assistant messages in history (trimmed to ~200
// chars each, with relative-age prefix). Returns "" when there are
// fewer than 2 prior assistant turns — the model can't repeat itself
// meaningfully on the very first reply.
//
// Why this exists: Qwen-class models pattern-match heavily and will
// re-use the same joke, phrasing, or pivot ("Speaking of which…") for
// every turn unless told otherwise. The full history is already in
// context, but a near-prompt callout listing the recent replies gives
// the strongest signal that they should NOT be reused verbatim.
func recentAssistantReplies(history []PhantomMessage, n int) string {
	if n <= 0 {
		return ""
	}
	type pick struct{ ts, text string }
	var picks []pick
	for i := len(history) - 1; i >= 0 && len(picks) < n; i-- {
		m := history[i]
		if m.Role != "assistant" {
			continue
		}
		c := strings.TrimSpace(m.Text)
		if c == "" {
			continue
		}
		if len(c) > 200 {
			c = c[:200] + "…"
		}
		var ts string
		if t, err := time.Parse(time.RFC3339, m.Timestamp); err == nil && m.Timestamp != "" {
			ts = relTimeShort(t)
		}
		picks = append(picks, pick{ts: ts, text: c})
	}
	if len(picks) < 2 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nRECENT REPLIES YOU ALREADY SENT — do NOT repeat any of these phrasings, jokes, or openings verbatim, and do not paraphrase them closely. Vary your wording, vary your structure, vary your topic pivots. The other party remembers what you just said:\n")
	// picks is newest-first; reverse for chronological clarity.
	for i := len(picks) - 1; i >= 0; i-- {
		b.WriteString("- ")
		if picks[i].ts != "" {
			b.WriteString("(")
			b.WriteString(picks[i].ts)
			b.WriteString(") ")
		}
		b.WriteString(strconv.Quote(picks[i].text))
		b.WriteString("\n")
	}
	return b.String()
}

// formatMembers returns a compact member list string for injection into the
// system prompt, e.g. "Bob (+14155551234), Alice (alice@example.com)".
func formatMembers(members []ConvMember) string {
	if len(members) == 0 {
		return ""
	}
	var parts []string
	for _, m := range members {
		if m.Name != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", m.Name, m.Handle))
		} else {
			parts = append(parts, m.Handle)
		}
	}
	return strings.Join(parts, ", ")
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}


func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
