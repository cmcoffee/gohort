package phantom

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/apps/orchestrate"
)

// handleMigrateToChannel bootstraps a Channels setup from phantom's existing
// persona: it mints an orchestrate agent carrying the phantom persona (system
// prompt + enabled tools) and a whole-service iMessage Channel bound to it, so
// inbound iMessage routes to that orchestrate agent instead of phantom's own
// engine. The phantom config is left intact — coexistence means unbound chats
// keep using phantom until you widen the binding, so this is safe to run and
// verify before fully cutting over.
//
// Per-chat persona overrides are NOT migrated here: in the channel model a
// different persona is just a different agent, so each override becomes its
// own agent + a per-contact Channel (Address = that handle, which overrides
// the whole-service default). That's a follow-on. See
// docs/channels-and-agents.md.
//
// handleMigrateActions backs the dashboard's "Channel agent" ActionList. It
// offers the migrate action only while there's something to migrate and it
// hasn't been done yet — i.e. the owner has a persona but no iMessage Channel
// bound yet. Once a channel exists the list is empty (the EmptyText explains
// it's linked). This is the "button is migrate if one already exists" rule:
// a configured persona is the "one", and migrating carries it across.
//
//	GET /phantom/api/migrate-actions  → [] or [{Label, Desc}]
func (T *Phantom) handleMigrateActions(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	out := []map[string]string{}
	owner := phantomToolOwner(T.DB)
	if owner == user {
		if _, migrated := ownerIMessageChannel(owner); migrated {
			// Already migrated: offer a re-sync (idempotent re-run) so persona /
			// rule edits and newly-enabled chats get picked up without minting
			// duplicates.
			out = append(out, map[string]string{
				"Label": "Re-sync chats to channel agents",
				"Desc":  "Re-run the migration: refresh each enabled chat's agent (persona, rules, tools) and add any newly-enabled chats. Cleans up channels whose agent was deleted.",
			})
		} else {
			out = append(out, map[string]string{
				"Label": "Migrate chats to channel agents",
				"Desc":  "Turn each enabled (auto-reply) chat into its own orchestrate agent on a per-contact channel — its own persona if set, else the global default; rules combine global + that chat's own. Phantom keeps answering until you point traffic at the agents.",
			})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// proactiveRulesForAgent translates phantom's proactive-outreach config (the
// style prompt + "always do on wake" + "random on wake" pool) into
// newline-separated rules for the migrated agent's Rules section, so the
// persona's unprompted-outreach behavior isn't lost when phantom's proactive
// engine goes away. Framed as proactive-only so the rules don't misfire on
// ordinary replies. The cadence (window / max-per-day) is left to the agent's
// own scheduling. Returns "" when nothing was configured.
func proactiveRulesForAgent(cfg PhantomConfig) string {
	var lines []string
	if s := strings.TrimSpace(cfg.ProactivePrompt); s != "" {
		lines = append(lines, "When reaching out proactively (unprompted, scheduled outreach): "+s)
	}
	for _, a := range splitRules(cfg.ProactiveAlways) {
		lines = append(lines, "On every proactive outreach, always: "+a)
	}
	if pool := splitRules(cfg.ProactiveActions); len(pool) > 0 {
		lines = append(lines, "Vary proactive outreach — each time pick one of: "+strings.Join(pool, "; "))
	}
	return strings.Join(lines, "\n")
}

// ownerIMessageChannel returns the owner's iMessage Channel (the migration
// target) if one exists. The bool reports whether migration has run.
func ownerIMessageChannel(owner string) (Channel, bool) {
	for _, ch := range ListChannels(RootDB, owner) {
		if ch.Service == phantomDefaultService {
			return ch, true
		}
	}
	return Channel{}, false
}

//	POST /phantom/api/migrate-channel  → {agent_id, channel_id}
func (T *Phantom) handleMigrateToChannel(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	owner := phantomToolOwner(T.DB)
	if owner == "" || owner != user {
		http.Error(w, "only the phantom owner can migrate", http.StatusForbidden)
		return
	}
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "orchestrate runtime not available", http.StatusServiceUnavailable)
		return
	}
	cfg := defaultConfig(T.DB)

	// Per-chat migration: each ENABLED conversation (auto-reply on) becomes its
	// OWN agent bound to a per-contact channel. A chat with its own persona is
	// its own agent; a chat without one inherits the global default persona.
	// Idempotent — re-running updates existing chat agents in place and adds
	// newly-enabled ones (this same handler backs the "Re-sync" action).
	var created, updated, failed int
	for _, conv := range enabledConversations(T.DB) {
		wasCreated, err := T.upsertChatAgent(owner, orch, cfg, conv)
		if err != nil {
			failed++
			Log("[phantom] migrate: chat %s failed: %v", conv.ChatID, err)
			continue
		}
		if wasCreated {
			created++
		} else {
			updated++
		}
	}

	// Clean up any iMessage channel whose bound agent no longer exists — a
	// deleted-in-Agency agent, or a prior whole-service migration's agent that's
	// gone. Such a channel routes inbound to a missing agent and errors, so
	// dropping it restores phantom's own handling for those chats.
	pruned := T.pruneDanglingChannels(owner, orch)

	w.Header().Set("Content-Type", "application/json")
	if created+updated == 0 {
		msg := "No enabled chats to migrate. Turn on auto-reply for a conversation first."
		if pruned > 0 {
			msg = fmt.Sprintf("Cleaned up %d stale channel(s). No enabled chats to migrate yet.", pruned)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": msg, "pruned": pruned})
		return
	}
	Log("[phantom] per-chat migration: %d created, %d updated, %d failed, %d pruned", created, updated, failed, pruned)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"created": created, "updated": updated, "failed": failed, "pruned": pruned,
		"message": fmt.Sprintf("Migrated %d new and updated %d existing chat agent(s).", created, updated),
	})
}

// enabledConversations returns the non-alias conversations set to auto-reply —
// the chats worth migrating to their own agent. Aliases route to their primary,
// so they don't get a separate agent.
func enabledConversations(db Database) []Conversation {
	var out []Conversation
	for _, k := range db.Keys(conversationTable) {
		var c Conversation
		if !db.Get(conversationTable, k, &c) || c.ChatID == "" {
			continue
		}
		if c.AliasOf != "" || !c.AutoReply {
			continue
		}
		out = append(out, c)
	}
	return out
}

// firstNonEmpty returns the first trimmed-non-empty value.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// agentRecordForChat builds the orchestrate agent for one chat. Persona (name +
// personality) comes from the chat's override, falling back to the global
// default when blank. Rules combine the GLOBAL conversation rules and this
// chat's INDIVIDUAL rules (plus the persona's proactive-outreach rules). Tools
// use the chat override when set, else the global list.
func agentRecordForChat(cfg PhantomConfig, conv Conversation) orchestrate.AgentRecord {
	personaName := firstNonEmpty(conv.PersonaName, cfg.PersonaName, "iMessage agent")
	who := firstNonEmpty(conv.DisplayName, conv.Handle, conv.ChatID)
	name := personaName
	if who != "" {
		name = personaName + " — " + who
	}
	personality := strings.TrimSpace(firstNonEmpty(conv.Personality, cfg.Personality))
	if personality == "" {
		personality = "You are a helpful assistant answering messages on iMessage. Keep replies short and conversational."
	}
	// Rules: global conversation rules + this chat's individual rules + the
	// persona's proactive-outreach rules.
	var rules []string
	if g := strings.TrimSpace(cfg.SystemPrompt); g != "" {
		rules = append(rules, g)
	}
	if i := strings.TrimSpace(conv.SystemPrompt); i != "" {
		rules = append(rules, i)
	}
	if p := proactiveRulesForAgent(cfg); p != "" {
		rules = append(rules, p)
	}
	tools := cfg.EnabledTools
	if len(conv.EnabledTools) > 0 {
		tools = conv.EnabledTools
	}
	return orchestrate.AgentRecord{
		Name:               name,
		Description:        "Migrated from a phantom chat; answers on iMessage.",
		OrchestratorPrompt: personality,
		AllowedTools:       tools,
		Rules:              strings.Join(rules, "\n"),
		// Messaging persona: chatbot memory mode so Explicit Memory reads as
		// personalization "Saved notes" (who the contact is, preferences,
		// coherence) rather than task-focused "Lessons learned".
		MemoryMode: "chatbot",
		// Per-chat channel agent posture: no Fleet, no Cortex.
	}
}

// upsertChatAgent creates or refreshes the agent + per-contact channel for one
// conversation. Returns true when a NEW agent was created (false on update).
// Binds the channel to the contact's handle so this chat routes to its own
// agent (an exact-address binding overrides any whole-service default). The
// chat's individual gatekeeper rules become the channel's per-channel gate.
func (T *Phantom) upsertChatAgent(owner string, orch *orchestrate.OrchestrateApp, cfg PhantomConfig, conv Conversation) (bool, error) {
	rec := agentRecordForChat(cfg, conv)
	addr := firstNonEmpty(conv.Handle, conv.ChatID)
	gate := strings.TrimSpace(conv.GatekeeperPrompt)

	var agentID string
	created := false

	// Dedupe: match an existing channel against ANY id this conversation is known
	// by (address, handle, chat id, aliases), so phantom's same-chat-two-ids
	// quirk doesn't spawn a second channel — we reuse the one that's already
	// bound under a sibling id.
	convIDs := map[string]bool{}
	for _, id := range append([]string{addr, conv.Handle, conv.ChatID}, conv.AliasHandles...) {
		if id = strings.TrimSpace(id); id != "" {
			convIDs[id] = true
		}
	}

	// Existing channel for this conversation? Update its bound agent in place
	// (recreating the agent if it was deleted), keep the channel.
	matched := false
	for _, ch := range ListChannels(RootDB, owner) {
		if ch.Service != phantomDefaultService || !convIDs[ch.Address] {
			continue
		}
		matched = true
		bound := false
		for _, a := range orch.AgentsForUser(owner) {
			if a.ID == ch.AgentID {
				a.Name = rec.Name
				a.OrchestratorPrompt = rec.OrchestratorPrompt
				a.AllowedTools = rec.AllowedTools
				a.Rules = rec.Rules
				a.MemoryMode = rec.MemoryMode
				if err := orch.UpdateAgentForUser(owner, a); err != nil {
					return false, err
				}
				agentID = a.ID
				bound = true
				break
			}
		}
		if !bound {
			// Bound agent was deleted — mint a fresh one and rebind this channel.
			id, err := orch.SaveAgentForUser(owner, rec)
			if err != nil {
				return false, err
			}
			ch.AgentID = id
			agentID = id
			created = true
			SaveChannel(RootDB, ch)
		}
		// Channel-level settings (gatekeeper, direction, auto-reply, name) are
		// managed in the Channels UI after migration; a re-sync refreshes the
		// bound AGENT (persona/rules/tools/memory) but does NOT overwrite them.
		break
	}

	if !matched {
		// New agent + per-contact channel.
		id, err := orch.SaveAgentForUser(owner, rec)
		if err != nil {
			return false, err
		}
		agentID = id
		created = true
		SaveChannel(RootDB, Channel{
			ID:         NewChannelID(),
			Owner:      owner,
			Name:       firstNonEmpty(conv.DisplayName, conv.Handle, conv.ChatID), // service shown separately in the UI
			Service:    phantomDefaultService, // "imessage"
			Address:    addr,                  // per-contact binding
			AgentID:    id,
			AutoReply:  true,
			Direction:  DirectionBidirectional, // migrated chats reply on the same surface
			Gatekeeper: gate,
			Created:    time.Now().UTC().Format(time.RFC3339),
		})
	}

	// Carry this chat's remembered facts onto the agent as Explicit Memory
	// notes (the agent is in chatbot mode, so they read as personalization).
	// Dedup in the fact store makes re-running safe.
	if notes := chatMemoryNotes(T.DB, conv.ChatID); len(notes) > 0 {
		orch.ImportAgentNotes(owner, agentID, notes)
	}
	// Seed the channel thread with the chat's recent history so the agent and
	// the transcript have the back-story. Import only when the thread is empty
	// (ImportChannelHistory guards this), so re-syncs don't duplicate.
	if hist := chatHistoryForImport(T.DB, conv, cfg); len(hist) > 0 {
		orch.ImportChannelHistory(owner, agentID, "chan:"+addr, hist)
	}
	// Carry the chat's per-chat VECTOR knowledge (the searchable "folder"
	// memory: explicitly saved knowledge + folded-in aged history) onto the
	// agent's knowledge store. Guarded against re-sync duplication.
	if docs := chatKnowledgeForImport(T.DB, conv.ChatID); len(docs) > 0 {
		orch.ImportAgentKnowledge(owner, agentID, docs)
	}
	return created, nil
}

// chatKnowledgeForImport reads a conversation's per-chat vector knowledge —
// both the explicit per-chat store ("phantom:<chatID>") and the folded-in aged
// history ("phantom_convo:<chatID>") — as plain docs to re-ingest onto the
// agent's knowledge.
func chatKnowledgeForImport(db Database, chatID string) []orchestrate.KnowledgeDoc {
	var out []orchestrate.KnowledgeDoc
	for _, src := range []string{chatKnowledgeSource(chatID), phantomConvoKnowledgeSource(chatID)} {
		if src == "" {
			continue
		}
		for _, c := range ChunksForSource(db, src) {
			out = append(out, orchestrate.KnowledgeDoc{Title: c.Title, Section: c.Section, Text: c.Text, Kind: c.Kind})
		}
	}
	return out
}

// chatHistoryForImport maps a conversation's recent messages into the shape
// ImportChannelHistory wants: chronological, cleaned, each labeled by who said
// it (the contact on inbound, the persona on the agent's replies). Depth uses
// the chat's own history-depth override, else a sane default.
func chatHistoryForImport(db Database, conv Conversation, cfg PhantomConfig) []orchestrate.ChannelHistoryMessage {
	depth := conv.MessageHistoryDepth
	if depth <= 0 {
		depth = 30
	}
	personaName := firstNonEmpty(conv.PersonaName, cfg.PersonaName, "Assistant")
	contact := firstNonEmpty(conv.DisplayName, conv.Handle, conv.ChatID)
	// Resolve individual senders by handle from the conversation's known members
	// (group chats), so each line reads by the person who sent it — not the chat
	// name. Includes alias handles for the same person.
	memberName := map[string]string{}
	for _, mem := range conv.Members {
		if h := strings.TrimSpace(mem.Handle); h != "" {
			memberName[h] = strings.TrimSpace(mem.Name)
		}
		for _, a := range mem.Aliases {
			if a = strings.TrimSpace(a); a != "" {
				memberName[a] = strings.TrimSpace(mem.Name)
			}
		}
	}
	var out []orchestrate.ChannelHistoryMessage
	for _, m := range recentMessages(db, conv.ChatID, depth) {
		text := strings.TrimSpace(cleanMessageText(m.Text))
		if text == "" {
			continue
		}
		role, sender := "user", ""
		if m.Role == "assistant" {
			role, sender = "assistant", personaName
		} else {
			// The individual who sent THIS message: its own display name, else a
			// member lookup by handle, else the raw handle, else the chat contact.
			sender = firstNonEmpty(m.DisplayName, memberName[m.Handle], m.Handle, contact)
		}
		created, _ := time.Parse(time.RFC3339, m.Timestamp)
		out = append(out, orchestrate.ChannelHistoryMessage{Role: role, Content: text, Sender: sender, Created: created})
	}
	return out
}

// chatMemoryNotes returns a conversation's remembered facts as plain note
// strings (phantom stores them in the core fact store under phantom:<chatID>).
func chatMemoryNotes(db Database, chatID string) []string {
	var notes []string
	for _, f := range loadMemories(db, chatID) {
		if n := strings.TrimSpace(f.Note); n != "" {
			notes = append(notes, n)
		}
	}
	return notes
}

// pruneDanglingChannels removes the owner's iMessage channels whose bound agent
// no longer exists. Returns how many were removed.
func (T *Phantom) pruneDanglingChannels(owner string, orch *orchestrate.OrchestrateApp) int {
	live := make(map[string]bool)
	for _, a := range orch.AgentsForUser(owner) {
		live[a.ID] = true
	}
	pruned := 0
	for _, ch := range ListChannels(RootDB, owner) {
		if ch.Service != phantomDefaultService || live[ch.AgentID] {
			continue
		}
		DeleteChannel(RootDB, owner, ch.ID)
		Log("[phantom] pruned dangling channel %s (missing agent %s)", ch.ID, ch.AgentID)
		pruned++
	}
	return pruned
}
