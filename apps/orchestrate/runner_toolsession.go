package orchestrate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
)

// newToolSession constructs the per-turn ToolSession, then loads the
// user's approved persistent temp tools onto it so the LLM sees them
// alongside the built-in registry. Private mode mirrors the chat
// app's policy: shell-mode persistent tools stay (no network), API-
// mode persistent tools (HTTP calls) get dropped.
//
// sess.DB is the user-scoped sub-store (t.udb), not the app's root
// DB — agent-CRUD tools (create_agent / update_agent / etc.) write
// AgentRecord entries via this DB, and listAgents in the chat page
// reads from the user-scoped store. Using the app DB here would save
// agents to a bucket nobody reads from, making them invisible.
//
// Shared by runPlan (orchestrator's inline tool surface) and
// runWorkerStep (worker step) so both paths get the same persistent
// tool pool without drift.
// captureActiveWorkspace records a managed-workspace switch a session
// performed (via workspace create/use, which set sess.WorkspaceID) so
// the next newToolSession() this turn restores the same workspace.
// No-op when the session stayed on the per-user root (WorkspaceID
// empty) — that case keeps activeWorkspaceID empty so we don't pin a
// transient root path. Safe to call via defer on any session.
func (t *chatTurn) captureActiveWorkspace(sess *ToolSession) {
	if sess != nil && sess.WorkspaceID != "" {
		t.activeWorkspaceID = sess.WorkspaceID
	}
	t.captureStagedFiles(sess)
}

// captureStagedFiles folds the files a session's tools created into the turn,
// so the delivery backstop sees everything this turn produced no matter which
// of the turn's sessions produced it. Paired with the seeding in
// newToolSession, which hands the accumulated list to each fresh session.
func (t *chatTurn) captureStagedFiles(sess *ToolSession) {
	staged := sess.StagedFiles()
	if len(staged) == 0 {
		return
	}
	t.stagedMu.Lock()
	defer t.stagedMu.Unlock()
	for _, n := range staged {
		if !slices.Contains(t.stagedFiles, n) {
			t.stagedFiles = append(t.stagedFiles, n)
		}
	}
}

// stagedThisTurn is what the turn has written into the workspace so far.
func (t *chatTurn) stagedThisTurn() []string {
	t.stagedMu.Lock()
	defer t.stagedMu.Unlock()
	return slices.Clone(t.stagedFiles)
}

// loadAgentTempTools hydrates sess.TempTools with the agent's custom (temp)
// tools and records the agent's uniquely-attached kit in t.agentOwnTools. Two
// sources: the user's approved PERSISTENT pool (drawn from poolUser/poolDB — the
// AGENT OWNER, so a channel/dispatch run under a synthetic runtime user still
// gets the owner's tools, not the empty pool of "phantom:<chat>") gated by the
// agent's AllowedTools allow-list + per-agent deny list + private mode + the
// Builder-authors-fresh skip; and the agent-scoped AgentRecord.Tools kit (no
// allow-list gate — attached directly to the record). Extracted from
// newToolSession so the web and channel/dispatch surfaces hydrate IDENTICALLY:
// without this, a dispatched/channel session built its own ToolSession and
// loaded ZERO custom tools, so an agent-authored tool (e.g. ts3_client_status)
// worked in the web chat but was invisible over a channel.
func (t *chatTurn) loadAgentTempTools(sess *ToolSession, poolUser string, poolDB Database) {
	if sess == nil || poolDB == nil || poolUser == "" {
		return
	}
	noTools := isNoToolsSentinel(t.agent.AllowedTools)
	// Treat any agent backed by an in-code seed as a seed regardless of
	// whether its per-user shadow carries Owner==seedOwner. The seed-chat
	// shadow may have Owner==username when the user saved customizations
	// through the admin UI, but the AllowedTools gate must still be nil so
	// all approved persistent tools (including toolbox-mode tools like vapi
	// that bypass the OnTempToolApproved path) auto-load.
	_, isSeedBacked := seedAgentByID(t.agent.ID)
	isSeed := t.agent.Owner == seedOwner || isSeedBacked
	disabledPersistent := make(map[string]bool, len(t.agent.DisabledPersistentTools))
	for _, n := range t.agent.DisabledPersistentTools {
		disabledPersistent[n] = true
	}
	// User-crafted agents only: an explicit AllowedTools list still gates
	// persistent temp tools. Empty list = default pool = include all. Nil for
	// seed agents (admin approval is enough).
	var allowPersistent map[string]bool
	if !isSeed && !noTools && len(t.agent.AllowedTools) > 0 {
		allowPersistent = make(map[string]bool, len(t.agent.AllowedTools))
		for _, n := range t.agent.AllowedTools {
			allowPersistent[canonicalToolName(n)] = true
		}
	}
	// The Builder loads the full tool pool like every other agent. It used to
	// "author fresh" — skipping the user's existing tools from its EXECUTABLE
	// catalog (enumerate-only via tool_def(action="list")) — but the Builder's job
	// is to build AND verify, so it needs to actually run existing tools: call an
	// api tool it just wrote, test one that dispatches through a credential, etc.
	// This is orthogonal to Secured credentials: a secured cred still exposes no
	// generic call tool (no improvising) and still refuses NEW tools that declare
	// it (the authoring gate); the Builder only gains the ability to use the
	// EXISTING declaring tools, which is the point.
	//
	// Deployment-shared (global) persistent tools are OPT-IN: a Shared tool loads
	// for this user's agents only once the user has adopted it from the global-
	// tool catalog (Account page). The user's own pool always loads; the shared
	// pool is filtered to the user's adoption set, deduped by name (own copy
	// wins). The per-agent gates below (private-mode, disabled list, user-crafted
	// allow-list) then apply uniformly. Existing users were grandfathered into
	// their previously auto-loaded shared tools by migrateGlobalToolAdoption.
	// Namespace-flatten stragglers: an agent record still carrying embedded
	// Tools (written by an old binary or an old import) folds into the
	// unified store before hydration, so the rows below are complete.
	if len(t.agent.Tools) > 0 {
		migrateAgentToolsToStore(t.udb, poolUser, &t.agent)
	}
	loaded := LoadPersistentTempTools(poolDB, poolUser)
	own := make(map[string]bool, len(loaded))
	for _, p := range loaded {
		own[p.Tool.Name] = true
	}
	adoptedGlobal := LoadAdoptedGlobalTools(poolDB, poolUser)
	for _, p := range LoadSharedPersistentTempTools(poolDB) {
		if !own[p.Tool.Name] && adoptedGlobal[p.Tool.Name] {
			loaded = append(loaded, p)
		}
	}
	if t.agentOwnTools == nil {
		t.agentOwnTools = map[string]bool{}
	}
	for _, p := range loaded {
		if noTools {
			continue
		}
		// Governance visibility: a Disabled tool (turned off in Extensions › Tools) and a
		// Builder-only tool are both hidden from every agent EXCEPT Builder.
		// Builder always loads the full pool so it can load, RUN, test, fix, and
		// re-enable them — building AND verifying is its whole job; a disabled
		// tool it couldn't run would be unfixable.
		// Bound-only joins the same carve-out for the same reason the comment
		// above gives: Builder has to be able to load, RUN and fix these, and a
		// tool it cannot run is a tool it cannot repair. A set of read tools
		// authored for one system is exactly the kind of thing somebody asks
		// Builder to fix, so reserving it away from the one agent that could
		// would be trading a real capability for a tidier catalog.
		if (p.Tool.Disabled || p.Tool.BuilderOnly || p.Tool.BoundOnly) && !isBuilderAgent(t.agent.ID) {
			continue
		}
		// Private mode hides API-mode temp tools (network side effects);
		// shell-mode stays (sandboxed local).
		if t.privateMode && p.Tool.Mode == TempToolModeAPI {
			continue
		}
		// Agent-scoped record (flattened namespace — the old AgentRecord.Tools
		// kit): part of THIS agent's own kit when scoped to it, with NO
		// allow/deny gates (it's attached by intent). Other agents' scoped
		// tools stay out of the catalog — except for Builder, which loads
		// everything so it can run, test, and fix any tool it can edit.
		if len(p.ScopeAgents) > 0 {
			if !p.ScopedToAgent(t.agent.ID) && !isBuilderAgent(t.agent.ID) {
				continue
			}
			// An explicit per-agent opt-out beats "attached by intent". Scoping
			// says WHICH agents may have the tool; the deny list is the owner
			// saying this agent should not USE it right now. Without this check
			// a scoped tool had no off switch at all — which is why the Tools
			// modal could only list it read-only while every other tool got a
			// checkbox. Builder still loads it: turning a tool off must not make
			// it unfixable by the surface that fixes tools.
			if disabledPersistent[p.Tool.Name] && !isBuilderAgent(t.agent.ID) {
				continue
			}
			tool := p.Tool
			t.agentOwnTools[tool.Name] = true // the agent's deliberate kit (first-classed in setupCustomTools)
			if err := sess.AppendTempTool(&tool); err != nil {
				Log("[orchestrate.tools] agent-scoped tool %q failed to load: %v", tool.Name, err)
			}
			continue
		}
		if disabledPersistent[p.Tool.Name] { // per-agent opt-out
			continue
		}
		if allowPersistent != nil && !allowPersistent[p.Tool.Name] { // user-crafted allow-list
			continue
		}
		tool := p.Tool
		if err := sess.AppendTempTool(&tool); err != nil {
			Log("[orchestrate.tools] persistent temp tool %q failed to load: %v", tool.Name, err)
		}
	}
	if n := len(loaded); n > 0 {
		Log("[orchestrate.tools] loaded %d persistent temp tool(s) for %s", n, poolUser)
	}
	// Agent-scoped tools now ride the unified store (ScopeAgents on the
	// record) and were folded into the pool loop above — AgentRecord.Tools is
	// no longer a runtime source (see migrateAgentToolsToStore).
	if n := len(t.agentOwnTools); n > 0 {
		Log("[orchestrate.tools] attached %d agent-scoped tool(s) for agent=%s", n, t.agent.ID)
	}
	// Expose the bundled set + an unbundle path to tool_def so a
	// record-attached tool is legible as such (list/get tag it) and
	// actually removable (delete routes through the agent record). Every
	// tool in agent.Tools is bundled, even the ones already covered by
	// the persistent pool — the point is that tool_def's delete must
	// reach the RECORD, not just the session copy. Scope the callback to
	// this agent + owner so a delete can't touch another agent's kit.
	// Wire the agent-scope authoring + bundle-management callbacks, all
	// owner-scoped to THIS agent: BundleTool attaches a newly authored
	// tool to the record, UnbundleTool removes one, BundledToolNames tags
	// the already-bundled set. CanScopeGlobal gates whether tool_def may
	// instead persist a tool user-wide — Builder only; every other agent
	// is forced to agent scope. BundleTool + CanScopeGlobal are wired
	// unconditionally (even from an empty Tools[]) so a FIRST agent-scoped
	// tool has a durable home; BundledToolNames only matters once the
	// record already carries tools.
	base := t.agent
	agentID, owner, poolDBRef := t.agent.ID, poolUser, poolDB
	sess.CanScopeGlobal = isBuilderAgent(agentID)
	sess.BundleTool = func(tt TempTool) error {
		return bundleAgentTool(poolDBRef, owner, base, tt)
	}
	sess.UnbundleTool = func(name string) error {
		return unbundleAgentTool(poolDBRef, owner, agentID, name)
	}
	if scoped := AgentScopedTools(poolDB, poolUser, t.agent.ID); len(scoped) > 0 {
		bundled := make(map[string]bool, len(scoped))
		for _, p := range scoped {
			bundled[p.Tool.Name] = true
		}
		sess.BundledToolNames = bundled
	}
}

// wireLiveCallbacks attaches the mid-turn user-facing hooks (send_status and the
// per-user OAuth Connect prompt) to a ToolSession — the main turn session AND any
// sub-agent session (pipeline stages) that should reach the live conversation.
// Both only fire when this turn has a live SSE stream: a standing-agent / wake
// turn has no watcher, so the tools fall through to their no-op guidance instead
// of emitting into a dead writer.
func (t *chatTurn) wireLiveCallbacks(sess *ToolSession) {
	if sess == nil || t.sse == nil {
		return
	}
	// send_status → a PERSISTENT muted line in the conversation flow
	// (kind:status_note → convoLog), above the eventual reply — NOT the topbar
	// status bar, which is cleared on 'done' so a mid-turn status would vanish
	// before the user could read it.
	sess.StatusCallback = func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		t.sse.Send(map[string]any{"kind": "status_note", "text": text})
	}
	// A tool needs the user to authorize a per-user OAuth integration (e.g. an
	// MCP server backing this agent/pipeline). Emit a connect_required block: the
	// client renders a Connect button that opens the consent popup at the account
	// connect endpoint, so the user authorizes in-place and retries — no trip to
	// the Account page. Site-absolute path since chat runs under a different app
	// root than /account.
	sess.ConnectPrompt = func(server string) {
		server = strings.TrimSpace(server)
		if server == "" {
			return
		}
		t.sse.Send(map[string]any{
			"kind":   "block",
			"type":   "connect_required",
			"id":     fmt.Sprintf("connect-%d", time.Now().UnixNano()),
			"server": server,
			"url":    "/account/mcp/connect?server=" + url.QueryEscape(server),
		})
	}
	// Inline sub-agent approval card — surfaces a drafted-but-held sub-agent as an
	// Approve/Deny card right in the conversation, wired to the same authorization
	// the Permissions pane shows. See ToolSession.ApprovalPrompt / create_agent.
	sess.ApprovalPrompt = func(authID, agentName, brief string) {
		authID = strings.TrimSpace(authID)
		if authID == "" {
			return
		}
		t.sse.Send(map[string]any{
			"kind":    "block",
			"type":    "agent_approval",
			"id":      "agent-approval-" + authID,
			"auth_id": authID,
			"name":    agentName,
			"brief":   brief,
		})
	}
	// Inline pending-approval card — a request queued during a turn the user is
	// watching, shown here rather than only counted on the Permissions tile.
	// Session loads rebuild the same card from the stored record (see
	// pendingApprovalBlocks), so this is purely the live path.
	sess.PendingApprovalPrompt = func(auth Authorization) {
		if strings.TrimSpace(auth.ID) == "" {
			return
		}
		who, detail := approvalDisplay(t.udb, t.user, auth)
		data := map[string]string{
			"auth_id": auth.ID,
			"action":  auth.Action,
			"who":     who,
			"detail":  detail,
		}
		if approvalAlwaysMeans(auth.Action) {
			data["always"] = "1"
		}
		t.sse.Send(map[string]any{
			"kind":  "block",
			"type":  "pending_approval",
			"id":    "approval-" + auth.ID,
			"title": who,
			"text":  detail,
			"data":  data,
		})
	}
	// Inline privileges card — what an agent an authoring tool just saved may
	// now do, editable in place. Persisted as a UIBlock (like the credential
	// card) so it replays with the session, and upserted per agent so a build
	// that saves the same agent twice refreshes one card instead of stacking.
	sess.PrivilegePrompt = func(agentID, agentName string, data map[string]string) {
		agentID = strings.TrimSpace(agentID)
		if agentID == "" {
			return
		}
		if data == nil {
			data = map[string]string{}
		}
		// The card's target rides in data, not a top-level field: UIBlock has no
		// agent column, so a replayed block would otherwise lose the id the
		// controls POST against and render a card that can't write.
		data["agent_id"] = agentID
		id := "privileges-" + agentID
		t.sse.Send(map[string]any{
			"kind":  "block",
			"type":  "privilege_grant",
			"id":    id,
			"title": agentName,
			"data":  data,
		})
		if t.session != nil {
			blk := UIBlock{Type: "privilege_grant", ID: id, Title: agentName, Data: data}
			t.toolMu.Lock()
			t.session.upsert_ui_block(blk, func(b *UIBlock) bool { return b.ID == id })
			t.toolMu.Unlock()
		}
	}
}

// sandboxCallerCtx is this turn's context for a sandboxed run started OUTSIDE
// a tool session — the app-authoring syntax checks, which run node over the
// user's own draft rather than anything the model asked to execute.
//
// It exists because those checks previously built their context from
// Background and so arrived at the sandbox anonymous. On a host that cannot
// confine, with the bypass set to admin-only, that made the checker refuse for
// the one person entitled to run it, and report "no verdict" — the checker
// meant to catch a typo silently declining to look.
func (t *chatTurn) sandboxCallerCtx() context.Context {
	if t == nil {
		return context.Background()
	}
	return ContextWithSandboxUser(context.Background(), t.user)
}

// detachLedger is the turn's one background-job ledger. See chatTurn.detach.
func (t *chatTurn) detachLedger() *DetachLedger {
	t.detachOnce.Do(func() { t.detach = NewDetachLedger() })
	return t.detach
}

// chatSessionID is this turn's conversation id, "" when the turn has no
// persisted session (a wake, a one-shot). Anything keyed per-conversation —
// the commitment ledger — simply does nothing without one.
func (t *chatTurn) chatSessionID() string {
	if t == nil || t.session == nil {
		return ""
	}
	return t.session.ID
}

func (t *chatTurn) newToolSession() *ToolSession {
	sess := &ToolSession{
		LLM:      t.app.LLM,
		LeadLLM:  t.app.LeadLLM,
		Username: t.user,
		DB:       t.udb,
		// Whose turn this is — an authoring tool commits what it writes to
		// this agent rather than to a per-chat-session pool.
		AgentID: t.agent.ID,
		// …unless the turn is authoring FOR another agent, which is Builder's
		// whole job. Read live: focus moves when get_agent / create_agent runs.
		AuthoringAgentFn: func() string {
			if t.session != nil {
				return t.session.AuthoringAgentID
			}
			return ""
		},
		// Network connector — the SAME instance carried on t.ctx +
		// the inflight registry. Sharing one pointer is what makes
		// the mid-turn cutoff propagate: when the privacy endpoint
		// flips it, sess.NetworkAllowed() and
		// NetworkAllowedFromContext(ctx) both see the new state.
		Network: t.network,
		// Turn context — tools that spawn synchronous sub-runs (delegate)
		// root them here via sess.Context() so a Stop / cancel of this
		// turn also cancels the outgoing agent call instead of leaving it
		// detached on context.Background().
		Ctx: t.ctx,
		// Pipeline-mode temp tools dispatch through this runner. Caps
		// recursion depth via t.pipelineDepth (incremented on entry,
		// decremented on exit) so pipeline-calls-pipeline can't
		// infinite-loop. See runPipelineSubAgent.
		SubAgentRunner: t.runPipelineSubAgent,
	}
	// Credential scope — the set of credentials this agent has been denied
	// (scope pill). Carried on the session so the fetch_url auto-route (LLM
	// tool + script gohort.fetch_url) blocks a covered host whose credential
	// is revoked, closing the bypass that the tool-kit filter alone leaves
	// open (a plain fetch to the host instead of a credential-bound tool).
	// One background-job allowance for the whole turn, not one per session. A
	// plan step gets its own session; it does not get its own right to start
	// another render.
	sess.Detach = t.detachLedger()
	sess.DeniedCredentials = credentialDenySet(t.agent, sess.Username)
	// Tag with the active chat session id so SaveSessionTempTool /
	// LoadSessionTempTools can scope tool drafts to this conversation.
	// Tools the LLM authors mid-conversation (via create_pipeline_tool
	// or create_agent's inline tools[]) land in the session_temp_tools
	// bucket keyed by this id so they're callable in this session
	// immediately for verification before any other commit step.
	if t.session != nil {
		sess.ChatSessionID = t.session.ID
	}
	// An uploaded picture reaches the LLM as vision content, which lets the
	// model SEE it and gives it no way to NAME it. Asked to blend that photo
	// with one it generated, the only handle it has is the generated image#1 —
	// so it passes what it can, gets a picture back, and reports success while
	// the user's photo was never involved.
	//
	// Registering it here gives it the same handles the dispatch path already
	// mints: media#N for this turn, and (through RegisterInboundMedia) a place
	// in the image space, so it stays editable on later turns too.
	for _, img := range t.userImages {
		sess.RegisterInboundMedia("image", img, "")
	}
	// send_status delivery for chat. Renders as a PERSISTENT muted line in
	// the conversation flow (kind:status_note → appended to convoLog),
	// above the eventual reply — NOT the topbar status bar, which is
	// cleared on 'done' so a mid-turn status vanished before the user
	// could read it (the "send_status isn't working" symptom). Only wired
	// when this turn has a live SSE stream — a standing-agent / wake turn
	// has no watcher, so send_status falls through to its built-in no-op
	// guidance there instead of emitting into a dead writer.
	t.wireLiveCallbacks(sess)
	// Default workspace = per-user root. Tools that author scripts
	// via script_body ship them here at registration; dispatch finds
	// the same path. Tools that want explicit per-conversation
	// isolation call workspace(create) which switches sess.WorkspaceDir
	// to a managed workspace; otherwise everything operates at the
	// user root.
	//
	// History note: this was briefly auto-minting a managed workspace
	// per session. That broke existing persistent_temp_tools whose
	// script_body was shipped to the user root at authoring time —
	// the new managed workspace had no copy. Reverted; per-session
	// isolation is now opt-in via workspace(create).
	//
	// If a PRIOR step this turn switched to a managed workspace (via
	// workspace create/use), restore it so multi-step authoring shares
	// one workspace: a script written in step N is visible to a run in
	// step N+1. Falls through to the per-user root if the managed
	// workspace is gone (deleted / not ours).
	sess.WorkspaceDir, sess.WorkspaceID, sess.WorkspaceFallback = t.turnWorkspace()
	if sess.DB == nil || sess.Username == "" {
		return sess
	}
	// Persistent temp tools — gate logic depends on whether this is a
	// SEED agent (framework default, owner="system") or a USER-crafted
	// agent. Seed agents reflect "everything deployment-trusted is
	// available" — admin-approved persistent tools flow into them
	// automatically. User-crafted agents have deliberate AllowedTools
	// lists that should not be silently expanded by approvals
	// happening elsewhere; for those, persistent tools still follow
	// the explicit allow-list semantic.
	//
	// Both honor DisabledPersistentTools (per-agent deny list) as a
	// per-agent opt-out, and the no-tools sentinel (the "give me
	// absolutely no optional tools" mode) still suppresses everything.
	// Custom (temp) tools — persistent pool (this user) + the agent-scoped kit.
	// Shared with the channel/dispatch path via loadAgentTempTools so both
	// surfaces hydrate identically.
	// The OWNER's pool, not the runtime user's. loadAgentTempTools says so in
	// its own contract — "drawn from poolUser/poolDB — the AGENT OWNER, so a
	// channel/dispatch run under a synthetic runtime user still gets the owner's
	// tools" — and the dispatch path honours it. This one passed the session's
	// identity flat, so a PUBLISHED agent chatted by anyone but its author
	// resolved its AllowedTools against a store holding none of them: the tools
	// silently vanished while the prompt still claimed the capabilities. On an
	// owner-driven turn ownerView returns the same pair this used to pass.
	poolDB, poolUser := t.ownerView()
	t.loadAgentTempTools(sess, poolUser, poolDB)
	// Session-scoped tool drafts — the LLM authored these in THIS
	// conversation (e.g. for_agent-attached pipeline + bundled inline
	// tools from create_agent). Load them so the LLM can dispatch by
	// name to verify the tool works before relying on it. Persistence
	// to the agent record / approval queue already happened at author
	// time; this is purely for in-session testability.
	//
	// Drafts intentionally BYPASS the AllowedTools gate: the agent's
	// allowlist is the *committed* surface, but drafts are the
	// authoring scratchpad — the LLM needs to dispatch a draft to
	// verify it works *before* the user approves it into the agent.
	// Gating drafts on AllowedTools would make authoring impossible
	// (the tool can't be tested until it's allowed, but it isn't
	// allowed until the user has seen it work).
	if t.session != nil {
		drafts := LoadSessionTempTools(t.udb, t.session.ID)
		// Set of tool names already COMMITTED to any agent the user owns.
		// A draft whose canonical copy now lives on an agent record is
		// redundant and gets pruned. This is the Builder fix: Builder
		// authors tools for OTHER agents (committed to the TARGET's
		// record), but the session draft is keyed to BUILDER's session —
		// which never loads the target agent's Tools, so the name-conflict
		// prune below never caught them. They piled up turn after turn,
		// growing the "Your custom tools" prompt section (and busting the
		// prompt cache) for the whole build. Only built when drafts exist,
		// so the listAgents walk doesn't tax draft-free agents.
		var committed map[string]bool
		if len(drafts) > 0 {
			// Flattened namespace: every durable commit (shared OR
			// agent-scoped) is one store row — no per-agent record walk.
			committed = map[string]bool{}
			for _, p := range LoadPersistentTempTools(t.udb, t.user) {
				committed[p.Tool.Name] = true
			}
		}
		var cleaned int
		for i := range drafts {
			tool := drafts[i]
			if t.privateMode && tool.Mode == TempToolModeAPI {
				continue
			}
			// Already committed to some agent → prune; don't load a stale
			// duplicate into this session's catalog. (Catches cross-agent
			// authoring, which the name-conflict path below misses because
			// the committed copy isn't in THIS session's tool set.)
			if committed[tool.Name] {
				RemoveSessionTempTool(t.udb, t.session.ID, tool.Name)
				cleaned++
				Debug("[orchestrate.tools] pruned committed session draft %q (now lives on an agent record)", tool.Name)
				continue
			}
			if err := sess.AppendTempTool(&tool); err != nil {
				// Name conflict with the persistent or agent-scoped pool — the
				// committed version wins (it's the canonical copy) and this
				// draft is stale duplication. Drop it so the runtime and the
				// Tools UI stop showing two of the same thing.
				RemoveSessionTempTool(t.udb, t.session.ID, tool.Name)
				cleaned++
				Debug("[orchestrate.tools] dropped redundant session draft %q (committed copy exists)", tool.Name)
				continue
			}
			// MIGRATION: session-scoped tools are retired — an authored tool now
			// commits to the agent that asked for it. A draft that reaches here
			// is committed nowhere (the prune above caught the rest), so it is
			// real work living only in this conversation. Give it a durable home
			// on this agent, marked Trial, and clear the draft.
			//
			// Done here rather than in a migration script because this is where
			// drafts are consumed: they drain as conversations resume, and a
			// conversation nobody reopens has nothing worth keeping.
			// A draft authored FOR another agent belongs to that agent, not
			// to the one running the turn — the same distinction
			// ToolSession.AuthoringAgentFn draws for fresh authoring. The
			// prune above catches drafts already committed somewhere, so what
			// lands here is uncommitted work; without the focus check every
			// such draft settles on the authoring agent (Builder) and it
			// carries another agent's tool schema in its prompt from then on.
			if target := migrationTargetAgent(t.agent.ID, t.authoringFocus()); AttachToolToAgent != nil && target != "" {
				migrated := tool
				migrated.Trial = true
				migrated.TrialSince = time.Now()
				if err := AttachToolToAgent(t.udb, t.user, target, migrated); err == nil {
					RemoveSessionTempTool(t.udb, t.session.ID, tool.Name)
					Log("[orchestrate.tools] migrated session draft %q onto agent %q as a trial tool", tool.Name, target)
				} else {
					Debug("[orchestrate.tools] could not migrate session draft %q: %v", tool.Name, err)
				}
			}
		}
		if ReapTrialTools != nil {
			// Cheap in the common case: no trial tools means one agent walk and
			// no writes. Keeps an agent in regular use tidy without requiring
			// anyone to visit a settings page.
			_ = ReapTrialTools(t.udb, t.user)
		}
		if n := len(drafts); n > 0 {
			Log("[orchestrate.tools] loaded %d session-draft tool(s) for session=%s (cleaned %d redundant)", n, t.session.ID, cleaned)
		}
	}
	// Hand the new session what the turn has already staged, so any session
	// this turn mints — including the one the delivery backstop runs on — can
	// tell this turn's files apart from everything else in the shared root.
	sess.AddStagedFiles(t.stagedThisTurn())
	return sess
}

// authoringFocus reports the agent this turn is authoring FOR, or "" when the
// turn is authoring for itself. Mirrors ToolSession.AuthoringAgentFn.
func (t *chatTurn) authoringFocus() string {
	if t == nil || t.session == nil {
		return ""
	}
	return strings.TrimSpace(t.session.AuthoringAgentID)
}

// migrationTargetAgent picks which agent an uncommitted session draft settles
// on: the authoring focus when the turn is building for someone else,
// otherwise the running agent. Returns "" when neither is known, which the
// caller treats as "leave the draft alone" — the failure mode here must be
// "left something behind", never "attached it to the wrong agent", since a
// misplaced tool both hides from its owner and inflates the prompt of an agent
// that never asked for it.
func migrationTargetAgent(runningID, authoringID string) string {
	if authoringID != "" {
		return authoringID
	}
	return runningID
}

// loadToolToolDef builds the load_tool meta-tool. Custom (temp) tools
// that take arguments are presented to the LLM by name + description
// only (a prompt section), keeping their schemas out of the catalog.
// load_tool fetches a named custom tool's full schema, marks it loaded
// (so the DynamicTools feed surfaces it next round), and returns the
// parameter spec so the LLM can call it correctly.
func (t *chatTurn) loadToolToolDef(sess *ToolSession) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "load_tool",
			Description: "Load one or more of your custom tools so you can call them. Custom tools are listed by name + description under \"Your custom tools\" but their parameters aren't loaded until you call this. Pass ALL the tools you expect to need for the task in ONE call (names[]) — batching loads them in a single round instead of one round per tool. Returns their parameters and makes them callable on your next step. Only needed for custom tools shown as needing a load — built-in tools are always ready.",
			Parameters: map[string]ToolParam{
				"names": {Type: "array", Items: &ToolParam{Type: "string"}, Description: "Exact names of the custom tools to load (from the \"Your custom tools\" list). Pass every tool you anticipate needing — one or many."},
			},
			Required: nil, // validated in the handler (also tolerates a singular `name`)
			Caps:     nil, // control/meta — no side effects of its own
		},
		Handler: func(args map[string]any) (string, error) {
			// Collect from names[] plus a tolerated singular `name` (LLMs
			// fall back to the old singular form out of habit), dedupe.
			raw := stringSliceFromArgs(args, "names")
			if single := strings.TrimSpace(stringArg(args, "name")); single != "" {
				raw = append(raw, single)
			}
			seen := make(map[string]bool, len(raw))
			var want []string
			for _, n := range raw {
				if n = strings.TrimSpace(n); n != "" && !seen[n] {
					seen[n] = true
					want = append(want, n)
				}
			}
			if len(want) == 0 {
				return "", errors.New("pass at least one tool name in names[]")
			}
			// Partial success: load every valid name, bucket the rest, so
			// one bad name doesn't sink the batch or make the LLM loop.
			var loaded, already, unknown []string
			schemas := make([]map[string]any, 0, len(want))
			for _, n := range want {
				td, ok := t.lazyCustomToolDefs[n]
				if !ok {
					if t.staticTempToolNames[n] {
						already = append(already, n)
						continue
					}
					// Deferred authoring tool (see registerLazyAuthoringTools).
					// Checked before the persistent pool: these are framework tools,
					// not pool entries, so the pool lookup would miss and report the
					// very tool the prompt index told the model to load as unknown.
					// Handled entirely in the turn's own maps — no elevation
					// recording (that signal is for user pool tools, and scoping a
					// framework tool onto an agent is meaningless).
					if dtd, isDeferred := t.deferredAuthoringDefs[n]; isDeferred {
						t.deferredAuthoringLoaded[n] = true
						loaded = append(loaded, n)
						schemas = append(schemas, map[string]any{
							"name":        dtd.Tool.Name,
							"description": dtd.Tool.Description,
							"parameters":  dtd.Tool.Parameters,
						})
						continue
					}
					// On-demand from the persistent pool. Builder skips loading
					// user-authored persistent tools at session setup ("authors
					// fresh"), so a tool the "approved but not loaded" list shows
					// (and that tool_def get can read) isn't in lazyCustomToolDefs
					// — without this branch load_tool would reject the very tool
					// that list told the model to load, an observed dead-end loop.
					if def, ok := t.loadPersistentToolOnDemand(sess, n); ok {
						t.lazyCustomToolDefs[n] = def
						t.lazyCustomToolNames[n] = true
						td = def
					} else {
						unknown = append(unknown, n)
						continue
					}
				}
				t.loadedCustomTools[n] = true
				loaded = append(loaded, n)
				// Tier-2 elevation signal: an agent that loads the same tool
				// across enough distinct sessions is declaring its kit —
				// recorded per (agent, tool, session), promoted in
				// elevatedToolSet, surfaced as a scope suggestion.
				recordToolLoads(t.udb, t.agent.ID, sess.ChatSessionID, []string{n})
				schemas = append(schemas, map[string]any{
					"name":        td.Tool.Name,
					"description": td.Tool.Description,
					"parameters":  td.Tool.Parameters,
					"required":    td.Tool.Required,
				})
			}
			var b strings.Builder
			if len(loaded) > 0 {
				sb, _ := json.Marshal(schemas)
				fmt.Fprintf(&b, "Loaded %s — now callable. Schemas:\n%s\n", strings.Join(loaded, ", "), string(sb))
			}
			if len(already) > 0 {
				fmt.Fprintf(&b, "Already loaded (call directly): %s\n", strings.Join(already, ", "))
			}
			if len(unknown) > 0 {
				fmt.Fprintf(&b, "Unknown — check the \"Your custom tools\" list or call find_tools: %s\n", strings.Join(unknown, ", "))
			}
			return strings.TrimSpace(b.String()), nil
		},
	}
}

// loadPersistentToolOnDemand pulls a tool from the user's persistent pool
// into the live session on request, so load_tool can resolve a tool that
// wasn't pre-loaded at session setup (the Builder case: it deliberately
// doesn't auto-load user tools, but MUST be able to load one explicitly to
// inspect or test it). Appends it to sess.TempTools (so dynamicNewTempTools
// surfaces the wrapped, callable version next round) and returns the
// activity-wrapped def for the same-round lazyToolFallback path. Returns
// false when no persistent tool of that name exists.
func (t *chatTurn) loadPersistentToolOnDemand(sess *ToolSession, name string) (AgentToolDef, bool) {
	if sess == nil || t.udb == nil || t.user == "" {
		return AgentToolDef{}, false
	}
	var found *TempTool
	for _, p := range LoadPersistentTempTools(t.udb, t.user) {
		if p.Tool.Name == name {
			tt := p.Tool
			found = &tt
			break
		}
	}
	if found == nil {
		return AgentToolDef{}, false
	}
	if !sess.HasTempTool(name) {
		if err := sess.AppendTempTool(found); err != nil {
			Log("[orchestrate.tools] load_tool on-demand append %q failed: %v", name, err)
			return AgentToolDef{}, false
		}
	}
	// Build + activity-wrap the single def for the immediate fallback path;
	// the next round's dynamicNewTempTools rebuilds it the same way.
	for _, td := range temptool.BuildAgentToolDefs(sess) {
		if td.Tool.Name == name {
			wrapped := t.wrapToolsForActivity(sess, []AgentToolDef{td}, t.agent)
			if len(wrapped) > 0 {
				return wrapped[0], true
			}
			return td, true
		}
	}
	return AgentToolDef{}, false
}

// lazyToolFallback resolves a direct call to a lazy custom tool that
// isn't in this round's catalog. The model learned the tool's schema on a
// prior turn (via load_tool) and is now calling it straight from context,
// so forcing a re-load would be pointless friction — its schema is lazy
// (kept out of the LLM tool array to save tokens), but its handler is
// still valid. Returns the (already activity-wrapped) handler and marks
// the tool loaded so its schema also rejoins the catalog next round.
// Wired as the agent loop's ToolFallbackResolver.
func (t *chatTurn) lazyToolFallback(name string) (ToolHandlerFunc, bool) {
	if td, ok := t.lazyCustomToolDefs[name]; ok {
		t.loadedCustomTools[name] = true
		return td.Handler, true
	}
	// A deferred authoring tool called DIRECTLY, load_tool skipped. The index
	// asks for a load first, but a model acting on an index listing will often
	// just call the tool it read about — the same habit this fallback already
	// covers for lazy custom tools. Resolve the call and mark the tool loaded,
	// so its schema surfaces render-late on the following rounds; refusing here
	// would turn a working one-round authoring turn into an error plus a lecture
	// about load_tool.
	if td, ok := t.deferredAuthoringDefs[name]; ok {
		t.deferredAuthoringLoaded[name] = true
		return td.Handler, true
	}
	return nil, false
}

// dynamicTempTools returns a DynamicTools callback that exposes the
// session's loaded temp tools to the agent loop each round. Wraps
// each one through wrapToolsForActivity so they get the same inline
// tool_call / tool_result SSE emissions + activity-pane rendering +
// per-turn cache as the built-in tools — without this they fire
// invisibly and the user can't see the temp tool ran.
func (t *chatTurn) dynamicTempTools(sess *ToolSession) func() []AgentToolDef {
	return func() []AgentToolDef {
		defs := temptool.BuildAgentToolDefs(sess)
		t.wrapToolsForActivity(sess, defs, t.agent)
		return defs
	}
}

// attachDeliveredSkillTools loads the bundled Tools of any skill the LLM has
// consulted this turn (t.deliveredSkills) into the session pool, so a skill's
// own shipped scripts become callable once it's active — and never before that.
// Idempotent: a tool already present (persistent pool, agent kit, or a prior
// round) is skipped. Surfaced through the normal BuildAgentToolDefs feed, so
// the tools inherit private-mode filtering + the lazy/static split for free.
func (t *chatTurn) attachDeliveredSkillTools(sess *ToolSession) {
	// Shared with phantom via core.AttachDeliveredSkillTools so both surfaces
	// behave identically. Orchestrate's dynamicNewTempTools surfaces sess temp
	// tools itself (brand-new mid-turn tools fall through to a direct surface),
	// so we don't need the returned names here.
	AttachDeliveredSkillTools(sess, t.udb, t.user, t.deliveredSkills, t.privateMode)
}

// dynamicNewTempTools returns the agent loop's per-round dynamic
// tool feed: ONLY temp tools created mid-turn that weren't in the
// static snapshot. Persistent temp tools live in the static catalog;
// this surfaces freshly-authored ones so the LLM can dispatch by
// name to verify a new tool works before the user approves it.
//
// Replaced the old dynamicToolsWithGroups now that group-expansion
// is retired (vector pre-selection picks tools by relevance; no
// expand_tool_group meta-tool, no synthetic group placeholders).
func (t *chatTurn) dynamicNewTempTools(sess *ToolSession) func() []AgentToolDef {
	base := t.dynamicTempTools(sess)
	return func() []AgentToolDef {
		// Bundled skill tools: once a skill is consulted this turn, load its
		// shipped scripts into the session pool so they become callable. Polled
		// each round (cheap, idempotent) so a skill consulted mid-turn surfaces
		// its tools the next round, exactly like a freshly-authored temp tool.
		t.attachDeliveredSkillTools(sess)
		raw := base()
		out := make([]AgentToolDef, 0, len(raw))
		for _, td := range raw {
			if t.staticTempToolNames[td.Tool.Name] {
				continue // zero-arg custom already in the static catalog
			}
			// Lazy (has-args) custom tools stay name+desc-only until the
			// LLM loads them via load_tool — surface the full def only
			// once loaded, so the schema enters the catalog exactly when
			// it's needed. Brand-new mid-turn tools (in neither set) fall
			// through and surface directly, preserving the verify flow.
			if t.lazyCustomToolNames[td.Tool.Name] && !t.loadedCustomTools[td.Tool.Name] {
				continue
			}
			// A lazy custom tool that HAS been loaded this session: surface it,
			// but mark it render-late so the split chat template puts its schema
			// at the BOTTOM of the prompt (via chat_template_kwargs.lazy_tool_names)
			// instead of the top-of-prompt tools block. That keeps loading a tool
			// from invalidating the cached prefix (the ~13s load_tool cold-prefill).
			// Zero-arg static temp tools and the agent's own kit stay at the top.
			if t.lazyCustomToolNames[td.Tool.Name] {
				td.Tool.RenderLate = true
			}
			out = append(out, td)
		}
		// Deferred authoring tools the model loaded this turn — surfaced
		// render-late so their schemas land at the bottom of the prompt and
		// the cached prefix survives. Empty for every agent that isn't
		// Author-flagged, and for Author-flagged turns that never loaded one.
		out = append(out, t.loadedDeferredAuthoringTools()...)
		// Source-hook dispatcher: ONE query_source tool over all exposed
		// hooks (the agents pattern) instead of N per-hook tools — see
		// RenderAvailableSourcesBlock for the shown "Available sources"
		// menu that carries each source's "use when". Walked per-round so
		// admin add/remove/toggle takes effect without a restart; cheap
		// (reads the in-memory sourceHookRegistry). Skills still grant a
		// SPECIFIC hook as a focused per-hook tool via the skill path.
		if qs, ok := QuerySourceToolDef(t.app.DB); ok {
			out = append(out, qs)
		}
		// (No skill-granted tools here anymore. Experts run as dispatched
		// use_expert workers with their own catalog; behavior skills only
		// inject instructions. Neither puts tools in the MAIN catalog.)
		// Private-mode backstop. The dynamic feed re-introduces tools
		// that the static catalog filter already dropped:
		//   - mid-turn temp tools (LLM authored after runPlan started)
		//   - source-hook auto-tools (RSS / API readers — network by definition)
		// Without this pass, a Private turn gets network tools handed
		// back round-by-round even though the opening catalog was clean.
		// Mirrors the static-catalog filter in runPlan.
		if t.privateMode {
			filtered := out[:0]
			for _, td := range out {
				hasNet := false
				for _, c := range td.Tool.Caps {
					if c == CapNetwork {
						hasNet = true
						break
					}
				}
				if hasNet {
					continue
				}
				filtered = append(filtered, td)
			}
			out = filtered
		}
		return out
	}
}

// uploadedImageHandles tells the model how to REFER to the pictures it can see.
// Deliberately not the channel manifest: that one is about a group thread
// (don't echo a photo back to the people who just posted it), which is the
// wrong advice here and would suppress exactly the delivery the user wants.
func uploadedImageHandles(n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[the user attached ")
	if n == 1 {
		b.WriteString("this image, and you can see it above. Refer to it as media#1")
	} else {
		fmt.Fprintf(&b, "these %d images, and you can see them above. Refer to them as media#1 through media#%d, in the order they were sent", n, n)
	}
	b.WriteString(". Pass those ids to the image tool to edit, blend, or combine them — do NOT invent a filename, and do NOT substitute a picture you made earlier.]")
	return b.String()
}

// decodeUserImages turns the base64 strings the chat panel ships in
// the send body into raw bytes for the Message.Images field. Failures
// are logged and skipped — a corrupt image shouldn't kill the turn.
func decodeUserImages(b64s []string) [][]byte {
	if len(b64s) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(b64s))
	for i, s := range b64s {
		if s == "" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			Log("[orchestrate.images] decode failed for attachment %d: %v", i, err)
			continue
		}
		out = append(out, b)
	}
	return out
}

// setCurrentMsgID records which assistant bubble the wrap-emit
// helpers should target for tool_call/tool_result SSE events.
// Concurrent-safe so a tool call firing during a stream chunk
// can't race with the streamHandler that sets the id.
func (t *chatTurn) setCurrentMsgID(id string) {
	t.currentMu.Lock()
	t.currentMsgID = id
	t.currentMu.Unlock()
}

// getCurrentMsgID returns the active bubble id (may be empty).
func (t *chatTurn) getCurrentMsgID() string {
	t.currentMu.Lock()
	id := t.currentMsgID
	t.currentMu.Unlock()
	return id
}
