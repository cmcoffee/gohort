package core

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// ToolSession carries mutable per-session state shared between the caller
// and session-aware tools. Pass a *ToolSession when building agent tools via
// GetAgentToolsWithSession; read results back from it after the loop completes.
type ToolSession struct {
	Images            []string           // base64-encoded images accumulated by image tools (delivered as outbound attachments / displayed inline)
	Videos            []string           // base64-encoded video data accumulated by video tools; consumers (phantom outbox) deliver as attachments
	PendingViewImages []ViewImage        // images a tool wants the LLM to see on its NEXT round, each carrying a label saying what it is; the agent loop's caller injects these as a synthetic user message before the next LLM call, then clears. NOT delivered to the user — different channel from Images.
	InboundMedia      []InboundMediaItem // turn-scoped registry of media that arrived on THIS turn (a contact's photo/clip), each addressable by a stable id (media#1, …) so the model can post a specific inbound item back BY ID. Populated at dispatch, listed via the media manifest, resolved by the outbound attachment collector. See RegisterInboundMedia.
	imgGenAttempts    int                // Tier-2 auto-retry CHAIN length for the current image (resets on a new subject); drives the retry budget. See NextImageAttempt.
	imgGenTotal       int                // Tier-2 absolute generate_image calls this turn (never resets); the runaway hard cap. See NextImageAttempt.
	imageRefsFrozen   []string           // what image#1, image#2 … meant when this round's tool calls were written; index i is the stable ref for image#(i+1), "" where the picture has no stable id. Positional refs in tool arguments resolve against THIS, not the live ring. See SnapshotImageRefs.
	Silenced          bool               // set true by the stay_silent tool — caller suppresses the LLM's text reply but still flushes attachments
	availableTools    map[string]bool    // names that actually resolved for this caller; see SetAvailableTools/HasTool. nil = unknown, which callers must treat as "name no tools"
	LLM               LLM                // optional LLM made available to tools that need sub-calls
	LeadLLM           LLM                // optional lead/judge LLM for tools that want a higher-tier reasoner (delegate orchestrator); falls back to LLM when nil
	DB                Database           // optional DB handle for tools that need persistence (e.g. create_temp_tool with persist=true)
	WorkspaceDir      string             // absolute path to the sandbox dir for local-exec / file-I/O tools; empty disables sandboxed tools entirely
	WorkspaceID       string             // ID of the managed workspace currently active; "" when WorkspaceDir is the app's default user workspace, set by workspace(action=create|use)
	// WorkspaceFallback is a second root READS may fall back to when a path
	// isn't in WorkspaceDir — the user's own root, when the turn is running in
	// a per-agent directory. Writes never use it: an agent writes into its own
	// workspace or the separation means nothing. Empty = no fallback.
	WorkspaceFallback string

	// imageBackends memoizes ReachableImageBackends for this turn. Resolving it
	// reads the connector table, and the grouped `image` tool's schema is
	// rebuilt on every catalog assembly — the DynamicChatTool cheapness contract
	// depends on this cache. Turn-scoped with the session, so a connector
	// approved mid-conversation appears on the next turn. imageBackendsSet
	// distinguishes "resolved to nothing" from "not yet resolved".
	imageBackends    []ImageBackendChoice
	imageBackendsSet bool

	// ReplyAuthorizedKey, when set, is the recipient key (chat id or handle) of
	// the conversation this run is REPLYING to — a channel inbound the agent is
	// answering. Sending back to that same conversation is a reply, not a
	// proactive reach-out, so the messaging tools (send_message / message_contact)
	// deliver to it WITHOUT the approval queue. Empty on web / dispatch runs, so
	// the gate is unchanged everywhere except a live channel reply.
	ReplyAuthorizedKey string
	// ChannelChatID and ChannelHandle name the conversation an inbound arrived
	// on, kept so work that OUTLIVES the turn can still be delivered back to the
	// person who asked. ReplyAuthorizedKey collapses the two into one key for an
	// authorization check; delivery needs them apart, since the transport treats
	// a chat id and a bare handle differently. Empty on web and dispatch runs.
	ChannelChatID string
	ChannelHandle string
	// SpeakerName and SpeakerHandle are WHO this turn is answering — the
	// display name they chose, and the transport's own attribution of them.
	// Kept apart on purpose, and never collapsed: the name is what the agent
	// calls them in prose, the handle is the only one of the two that means
	// anything about identity. Anything deciding who somebody IS reads the
	// handle; anything writing a sentence reads the name.
	//
	// SpeakerIsOwner is the framework's answer, derived from the handle by the
	// layer that holds the owner's, so a tool never has to compare handles
	// itself and cannot be talked into a wrong answer by a chosen name.
	//
	// All three are empty on web and dispatch runs, where the person on the
	// other end is the account holder and there is nobody to tell apart.
	SpeakerName    string
	SpeakerHandle  string
	SpeakerIsOwner bool
	// StatusCallback, if set, is invoked by the send_status tool to deliver
	// an in-progress status message to the user mid-turn ("Working on it…").
	// Each app wires it differently: chat emits an SSE status event, phantom
	// enqueues an outbox item that becomes its own iMessage. If the callback
	// is nil the tool no-ops gracefully (apps that don't support live status
	// just ignore the call). Must be safe to call from a tool handler
	// goroutine — set it once at session creation and don't mutate after.
	StatusCallback func(text string)

	// ConnectPrompt, if set, is invoked by a tool that needs the user to
	// authorize an external integration (today: a per-user OAuth MCP server)
	// before it can run. The app wires it to surface an inline Connect
	// affordance — chat emits a connect_required block carrying the consent URL
	// so the user authorizes in-place instead of navigating to their Account
	// page. server is the integration id; the app builds the per-user connect
	// URL from it. Nil ⇒ the tool falls back to an actionable error only (a
	// standing-agent / wake turn with no live watcher). Must be safe to call
	// from a tool goroutine — set once at session creation, don't mutate after.
	ConnectPrompt func(server string)

	// ApprovalPrompt, if set, surfaces an INLINE approve/deny card in the
	// conversation for a one-shot authorization the user must decide on (today:
	// activating a Builder-drafted sub-agent). The app wires it to emit an
	// agent_approval block carrying the authorization id, so the user approves in
	// place instead of hunting for it in the Permissions pane. Nil ⇒ the tool
	// falls back to the "held in the Authorizations pane" text only (a wake /
	// non-interactive run with no live viewer). Must be safe to call from a tool
	// goroutine — set once at session creation, don't mutate after.
	ApprovalPrompt func(authID, agentName, brief string)

	// PendingApprovalPrompt, if set, surfaces a queued authorization as an
	// inline card the moment it is queued, for the case where someone IS
	// watching — an interactive turn that tried to reach a real person, or a
	// delegation held for consent. Without it the tool's text is the only
	// signal and the request goes and sits on the approvals pane. The app wires
	// it to emit a pending_approval block; the same card is rebuilt from the
	// stored record on later session loads, so this is a liveness improvement,
	// not the only path. Nil ⇒ text only. Must be safe to call from a tool
	// goroutine — set once at session creation, don't mutate after.
	PendingApprovalPrompt func(auth Authorization)

	// PrivilegePrompt, if set, surfaces an INLINE privileges card for an agent
	// an authoring tool just created or changed: what it may now do, and which
	// of those powers still need a human's say-so on an unattended run. The app
	// wires it to emit a privilege_grant block whose controls write to the same
	// governed records the Permissions pane edits — so the grant is decided in
	// the conversation that produced it, instead of being discovered later by a
	// trip to the pane. data is the renderer-specific payload (the caller owns
	// its shape); core neither reads nor validates it. Nil ⇒ the authoring tool
	// just returns its text (a wake / non-interactive run with no live viewer).
	// Must be safe to call from a tool goroutine — set once at session creation,
	// don't mutate after.
	PrivilegePrompt func(agentID, agentName string, data map[string]string)

	// SubAgentRunner spawns a one-shot sub-agent loop for tools that
	// need to dispatch their OWN LLM round (today: pipeline-mode
	// temp tools). Apps wire this on session creation; nil means
	// pipeline-mode tools degrade gracefully to an error. Signature:
	// (ctx, sysPrompt, userMessage, allowedToolNames, maxRounds) →
	// (final_text, error). The runner is responsible for capping
	// recursion depth so pipeline-calls-pipeline can't infinite-loop.
	SubAgentRunner func(ctx context.Context, sysPrompt, userMsg string, allowedToolNames []string, maxRounds int) (string, error)

	// TempTools holds tools the LLM defined at runtime (via the
	// create_temp_tool tool). They live for the session only — never
	// persisted, never shared across users. Apps that want to expose
	// runtime-defined tools wire AgentLoopConfig.DynamicTools to a
	// closure that converts these to AgentToolDef. Empty by default;
	// nil-safe.
	TempTools []*TempTool

	// DeniedCredentials is the set of SecureAPI credential names the running
	// agent may NOT dispatch through (mirrors AgentRecord.DisabledCredentials).
	// The app populates it at session setup for an agent turn. Enforced at the
	// fetch_url auto-route (LLM tool + script gohort.fetch_url): a covered host
	// whose credential is denied is BLOCKED rather than routed, so credential
	// scope can't be bypassed by fetching the host directly. Empty = no
	// restriction. nil-safe via CredentialDenied.
	DeniedCredentials map[string]bool

	// BundledToolNames is the set of tool names attached DIRECTLY to the
	// running agent's record (AgentRecord.Tools) rather than authored as
	// session drafts or approved into the user pool. The app wires it at
	// session setup. tool_def uses it to TAG these in list/get and to
	// route delete through UnbundleTool — otherwise an agent-bundled tool
	// looks identical to a session draft in the catalog but silently
	// reloads every turn, so a plain delete removes only the session copy
	// and the "zombie" keeps firing. Nil ⇒ no agent-bundled tools.
	BundledToolNames map[string]bool

	// UnbundleTool, when set, removes a tool from the running agent's
	// record (AgentRecord.Tools) and persists — the ONLY way to actually
	// kill an agent-bundled tool, since it's reconstituted from the record
	// on every turn. tool_def's delete calls it for a name in
	// BundledToolNames. The app owns agent persistence, so this lives as a
	// callback rather than temptool reaching into agent storage. Nil ⇒
	// delete reports the tool as unremovable and points at the editor.
	UnbundleTool func(name string) error

	// BundleTool, when set, attaches (or replaces by name) an authored
	// tool on the CALLING agent's OWN record (AgentRecord.Tools) — the
	// agent-scoped authoring target. tool_def(create) routes here for
	// every non-Builder agent so a self-serve agent grows only its own
	// kit and never writes to the user-wide pool. The app wires it at
	// session setup, capturing the running agent's identity, so temptool
	// never reaches into agent storage. Nil ⇒ agent scope degrades to a
	// session-only draft (no durable record write).
	BundleTool func(t TempTool) error

	// CanScopeGlobal reports whether THIS caller may author a tool into
	// the user-wide persistent pool. True only for the Builder agent —
	// the trusted authoring surface. Every other agent is forced to
	// agent scope (its own record) regardless, so it can never grow the
	// shared capability surface. tool_def(create) reads this to choose
	// between AdminPersistTempTool (global) and BundleTool (agent).
	CanScopeGlobal bool

	// BundleAuthoredToolTo, when non-empty, overrides the normal authoring
	// scope: the freshly authored/updated tool is written back to THIS agent's
	// own record (via AttachToolToAgent), not the user-wide pool and not the
	// running agent's own bundle. It's how Builder edits a tool that lives on
	// ANOTHER of the user's agents IN PLACE — repairing it where it lives
	// without silently promoting it to the shared pool. Set for the duration of
	// one update call and cleared after. Empty ⇒ normal scope (CanScopeGlobal
	// vs BundleTool) applies.
	BundleAuthoredToolTo string

	// Network is the framework-managed network-access gate for this
	// session and every descendant spawned from it. Top-level turns
	// build it from privacy mode; sub-agent dispatches
	// inherit the parent's connector (descendants can't be more
	// permissive than their parent). Tools that issue HTTP read it
	// via sess.NetworkAllowed() OR derive a context with the connector
	// via WithNetworkConnector(parentCtx, sess.Network) before calling
	// helpers that read from context. Nil = no constraint (back-compat
	// default; treated as allowed).
	Network *NetworkConnector

	// Ctx is the turn's cancelable context — the SAME context the agent
	// loop runs under, so it's canceled when the user stops the turn
	// (/api/cancel) or the run registry replaces it. Tools that spawn
	// their OWN synchronous sub-run (e.g. the delegate tool calling
	// RunDelegation) must derive their run context from this — via
	// sess.Context() — instead of context.Background(), so a cancel of
	// the parent turn also cancels the outgoing agent call. Without it
	// the sub-run is detached and keeps running after Stop. Nil-safe:
	// sess.Context() falls back to context.Background() when unset (apps
	// that don't wire it, or detached background runs).
	Ctx context.Context

	// Username scopes session-aware features that need a stable identity:
	// loading a user's persistent temp tools, scoping the workspace,
	// approval-queue association. Empty for unauthenticated sessions
	// (chat without auth) or apps that don't support per-user features
	// (phantom, since persistence isn't honored there).
	Username string

	// ChatSessionID identifies the chat session this tool call is running
	// within (chat app only). Used by tools that need to schedule recurring
	// callbacks back into the same conversation (schedule_chat_update,
	// stock-tracker style features). Empty for non-chat apps.
	ChatSessionID string

	// DeliverySessionID is the conversation a background result must be
	// delivered INTO, when that is not the same as the session the work ran
	// under. Empty means they are the same, which is the ordinary case.
	//
	// They come apart wherever a turn runs under a deliberately separate
	// sub-session id. A scheduled fire and a background-task wake both do:
	// their tool session is "scheduled:<real>", so this fire's ephemeral
	// load_tool and temp-tool state stays off the user's interactive thread —
	// correct, and invisible until something detached from one of those turns
	// and tried to come home. It went home to "scheduled:<real>", a session the
	// user is not looking at: the pictures were generated, delivered, and
	// arrived in a conversation nobody was in.
	//
	// So the id the work runs under and the id its result belongs to are two
	// facts, and the second one is the one delivery has to use.
	DeliverySessionID string

	// IntentText is the turn's driving text — the user message, standing
	// mission, or dispatch brief this session was built to serve. Hosts set
	// it so catalog assembly can make intent-aware choices (e.g. elevating a
	// lazily-listed tool the intent literally names). Optional; empty means
	// no intent-derived behavior.
	IntentText string

	// AgentID is the agent whose turn this is. Set by the host app that owns
	// agents; empty for hosts that have none.
	//
	// It exists so an authoring tool can commit what it writes to the agent that
	// asked for it. Without it, a tool authored mid-conversation had nowhere to
	// live but a per-CHAT-SESSION pool — a scope no user thinks in (delete the
	// chat, lose the tool) that also had to be reconciled against the real
	// scopes on every read.
	AgentID string

	// AuthoringAgentFn, when set, returns the agent currently in AUTHORING FOCUS
	// — the one an authoring turn is building for, which is usually NOT the
	// agent running the turn. Builder is the case that matters: it authors tools
	// for other agents, so committing what it writes to AgentID would pile every
	// tool it ever built onto Builder itself.
	//
	// A function rather than a field because focus moves mid-turn (a get_agent
	// or create_agent call reassigns it), and a value captured at session
	// construction would be stale by the time a tool is authored.
	AuthoringAgentFn func() string

	// DispatchParentAgentID is set when this session runs a sub-agent dispatched
	// by a parent (e.g. Chat dispatching Builder). It carries the PARENT agent's
	// id so authoring tools can stamp creations as owned by the parent and route
	// them to the parent owner's approval queue. Empty for top-level turns.
	DispatchParentAgentID string

	// RoutingTarget is a generic "where does this session belong?" identifier
	// the host app stamps on at construction time. Format is "<prefix>:<ref>"
	// — e.g. "phantom:iMessage;-;+14155551234", "chat:<sessionID>". It scopes
	// session-bound state for tools that act on behalf of an originating
	// context; today it keys the managed workspace (see workspace_managed.go)
	// so files a tool writes for a phantom conversation land in a stable spot.
	RoutingTarget string

	// Detached marks a session built for work that OUTLIVES the turn
	// that started it (see ForDetachedTask). It changes one thing for
	// a tool that produces a deliverable: there is no model round
	// after this call, so nothing downstream will be told to attach
	// what it made. A tool that would normally hand back "call
	// workspace(attach) with this path" must attach it ITSELF here,
	// or the file it just produced is never delivered to anyone.
	Detached bool

	// Files holds generic file attachments the LLM produced via
	// attach_file. Distinct from Images/Videos — those have inline
	// rendering paths in the chat UI and bridge protocol; Files is
	// for arbitrary types (PDFs, archives, CSVs, audio, etc.) and
	// renders as a download link in chat. Phantom currently ignores
	// this channel (delivery via the bridge's iMessage attachment
	// path is a future addition).
	Files []FileAttachment

	// Per-session "flushed up to here" markers for the attachment
	// channels. Used by the app's flush-new-attachments hook to
	// claim an exclusive range atomically — without this, parallel
	// tool-call goroutines that all snapshot len(sess.Images)
	// before any append capture the same starting index and each
	// goroutine ends up flushing the FULL accumulated slice. Each
	// image then gets delivered once per concurrent attach call:
	// 2 distinct attaches → 4 deliveries, 3 → 9, etc.
	//
	// The markers are read/written under mu. ClaimUnflushedImages
	// and friends return the previously-unflushed slice + advance
	// the marker so the same range can't be claimed twice.
	flushedImages int
	flushedVideos int
	flushedFiles  int

	// stagedFiles names the workspace files THIS turn's tool calls created,
	// accumulated as they happen. The framework already computes this per call
	// to tell the model what appeared; keeping it is what lets a host tell "the
	// picture this turn made" apart from "a picture in the workspace".
	//
	// The workspace root is per USER, shared by every session, agent and turn,
	// so "the newest deliverable file in it" is not the same question as "what
	// did this turn produce" — and answering the first when you meant the
	// second is how a reply about one thing arrives carrying a picture of
	// another.
	stagedFiles []string

	// continuation is what a DETACHED call wants the turn that delivers its
	// result to do next — today, start the next piece of a declared series.
	//
	// It travels with the product rather than in it. The result text is kept in
	// the thread as fact (it names the handle for the finished picture); an
	// instruction kept there is one the model can read again three turns later
	// and obey a second time, which is how a series of three became a series
	// with no end. This channel is one-shot and lands in the delivering turn's
	// prompt only.
	continuation string

	// Detach is this TURN's background-job ledger, one slot per tool. Shared by
	// every session a turn mints — a host that runs a turn across several
	// sessions (one per plan step, one for the inline round) points them all at
	// the same ledger, or the cap is per session and a three-step plan starts
	// three jobs for one request. Nil means a private ledger is minted on first
	// use, which is the right default for a standalone session. See
	// ClaimDetachSlot.
	Detach *DetachLedger

	mu sync.Mutex
}

// ClaimDetachSlot reserves this turn's background slot for one tool, atomically.
// ok is false when a job already holds it, and prior names that job.
//
// Same-round siblings are refused too, and that is deliberate: three renders
// started at once are three separate deliveries whichever round they came from,
// and the model can ask for the next one after the first reports back. A nil
// session keeps its old behaviour and claims nothing. See DetachLedger.
func (s *ToolSession) ClaimDetachSlot(tool string) (prior TaskRun, ok bool) {
	if s == nil {
		return TaskRun{}, true
	}
	return s.detachLedger().claim(tool)
}

// RecordDetachSlot fills in the job that took the slot, so a later refusal can
// name it.
func (s *ToolSession) RecordDetachSlot(tool string, run TaskRun) {
	if s == nil {
		return
	}
	s.detachLedger().record(tool, run)
}

// RecordDetachEstimate remembers the wait the model was invited to quote, so
// the turn judge can tell a supplied duration from an invented one.
func (s *ToolSession) RecordDetachEstimate(d time.Duration) {
	if s == nil || d <= 0 {
		return
	}
	s.detachLedger().recordEstimate(d)
}

// ReleaseDetachSlot gives the slot back when the detach did not happen after all
// — the run failed to start and the call went inline instead. Holding it then
// would refuse the next call over a job that never existed.
func (s *ToolSession) ReleaseDetachSlot(tool string) {
	if s == nil {
		return
	}
	s.detachLedger().release(tool)
}

// SetTaskContinuation records what the turn delivering this detached call's
// result should do next. Only meaningful on a detached session — an inline call
// still has a round of its own and says so in its return value.
func (s *ToolSession) SetTaskContinuation(instruction string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.continuation = instruction
}

// TakeTaskContinuation returns the pending instruction and clears it, so it
// reaches exactly one turn.
func (s *ToolSession) TakeTaskContinuation() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.continuation
	s.continuation = ""
	return out
}

// detachLedger returns the turn's ledger, minting a private one on first use for
// a session nobody shared one with.
func (s *ToolSession) detachLedger() *DetachLedger {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Detach == nil {
		s.Detach = NewDetachLedger()
	}
	return s.Detach
}

// AddStagedFiles records workspace files this turn produced. Idempotent per
// name, order-preserving.
//
// Exported because a host may run a turn across several ToolSessions — one per
// step, one for the inline round — and the files a turn staged belong to the
// TURN, not to whichever session happened to be live when a tool wrote them.
// A host that mints sessions per step carries the list forward with this.
func (s *ToolSession) AddStagedFiles(names []string) {
	if s == nil || len(names) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || slices.Contains(s.stagedFiles, n) {
			continue
		}
		s.stagedFiles = append(s.stagedFiles, n)
	}
}

// StagedFiles is what this turn wrote into the workspace, oldest first. The
// caller decides which of them is deliverable — that judgement is the app's.
func (s *ToolSession) StagedFiles() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.stagedFiles)
}

// ClaimUnflushedImages returns the slice of images that haven't
// been claimed for delivery yet, and advances the per-session
// NetworkAllowed reports whether the session's network connector
// permits network calls. Nil session or nil connector = allowed
// (back-compat default; matches NetworkConnector.Allowed). Tools
// that issue HTTP should check this before making the call.
func (s *ToolSession) NetworkAllowed() bool {
	if s == nil {
		return true
	}
	return s.Network.Allowed()
}

// SetAvailableTools records the tool names that actually resolved for this
// caller. Called once where the catalog is built; safe on a nil session.
func (s *ToolSession) SetAvailableTools(names []string) {
	if s == nil {
		return
	}
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	s.availableTools = m
}

// HasTool reports whether name resolved for this caller.
//
// Unknown (no session, or nobody called SetAvailableTools) returns FALSE, so a
// caller that suggests a recovery path suggests nothing rather than something
// imaginary. Naming a tool the model does not have is worse than naming none:
// it spends rounds on a door that isn't there. See FirstAvailableTool.
func (s *ToolSession) HasTool(name string) bool {
	if s == nil || s.availableTools == nil {
		return false
	}
	return s.availableTools[name]
}

// FirstAvailableTool returns the first of names that resolved for this caller,
// or "" if none did. For building an actionable suggestion out of whichever
// capability the caller actually has.
func (s *ToolSession) FirstAvailableTool(names ...string) string {
	for _, n := range names {
		if s.HasTool(n) {
			return n
		}
	}
	return ""
}

// Context returns the session's turn context (s.Ctx), or
// context.Background() when unset or the session is nil. Tools that
// spawn a synchronous sub-run should root it here so a parent-turn
// cancel propagates to the child.
func (s *ToolSession) Context() context.Context {
	if s == nil || s.Ctx == nil {
		return context.Background()
	}
	return s.Ctx
}

// ContextWithNetworkConnector wraps ctx with the session's connector
// (when set) so context-based helpers like RunSandboxedShellWithEnv
// see the same gate. Returns ctx unchanged when no connector is set.
func (s *ToolSession) ContextWithNetworkConnector(ctx context.Context) context.Context {
	if s == nil {
		return ctx
	}
	// The workspace rides along with the connector. Both are per-turn facts a
	// delegated run has to inherit, and this is the one place that knows the
	// CURRENT one — a turn that called workspace(create) mid-flight has
	// already moved, and a dir captured when the turn started would be stale.
	ctx = WithWorkspaceDir(ctx, s.WorkspaceDir)
	if s.Network == nil {
		return ctx
	}
	return WithNetworkConnector(ctx, s.Network)
}

// ContextWithSandboxCaller stamps the run with whether the human behind it is
// an admin, which is what the "admin only" unsandboxed-bypass keys on.
//
// Applied here rather than resolved inside the sandbox because by the time a
// command reaches exec there is no request, no cookie and no session left to
// resolve it FROM — only a context and a string of shell. Anything that does
// not pass through a ToolSession (a schedule, a channel wake, a monitor
// evaluator, an export generator) never gets stamped and is therefore not an
// admin, which is the correct answer for all four: nobody is at the keyboard.
//
// Note this asks about the OWNER of the session, not about what the model
// wants. An LLM cannot stamp itself admin; it can only run inside a session
// that already belongs to one.
func (s *ToolSession) ContextWithSandboxCaller(ctx context.Context) context.Context {
	if s == nil {
		return ctx
	}
	return ContextWithSandboxUser(ctx, s.Username)
}

// ContextWithSandboxUser is ContextWithSandboxCaller for a caller that knows
// WHO it is acting for but has no ToolSession to say it through — the app
// authoring path, where a chat turn runs node --check over a section the user
// is editing. Same rule: only the named user's own admin flag counts, an
// unknown or non-admin name stamps nothing, and nothing stamped means not an
// admin.
func ContextWithSandboxUser(ctx context.Context, username string) context.Context {
	if strings.TrimSpace(username) == "" || AuthDB == nil {
		return ctx
	}
	db := AuthDB()
	if db == nil {
		return ctx
	}
	user, ok := AuthGetUser(db, username)
	if !ok || !user.Admin {
		return ctx
	}
	return WithAdminCaller(ctx, true)
}

// flushed marker. Call once per tool-call dispatch (typically
// from the app's flushNewAttachments hook). Returns an empty
// slice when there's nothing new.
func (s *ToolSession) ClaimUnflushedImages() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flushedImages >= len(s.Images) {
		return nil
	}
	out := append([]string(nil), s.Images[s.flushedImages:]...)
	s.flushedImages = len(s.Images)
	return out
}

// ClaimUnflushedVideos — see ClaimUnflushedImages.
func (s *ToolSession) ClaimUnflushedVideos() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flushedVideos >= len(s.Videos) {
		return nil
	}
	out := append([]string(nil), s.Videos[s.flushedVideos:]...)
	s.flushedVideos = len(s.Videos)
	return out
}

// ClaimUnflushedFiles — see ClaimUnflushedImages.
func (s *ToolSession) ClaimUnflushedFiles() []FileAttachment {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flushedFiles >= len(s.Files) {
		return nil
	}
	out := append([]FileAttachment(nil), s.Files[s.flushedFiles:]...)
	s.flushedFiles = len(s.Files)
	return out
}

// FileAttachment is one entry in ToolSession.Files. Data is base64-
// encoded bytes; MimeType is sniffed from the content (not trusted
// from any user-supplied extension); Name is the workspace-relative
// path the LLM referenced — useful as the suggested filename when
// the user downloads.
type FileAttachment struct {
	Name     string `json:"name"`
	MimeType string `json:"mime_type"`
	Data     string `json:"data"` // base64
	Size     int    `json:"size"` // raw byte count, pre-base64
}

// AppendFile records a file attachment on the session under the lock.
// Content-dedup by data payload: duplicate content with the same Name
// gets skipped to defend against LLMs that emit redundant attach
// calls in one batch.
func (s *ToolSession) AppendFile(f FileAttachment) {
	if s == nil || f.Data == "" {
		return
	}
	s.mu.Lock()
	for _, existing := range s.Files {
		if existing.Data == f.Data && existing.Name == f.Name {
			s.mu.Unlock()
			return
		}
	}
	s.Files = append(s.Files, f)
	s.mu.Unlock()
}
