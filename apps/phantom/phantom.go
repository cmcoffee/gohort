// Package relay provides a bridge between remote messaging apps (iMessage, Teams, etc.)
// and gohort's LLM pipeline. A lightweight agent binary running on the user's machine
// POSTs incoming messages here; the server processes them through a configurable AI
// persona and queues replies for the agent to deliver.
package phantom

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterApp(new(Phantom))
	RegisterRouteStage(RouteStage{
		Key:     "app.phantom",
		Label:   "Phantom",
		Default: "worker (thinking)",
		Group:   "Apps",
	})
}

// chatPending tracks coalesced messages that arrived while a processMessage
// goroutine was already running for a given convChatID.
type chatPending struct {
	mu            sync.Mutex
	active        bool
	generation    int // incremented each time a newer message is queued
	handle        string
	text          string
	conv          Conversation
	deliverChatID string // outbound destination for the next coalesced reply (may differ from convChatID for aliased inbound)
	queued        bool   // a message arrived while active; re-run when done
}

type Phantom struct {
	AppCore
	chatSlots sync.Map // chatID → *chatPending

	// recentRepliesMu / recentReplies tracks text we've recently sent to
	// any conversation. A loop-back (bridge re-reads our outbound from
	// chat.db with is_from_me=1 but ROWID skip missed) shows up at the
	// hook handler with empty handle and our own text. Even with the
	// bridge's text-content skip, this is a belt-and-suspenders defense
	// at the server boundary.
	recentRepliesMu sync.Mutex
	recentReplies   map[string]time.Time // text → time sent
}

const recentReplyTTL = 10 * time.Minute

// rememberRecentReply records text with the current time. Caller is the
// outbox enqueue path. Empty / very short strings are skipped to avoid
// false positives on common short replies.
func (T *Phantom) rememberRecentReply(text string) {
	text = strings.TrimSpace(text)
	if len(text) < 8 {
		return
	}
	T.recentRepliesMu.Lock()
	defer T.recentRepliesMu.Unlock()
	if T.recentReplies == nil {
		T.recentReplies = make(map[string]time.Time)
	}
	T.recentReplies[text] = time.Now()
	// Lazy GC.
	if len(T.recentReplies) > 200 {
		cutoff := time.Now().Add(-recentReplyTTL)
		for k, t := range T.recentReplies {
			if t.Before(cutoff) {
				delete(T.recentReplies, k)
			}
		}
	}
}

// matchesRecentReply returns true if text was sent by us within
// recentReplyTTL. Used by the hook handler to drop loop-back hooks.
func (T *Phantom) matchesRecentReply(text string) bool {
	text = strings.TrimSpace(text)
	if len(text) < 8 {
		return false
	}
	T.recentRepliesMu.Lock()
	defer T.recentRepliesMu.Unlock()
	if T.recentReplies == nil {
		return false
	}
	t, ok := T.recentReplies[text]
	if !ok {
		return false
	}
	if time.Since(t) > recentReplyTTL {
		delete(T.recentReplies, text)
		return false
	}
	return true
}

func (T *Phantom) Name() string    { return "phantom" }
func (T *Phantom) WebPath() string { return "/phantom" }
func (T *Phantom) WebName() string { return "Phantom" }
func (T *Phantom) WebDesc() string {
	return "Bridge iMessage (and other messaging apps) to an AI persona via a lightweight local agent."
}
func (T *Phantom) Desc() string { return "Apps: iMessage/Teams AI persona bridge." }

func (T *Phantom) Init() error {
	// Register this instance with the agency-export source (see
	// agency_export.go) so Agency's /api/sessions can enumerate
	// phantom-dispatched sessions for the current admin user.
	// Called from Init so T.DB is already set by the framework.
	setPhantomForExport(T)
	return T.Flags.Parse()
}
func (T *Phantom) Main() error {
	Log("Phantom is a dashboard-only app. Start with:\n  gohort serve :8080")
	return nil
}

// --- DB table constants ---

const (
	apiKeyTable        = "phantom_apikeys"
	conversationTable  = "phantom_conversations"
	messageTable       = "phantom_messages"
	outboxTable        = "phantom_outbox"
	configTable        = "phantom_config"
	sentImagesTable    = "phantom_sent_images"
	phantomTasksTable  = "phantom_tasks"
	phantomCountsTable = "phantom_proactive_counts"
	proactiveIDsTable  = "phantom_proactive_ids"
	configKey          = "persona"
)

// --- Types ---

// APIKey authenticates a relay agent (e.g. the Mac bridge binary).
type APIKey struct {
	ID       string `json:"id"`
	Name     string `json:"name"`  // friendly label, e.g. "Craig's MacBook"
	Key      string `json:"key"`   // the secret token, shown once on creation
	Owner    string `json:"owner"` // gohort username the key belongs to; binds the key to a user for the per-user desktop tool bridge
	Created  string `json:"created"`
	LastSeen string `json:"last_seen,omitempty"`
}

// ConvMember is one participant in a group conversation.
type ConvMember struct {
	Handle  string   `json:"handle"`            // primary phone number or email
	Name    string   `json:"name"`              // display name (optional)
	Aliases []string `json:"aliases,omitempty"` // alternate handles (same person, different address)
}

// Conversation tracks one chat thread (one contact or group chat).
type Conversation struct {
	ChatID           string       `json:"chat_id"`           // e.g. "iMessage;-;+14155551234"
	Handle           string       `json:"handle"`            // phone number or email
	DisplayName      string       `json:"display_name"`      // contact name if known
	Members          []ConvMember `json:"members,omitempty"` // group chat participants
	AutoReply        bool         `json:"auto_reply"`
	PersonaName      string       `json:"persona_name,omitempty"`      // overrides global if set
	Personality      string       `json:"personality,omitempty"`       // overrides global if set
	SystemPrompt     string       `json:"system_prompt,omitempty"`     // conversation rules; overrides global if set
	EnabledTools     []string     `json:"enabled_tools,omitempty"`     // overrides global if non-nil
	GatekeeperPrompt string       `json:"gatekeeper_prompt,omitempty"` // overrides global if set
	AliasHandles     []string     `json:"alias_handles,omitempty"`     // handles/chat_ids that route into this conversation
	AliasOf          string       `json:"alias_of,omitempty"`          // cached: this chat_id is an alias of the named primary
	ProactiveEnabled bool         `json:"proactive_enabled,omitempty"` // opt-in to global proactive messaging
	// ProgressUpdates controls the interactive "working out loud" status
	// pings ("searching…", "checking your calendar…") sent during a
	// multi-round turn. nil/true = on, false = off. Only the interactive
	// hook path emits these; autonomous (scheduled/proactive) turns never
	// do, regardless of this flag.
	ProgressUpdates *bool `json:"progress_updates,omitempty"`
	// AllowedAgents is the allowlist of Agency agent IDs that the LLM
	// in THIS chat may dispatch to via the dispatch_agent tool. Empty
	// disables the surface for this chat — the tool drops out of the
	// catalog. Names + descriptions for these agents come from the
	// per-instance DispatchOwnerUsername's agent store.
	AllowedAgents []string `json:"allowed_agents,omitempty"`

	// AllowedSkills is the strict allowlist of skill IDs (from the
	// operator's skill pool) the LLM in THIS chat may pull in via
	// activate_skill. Empty = skills disabled for this chat (the
	// activate/deactivate tools drop out and no skills block renders).
	// Mirrors orchestrate's per-agent AllowedSkills.
	AllowedSkills []string `json:"allowed_skills,omitempty"`
	// ActiveSkillIDs is VESTIGIAL — skills are per-turn now (the LLM
	// re-activates each message it stays in-domain; nothing carries
	// across messages), so this is no longer read or written. Kept only
	// so older stored conversations with the JSON deserialize cleanly.
	ActiveSkillIDs []string `json:"active_skill_ids,omitempty"`
	// DisableSkills hard-mutes skills for this chat regardless of
	// AllowedSkills (parity with orchestrate's per-agent toggle).
	DisableSkills bool `json:"disable_skills,omitempty"`

	Updated string `json:"updated"`

	// MessageHistoryDepth overrides the global PhantomConfig depth
	// for THIS conversation. Lets per-contact tuning: quick contacts
	// → 10 messages is plenty; long collaborative threads → 50;
	// group chats → 30. 0 means "use global default."
	MessageHistoryDepth int `json:"message_history_depth,omitempty"`

	// CompactionEnabled overrides the global toggle for THIS
	// conversation. nil = inherit global; set true/false to override.
	CompactionEnabled *bool `json:"compaction_enabled,omitempty"`

	// Summary is the rolling compaction of older messages — generated
	// by the worker LLM when total messages exceed 2× the configured
	// history depth. Prepended to the LLM's context as "Prior
	// conversation context:" so the model still knows the gist of
	// older exchanges even when their raw text has aged out of the
	// verbatim window. SummarizedThroughTimestamp tracks how much
	// has been summarized so the next pass only handles new aging-out
	// messages, not the whole history again.
	Summary                    string `json:"summary,omitempty"`
	SummarizedThroughTimestamp string `json:"summarized_through_timestamp,omitempty"`
}

// effectiveHistoryDepth returns the per-conversation override if set,
// else the deployment-wide default, else 20. Single source of truth
// for "how many recent messages does this LLM call see verbatim?"
func effectiveHistoryDepth(conv Conversation, cfg PhantomConfig) int {
	if conv.MessageHistoryDepth > 0 {
		return conv.MessageHistoryDepth
	}
	if cfg.MessageHistoryDepth > 0 {
		return cfg.MessageHistoryDepth
	}
	return 20
}

// effectiveCompactionEnabled merges the per-conversation override
// with the global default. Per-conv pointer non-nil wins; otherwise
// fall back to global (which defaults to true via compactionEnabled()).
func effectiveCompactionEnabled(conv Conversation, cfg PhantomConfig) bool {
	if conv.CompactionEnabled != nil {
		return *conv.CompactionEnabled
	}
	return cfg.compactionEnabled()
}

// PhantomMessage is one message in a conversation.
type PhantomMessage struct {
	ID          string   `json:"id"`
	ChatID      string   `json:"chat_id"`
	Role        string   `json:"role"`             // "user" | "assistant"
	Handle      string   `json:"handle,omitempty"` // sender's phone/email (user messages only)
	DisplayName string   `json:"display_name,omitempty"`
	Text        string   `json:"text"`
	Reasoning   string   `json:"reasoning,omitempty"` // thinking content for assistant messages
	Images      []string `json:"images,omitempty"`    // base64-encoded image data
	Videos      []string `json:"videos,omitempty"`    // base64-encoded video data; sampled into frames at LLM-call time
	Timestamp   string   `json:"timestamp"`
}

// OutboxItem is a message queued for the agent to deliver.
type OutboxItem struct {
	ID      string   `json:"id"`
	ChatID  string   `json:"chat_id"`
	Handle  string   `json:"handle"` // phone/email the agent sends to
	Text    string   `json:"text"`
	Images  []string `json:"images,omitempty"` // base64-encoded images to send as attachments
	Videos  []string `json:"videos,omitempty"` // base64-encoded video files to send as attachments
	Type    string   `json:"type"`             // "reply" | "announce"
	Created string   `json:"created"`
}

// PhantomTaskRecord tracks an LLM-created scheduled task so it can be listed and cancelled.
type PhantomTaskRecord struct {
	PhantomID   string `json:"phantom_id"`   // our short ID, shown to the LLM
	SchedulerID string `json:"scheduler_id"` // global scheduler task ID (for UnscheduleTask)
	ChatID      string `json:"chat_id"`
	Prompt      string `json:"prompt"`
	RunAt       string `json:"run_at"` // RFC3339
	Repeat      string `json:"repeat,omitempty"`
	Until       string `json:"until,omitempty"`
}

// PhantomConfig is the global persona and behaviour config for this relay.
type PhantomConfig struct {
	PersonaName      string   `json:"persona_name"`   // name the AI introduces itself as
	OwnerName        string   `json:"owner_name"`     // name for the phone owner ("from_me" messages)
	OwnerHandle      string   `json:"owner_handle"`   // phone number of the device owner; messages from this handle are treated as from_me
	Personality      string   `json:"personality"`    // who the AI is — prepended to SystemPrompt
	SystemPrompt     string   `json:"system_prompt"`  // conversation rules
	AutoReplyAll     bool     `json:"auto_reply_all"` // if false, enable per-conversation
	Enabled          bool     `json:"enabled"`
	EnabledTools     []string `json:"enabled_tools"`     // tool names to give the persona
	GatekeeperPrompt string   `json:"gatekeeper_prompt"` // if set, LLM decides whether to respond
	// Proactive messaging — admin-configured only, never LLM-triggered.
	ProactiveEnabled   bool   `json:"proactive_enabled"`
	ProactiveWindow    string `json:"proactive_window"`      // "HH:MM-HH:MM" daily window
	ProactivePrompt    string `json:"proactive_prompt"`      // language rules / what to say
	ProactiveMaxPerDay int    `json:"proactive_max_per_day"` // 0 = unlimited
	// SecureAPIEnabled is the master switch for secure-API tools in
	// phantom. When false, BuildSecureAPITools is skipped during
	// buildConvTools so no credential is reachable via phantom even
	// if a conv has call_<credname> in its EnabledTools list. Off by
	// default — explicit opt-in given the elevated trust required to
	// expose credentials to potentially-untrusted iMessage senders.
	SecureAPIEnabled bool `json:"secure_api_enabled"`
	// KnowledgeRetentionDays caps how long an auto-ingested chunk
	// stays in the per-chat vector store before it gets swept. 0 =
	// unbounded (keep forever). Default 180 — old enough to be useful
	// recall, recent enough that the store doesn't grow without bound
	// across years of casual conversation. Sweep runs piggybacked on
	// each ingest so there's no background goroutine to manage.
	KnowledgeRetentionDays int `json:"knowledge_retention_days,omitempty"`

	// MessageHistoryDepth bounds how many recent messages from each
	// conversation get included verbatim in the LLM's context. Older
	// messages roll into Conversation.Summary via the compaction
	// pass (when CompactionEnabled). Smaller models want a tighter
	// bound (frees prompt budget for tools + skills); larger models
	// can keep more verbatim. Default 20.
	MessageHistoryDepth int `json:"message_history_depth,omitempty"`

	// CompactionEnabled toggles the rolling-summary pass. When true,
	// each turn checks whether total stored messages exceed 2× the
	// history depth; if so, the oldest unseen messages are folded
	// into the conversation's summary via WorkerChat. The summary
	// then prepends the LLM's context so context-window-evicted
	// content still informs replies. Default true — turning it off
	// drops to "verbatim history only, anything older is forgotten."
	CompactionEnabled *bool `json:"compaction_enabled,omitempty"`
}

// phantomAgentOwner returns the gohort username whose Agency agent
// store Phantom dispatches against. Auto-detected as the first admin
// user in the deployment's AuthDB — phantom is single-tenant by
// nature (one phone, one admin), so we don't expose this as a config
// field. Returns "" when no admin exists yet (fresh install pre-
// setup) so callers can refuse the dispatch with a clear error.
//
// The unused db arg is preserved for callers that already pass their
// app DB; the lookup goes through the global AuthDB() handle which
// is the only DB that actually carries auth users.
func phantomAgentOwner(_ Database) string {
	if AuthDB == nil {
		return ""
	}
	auth := AuthDB()
	if auth == nil {
		return ""
	}
	for _, u := range AuthListUsers(auth) {
		if u.Admin {
			return u.Username
		}
	}
	return ""
}

// phantomDispatchRuntimeUser returns the synthetic per-chat user
// identity that dispatched agents run AS. Memory, facts, knowledge,
// workspace, and session temp tools all scope under this name, so
// each chat's dispatched-agent state stays isolated from the
// owner's interactive use of the same agent. Format kept human-
// readable so the per-user data directories are debuggable.
func phantomDispatchRuntimeUser(chatID string) string {
	return "phantom:" + chatID
}

// ownerLabel returns the configured name for the phone owner, defaulting to "me".
func (c PhantomConfig) ownerLabel() string {
	if c.OwnerName != "" {
		return c.OwnerName
	}
	return "me"
}

// --- Helpers ---

func newID() string {
	b := make([]byte, 8)
	cryptorand.Read(b)
	return hex.EncodeToString(b)
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// imageHash returns a short hex fingerprint of a base64-encoded image,
// used to deduplicate images across sends to the same conversation.
func imageHash(b64 string) string {
	h := sha256.Sum256([]byte(b64))
	return hex.EncodeToString(h[:12]) // 96-bit prefix — collision probability negligible
}

// filterNewImages deduplicates images within the current session — prevents
// the same bytes from being delivered twice if multiple tools fire in one
// agent loop. Does not persist across sessions; user-requested images (e.g.
// find_image) should always be delivered even if sent in a prior session.
func filterNewImages(images []string) []string {
	if len(images) == 0 {
		return images
	}
	seen := make(map[string]bool, len(images))
	var fresh []string
	for _, img := range images {
		h := imageHash(img)
		if !seen[h] {
			seen[h] = true
			fresh = append(fresh, img)
		}
	}
	return fresh
}

// filterNewVideos is the video-tier sibling of filterNewImages — same
// per-session dedup against multi-fire tools (a download_video tool that
// gets called twice with the same URL won't double-send the file).
func filterNewVideos(videos []string) []string {
	if len(videos) == 0 {
		return videos
	}
	seen := make(map[string]bool, len(videos))
	var fresh []string
	for _, v := range videos {
		h := imageHash(v) // SHA-256 over base64; bytes don't care about media type
		if !seen[h] {
			seen[h] = true
			fresh = append(fresh, v)
		}
	}
	return fresh
}

// proactiveDayCount tracks how many proactive messages fired today for a
// conversation, plus the day's target fire count N (used by the slot-based
// scheduler so every fire across the day uses the same target).
type proactiveDayCount struct {
	Date     string `json:"date"` // local YYYY-MM-DD
	Count    int    `json:"count"`
	DailyN   int    `json:"daily_n,omitempty"`   // target fire count for the day; 0 = not yet chosen
	LastFire string `json:"last_fire,omitempty"` // RFC3339 — dedup safety net
}

// proactiveTodayCount returns the number of proactive fires today for chatID.
func proactiveTodayCount(db Database, chatID string) int {
	var rec proactiveDayCount
	if !db.Get(phantomCountsTable, chatID, &rec) {
		return 0
	}
	if rec.Date != time.Now().Local().Format("2006-01-02") {
		return 0
	}
	return rec.Count
}

// proactiveLastFire returns the time of the last proactive fire for chatID.
func proactiveLastFire(db Database, chatID string) time.Time {
	var rec proactiveDayCount
	if !db.Get(phantomCountsTable, chatID, &rec) || rec.LastFire == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, rec.LastFire)
	return t
}

// incrementProactiveCount records one more proactive fire today for chatID.
func incrementProactiveCount(db Database, chatID string) {
	today := time.Now().Local().Format("2006-01-02")
	var rec proactiveDayCount
	db.Get(phantomCountsTable, chatID, &rec)
	if rec.Date != today {
		rec = proactiveDayCount{Date: today}
	}
	rec.Count++
	rec.LastFire = now()
	db.Set(phantomCountsTable, chatID, rec)
}

// proactiveDailyN returns today's target fire count (N) for chatID, computing
// and persisting it on first call of the day. maxPerDay > 0 makes N deterministic;
// maxPerDay == 0 generates a random N in [1, max(1, ceil(windowHours))] so unlimited
// mode varies day to day. windowHours is the duration of the fire window.
func proactiveDailyN(db Database, chatID string, maxPerDay int, windowHours float64) int {
	today := time.Now().Local().Format("2006-01-02")
	var rec proactiveDayCount
	db.Get(phantomCountsTable, chatID, &rec)
	if rec.Date != today {
		rec = proactiveDayCount{Date: today}
	}
	if maxPerDay > 0 {
		// Deterministic: always honor the configured cap. Refresh in case the
		// admin lowered it mid-day; we don't want a stale higher N to override.
		if rec.DailyN != maxPerDay {
			rec.DailyN = maxPerDay
			db.Set(phantomCountsTable, chatID, rec)
		}
		return maxPerDay
	}
	if rec.DailyN > 0 {
		return rec.DailyN
	}
	// Unlimited mode: pick a random N for the day, biased so the expected
	// value is windowHours/2 but variance allows higher rolls. Range is
	// [1, ceil(windowHours)].
	upper := int(math.Ceil(windowHours))
	if upper < 1 {
		upper = 1
	}
	rec.DailyN = 1 + rand.Intn(upper)
	db.Set(phantomCountsTable, chatID, rec)
	return rec.DailyN
}

// stripLeadingArtifact removes encoding artifacts (e.g. +$, +E) that
// sometimes appear at the start of messages from the iMessage bridge.
func stripLeadingArtifact(s string) string {
	if len(s) >= 2 && s[0] == '+' {
		return s[2:]
	}
	return s
}

// validateAPIKey looks up the key string and returns the APIKey record if valid.
// It also updates the LastSeen timestamp.
func validateAPIKey(db Database, secret string) (APIKey, bool) {
	if db == nil || secret == "" {
		return APIKey{}, false
	}
	for _, k := range db.Keys(apiKeyTable) {
		var ak APIKey
		if db.Get(apiKeyTable, k, &ak) && ak.Key == secret {
			ak.LastSeen = now()
			db.Set(apiKeyTable, k, ak)
			return ak, true
		}
	}
	return APIKey{}, false
}

// bridgeKeyValid accepts the X-API-Key for phantom's hook/poll if it's
// EITHER a legacy phantom APIKey OR the core desktop bridge key
// (LookupDesktopKey). Phantom is now a CONSUMER of the core-owned desktop
// key, not the owner — the daemon's auto-provisioned key works here too,
// and existing phantom keys keep working for backward compatibility.
func bridgeKeyValid(db Database, r *http.Request) bool {
	key := r.Header.Get("X-API-Key")
	if _, ok := validateAPIKey(db, key); ok {
		return true
	}
	_, ok := LookupDesktopKey(key)
	return ok
}

// defaultConfig returns the persona config, falling back to defaults if none saved.
func defaultConfig(db Database) PhantomConfig {
	var cfg PhantomConfig
	if db != nil {
		db.Get(configTable, configKey, &cfg)
	}
	if cfg.PersonaName == "" {
		cfg.PersonaName = "AI Assistant"
	}
	if cfg.Personality == "" && cfg.SystemPrompt == "" {
		cfg.SystemPrompt = "You are a friendly AI assistant. Keep responses concise and conversational — this is a text message, not an essay."
	}
	if cfg.KnowledgeRetentionDays == 0 {
		// Zero on disk = "use the default" rather than "unbounded" —
		// matches the user expectation that a fresh install gets a
		// sensible cap. To mean "unbounded" explicitly the operator
		// sets a negative value (see knowledgeRetentionCutoff below).
		cfg.KnowledgeRetentionDays = 180
	}
	if cfg.MessageHistoryDepth <= 0 {
		cfg.MessageHistoryDepth = 20
	}
	return cfg
}

// compactionEnabled returns the effective Compaction state — true by
// default, opt-out only. nil pointer means "operator hasn't set it";
// treat that as enabled.
func (c PhantomConfig) compactionEnabled() bool {
	if c.CompactionEnabled == nil {
		return true
	}
	return *c.CompactionEnabled
}

const emojiRule = "Use at most one emoji per message, only when it genuinely fits. Most messages should have no emoji at all."

const caseRule = "Always use proper capitalization: start sentences with a capital letter and capitalize proper nouns. This rule overrides any instruction to mirror the group's casing — match their tone and slang, but not their lowercase style."

const statusRule = "When a task will take more than a few seconds (download_video, delegate, multi-step research, scheduled callbacks, phone calls), call send_status BEFORE starting so the user sees you're working — examples: 'One moment, looking that up.' / 'Placing the call, will follow up when it ends.' Send another status when you switch phases. This is the right way to surface progress; do NOT narrate via your final reply text."

const followThroughRule = "FOLLOW-THROUGH: if you say 'let me try', 'I'll figure this out', 'one moment', or any phrase that promises an action, you MUST either (a) call the corresponding tool in the SAME turn, or (b) call keep_going to invisibly request another round and act on the next one. Never end a reply with stated intent and no tool call — that leaves the user waiting on nothing. Either execute and report, or say plainly 'I can't do X' without promising further effort. When an API rejects a request with a 4xx error, READ the message field — it usually names the exact field to fix. Adjust and retry; do not give up after one failure or fabricate field names from training data when the error tells you what's actually wrong."

const learnAndSaveRule = "LEARN-AND-SAVE: as soon as you figure out a working API call (especially after iterating through 4xx errors), IMMEDIATELY wrap it as a persistent tool via create_api_tool with persist=true — hardcode the discovered url_template/method/body_template, expose only the variable bits as params. Pending approval from the operator, but it stops you from re-discovering the same schema next session. The operator notices when they have to teach you the same API twice; it feels broken. Same applies to multi-step shell flows worth saving: create_temp_tool with persist=true."

const freshTurnRule = "FRESH-TURN-EVAL: each `--- NEW MESSAGE` is its own request — read what's being asked NOW. Tool intent does NOT carry across turns: a prior download_video / delegate / web_search does not repeat just because the conversation continues. Inspect THIS message's content + any [CURRENT ATTACHMENT: ...] tag in isolation and pick the right tool (or none) for it. When the new message ASKS ABOUT prior work ('what did that call say?', 'what did the search find?', 'did you save that?'), answer from your conversation history — do NOT re-run the tool to answer a question about it. For a past picture, answer from your earlier description; you only see the current turn's attachments, so ask them to re-send if needed. Earlier conversation is context, not standing instructions."

const attachRule = "ATTACH FILES VIA MARKER: when you want to send a file from your workspace to the user (image, video, screenshot, generated picture, document), append a marker on its own line at the END of your reply in this exact form: `[ATTACH: filename.ext]`. Add `, cleanup=true` (e.g. `[ATTACH: meme.jpg, cleanup=true]`) for one-shot files produced by find_image / fetch_image / generate_image / screenshot_page that you don't need to keep around. Use ONE marker per file; multiple markers are fine for multiple files. DO NOT write the tool-call syntax as text (e.g. `workspace(action=\"attach\", ...)`); that is a real tool call, not something to type into your reply. The marker is the canonical way to deliver a file; the framework strips it from the visible message and ships the file."

const sideEffectGuardRule = "SIDE-EFFECT GUARD — STRICT: never repeat a tool that contacts a person or changes external state (placing calls, sending messages/emails/SMS, payments, posting to feeds, scheduling) on a follow-up turn unless the user EXPLICITLY says so ('call them again' / 'send another' / names a different recipient). When in doubt, do NOT call — re-contacting someone they only asked about once is real harm (unwanted second contact, burned trust), not a UX nit. Read-only tools (GET, search, fetch) are exempt and can be re-called freely."

// phantomWorkspaceID returns a stable, filesystem-safe identifier for
// the workspace shared across all phantom conversations on this host.
// Phantom acts as one persona (the device owner), so all convs share
// one workspace — that lets a tool the LLM uses in convo A leave files
// the LLM can pick up in convo B (e.g. a reusable script written via
// write_file). If OwnerHandle isn't configured, falls back to a fixed
// label so workspace provisioning still works.
func phantomWorkspaceID(cfg PhantomConfig) string {
	id := cfg.OwnerHandle
	if id == "" {
		return "phantom"
	}
	// Replace anything that EnsureWorkspaceDir would reject (path
	// separators, "..") with underscore. Email and phone-number
	// handles otherwise pass through fine.
	var b strings.Builder
	b.WriteString("phantom_")
	for _, r := range id {
		switch r {
		case '/', '\\', '.':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// phantomToolOwner returns the username that owns this phantom's
// persistent temp tool pool. Picks the first admin account in the
// auth table — gohort is single-operator, and that admin is the one
// approving pending temp tools through the admin UI, so loading
// under the same name surfaces them here. Empty string when no
// admin user exists yet (auth table empty); callers treat that as
// "no persistent tools loaded" without crashing.
//
// Reads from RootDB, not the per-app bucket: the auth_config table
// lives at the process root (same place tempToolStore points to),
// so passing in phantom's own T.DB would query an empty namespace
// and silently return no admins. Falls back to the supplied db
// only when RootDB isn't set yet (early-init), matching the
// tempToolStore fallback shape.
func phantomToolOwner(db Database) string {
	src := RootDB
	if src == nil {
		src = db
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

// ensurePhantomWorkspace provisions and returns the phantom workspace
// dir, or empty string + a Debug log on failure (caller treats empty
// as "sandboxed tools disabled" — same posture chat uses).
func ensurePhantomWorkspace(cfg PhantomConfig) string {
	id := phantomWorkspaceID(cfg)
	ws, err := EnsureWorkspaceDir(id)
	if err != nil {
		Debug("[phantom] workspace setup failed (id=%q): %v — sandboxed tools disabled", id, err)
		return ""
	}
	return ws
}

// stripEmojis removes all but the first emoji cluster from s.
// Handles single emojis, variation selectors, skin tone modifiers, ZWJ
// compound sequences (e.g. 👨‍💻), and flag pairs (two regional indicators).
func stripEmojis(s string) string {
	// Rune-slice walk (not strings.Map) because flags need ONE rune of
	// look-ahead: a regional indicator is only meaningful as a pair. A lone
	// one renders as a stray boxed letter (🇪→E, 🇷→R), which is how a reply
	// that ended in a flag — or a flag split across message chunks — leaked a
	// trailing "E"/"R". strings.Map is forward-only and committed the first
	// half before knowing a second followed; this drops unpaired indicators.
	runes := []rune(s)
	var b strings.Builder
	found := 0        // number of base emoji clusters kept (cap 1)
	afterZWJ := false // previous char was ZWJ — next emoji continues the sequence
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		// ZWJ: extends the current emoji sequence, never starts a new one.
		if r == 0x200D {
			if found > 0 {
				afterZWJ = true
				b.WriteRune(r)
			}
			continue
		}
		// Variation selector, keycap combiner: modifies the preceding char.
		if r == 0xFE0F || r == 0x20E3 {
			if found > 0 {
				b.WriteRune(r)
			}
			continue
		}
		// Fitzpatrick skin tone modifiers.
		if r >= 0x1F3FB && r <= 0x1F3FF {
			if found > 0 {
				b.WriteRune(r)
			}
			continue
		}
		// Regional indicators: keep a COMPLETE pair (a flag) only as the
		// first cluster; drop any unpaired indicator so it can't surface as
		// a stray letter.
		if r >= 0x1F1E0 && r <= 0x1F1FF {
			paired := i+1 < len(runes) && runes[i+1] >= 0x1F1E0 && runes[i+1] <= 0x1F1FF
			if paired {
				if found < 1 {
					found++
					b.WriteRune(r)
					b.WriteRune(runes[i+1])
				}
				i++ // consume both halves whether kept or dropped
			}
			// lone indicator: drop it
			continue
		}

		// Base emoji codepoints.
		isEmoji := (r >= 0x1F300 && r <= 0x1FAFF) ||
			(r >= 0x2600 && r <= 0x27BF) ||
			(r >= 0x2B00 && r <= 0x2BFF)
		if isEmoji {
			if afterZWJ {
				afterZWJ = false
				b.WriteRune(r) // ZWJ continuation — same cluster
				continue
			}
			if found < 1 {
				found++
				b.WriteRune(r)
			}
			continue
		}
		afterZWJ = false
		b.WriteRune(r)
	}
	return b.String()
}

// gapRule keeps phantom honest when it's out of its depth: a specialized
// or high-stakes question with no specialist agent to cover it should get
// an honest "not my area" + an offer to set one up, not a confident
// ad-lib (and never an invented skill/agent).
const gapRule = "OUT-OF-DEPTH HONESTY: when a question is in a specialized or high-stakes domain (medical, legal, financial, safety, deep technical) that none of your available specialist agents covers and you can't answer it reliably, do NOT ad-lib a confident answer. Say plainly you're not the right tool for it, give only what you can stand behind (noting it isn't expert-grounded), and suggest setting up a dedicated specialist for it in Agency so future questions like it get a real answer. Never invent a skill or agent name to use; when there's no specialist and no tool that grounds it, an honest 'I can't verify this' beats a confident guess. Catch one shape proactively: when someone asks you to be their ongoing go-to or advisor for a specialized area ('be my go-to', 'going forward', 'from now on' plus a real domain), don't default to volunteering yourself and collecting their details — that belongs in a dedicated grounded specialist. Lead by suggesting they set one up in Agency; don't wait to be asked whether it should be an agent."

// buildSystemPrompt combines Personality and Conversation Rules (SystemPrompt)
// into the final system prompt. Personality is prepended; either may be empty.
// The emoji and case rules are always appended.
func buildSystemPrompt(personality, rules string) string {
	personality = strings.TrimSpace(personality)
	rules = strings.TrimSpace(rules)
	var base string
	switch {
	case personality != "" && rules != "":
		base = personality + "\n\n" + rules
	case personality != "":
		base = personality
	default:
		base = rules
	}
	trailing := emojiRule + " " + caseRule + " " + statusRule + " " + followThroughRule + " " + learnAndSaveRule + " " + freshTurnRule + " " + sideEffectGuardRule + " " + gapRule + " " + attachRule
	if base != "" {
		return base + "\n\n" + trailing
	}
	return trailing
}

// recentMessages returns the last n messages for a conversation, oldest first.
func recentMessages(db Database, chatID string, n int) []PhantomMessage {
	if db == nil {
		return nil
	}
	prefix := chatID + ":"
	var msgs []PhantomMessage
	for _, k := range db.Keys(messageTable) {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			var m PhantomMessage
			if db.Get(messageTable, k, &m) {
				msgs = append(msgs, m)
			}
		}
	}
	// sort by ID (which embeds timestamp as RFC3339 prefix)
	for i := 1; i < len(msgs); i++ {
		for j := i; j > 0 && msgs[j].ID < msgs[j-1].ID; j-- {
			msgs[j], msgs[j-1] = msgs[j-1], msgs[j]
		}
	}
	if len(msgs) > n {
		msgs = msgs[len(msgs)-n:]
	}
	return msgs
}

// storeMessage persists a message and trims the history to 100 per conversation.
func storeMessage(db Database, m PhantomMessage) {
	if db == nil {
		return
	}
	key := m.ChatID + ":" + m.ID
	db.Set(messageTable, key, m)
}

// enqueueOutbox adds an item to the outbox for the agent to deliver.
//
// Special-cased: when an item carries both videos AND text, it's
// split into two separate outbox entries. The video portion goes
// immediately (with an empty Text field so the bridge sends only
// the attachments). The text portion is scheduled to be enqueued
// 5 seconds later — long enough that iMessage has started uploading
// the video, so the recipient device sees them in order:
//
//	[videos] (5s later) [text reply]
//
// This replaces the older "send videos, sleep size-scaled delay,
// send text in the same RPC" pattern that blocked the bridge's
// deliver worker. Now the worker frees immediately after each part
// and the gap is owned by gohort.
func enqueueOutbox(db Database, item OutboxItem) {
	if db == nil {
		return
	}
	// De-markdown every outbound message at the chokepoint. iMessage
	// renders no markdown, so literal **bold**, `code`, # headers and
	// the like would otherwise reach the user as punctuation noise. The
	// main reply paths already call markdownToPlain; doing it here too is
	// belt-and-suspenders for any path that doesn't, and it's idempotent
	// (already-plain text is unchanged).
	if item.Text != "" {
		item.Text = markdownToPlain(item.Text)
	}
	if len(item.Videos) > 0 && strings.TrimSpace(item.Text) != "" {
		// Send the video portion now.
		videosOnly := item
		videosOnly.Text = ""
		db.Set(outboxTable, videosOnly.ID, videosOnly)
		// Schedule the text follow-up. Use a fresh ID so the bridge
		// treats it as a distinct queued item; otherwise the second
		// db.Set would overwrite the first under the same key and
		// the videos would never deliver.
		textOnly := item
		textOnly.ID = newID()
		textOnly.Videos = nil
		go func() {
			time.Sleep(5 * time.Second)
			db.Set(outboxTable, textOnly.ID, textOnly)
		}()
		return
	}
	db.Set(outboxTable, item.ID, item)
}

// drainOutbox returns all pending outbox items and deletes them atomically.
// The agent retries failed deliveries from its own in-memory retry queue.
// migrateFromRelay copies data from the old relay app bucket into phantom tables.
// Safe to call on every startup — no-op once the relay bucket is empty.
func migrateFromRelay(dst Database) {
	if dst == nil || RootDB == nil {
		return
	}
	src := RootDB.Bucket("relay")
	if len(src.Tables()) == 0 {
		return
	}
	Log("[phantom] migrating data from relay bucket...")
	total := 0

	for _, k := range src.Keys("relay_apikeys") {
		var v APIKey
		if src.Get("relay_apikeys", k, &v) {
			dst.Set(apiKeyTable, k, v)
			src.Unset("relay_apikeys", k)
			total++
		}
	}
	for _, k := range src.Keys("relay_conversations") {
		var v Conversation
		if src.Get("relay_conversations", k, &v) {
			dst.Set(conversationTable, k, v)
			src.Unset("relay_conversations", k)
			total++
		}
	}
	for _, k := range src.Keys("relay_messages") {
		var v PhantomMessage
		if src.Get("relay_messages", k, &v) {
			dst.Set(messageTable, k, v)
			src.Unset("relay_messages", k)
			total++
		}
	}
	for _, k := range src.Keys("relay_outbox") {
		var v OutboxItem
		if src.Get("relay_outbox", k, &v) {
			dst.Set(outboxTable, k, v)
			src.Unset("relay_outbox", k)
			total++
		}
	}
	for _, k := range src.Keys("relay_config") {
		var v PhantomConfig
		if src.Get("relay_config", k, &v) {
			dst.Set(configTable, k, v)
			src.Unset("relay_config", k)
			total++
		}
	}
	Log("[phantom] migration complete: %d entries moved", total)
}

// buildThinkOpts returns ChatOption slice for a route key's thinking config.
func buildThinkOpts(routeKey string) []ChatOption {
	think := RouteThink(routeKey)
	if think == nil {
		return nil
	}
	opts := []ChatOption{WithThink(*think)}
	if *think {
		if budget := RouteThinkBudget(routeKey); budget != nil {
			opts = append(opts, WithThinkBudget(*budget))
		}
	}
	return opts
}

func drainOutbox(db Database) []OutboxItem {
	if db == nil {
		return nil
	}
	var items []OutboxItem
	for _, k := range db.Keys(outboxTable) {
		var item OutboxItem
		if db.Get(outboxTable, k, &item) {
			items = append(items, item)
			db.Unset(outboxTable, k)
		}
	}
	return items
}

// metaKeySet holds NSKeyedArchiver / NSAttributedString binary-plist metadata tokens
// that the local agent may leak into extracted text. cleanMessageText strips them.
var metaKeySet = map[string]bool{
	"$null": true, "$objects": true, "$archiver": true, "$version": true,
	"$top": true, "$class": true, "root": true, "bplist00": true,
	"streamtyped":     true,
	"NSKeyedArchiver": true, "NSAttributedString": true, "NSMutableAttributedString": true,
	"NSString": true, "NSMutableString": true, "NSObject": true,
	"NS.string": true, "NS.keys": true, "NS.objects": true,
	"NSColor": true, "NSFont": true, "NSDictionary": true, "NSArray": true,
	"NSURL": true, "NSDate": true, "NSValue": true, "NSNumber": true,
	"NSData": true, "NSMutableData": true, "NSMutableDictionary": true,
}

// cleanMessageText strips NSKeyedArchiver metadata tokens that leak into
// extracted NSAttributedString body blobs. It splits on whitespace, removes
// any token that is or contains a metadata key, and re-joins. It also drops
// tokens that look like opaque machine identifiers — long base64-style runs
// or MMCS asset references of the form id#key — which the agent's heuristic
// extractor scrapes from non-text payloads (audio messages, stickers,
// encrypted attachments) where attributedBody has no NS.string field.
func cleanMessageText(text string) string {
	if text == "" {
		return ""
	}
	// If the entire text is a single metadata token, return empty.
	if metaKeySet[text] {
		return ""
	}
	parts := strings.Fields(text)
	// Single-token short input is the user's literal message, not
	// metadata leaked alongside real text. The typedstream-artifact
	// filter was designed for MIXED content (real text + extracted
	// archiver garbage); applying it to a bare single token false-
	// positives on legitimate one-word answers — most painfully on
	// "i", "I", and "iI" which match a signed/unsigned-int type tag
	// from NSAttributedString. A user typing "i" as a trivia answer
	// got their whole message dropped. Skip the artifact-stripping
	// pass when there's only one token; the metadata-key and
	// NS-key filters above still apply (they target exact strings).
	if len(parts) == 1 {
		p := parts[0]
		if metaKeySet[p] || looksLikeNSKey(p) {
			return ""
		}
		return p
	}
	var out []string
	seenURLs := make(map[string]bool)
	for _, p := range parts {
		// Skip exact metadata tokens.
		if metaKeySet[p] {
			continue
		}
		// Skip tokens that look like NSKeyedArchiver prefix keys —
		// "$class", "$null", "NSString", "NSKeyedArchiver". Tightened
		// from a blanket "$ or NS prefix" check because plain text
		// messages legitimately use dollar amounts ("$5.99", "$100")
		// and short word-starts that happen to begin with NS would be
		// caught (rare but possible).
		//
		// Match rules:
		//   - $X — followed entirely by letters (no digits, no punct)
		//   - NSX — followed by an uppercase letter (PascalCase NS class names)
		if looksLikeNSKey(p) {
			continue
		}
		// Skip tokens that contain a metadata key as a substring.
		containsMeta := false
		for k := range metaKeySet {
			if len(k) >= 4 && strings.Contains(p, k) {
				containsMeta = true
				break
			}
		}
		if containsMeta {
			continue
		}
		// Skip tokens that look like opaque identifiers (MMCS asset refs,
		// GUIDs, base64 blobs). Real text has punctuation or whitespace and
		// breaks into smaller tokens before reaching this length.
		if looksLikeIdentifier(p) {
			continue
		}
		// Skip NSAttributedString typedstream artifacts that the bridge's
		// printable-run heuristic scrapes alongside the real text when a
		// message contains a URL (data-detector attribute runs:
		// DDScannerResult, WHttpURL, dd-result, plus type tags like "iI"
		// and tagged-URL fragments like "Ahttps://...").
		if looksLikeTypedstreamArtifact(p) {
			continue
		}
		// Dedup repeated URLs — data-detector attribute spans cause the
		// same URL to appear multiple times in the extracted blob.
		if isLikelyURL(p) {
			if seenURLs[p] {
				continue
			}
			seenURLs[p] = true
		}
		out = append(out, p)
	}
	return strings.Join(out, " ")
}

// looksLikeTypedstreamArtifact recognizes the leakage patterns from
// NSAttributedString / NSKeyedArchiver typedstream parsing when the
// bridge extracts text from a message with attribute runs (URLs, dates,
// data-detector results). Each pattern was observed in the wild on
// macOS Sonoma+ chat.db when a URL is in the message body.
// looksLikeNSKey discriminates NSKeyedArchiver / NSAttributedString
// metadata tokens from real message content. Returns true only for
// tokens that have the strict shape of a metadata key:
//   - "$word" where word is one or more letters only ($null, $class,
//     $objects). NOT $5.99, $100, $1,000.
//   - "NSWord..." starting with the literal "NS" followed by an
//     uppercase letter (NSString, NSAttributedString, NSURL).
//
// Trailing punctuation (commas, periods at sentence end) is tolerated
// — "NSString." still counts as metadata. Real prose rarely starts a
// word with the exact "NS" capitalization pattern.
func looksLikeNSKey(s string) bool {
	if len(s) < 2 {
		return false
	}
	// Strip a single trailing punctuation char so "$null," still matches.
	if last := s[len(s)-1]; last == ',' || last == '.' || last == ';' || last == ':' {
		s = s[:len(s)-1]
	}
	if len(s) < 2 {
		return false
	}
	if s[0] == '$' {
		for i := 1; i < len(s); i++ {
			c := s[i]
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
				return false
			}
		}
		return true
	}
	// Real NSKeyedArchiver class names are PascalCase: "NS" + an uppercase
	// initial + a lowercase letter (NSString, NSDictionary, NSAttributedString,
	// NSData, NSDate, NSValue, NSError, NSSet, ...). Requiring s[3] to be
	// lowercase keeps every real class name matching while no longer false-
	// positiving on all-caps acronyms a user actually types, e.g. "NSAIDs"
	// (NS + "AID...") — a medical term that was being silently dropped — or
	// "NSA". The bare-3-char "NS?" case is intentionally dropped too: a real
	// class name is always longer, and "NSA"-style acronyms are legitimate.
	if s[0] == 'N' && s[1] == 'S' && len(s) >= 4 {
		return s[2] >= 'A' && s[2] <= 'Z' && s[3] >= 'a' && s[3] <= 'z'
	}
	return false
}

func looksLikeTypedstreamArtifact(s string) bool {
	if s == "" {
		return false
	}
	// Bare type tags from typedstream: i+I (signed int + unsigned int)
	// is the most common, marking attribute-run start positions and lengths.
	if s == "iI" || s == "i" || s == "I" {
		return true
	}
	// Class names from data detectors. These appear verbatim because the
	// archiver stores class names as plain ASCII bytes.
	switch {
	case strings.Contains(s, "DDScannerResult"),
		strings.Contains(s, "HttpURL"),
		strings.Contains(s, "dd-result"),
		strings.Contains(s, "Wversion"),
		// NSValue range encodings — leak when the message has date-
		// detector spans (e.g. "tonight" auto-tagged). Patterns seen:
		// NS.rangeval.length_, NS.rangeval.locationZNS.special.
		strings.HasPrefix(s, "NS."),
		strings.Contains(s, "NS.rangeval"),
		// Date-detector class names from the same family.
		s == "SpecialDateTime",
		strings.Contains(s, "SpecialDateTime"),
		strings.Contains(s, "DataDetector"):
		return true
	}
	// Short token containing an underscore is almost always metadata
	// (compact-key markers like `()HI_`, `J,_`, `length_`). Real
	// user text rarely uses bare underscores; common cases (handles
	// like @user_name, file names) are 5+ chars and contain word
	// chars on both sides of the underscore. Tighten this to "short
	// token, ends with `_` OR underscore-followed-by-non-letter" to
	// avoid stripping legit identifiers.
	if len(s) <= 5 && (strings.HasSuffix(s, "_") || strings.Contains(s, "_")) {
		return true
	}
	// Short token (≤3 chars) with mixed letters and non-letter/digit
	// chars — captures patterns like "E,", "B3", "QV", "J,_" that
	// emerge from typedstream length-byte runs.
	if len(s) <= 3 {
		hasLetter := false
		hasNonAlnum := false
		for _, r := range s {
			switch {
			case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
				hasLetter = true
			case r >= '0' && r <= '9':
				// digit, fine
			default:
				hasNonAlnum = true
			}
		}
		if hasLetter && hasNonAlnum {
			return true
		}
	}
	// Tokens that are pure punctuation/digits with no letters — typedstream
	// length-byte markers and small int values manifest as printable junk
	// like `3+`, `8+_`, `'(67_`, `!"#_` after UTF-8 run scanning. But
	// pure-numeric content ("5", "12345", "12.5", "1,000", "-3") is
	// legitimate user text — a lone numeric reply, a quantity, a measurement.
	// Whitelist numeric shapes before falling through to the letterless-
	// means-artifact rule so those don't get stripped.
	letterCount := 0
	digitCount := 0
	numericShape := true
	for i, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			letterCount++
			numericShape = false
		case r >= '0' && r <= '9':
			digitCount++
		case r == '.' || r == ',':
			// allowed inside numbers ("12.5", "1,000")
		case (r == '-' || r == '+') && i == 0:
			// leading sign allowed ("-5", "+5")
		default:
			numericShape = false
		}
	}
	if numericShape && digitCount > 0 {
		return false
	}
	if letterCount == 0 {
		return true
	}
	// Single capital letter + URL pattern: typedstream prefixes archived
	// strings with a one-byte type marker that lands as `A`, `W`, `Y`, etc.
	// directly before an http URL. `Ahttps://...` and `WHttpURL...` are both
	// metadata, not user text.
	if len(s) > 8 && s[0] >= 'A' && s[0] <= 'Z' &&
		(strings.HasPrefix(s[1:], "http://") || strings.HasPrefix(s[1:], "https://")) {
		return true
	}
	return false
}

// isLikelyURL is a coarse URL check used for dedup. Real URLs are
// recognized by scheme prefix; we don't need to validate them.
func isLikelyURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// looksLikeIdentifier returns true for tokens that are almost certainly
// machine identifiers rather than user text: a contiguous run of base64-style
// characters (and optional '#' separator) of 20 chars or more. Real text
// contains whitespace or non-base64 punctuation that breaks tokens up before
// they grow this long.
func looksLikeIdentifier(s string) bool {
	if len(s) < 20 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '+' || r == '/' || r == '=' || r == '#':
		default:
			return false
		}
	}
	return true
}
