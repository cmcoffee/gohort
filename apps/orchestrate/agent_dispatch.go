// dispatch_to_agent — the LLM-facing tool that lets one orchestrate
// agent call another by name and get its synthesis back as the tool
// result. Turns the agent fleet into a service mesh: a generalist
// agent can fan out to specialists ("Research", "Resume Reviewer",
// "Code Reviewer") instead of trying to do every job itself, and
// pipeline tools can chain multiple sub-agents inside a single
// composed flow.
//
// Mechanics: the call resolves the target agent in the same user's
// store, builds a worker-tier sub-loop with the TARGET agent's
// orchestrator_prompt + memory + facts + allowed tools, runs to
// completion, and returns the final text. The calling agent sees
// the result as a normal tool output and continues its own turn.
//
// Recursion guard: dispatchDepth on chatTurn is capped by
// maxDispatchDepth so A→B→A or transitively-cyclic chains can't
// run away. Distinct from pipelineDepth (pipeline-mode temp tools)
// — the two surfaces share a sub-loop runner but track their own
// counters because their failure modes differ.

package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// maxDispatchDepth caps recursive agent dispatch. 3 levels covers
// realistic mesh patterns (router → specialist → helper) without
// letting a misconfigured fleet thrash.
const maxDispatchDepth = 3

// AgentsForUser returns the agent records visible to the given user
// (their own customizations + un-shadowed seeds). Exposed for other
// apps (e.g. Phantom's dispatch_agent picker) that need to enumerate
// an admin's agent fleet to build a UI or validate an allowlist.
func (T *OrchestrateApp) AgentsForUser(user string) []AgentRecord {
	if T == nil || T.DB == nil || user == "" {
		return nil
	}
	return listAgents(UserDB(T.DB, user), user)
}

// SaveAgentForUser writes an AgentRecord into the user's orchestrate
// store and returns the assigned ID. Used by external authoring
// surfaces (Builder) that compose the record from a form instead of
// inline via create_agent.
//
// The Owner field is forced to the passed user — callers can't
// implant records under another user's name. ID is reset before save
// so the storage layer mints a fresh one.
func (T *OrchestrateApp) SaveAgentForUser(user string, rec AgentRecord) (string, error) {
	if T == nil || T.DB == nil {
		return "", errors.New("orchestrate runtime not initialized")
	}
	if user == "" {
		return "", errors.New("user is required")
	}
	udb := UserDB(T.DB, user)
	if udb == nil {
		return "", fmt.Errorf("no per-user db for %q", user)
	}
	rec.ID = ""
	rec.Owner = user
	saved, err := saveAgent(udb, rec)
	if err != nil {
		return "", err
	}
	return saved.ID, nil
}

// RunAgentSync runs the named agent against a single user message
// and returns the synthesized reply. Exposed for OTHER apps (e.g.
// Phantom) that want to delegate work into an orchestrate agent and
// block on the result.
//
// Two identities are passed for a reason:
//
//   - agentOwner is the gohort user whose agent store contains the
//     TARGET RECORD (the persona, allowed_tools, etc. an admin built
//     in Agency). Typically the deployment owner.
//   - runtimeUser is the identity the SUB-AGENT RUNS AS. Its memory,
//     facts, knowledge, session temp tools, and workspace land under
//     this name. Use a synthetic per-context value (e.g.
//     "phantom:<chat_id>") so each caller's dispatch state stays
//     isolated from the agent owner's interactive use of the same
//     agent. Pass agentOwner here too if you intentionally want
//     shared state.
//
// agentKey can be an agent ID or a case-insensitive name match
// against agentOwner's store. Sub-session is torn down on return so
// transient state (authoring focus, session temp tools) doesn't
// leak.
// applyForcePrivateToDispatch enforces target.ForcePrivate on a
// dispatched run — RunAgentSync / RunAgentSyncContinuing / phantom's
// dispatch_agent / agents(action="run") all funnel through this so a
// privacy-locked agent stays locked regardless of how it was invoked.
//
// Without this, the direct-chat path read ForcePrivate and built a
// blocked NetworkConnector + filtered CapNetwork tools (runner.go's
// privateMode branch), but the dispatch paths built their sub-session
// with sess.Network = nil and shipped every tool through — so a
// phantom-dispatched compliance-locked agent reached the internet
// freely. Three things happen here when ForcePrivate is on:
//
//   1. ctx gets a blocked NetworkConnector attached so any callee
//      that gates on NetworkAllowedFromContext (sandbox shell hook,
//      direct HTTP helpers) sees the block.
//   2. subSess.Network points at the same connector so tools that
//      gate on ToolSession.NetworkAllowed() (web_search, fetch_url,
//      browse_page) refuse the call up front.
//   3. CapNetwork-tagged AgentToolDefs are removed from the catalog
//      so the LLM never sees web_search / fetch_url / browse_page /
//      etc. in the first place — the cleanest signal that this turn
//      runs offline.
//
// No-op when ForcePrivate is false. Returns ctx + the (possibly
// filtered) tool slice so the caller can replace its local references.
func applyForcePrivateToDispatch(ctx context.Context, subSess *ToolSession, tools []AgentToolDef, target AgentRecord) (context.Context, []AgentToolDef) {
	if !target.ForcePrivate {
		return ctx, tools
	}
	connector := NewNetworkConnector(true)
	ctx = WithNetworkConnector(ctx, connector)
	if subSess != nil {
		subSess.Network = connector
	}
	filtered := tools[:0]
	dropped := []string{}
	for _, td := range tools {
		hasNet := false
		for _, c := range td.Tool.Caps {
			if c == CapNetwork {
				hasNet = true
				break
			}
		}
		if hasNet {
			dropped = append(dropped, td.Tool.Name)
			continue
		}
		filtered = append(filtered, td)
	}
	if len(dropped) > 0 {
		Log("[orchestrate.dispatch] ForcePrivate active on %s — dropped %d network-capable tool(s): %v",
			target.ID, len(dropped), dropped)
	}
	return ctx, filtered
}

// buildDispatchTurnExtras assembles the per-turn closure tools and the
// "Available agents" prompt block that the target agent needs to behave
// the same way it would on its own chat surface — knowledge_search,
// memory_*, agents grouped tool (so it can dispatch to sub-agents),
// activate_skill — plus the fleet-awareness block so the LLM knows
// there's a fleet to delegate to.
//
// Without this, an agent dispatched via phantom / external callers ran
// with only its bare allowed_tools and no peer awareness — the fleet
// it could reach from the Agency chat UI was invisible. Both
// RunAgentSync and RunAgentSyncContinuing call this so the experience
// matches across surfaces.
func (T *OrchestrateApp) buildDispatchTurnExtras(ctx context.Context, target AgentRecord, runtimeUser string, runtimeDB Database, subSess *ToolSession) (extraTools []AgentToolDef, availableBlock string) {
	return T.buildDispatchTurnExtrasWithOwner(ctx, target, runtimeUser, runtimeDB, subSess, "", nil)
}

// buildDispatchTurnExtrasWithOwner is the underlying builder. When
// ownerUser / ownerDB are non-empty, the dispatched subTurn's fleet
// view (renderAvailableAgentsBlock + agents(action="run") dispatch
// resolution) reads from the OWNER's per-user DB instead of the
// runtime user's. Phantom passes ownerUser=<agentOwner>, ownerDB=
// UserDB(T.DB, agentOwner) so dispatched agents see and can reach
// their authored sub-agent fleet — without this, phantom-dispatched
// OSINT couldn't find "OSINT Family Tracker" etc. because the fleet
// read hit phantom:<chatID>'s empty DB. Sessions / memory / facts
// remain on the runtime user's DB regardless.
func (T *OrchestrateApp) buildDispatchTurnExtrasWithOwner(ctx context.Context, target AgentRecord, runtimeUser string, runtimeDB Database, subSess *ToolSession, ownerUser string, ownerDB Database) (extraTools []AgentToolDef, availableBlock string) {
	// Phantom-dispatched runs accumulate Reference Memory and
	// Explicit facts under the synthetic per-chat user
	// (phantom:<chatID>) — a namespace the agent's owner can't see
	// from Agency (memory queries scope to the LOGGED-IN user). That
	// hidden state then feeds memory_search on the NEXT dispatch
	// from the same chat, so a wrong derived chunk compounds across
	// calls (same self-contamination loop we patched in phantom's
	// auto-inject layer). Force both layers off for phantom runs —
	// Knowledge (read-only uploads) stays available, Session
	// continuity within one phantom chat stays available, but neither
	// the memory_save / memory_search path nor the store_fact /
	// list_facts path is offered to the LLM. Stops new contamination
	// at the source; the phantom-memory surface (handlers below) lets
	// the operator wipe whatever has already accumulated.
	if strings.HasPrefix(runtimeUser, "phantom:") {
		target.DisableInferred = true
		target.DisableExplicit = true
	}
	subTurn := &chatTurn{
		app:       T,
		agent:     target,
		user:      runtimeUser,
		udb:       runtimeDB,
		ctx:       ctx,
		topic:     generalTopic,
		ownerUser: ownerUser,
		ownerDB:   ownerDB,
	}
	extraTools = append(extraTools, subTurn.searchKnowledgeToolDef())
	if !subTurn.inferredOff() {
		extraTools = append(extraTools, subTurn.memoryToolDef())
	}
	if !subTurn.explicitOff() {
		extraTools = append(extraTools,
			subTurn.storeFactToolDef(),
			subTurn.forgetFactToolDef(),
		)
	}
	// agents grouped tool — sub-agents (OwnedBy set) are LEAVES in the
	// dispatch tree: their job is one focused capability, return result,
	// done. Stripping the agents tool from sub-agents eliminates depth-
	// limit cascades (parent → sub → sub-sub-…) and forces hierarchical
	// composition: if a workflow needs two specialists, they become
	// siblings under the parent, not chained sub-agents. Builder targets
	// stay read-only on dispatch (no recursive Builder-from-Builder).
	// Top-level targets get the full grouped surface so they can reach
	// their own sub-agents the same way they would from the Agency chat.
	if target.OwnedBy == "" {
		extraTools = append(extraTools, subTurn.agentsGroupedToolDef(!isBuilderAgent(target.ID)))
	}
	if !target.DisableSkills {
		extraTools = append(extraTools, subTurn.activateSkillToolDef(), subTurn.deactivateSkillToolDef())
	}
	// Sub-agents also skip the Available agents block — no point
	// telling a leaf about fleet peers it can't dispatch to. Saves
	// tokens AND removes the "DELEGATE FIRST" nudge that would
	// otherwise contradict the missing tool.
	if target.OwnedBy == "" {
		availableBlock = subTurn.renderAvailableAgentsBlock()
	}
	// "Available skills" block — same parity issue as agents. The
	// dispatch path adds activate_skill to the tool catalog when the
	// agent has skills enabled, but without this block the LLM has
	// no idea which skills it can invoke. That bit a phantom-
	// dispatched agent whose network capability came via a Skill's
	// AllowedTools: the LLM "knew" it had activate_skill but couldn't
	// see fetch_url was reachable through it, so it hallucinated
	// that the network was unavailable. Always emit alongside
	// activate_skill so the tool and its catalog stay in sync.
	availableBlock += subTurn.renderAvailableSkillsBlock()
	// "Known topics" block — surfaces the (user, agent) topic
	// accumulator so memory_save / memory_search reuse existing
	// snake_case slugs instead of minting near-duplicates. Cheap to
	// add; matches what the direct path does.
	availableBlock += subTurn.renderKnownTopicsBlock()
	return extraTools, availableBlock
}

func (T *OrchestrateApp) RunAgentSync(ctx context.Context, agentOwner, runtimeUser, agentKey, message string) (string, error) {
	if T == nil || T.LLM == nil {
		return "", errors.New("orchestrate runtime not initialized")
	}
	if agentOwner == "" {
		return "", errors.New("agentOwner is required")
	}
	if runtimeUser == "" {
		runtimeUser = agentOwner
	}
	if strings.TrimSpace(message) == "" {
		return "", errors.New("message is required")
	}
	ownerDB := UserDB(T.DB, agentOwner)
	if ownerDB == nil {
		return "", fmt.Errorf("no per-user db for agentOwner %q", agentOwner)
	}
	target, ok := findAgentByNameOrID(ownerDB, agentOwner, agentKey)
	if !ok {
		return "", fmt.Errorf("agent %q not found in agentOwner %q store", agentKey, agentOwner)
	}
	runtimeDB := ownerDB
	if runtimeUser != agentOwner {
		runtimeDB = UserDB(T.DB, runtimeUser)
		if runtimeDB == nil {
			return "", fmt.Errorf("no per-user db for runtimeUser %q", runtimeUser)
		}
	}
	subSessID := "external-dispatch:" + runtimeUser + ":" + target.ID
	subSess := &ToolSession{
		LLM:           T.LLM,
		LeadLLM:       T.LeadLLM,
		Username:      runtimeUser,
		DB:            runtimeDB,
		ChatSessionID: subSessID,
	}
	if ws, werr := EnsureWorkspaceDir(runtimeUser); werr == nil {
		subSess.WorkspaceDir = ws
	}
	defer clearAuthoringInProgress(runtimeDB, subSessID)
	defer DeleteSessionTempTools(runtimeDB, subSessID)
	toolNames := target.AllowedTools
	if len(toolNames) == 0 {
		for _, td := range RegisteredChatTools() {
			toolNames = append(toolNames, td.Name())
		}
	}
	tools, err := GetAgentToolsWithSession(subSess, toolNames...)
	if err != nil {
		tools = nil
		for _, n := range toolNames {
			if td, terr := GetAgentToolsWithSession(subSess, n); terr == nil && len(td) > 0 {
				tools = append(tools, td[0])
			}
		}
	}
	// Builder gets its unregistered authoring tools appended here on
	// the sync-dispatch path. Identity-gated against the Builder
	// seed ID — a non-Builder target never receives the appendage.
	if isBuilderAgent(target.ID) {
		tools = append(tools, builderAuthoringTools(subSess)...)
	}
	// Phantom dispatches: pin the local target's posture flags AND
	// skip the facts-load so prependAgentContext can't inject any
	// pre-existing phantom-side facts into the prompt. See the
	// matching block in RunAgentSyncContinuing for the full rationale.
	isPhantomDispatch := strings.HasPrefix(runtimeUser, "phantom:")
	if isPhantomDispatch {
		target.DisableInferred = true
		target.DisableExplicit = true
	}
	// Per-turn closure tools (knowledge_search, memory_*, agents,
	// activate_skill) + Available agents block. Without these, the
	// dispatched agent couldn't reach its own knowledge / memory /
	// peer fleet — the exact gap that hid sub-agents from phantom-
	// dispatched runs even though they were reachable from the
	// Agency chat UI.
	extraTools, availableBlock := T.buildDispatchTurnExtrasWithOwner(ctx, target, runtimeUser, runtimeDB, subSess, agentOwner, ownerDB)
	tools = append(tools, extraTools...)
	// ForcePrivate enforcement — drop network tools + attach blocked
	// connector. Done AFTER tools are fully assembled (allowlist +
	// dispatch extras) so the filter sees everything that would have
	// reached the LLM and removes the network-capable subset in one
	// pass. No-op when ForcePrivate is false.
	ctx, tools = applyForcePrivateToDispatch(ctx, subSess, tools, target)
	// Facts read from the RUNTIME user's DB, so dispatches from
	// different phantom chats see isolated state for the same agent
	// record. First call against a fresh runtimeUser starts clean —
	// no leakage from the owner's interactive Explicit Memory.
	// Skipped for phantom runs (no facts in prompt at all).
	var subFacts []MemoryFact
	if !isPhantomDispatch {
		subFacts = ListMemoryFacts(runtimeDB, factsNamespace(target.ID))
	}
	sysPrompt := prependAgentContext(target.OrchestratorPrompt, target, subFacts)
	sysPrompt += availableBlock
	// Mark delegated invocations so the target agent knows there's no
	// human on the other end of ask_user / ask_user_form. Builder
	// keys off this marker to skip Phase 1 conversation + the Phase 3
	// confirmation pause; other agents can ignore it.
	deliveredMessage := markAsDelegated(message)
	// Resolve thinking the same way the chat surface does so a
	// dispatched agent runs with the SAME default as if it were
	// invoked directly from Agency. Base = route default. Target's
	// explicit Think="on"/"off" wins; empty Think falls through to
	// the route default rather than getting a different "dispatch-
	// only" default.
	think := true
	if p := RouteThink("app.orchestrate.orchestrator"); p != nil {
		think = *p
	}
	switch target.Think {
	case "on":
		think = true
	case "off":
		think = false
	}
	// Telemetry — each RunAgentSync invocation gets its own per-turn
	// accumulator so pipeline agent stages, external dispatches, and
	// any other sync sub-agent run leaves a grep-able forensic record
	// in the log. Without this, pipeline-internal tool calls are a
	// black box from the parent's perspective.
	telem := newTurnTelemetry()
	resp, _, runErr := T.RunAgentLoop(ctx, []Message{{Role: "user", Content: deliveredMessage}}, AgentLoopConfig{
		SystemPrompt: sysPrompt,
		Tools:        tools,
		MaxRounds:    resolveMaxWorkerRounds(target),
		ThinkBudget:  target.ThinkBudget, // per-agent override; 0 = inherit route/global
		OnStep:       func(info StepInfo) { telem.record(info) },
		Confirm:      func(name, args string) bool { return true },
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(think),
		},
	})
	Log("[orchestrate.RunAgentSync] owner=%s runtime=%s target=%s msg_chars=%d err=%v",
		agentOwner, runtimeUser, target.ID, len(message), runErr)
	// Per-sub-agent telemetry summary — same shape as the orchestrator
	// and worker step summaries so the same greps work uniformly.
	softCap := resolveMaxWorkerRounds(target)
	outLen := 0
	if resp != nil {
		outLen = len(resp.Content)
	}
	exitReason := classifyWorkerExit(runErr, telem.rounds, softCap, outLen)
	label := fmt.Sprintf("orchestrate.sub-agent agent=%s", target.Name)
	Log("%s", telem.summary(label, softCap, softCap, exitReason))
	if line := telem.toolCallSummary(label); line != "" {
		Log("%s", line)
	}
	if runErr != nil {
		return "", runErr
	}
	if resp == nil {
		return "", errors.New("agent returned no response")
	}
	return strings.TrimSpace(resp.Content), nil
}

// RunAgentSyncContinuing is RunAgentSync's continuation variant —
// loads prior messages from the named sub-session before running so
// the target picks up where it left off, and persists the new
// (user, assistant) exchange back so subsequent calls see it too.
//
// Used by callers that promote a previously-dispatched agent into
// a side-conversation (phantom's processMessage promotion path,
// orchestrate's handleSend promotion path). The subSessionID picks
// the storage slot; pass the same ID across promotion turns to get
// continuity, or a fresh ID for an isolated thread.
//
// Optional injectionQueueID — when non-empty, the agent loop drains
// the named injection queue between rounds (mid-flight user notes
// arriving while this dispatch is in flight). Pass "" to disable.
//
// freshSession=true wipes the prior session at the deterministic ID
// before loading — the new dispatch runs without any inherited
// history. Used by callers (phantom's dispatch_agent fresh_session
// flag) that have semantic evidence the user is on a new thread and
// don't want compounding context-contamination from accumulated old
// turns. The wipe is irreversible — older turns are gone, not just
// hidden from the LLM. Default false preserves the continuity model.
func (T *OrchestrateApp) RunAgentSyncContinuing(ctx context.Context, agentOwner, runtimeUser, agentKey, subSessionID, injectionQueueID, message string, freshSession bool) (string, error) {
	if T == nil || T.LLM == nil {
		return "", errors.New("orchestrate runtime not initialized")
	}
	if agentOwner == "" {
		return "", errors.New("agentOwner is required")
	}
	if runtimeUser == "" {
		runtimeUser = agentOwner
	}
	if strings.TrimSpace(message) == "" {
		return "", errors.New("message is required")
	}
	ownerDB := UserDB(T.DB, agentOwner)
	if ownerDB == nil {
		return "", fmt.Errorf("no per-user db for agentOwner %q", agentOwner)
	}
	target, ok := findAgentByNameOrID(ownerDB, agentOwner, agentKey)
	if !ok {
		return "", fmt.Errorf("agent %q not found in agentOwner %q store", agentKey, agentOwner)
	}
	runtimeDB := ownerDB
	if runtimeUser != agentOwner {
		runtimeDB = UserDB(T.DB, runtimeUser)
		if runtimeDB == nil {
			return "", fmt.Errorf("no per-user db for runtimeUser %q", runtimeUser)
		}
	}
	if subSessionID == "" {
		subSessionID = "external-dispatch:" + runtimeUser + ":" + target.ID
	}
	subSess := &ToolSession{
		LLM:           T.LLM,
		LeadLLM:       T.LeadLLM,
		Username:      runtimeUser,
		DB:            runtimeDB,
		ChatSessionID: subSessionID,
	}
	if ws, werr := EnsureWorkspaceDir(runtimeUser); werr == nil {
		subSess.WorkspaceDir = ws
	}
	defer clearAuthoringInProgress(runtimeDB, subSessionID)
	defer DeleteSessionTempTools(runtimeDB, subSessionID)
	toolNames := target.AllowedTools
	if len(toolNames) == 0 {
		for _, td := range RegisteredChatTools() {
			toolNames = append(toolNames, td.Name())
		}
	}
	tools, err := GetAgentToolsWithSession(subSess, toolNames...)
	if err != nil {
		tools = nil
		for _, n := range toolNames {
			if td, terr := GetAgentToolsWithSession(subSess, n); terr == nil && len(td) > 0 {
				tools = append(tools, td[0])
			}
		}
	}
	if isBuilderAgent(target.ID) {
		tools = append(tools, builderAuthoringTools(subSess)...)
	}
	// Phantom dispatches: force the sub-agent posture (no memory layer,
	// no facts layer) on the LOCAL target so prependAgentContext below
	// doesn't accidentally inject pre-existing phantom-side facts into
	// the prompt. The earlier in-function flip inside
	// buildDispatchTurnExtras only affected its local copy; this one
	// shapes the outer flow. ListMemoryFacts also gets skipped — even
	// if facts exist under phantom:<chatID>'s namespace from before
	// these guards landed, they do NOT inject into the dispatched
	// LLM's prompt. Knowledge (read-only uploads) and session
	// continuity remain controllable by the LLM via fresh_session.
	isPhantomDispatch := strings.HasPrefix(runtimeUser, "phantom:")
	if isPhantomDispatch {
		target.DisableInferred = true
		target.DisableExplicit = true
	}
	// Per-turn closure tools + Available agents block (mirrors
	// RunAgentSync — see buildDispatchTurnExtras for rationale).
	extraTools, availableBlock := T.buildDispatchTurnExtrasWithOwner(ctx, target, runtimeUser, runtimeDB, subSess, agentOwner, ownerDB)
	tools = append(tools, extraTools...)
	// ForcePrivate enforcement — see applyForcePrivateToDispatch.
	// No-op when target.ForcePrivate is false.
	ctx, tools = applyForcePrivateToDispatch(ctx, subSess, tools, target)
	var subFacts []MemoryFact
	if !isPhantomDispatch {
		subFacts = ListMemoryFacts(runtimeDB, factsNamespace(target.ID))
	}
	sysPrompt := prependAgentContext(target.OrchestratorPrompt, target, subFacts)
	sysPrompt += availableBlock
	// freshSession wipes the prior session BEFORE the load — caller
	// (phantom's dispatch_agent fresh_session=true) is signaling a
	// new thread, so the deterministic-ID session record gets cleared
	// and the dispatch runs as if it's the first ever. The wipe is
	// irreversible; older turns are not preserved elsewhere.
	if freshSession {
		deleteChatSession(runtimeDB, target.ID, subSessionID)
		Log("[orchestrate.RunAgentSyncContinuing] fresh_session wipe — runtime=%s target=%s sub=%s",
			runtimeUser, target.ID, subSessionID)
	}
	// Load prior session (if any) and build history.
	priorSession, _ := loadChatSession(runtimeDB, target.ID, subSessionID)
	if priorSession.ID == "" {
		priorSession.ID = subSessionID
		priorSession.AgentID = target.ID
		priorSession.Created = time.Now()
	}
	deliveredMessage := markAsDelegated(message)
	llmMessages := make([]Message, 0, len(priorSession.Messages)+1)
	for _, m := range priorSession.Messages {
		llmMessages = append(llmMessages, Message{Role: m.Role, Content: m.Content})
	}
	llmMessages = append(llmMessages, Message{Role: "user", Content: deliveredMessage})

	// Optional injection-queue drain hook for mid-flight user notes.
	// Cheap no-op when the queue isn't registered.
	var onRoundStart func() []Message
	if injectionQueueID != "" {
		onRoundStart = func() []Message {
			q := LookupInjectionQueue(injectionQueueID)
			if q == nil {
				return nil
			}
			drained := q.Drain()
			if len(drained) == 0 {
				return nil
			}
			out := make([]Message, 0, len(drained))
			for _, n := range drained {
				out = append(out, Message{
					Role:    "user",
					Content: "[MID-FLIGHT NOTE — submitted by the user while this run was in progress] " + n.Text,
				})
			}
			return out
		}
	}

	// Resolve thinking the same way the chat surface does — see
	// RunAgentSync above for the rationale. Empty Think falls through
	// to the route default, NOT a dispatch-specific override.
	think := true
	if p := RouteThink("app.orchestrate.orchestrator"); p != nil {
		think = *p
	}
	switch target.Think {
	case "on":
		think = true
	case "off":
		think = false
	}
	resp, _, runErr := T.RunAgentLoop(ctx, llmMessages, AgentLoopConfig{
		SystemPrompt: sysPrompt,
		Tools:        tools,
		MaxRounds:    resolveMaxWorkerRounds(target),
		ThinkBudget:  target.ThinkBudget, // per-agent override; 0 = inherit route/global
		Confirm:      func(name, args string) bool { return true },
		// InjectionDrain (not OnRoundStart): the queue-drain closure
		// returns nil when empty, so the loop's pre-finalize re-call
		// terminates. A mid-flight note pushed while this dispatch runs
		// gets picked up at the next round AND right before finalizing.
		InjectionDrain: onRoundStart,
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(think),
		},
	})
	Log("[orchestrate.RunAgentSyncContinuing] owner=%s runtime=%s target=%s sub=%s prior_msgs=%d msg_chars=%d err=%v",
		agentOwner, runtimeUser, target.ID, subSessionID, len(priorSession.Messages), len(message), runErr)
	if runErr != nil {
		return "", runErr
	}
	if resp == nil {
		return "", errors.New("agent returned no response")
	}
	cleanReply := strings.TrimSpace(resp.Content)
	// Persist the new exchange for the next continuation.
	now := time.Now()
	priorSession.Messages = append(priorSession.Messages,
		ChatMessage{Role: "user", Content: deliveredMessage, Created: now},
		ChatMessage{Role: "assistant", Content: cleanReply, Created: now},
	)
	if _, serr := saveChatSession(runtimeDB, priorSession); serr != nil {
		Log("[orchestrate.RunAgentSyncContinuing] WARN failed to persist sub-session %s: %v", subSessionID, serr)
	}
	return cleanReply, nil
}

// markAsDelegated wraps an incoming user message with a delegated-
// invocation marker that agents (notably Builder) read to suppress
// conversational behavior — ask_user / ask_user_form pauses,
// multi-step intake, approval pauses. There's no human listening
// on a delegated dispatch; the target needs to work from the brief
// alone and produce its result.
//
// The marker is a single bracketed line + a one-line guidance, then
// the original brief verbatim. The LLM treats the whole block as
// the user's message but can pattern-match on the marker to adjust
// behavior. Agents that don't care about delegation context can
// ignore the marker — the brief still reads naturally below.
func markAsDelegated(msg string) string {
	return "[DELEGATED INVOCATION] No human is listening for ask_user — work from the brief below as a self-contained spec. Make reasonable defaults for anything unspecified. Skip intake conversation + approval pauses; go directly to execution.\n\nBrief: " + msg
}

// findAgentByNameOrID looks up an agent in udb either by exact ID
// match (preferred — stable across renames) or by case-insensitive
// name match. Returns the agent + a bool indicating found. Used
// only by the dispatch tool; the rest of orchestrate addresses
// agents by ID.
func findAgentByNameOrID(udb Database, owner, key string) (AgentRecord, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return AgentRecord{}, false
	}
	if a, ok := loadAgent(udb, key); ok {
		return a, true
	}
	low := strings.ToLower(key)
	for _, a := range listAgents(udb, owner) {
		if strings.ToLower(a.Name) == low {
			return a, true
		}
	}
	return AgentRecord{}, false
}

// dispatchToAgentToolDef removed — the LLM-facing dispatch surface
// now lives on the grouped `agents` tool (action="run") in
// agents_grouped_tool.go. RunAgentSync above is the cross-app
// dispatch path (Phantom → Agency); the in-LLM path is the agents
// tool's run action. Both share the same plumbing (delegated marker,
// Builder-exclusivity gate, sub-session setup, target memory/facts
// loading).
