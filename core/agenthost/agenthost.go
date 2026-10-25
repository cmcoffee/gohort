// Package agenthost is how an app reaches the agent runtime without importing
// the app that provides it.
//
// One app in a process hosts agents: it owns their records, runs their turns
// and keeps their threads. Other apps ask it to run an agent, to run one as a
// template under a state namespace of their own, or to look up the agents and
// live conversations a user has. They did that by importing the host app and
// type-asserting it out of the registry, which tied every one of them to it.
// Now the host registers itself here (Register), and they ask Get for a Host.
//
// The types are the requests and answers that cross that line. The host keeps
// aliases to them, so on its side nothing changed name.
package agenthost

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core"
)

// Host is what the agent runtime offers other apps.
type Host interface {
	// RunAgentSync runs agentKey (an id or a name) in agentOwner's store as
	// runtimeUser on one message and returns its reply.
	RunAgentSync(ctx context.Context, agentOwner, runtimeUser, agentKey, message string, via ...string) (string, error)
	// RunAgentSyncContinuingRich runs one turn of a continuing thread and
	// returns everything it produced.
	RunAgentSyncContinuingRich(ctx context.Context, run Run) (Result, error)
	// RunScopedAgentRich runs scope's template agent on one turn, with its
	// state kept under the scope.
	RunScopedAgentRich(ctx context.Context, scope Scope, run Run) (Result, error)

	// AgentNames maps each agent visible to user, by id, to its name.
	AgentNames(user string) map[string]string
	// ExternalChannels lists owner's live conversations an external caller
	// can join.
	ExternalChannels(owner string) []ChannelTarget
	// ResolveExternalChannel maps a chat id, handle or room name to the
	// conversation that serves it.
	ResolveExternalChannel(owner, key string) (ChannelTarget, bool)
	// RecentThread is the id of agentID's most recent thread under prefix
	// that has been active within idle, or "".
	RecentThread(owner, agentID, prefix string, idle time.Duration) string

	// MachineStore is the store owner's machines are kept in.
	MachineStore(owner string) core.Database
	// PublicRunMachine runs def as user on input.
	PublicRunMachine(ctx context.Context, def core.MachineDef, user, input string) (string, error)

	// An app's chat (chat.go). The handlers answer the request themselves,
	// checking the caller as the host's own routes do.

	// WebPath is where the host's own pages live.
	WebPath() string
	// LookupAppAgent is the agent agentID (an id or a name) in owner's store,
	// as an app holds it for a turn.
	LookupAppAgent(owner, agentID string) (Agent, bool)
	// AgentStateNamespace is the namespace agentID's facts and memory are
	// kept under.
	AgentStateNamespace(agentID string) string
	// ServeAppChat answers one of the endpoints Chat.Panel names, for agent.
	// It reports false for a path that is not one of them.
	ServeAppChat(w http.ResponseWriter, r *http.Request, agent Agent, c Chat, sub, listScope string) bool
	PublicHandleSend(w http.ResponseWriter, r *http.Request, agent Agent)
	PublicHandleSendWithAppTools(w http.ResponseWriter, r *http.Request, agent Agent, appTools []core.AgentToolDef)
	PublicHandleSendWithAppToolsPublishing(w http.ResponseWriter, r *http.Request, agent Agent, appTools []core.AgentToolDef, pub *Publisher)
	PublicHandleCancel(w http.ResponseWriter, r *http.Request, agent Agent)
	PublicHandleInject(w http.ResponseWriter, r *http.Request)
	PublicHandleConfirm(w http.ResponseWriter, r *http.Request)
	PublicHandleGrants(w http.ResponseWriter, r *http.Request, scope, grantID string)
	PublicHandleSessionList(w http.ResponseWriter, r *http.Request, agentID string)
	PublicHandleSessionListOnly(w http.ResponseWriter, r *http.Request, agentID, appContext string)
	PublicHandleSessionOne(w http.ResponseWriter, r *http.Request, agentID, sid string)
	PublicHandleSessionRename(w http.ResponseWriter, r *http.Request, agentID string)
	PublicHandleSessionDiag(w http.ResponseWriter, r *http.Request, agentID, sessionID string)
	PublicHandleBlockResolve(w http.ResponseWriter, r *http.Request, agentID, sid, blockID string)
	PublicHandleRunsActive(w http.ResponseWriter, r *http.Request)
	PublicHandleRunsDispatch(w http.ResponseWriter, r *http.Request)

	// An app that runs its own agent loops, and keeps an agent's state under a
	// scope of its own (Scope): the guardrails for its loops, the credentials
	// the agent may not use, and the agent's memory, knowledge and graph
	// under that scope, read and seeded by the app and edited through the
	// per-scope handlers its memory page calls.
	AppLoopGuard(ctx context.Context, user, agentID string) *LoopGuard
	AgentCredentialDeny(owner, agentID string) map[string]bool
	WipeScopedMemory(scope Scope) error
	SeedScopedMemory(scope Scope, notes ...string) error
	SeedScopedKnowledge(ctx context.Context, scope Scope, topic, title, body string) error
	SeedScopedGraphEntity(scope Scope, kind, name string, aliases []string, attrs map[string]string) error
	SeedScopedGraphLink(scope Scope, subjectKind, subject string, subjectAttrs map[string]string, relation, objectKind, object, note string, replace bool) error
	RemoveScopedGraphEntityAttr(scope Scope, nameOrAlias, key string) bool
	ScopedGraphAvailable(scope Scope) bool
	ScopedGraphCounts(scope Scope) (entities, edges int)
	ScopedGraphSummary(scope Scope) string
	ScopedGraphEntity(scope Scope, nameOrAlias string) (core.GraphEntity, bool)
	ScopedGraphEntityByID(scope Scope, id string) (core.GraphEntity, bool)
	ScopedGraphEdges(scope Scope, id string) (out, in []core.GraphEdge)
	PublicHandleAgentRecordForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string)
	PublicHandleAgentFactsForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string)
	PublicHandleAgentNotesForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string)
	PublicHandleAgentMemorySearchForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string)
	PublicHandleAgentMemoryAuditForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string)
	PublicHandleAgentInferredListForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string)
	PublicHandleAgentInferredDeleteForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID, chunkID string)
	PublicHandleAgentKnowledgeAutoInferredWipeForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string)
	PublicHandleAgentGraphForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string)
	PublicHandleAgentGraphEntityDeleteForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID, entityID string)
	PublicHandleAgentGraphAliasDeleteForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID, entityID string)
	PublicHandleAgentGraphAttrDeleteForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID, entityID string)
	PublicHandleAgentGraphEdgeDeleteForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string)

	// An app's own runs, from its scripts and actions: the agent asked one
	// question with no tools, the agent run on a prompt, and the pipeline
	// run on an input. Each returns the reply and what it cost.
	AppAgentAsk(ctx context.Context, owner, user, agentID, prompt string, jsonMode bool) (string, float64, error)
	AppAgentRun(ctx context.Context, owner, user, slug, agentID, agentKey, prompt string) (string, float64, error)
	AppPipelineRun(ctx context.Context, owner, user, slug, pipelineID, pipelineKey, input string) (string, float64, error)

	// An app's pipeline.
	LookupAppPipeline(owner, pipelineID string) (core.PipelineDef, bool)
	PublicHandleAppPipeline(w http.ResponseWriter, r *http.Request, def core.PipelineDef, sub string, live core.RunLiveInfo, key string)
	AppPipelineRunSurface(user string, def core.PipelineDef, live core.RunLiveInfo, key string) core.RunSurface
	PublicLatestPipelineRun(user, pipelineID string) (core.PipelineRun, bool)
}

var (
	hostMu sync.RWMutex
	host   Host
)

// Register makes h the process's agent host. The host app calls it once its
// stores are open, so a Host handed out is one that can answer.
func Register(h Host) {
	hostMu.Lock()
	defer hostMu.Unlock()
	host = h
}

// Get is the registered agent host, or nil when no app in this process hosts
// agents.
func Get() Host {
	hostMu.RLock()
	defer hostMu.RUnlock()
	return host
}

// LoopGuard is the host's guardrails on an agent loop an app runs itself
// rather than through the host's own runner: the deployment's rules, the
// agent's own rules, the tool-result fence and scan, and the tainted-action
// check. Built once per run (Host.AppLoopGuard) and applied to each loop
// config the run makes. A nil LoopGuard applies nothing, so a caller need not
// check.
type LoopGuard struct {
	apply func(core.AgentLoopConfig) core.AgentLoopConfig
}

// NewLoopGuard is the host's half: a guard that applies by apply.
func NewLoopGuard(apply func(core.AgentLoopConfig) core.AgentLoopConfig) *LoopGuard {
	return &LoopGuard{apply: apply}
}

// Apply returns cfg with the guardrails on it.
func (g *LoopGuard) Apply(cfg core.AgentLoopConfig) core.AgentLoopConfig {
	if g == nil || g.apply == nil {
		return cfg
	}
	return g.apply(cfg)
}

type noThinkingKey struct{}

// WithoutThinking marks ctx so that no agent run under it thinks: the run it
// starts, and every hand-off from there.
func WithoutThinking(ctx context.Context) context.Context {
	return context.WithValue(ctx, noThinkingKey{}, true)
}

// ThinkingOff reports whether ctx carries WithoutThinking.
func ThinkingOff(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	off, _ := ctx.Value(noThinkingKey{}).(bool)
	return off
}

// Run carries the inputs for RunAgentSyncContinuingRich. The channel
// path uses StatusCallback (mid-turn pings) and reads Result.Images
// (the agent's produced attachments); the legacy text-only callers go through
// the RunAgentSyncContinuing wrapper.
type Run struct {
	AgentOwner       string
	RuntimeUser      string
	AgentKey         string
	SubSessionID     string
	InjectionQueueID string
	// InputReportFrom / InputReportKind mark the STORED input as something that
	// HAPPENED rather than something the user typed — a monitor firing, a
	// scheduled fire — naming the producer and its card kind.
	//
	// The live turn is unaffected: the message is still the user turn this run
	// answers, because that is what it is. What changes is the record left
	// behind. Without these, a monitor wake sat in the owner's cortex as their
	// own message — the whole "[EVENT — monitor "x" fired] …" prompt, diff
	// payload and closing instruction included, in a user bubble, as though
	// they had typed it. It also read back to the model that way on the next
	// turn, where an assistant-role card gets a fenced origin marker
	// (llmHistoryContent) saying where it came from.
	//
	// Empty leaves every other caller exactly as it was: a channel inbound and
	// a dispatch really are somebody's message.
	InputReportFrom string
	InputReportKind string
	// InputCardText, when set with InputReportFrom, is what the stored card
	// SAYS, in place of the message. The message is still the turn the model
	// answers — a watch wake needs its diff and payload — but a thread is read
	// by a person, and the same text there was a wall of JSON. Empty keeps the
	// message as the card, which is right for an event that has no separate
	// readable form.
	InputCardText string
	// DeliverySessionID is the CONVERSATION anything this run starts in the
	// background must come home to. A dispatch runs under its own sub-session
	// id so its ephemeral state stays off the caller's thread; a picture
	// started inside it belongs to the thread all the same. Empty means the
	// sub-session id is also the conversation, which is true for a dispatch
	// with no parent thread. See ToolSession.DeliverySessionID.
	DeliverySessionID string
	Message           string
	FreshSession      bool
	// DelegatorAgentID names the agent that handed this run over, when one did.
	// Its rules ride into the run (inheritDelegatorGuardrails): a restriction the
	// owner wrote on the delegator is not something the delegate should be able
	// to step around by having none of its own. Empty for a run with no
	// delegator — a schedule, a channel inbound, a monitor wake — where there is
	// nobody's policy to inherit.
	DelegatorAgentID string
	// Kind labels this run in the live-activity ribbon / "Active now" pane
	// ("channel" for an inbound channel/iMessage turn, else it defaults to
	// "dispatch"). Cosmetic only — how the run is described, not how it runs.
	Kind           string
	StatusCallback func(string) // optional: wired to the sub-session's StatusCallback
	// Stream, when set, receives assistant text deltas AS THEY GENERATE, wired
	// straight to the agent loop's stream handler. Without it a caller only
	// gets Result.Text when the whole turn is done — fine for a
	// scheduled run, fatal for anything a person is waiting on out loud, where
	// time-to-first-word is the entire experience.
	//
	// It fires for EVERY round, so a turn that calls tools streams the model's
	// interim prose before the final answer. For a voice caller that reads as
	// natural filler ("let me check that…"); for a caller that wants only the
	// settled answer, leave this nil and use the returned Text.
	Stream func(string)
	// Think overrides the agent's own thinking setting for this run. nil keeps
	// the agent's default.
	//
	// It exists because thinking and a waiting caller are in direct conflict:
	// the model can spend its whole reasoning budget before emitting one word
	// of CONTENT, and a stream callback only ever sees content. Measured live:
	// a voice turn held an open stream for 19.75s producing nothing visible,
	// with think=true and thinking_budget=4096, until the caller hung up. For
	// anything with a human waiting on audio, set this false.
	Think *bool
	// Effort overrides the agent's own reasoning level for this run ("low",
	// "medium", "high"), for a caller that asked to wait for thinking. Empty
	// keeps the agent's.
	Effort string
	// Title, when set, names a FRESH session (used only if it has no title
	// yet). Channel rooms pass the contact's display name so the rail row and
	// transcript read as the conversation partner rather than the raw id.
	Title string
	// AutoTitle names the thread from its conversation, as web chat names
	// its sessions: the first message at once, then a written title from the
	// first exchanges. For a caller whose threads have no name to borrow (the
	// external API), so the rail reads as conversations, not raw ids.
	AutoTitle bool
	// SenderHandle, when set, is the TRANSPORT's attribution of who sent this
	// message — a phone number or email, as the carrier reported it. Distinct from
	// MessageSender, which is a display name the sender chose for themselves.
	//
	// Used to recognize the OWNER messaging their own agent from their own phone.
	// That case is otherwise indistinguishable from a stranger messaging in (both
	// run as the synthetic phantom:<chatID> user), which left the owner classed as
	// an outside party on their own device — so every audience-scoped guardrail
	// ("don't discuss pay with anyone but me") refused them there. Empty for
	// dispatch / web / scheduled runs.
	SenderHandle string
	// SenderIsOwner is the bridge's verdict that the channel owner sent this
	// (ChannelInbound.FromOwner). A channel run's owner check reads it, and
	// nothing else: the run executes as the owner's account whoever sent it.
	SenderIsOwner bool
	// MessageSender, when set, is stored as THIS message's author (ChatMessage
	// .Sender). Channel rooms pass the inbound contact's display name so a
	// GROUP thread renders real who-said-what — each inbound carries its own
	// sender, unlike Title which only names the session once.
	MessageSender string
	// Images carries decoded inbound image bytes (a contact's photo on a
	// channel) to ride on the user message as multimodal content the vision
	// model sees this turn. Empty for text-only callers.
	Images [][]byte
	// DispatchedBy carries the live dispatch chain when this run was delegated
	// by another agent, so the target can reach the channels its delegator
	// reaches. Empty for a schedule, a monitor wake, or a channel inbound —
	// none of those has a dispatching parent.
	DispatchedBy []string
	// Interactive marks this as a real person's message (a channel inbound),
	// NOT an agent-to-agent dispatch. When set, the delegated-invocation marker
	// is skipped — there IS a human, follow-up questions can be answered, and
	// the "[DELEGATED INVOCATION] no human is listening…" preamble must not leak
	// into the message. Default false (dispatch behavior unchanged).
	Interactive bool
	// ReplyAuthorizedKey, when set, is the recipient key of the conversation this
	// run replies to (a channel inbound). The messaging tools deliver back to it
	// without the approval gate — replying to whoever just messaged you is not a
	// proactive reach-out. Empty for dispatch / web runs. See ToolSession.
	ReplyAuthorizedKey string
	// ChannelChatID / ChannelHandle name the conversation this run is answering,
	// carried separately from ReplyAuthorizedKey (which fuses them into one
	// authorization key). Delivery of work that outlives the turn needs them
	// apart: the transport treats a chat id and a bare handle differently.
	ChannelChatID string
	ChannelHandle string
	// SurfaceContext, when set, is a one-line provenance note appended to the
	// LLM's copy of THIS user message only (NOT persisted to the transcript, so
	// it doesn't bloat a long thread on replay). A channel inbound passes it so
	// the agent knows which transport / conversation the message arrived on and
	// that its reply goes straight back there — preventing it from confabulating
	// a destination or offering to "send it to" the channel it's already on.
	SurfaceContext string
	// AppTools are extra, caller-built tools injected into THIS run's catalog —
	// the same mechanism the web path uses (chatTurn.appTools) but for a dispatched
	// / scoped run. An app instantiating a template (see RunScopedAgent) passes
	// per-instance closure tools here, e.g. servitor handing its worker the SSH
	// command tools bound to one appliance's connection. Empty for ordinary runs.
	AppTools []core.AgentToolDef
	// AskUser marks a run whose caller has a person watching who can answer a
	// question: an app's own window, such as an assist workbench. The run gets
	// ask_user, a call ends it, and the result carries Question and Options for
	// the caller to put to them; their answer is the next message in the
	// thread.
	AskUser bool
	// Loop, when set, supplies rich agent-loop knobs (pacing callbacks, chat
	// options, round budget) a sophisticated instance needs that the default
	// dispatch run doesn't — so e.g. servitor's investigator runs through the
	// scoped path with FULL behavior parity (step pacing, stuck detection,
	// plan-aware wrap-up) instead of a generic loop. nil ⇒ dispatch defaults.
	Loop *LoopOverrides
	// SystemPromptOverride, when set, REPLACES the prompt the dispatch normally
	// assembles from the template record (+ facts + available blocks). An app
	// whose instance builds its own complete, per-run system prompt (servitor's
	// investigator via buildLeadSystemPrompt) passes it here so the scoped run
	// uses that prompt verbatim — gaining scoped sessions + recording-to-scope
	// without changing the prompt content. Empty ⇒ the assembled prompt.
	SystemPromptOverride string
	// CallerContext is a system message an EXTERNAL caller sent with its
	// request (/v1). Appended to the agent's own prompt as the caller's
	// context, never in place of it: an override drops the persona, the rules
	// and the agent's machine (which narrows tools step by step), and a key
	// holder must not be able to take those off by sending a system message.
	CallerContext string
}

// LoopOverrides carries the subset of AgentLoopConfig a scoped/template run
// may override. Zero fields inherit the dispatch defaults; set only what the
// instance's loop needs. See Run.Loop.
type LoopOverrides struct {
	MaxRounds   int  // >0 overrides the resolved default
	SerialTools bool // run tool calls one-at-a-time
	// TierOverride pins this run to a model tier regardless of the agent's own
	// LeadModel setting. TierUnset (the zero value) inherits, so every existing
	// caller is unchanged.
	//
	// It exists because a scoped run can belong to something that has its OWN
	// opinion about which model should serve it. Servitor's per-appliance tier
	// reached the map/probe path — which builds an AgentLoopConfig directly —
	// and stopped dead at the chat path, which comes through here: the setting
	// saved, read back correctly, and did nothing on the surface an operator is
	// most likely to be looking at.
	//
	// It cannot escalate past the privacy pin. The loop gates every tier
	// decision on LeadDenied(), so a LEAD override on a ForcePrivate agent still
	// serves from the worker.
	TierOverride  core.LLMTier
	ChatOptions   []core.ChatOption     // APPENDED to the dispatch defaults (last wins per option)
	OnRoundReset  func() bool           // per-step pacing reset
	OnRoundStart  func() []core.Message // pre-round injection (stuck nudges, etc.)
	PendingWorkFn func() int            // remaining-work count for the wrap-up nudge
}

// Result is the bound agent's output: reply text plus any attachments
// it produced this turn (base64).
type Result struct {
	Text string
	// Parts is Text as the separate messages the turn deliberately sent to its
	// own conversation (send_message with parts), when it sent more than one.
	// Text is them joined, for the stored thread and every caller that takes
	// one reply; a channel delivers Parts one message each.
	Parts  []string
	Images []string
	// Videos are base64 video attachments the turn produced (video tools via
	// sess.Videos, or a [ATTACH: file.mp4] marker). Kept SEPARATE from Images so
	// they ride out as videos, not mislabeled images — restoring the outbound
	// video channel phantom had before the bridges migration dropped it.
	Videos []string
	// Made is the files the turn's tools created, by absolute path, so the
	// agent that ran it can be handed them (agent_media.go, handBackFiles).
	Made []string
	// HitRoundCap reports that the loop stopped because it exhausted its round
	// budget (not a natural finish). A caller driving a multi-pass loop (e.g.
	// servitor's investigator continuing while plan steps remain) re-runs with the
	// same SubSessionID while this is true.
	HitRoundCap bool
	// PhantomDelivery reports that the reply named a file it was sending, no
	// such file exists, and nothing was recovered to stand in for it. The turn
	// promised a picture it never made. Callers substituting a fallback for
	// empty output use this to say something TRUE about why — the generic
	// "could you rephrase it" blames the request, and the request was fine.
	PhantomDelivery bool
	// ToolCalls is every tool the turn ran, paired call-to-result. A channel
	// turn's whole record in the standing thread was its inbound text and its
	// reply, so anything it DID on the owner's behalf — searched, edited a
	// picture, messaged someone, armed a monitor — left no trace there at all.
	//
	// Structured rather than the names-only list this replaced, because the
	// names were being formatted into the card's BODY as prose. That is the one
	// shape the thread cannot use: the panel renders a persisted trace as the
	// same expandable tool-runs section every other surface has, and
	// toLLMMessages rebuilds it into proper call-and-result protocol — so a
	// later turn reading its own thread gets the outcomes, not a truncated
	// bullet list, and the reader gets chips instead of a paragraph.
	ToolCalls []ToolCall
	// Silenced reports that the model DELIBERATELY chose to say nothing —
	// stay_silent fired. Distinct from Text being empty by accident, which is a
	// failure. Callers that substitute a fallback for empty output must not do
	// so here: on a messaging channel, "don't reply to this" produced the
	// agent's "I wasn't able to put together a response" line, which is the
	// framework overriding an instruction the user gave and the model obeyed.
	Silenced bool
	// Question and Options are what the run asked with ask_user when it was
	// run with AskUser; Text is the question as the thread keeps it.
	Question string
	Options  []string
}

// Scope instantiates an agent TEMPLATE against a caller-supplied state
// namespace. The agent's persona / prompt / tools come from the template
// (TemplateOwner + AgentID — the single source of truth, edited in one place);
// its sessions, Explicit Memory, and Knowledge/RAG live under ScopeUser — a
// synthetic runtime identity (e.g. "app:servitor:appliance-123") isolated from
// the template owner's own state. Two scopes never share any state.
//
// This is the app-facing seam for "use an orchestrate agent as a reusable
// template with LOCAL state". It's the same mechanism phantom already uses to run
// an agent under "phantom:<contact>" — formalized so any app can adopt it (e.g.
// to run a per-appliance lead/worker pair off shared agent definitions). The
// underlying run goes through RunAgentSyncContinuingRich, which already resolves
// config (AgentOwner) and state (RuntimeUser) from separate stores.
type Scope struct {
	// AgentID is the template agent's id (or name). For an app-registered agent
	// (appagents.RegisterAppAgent) leave TemplateOwner blank — app + built-in
	// agents resolve from the seed store.
	AgentID string
	// ScopeUser is the synthetic runtime identity = the state namespace. REQUIRED.
	// Use a stable, app-prefixed string, e.g. "app:<app>:<instance>", so an app's
	// instances can't collide with each other or with a real user's email-keyed
	// store. (Avoid "/", "\\", ".." — it also names a workspace dir.)
	ScopeUser string
	// TemplateOwner is the owner whose store holds the template AgentRecord. Blank
	// ⇒ the seed store ("system"), where app-registered + built-in agents live.
	// Set it to run a specific user's own agent as a template.
	TemplateOwner string
	// SessionID names the conversation thread WITHIN the scope. Blank ⇒ a
	// deterministic per-(scope, agent) id — one continuing thread.
	SessionID string
}

// ChannelTarget is a live conversation an external caller can join: the
// agent that serves it and the session that IS its thread.
type ChannelTarget struct {
	AgentID   string
	AgentName string
	SessionID string
	ChatID    string
	Name      string // conversation/room display name, when the transport knows one
}

// ToolCall is one tool invocation persisted alongside the
// assistant message that owns it. Args is the LLM-supplied argument
// map (raw, not normalized); Result is what the handler returned (or
// Err when the call failed). Used by the session export and any
// future "show me what happened" UI affordance.
type ToolCall struct {
	Name   string         `json:"name"`
	Args   map[string]any `json:"args,omitempty"`
	Result string         `json:"result,omitempty"`
	Err    string         `json:"err,omitempty"`
	Cached bool           `json:"cached,omitempty"` // true = wrapper returned cached body (no fresh dispatch)
	// Framework marks a record the FRAMEWORK wrote, not a call the model made:
	// a machine step the turn walked before the model saw the message. Shown
	// with the turn's other tool runs, never replayed to the model as a call it
	// made (toLLMMessages), since a name like machine_step is not a tool it has
	// and history that says it called one invites it to try again.
	Framework bool `json:"framework,omitempty"`
	// Label is what the tool-runs list shows in place of Name, for a record
	// whose name alone says little ("machine_step" versus the step it was).
	Label string `json:"label,omitempty"`
	// ID is the call's card id, and Parent the id of the call it ran beneath
	// (a dispatched agent's call under the dispatch). The page nests a call
	// under its parent on reload, as it did live; empty for a top-level call.
	ID     string `json:"id,omitempty"`
	Parent string `json:"parent,omitempty"`
}
