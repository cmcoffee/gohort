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
// Recursion guard: chatTurn.dispatchHops is capped by maxDispatchDepth
// so A→B→A or transitively-cyclic chains can't run away. Distinct from
// pipelineDepth (pipeline-mode temp tools) — the two surfaces share a
// sub-loop runner but track their own counters because their failure
// modes differ.

package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// maxDispatchDepth caps recursive agent dispatch. 3 levels covers
// realistic mesh patterns (router → specialist → helper) without
// letting a misconfigured fleet thrash.
const maxDispatchDepth = 3

// dispatchHops is how far down a dispatch chain this turn already sits: 0 for
// a turn a human (or a schedule, or a monitor wake) started, 1 for the agent
// it dispatched to, and so on. Compared against maxDispatchDepth by all three
// run targets.
//
// Read off dispatchChain rather than a counter of its own. A counter measured
// the wrong thing twice over. It lived on chatTurn and was never copied into
// the sub-turn, so it RESET at every hop — the cap it was checked against was
// unreachable, and the comments on dispatchChain below already say so. And
// because it incremented for the duration of a call rather than for the depth
// of a chain, N sibling dispatches in flight at once read as recursion depth
// N: with the batch fanned out, call N+1 would fail "depth limit exceeded" at
// a true depth of 1. dispatchChain has neither problem — it is built once per
// sub-turn, carried unchanged, and never mutated by a call in flight.
func (t *chatTurn) dispatchHops() int {
	if t == nil {
		return 0
	}
	return len(t.dispatchChain)
}

// maxSameTargetDispatch caps how many times ONE user turn may dispatch the
// IDENTICAL call — same target agent AND same message — before it's treated as
// a loop: the model "answers and runs the app" over and over with no new input.
// It is keyed on target+message, NOT target alone: dispatching one agent with
// several DIFFERENT messages in a turn (e.g. the Builder verifying an agent by
// exercising each of its tools — profile, then post, then feed) is real
// progress, not a loop, and must not trip this. Enforced in agentsRunAction.
const maxSameTargetDispatch = 3

// maxTotalTargetDispatch is the anti-thrash ceiling on the TOTAL number of
// dispatches to one target in a single turn, across varying messages. It sits
// well above maxSameTargetDispatch because exercising several of a sub-agent's
// tools in one turn is legitimate; only an outsized volume signals a runaway.
const maxTotalTargetDispatch = 12

// maxBuilderTargetDispatch is the anti-thrash ceiling when the DISPATCHER is the
// Builder. Verifying an authored agent means driving each of its tools/actions
// (often with a retry or two), so Builder needs generous headroom — a full
// toolbox sweep can easily be 8-20 distinct dispatches — before the ceiling
// bites. The identical-call loop cap above still applies unchanged.
const maxBuilderTargetDispatch = 40

// dispatchCap is the turn-level entry to dispatchCapDecision: it holds
// dispatchMu across the whole decision and mints the map on first use, so the
// three run targets share one counter set safely.
//
// The lock is not optional. Every branch of the decision is a
// read-modify-write on a plain map, and with sibling dispatches fanned out
// across a batch the increments race: concurrent writes to the map itself, and
// a cap check where N callers can all read the same pre-increment value and
// sail past the ceiling together. Taken here rather than inside
// dispatchCapDecision so the counter map stays a plain argument and the
// decision stays testable without a turn.
func (t *chatTurn) dispatchCap(capKey, targetName, msg string) string {
	t.dispatchMu.Lock()
	defer t.dispatchMu.Unlock()
	if t.agentDispatchCounts == nil {
		t.agentDispatchCounts = map[string]int{}
	}
	return dispatchCapDecision(t.agentDispatchCounts, capKey, targetName, msg, isBuilderAgent(t.agent.ID))
}

// dispatchCapDecision applies the two per-turn dispatch caps against the
// running per-turn counts and returns a non-empty block message when one is
// hit (empty string = allowed). It mutates counts (incrementing the loop and
// total counters) and is pure over its inputs otherwise, so the cap contract
// can be unit-tested without a live sub-agent dispatch:
//   - LOOP: the IDENTICAL call (same target AND same message) past
//     maxSameTargetDispatch. Keyed on target+message so distinct messages to
//     one target — legitimate verification — never collide.
//   - THRASH: the TOTAL dispatches to one target past the ceiling
//     (maxBuilderTargetDispatch when the dispatcher is Builder, else
//     maxTotalTargetDispatch), regardless of message.
//
// Callers on a turn go through chatTurn.dispatchCap, which holds the lock the
// mutation needs.

func dispatchCapDecision(counts map[string]int, targetID, targetName, msg string, isBuilder bool) string {
	loopKey := "call\x00" + targetID + "\x00" + msg
	counts[loopKey]++
	if counts[loopKey] > maxSameTargetDispatch {
		return fmt.Sprintf("STOP — you have already dispatched %q with the SAME message %d times this turn; re-running the identical call won't produce a new result. Use what it already returned, or dispatch a DIFFERENT message (e.g. exercise another tool/action). If you're done verifying, reply to the user directly with what you found.", targetName, maxSameTargetDispatch)
	}
	totalCeiling := maxTotalTargetDispatch
	if isBuilder {
		totalCeiling = maxBuilderTargetDispatch
	}
	totalKey := "total\x00" + targetID
	counts[totalKey]++
	if counts[totalKey] > totalCeiling {
		return fmt.Sprintf("STOP — you've dispatched %q %d times this turn across varying messages, past the per-turn ceiling. Summarize what you've verified so far and continue any remaining checks on the user's NEXT message.", targetName, totalCeiling)
	}
	return ""
}

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

// UpdateAgentForUser saves changes to an EXISTING agent (rec.ID required) under
// the user's store, in place — no new ID is minted. The update half used by
// re-sync flows like phantom re-migrate, which refresh a bound agent's prompt /
// rules / tools from the current persona rather than creating a duplicate.
func (T *OrchestrateApp) UpdateAgentForUser(user string, rec AgentRecord) error {
	if T == nil || T.DB == nil {
		return errors.New("orchestrate runtime not initialized")
	}
	if user == "" || rec.ID == "" {
		return errors.New("user and agent id are required")
	}
	udb := UserDB(T.DB, user)
	if udb == nil {
		return fmt.Errorf("no per-user db for %q", user)
	}
	rec.Owner = user
	_, err := saveAgent(udb, rec)
	return err
}

// KnowledgeDoc is one prior knowledge chunk to seed an agent's knowledge with.
type KnowledgeDoc struct {
	Title   string
	Section string
	Text    string
	Kind    string
}

// ImportAgentKnowledge ingests prior knowledge chunks into an agent's knowledge
// store under the owner (the per-(user,agent) "imported" namespace), re-embedding
// via the configured model. Used by migration to carry a phantom chat's per-chat
// vector knowledge onto its channel agent. Guarded so a re-sync doesn't
// duplicate. Returns how many landed.
func (T *OrchestrateApp) ImportAgentKnowledge(owner, agentID string, docs []KnowledgeDoc) int {
	if T == nil || owner == "" || agentID == "" || len(docs) == 0 || VectorDB == nil {
		return 0
	}
	src := knowledgeSource(owner, agentID, "imported")
	if CountChunksBySource(VectorDB, src) > 0 {
		return 0 // already imported; re-sync-safe
	}
	ctx := context.Background()
	n := 0
	for i, d := range docs {
		body := strings.TrimSpace(d.Text)
		if body == "" {
			continue
		}
		title := strings.TrimSpace(d.Title)
		if title == "" {
			title = strings.TrimSpace(d.Section)
		}
		reportID := fmt.Sprintf("orch-import-%s-%d", agentID, i)
		IngestReportTitled(ctx, VectorDB, src, reportID, title, body, strings.TrimSpace(d.Kind))
		n++
	}
	return n
}

// ChannelHistoryMessage is one prior message used to seed a channel thread.
type ChannelHistoryMessage struct {
	Role    string
	Content string
	Sender  string // who said it — the contact on inbound, the agent on replies
	Created time.Time
}

// ImportChannelHistory seeds a channel's thread session with prior messages,
// but ONLY when the session is currently empty — so a re-run (re-sync) doesn't
// duplicate them. Used by migration to carry a chat's recent history onto its
// channel thread so the agent and the transcript have the back-story. Returns
// how many were written.
func (T *OrchestrateApp) ImportChannelHistory(owner, agentID, sessionID string, msgs []ChannelHistoryMessage) int {
	if T == nil || T.DB == nil || owner == "" || agentID == "" || sessionID == "" || len(msgs) == 0 {
		return 0
	}
	udb := UserDB(T.DB, owner)
	if udb == nil {
		return 0
	}
	sess, _ := loadChatSession(udb, agentID, sessionID)
	if len(sess.Messages) > 0 {
		return 0 // already has a thread; don't duplicate on re-sync
	}
	if sess.ID == "" {
		sess.ID = sessionID
		sess.AgentID = agentID
		sess.Created = time.Now()
	}
	for _, m := range msgs {
		sess.Messages = append(sess.Messages, ChatMessage{
			Role: m.Role, Content: m.Content, Sender: m.Sender, Created: m.Created,
		})
	}
	if _, err := saveChatSession(udb, sess); err != nil {
		Log("[orchestrate] ImportChannelHistory: save failed for %s: %v", sessionID, err)
		return 0
	}
	return len(msgs)
}

// ImportAgentNotes stores notes into an agent's Explicit Memory — the
// always-in-prompt "Saved notes" / facts block — under the owner's store. Used
// by migration to carry a phantom chat's remembered facts onto its channel
// agent. Returns how many NEW notes landed (the fact store dedups, so
// re-importing an existing note is a no-op). The agent should be in chatbot
// MemoryMode for these to read as personalization notes rather than lessons.
func (T *OrchestrateApp) ImportAgentNotes(owner, agentID string, notes []string) int {
	if T == nil || T.DB == nil || owner == "" || agentID == "" {
		return 0
	}
	udb := UserDB(T.DB, owner)
	if udb == nil {
		return 0
	}
	ns := factsNamespace(agentID)
	added := 0
	for _, note := range notes {
		note = strings.TrimSpace(note)
		if note == "" {
			continue
		}
		// Bulk-loaded from another app's store → imported: recorded on the
		// envelope so these rows are distinguishable from in-session writes
		// (and stay out of the grounding corpus). No worker chat by design —
		// imports are mechanical, not a supersession event.
		res := StoreMemoryFactP(udb, ns, note, FactWritePolicy{Source: MemSourceImported})
		if res.Reason == FactStored {
			added++
		}
	}
	return added
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
//  1. ctx gets a blocked NetworkConnector attached so any callee
//     that gates on NetworkAllowedFromContext (sandbox shell hook,
//     direct HTTP helpers) sees the block.
//  2. subSess.Network points at the same connector so tools that
//     gate on ToolSession.NetworkAllowed() (web_search, fetch_url,
//     browse_page) refuse the call up front.
//  3. CapNetwork-tagged AgentToolDefs are removed from the catalog
//     so the LLM never sees web_search / fetch_url / browse_page /
//     etc. in the first place — the cleanest signal that this turn
//     runs offline.
//
// No-op when ForcePrivate is false. Returns ctx + the (possibly
// filtered) tool slice so the caller can replace its local references.
func applyForcePrivateToDispatch(ctx context.Context, subSess *ToolSession, tools []AgentToolDef, target AgentRecord) (context.Context, []AgentToolDef) {
	// Enforce private when the TARGET is permanently private (ForcePrivate) OR
	// the PARENT turn is already running private — the parent's connector rides
	// on ctx, so a blocked incoming ctx means a Private parent delegated /
	// dispatched here and the privacy must NOT be lost in the sub-run.
	if !agentForcesPrivate(target) && NetworkAllowedFromContext(ctx) {
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
func (T *OrchestrateApp) buildDispatchTurnExtras(ctx context.Context, target AgentRecord, runtimeUser string, runtimeDB Database, subSess *ToolSession) (extraTools []AgentToolDef, availableBlock string, customToolPrompt string, subTurn *chatTurn) {
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
func (T *OrchestrateApp) buildDispatchTurnExtrasWithOwner(ctx context.Context, target AgentRecord, runtimeUser string, runtimeDB Database, subSess *ToolSession, ownerUser string, ownerDB Database) (extraTools []AgentToolDef, availableBlock string, customToolPrompt string, subTurn *chatTurn) {
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
	subTurn = &chatTurn{
		app:       T,
		agent:     target,
		user:      runtimeUser,
		udb:       runtimeDB,
		ctx:       ctx,
		topic:     generalTopic,
		ownerUser: ownerUser,
		ownerDB:   ownerDB,
	}
	// Intent-aware catalog assembly (Tier-1 tool elevation): the caller
	// stamps the turn's driving text on the session it built.
	subTurn.intentText = subSess.IntentText
	// Shared sub-agent dispatch catalog — framework conversational tools, the
	// agents grouped tool, attached pipelines, and the agent's custom tools
	// (hydrated from the OWNER's pool: the runtime user may be a synthetic channel
	// identity whose pool is empty). dispatchExtraTools is the SINGLE source of
	// truth shared with the inline agents(action="run") path, so the two sub-agent
	// surfaces can't drift. The caller wires the fallback resolver + dynamic feed
	// (below) and appends customToolPrompt to the system prompt.
	poolUser, poolDB := ownerUser, ownerDB
	if poolDB == nil || poolUser == "" {
		poolUser, poolDB = runtimeUser, runtimeDB
	}
	var extra []AgentToolDef
	extra, customToolPrompt = subTurn.dispatchExtraTools(subSess, poolUser, poolDB)
	extraTools = append(extraTools, extra...)
	availableBlock = subTurn.dispatchContextBlocks()
	return extraTools, availableBlock, customToolPrompt, subTurn
}

// dispatchContextBlocks renders the prompt blocks a dispatched agent needs to
// behave the way it does on its own chat surface. THE single place they are
// assembled, so a block added for one dispatch surface reaches the other —
// dispatchExtraTools is that place for the tool catalog and dispatchSystemPrompt
// for the prompt around them; this is the missing third.
//
// It was missing, and the two dispatch paths had drifted apart because of it:
// RunAgentSync built these three inline while the in-session agents(action=
// "run") path built none of them. Same target reached two ways got two
// different prompts — one handed the fleet catalog, the other left to discover
// peers by calling agents(action="list"), which a model almost never does
// speculatively.
func (t *chatTurn) dispatchContextBlocks() string {
	// Sub-agents skip the Available agents block — no point telling a leaf
	// about fleet peers it can't dispatch to. Saves tokens AND removes the
	// "DELEGATE FIRST" nudge that would otherwise contradict the missing tool.
	var block string
	if t.agent.OwnedBy == "" {
		block = t.renderAvailableAgentsBlock()
	}
	// NO "Available skills" here. It reaches a dispatched agent through
	// appendAgentCapabilityBlocks, which dispatchSystemPrompt calls after
	// splicing in this block — the shared capability assembler owns it for every
	// surface, which is exactly what the web turn's runPlan was changed to rely
	// on ("do NOT re-append here or it doubles", runner.go). Appending it here
	// too shipped the whole skills catalog TWICE in every dispatch, channel and
	// scheduled prompt. The chatTurn wrapper that made that easy is gone; the
	// only renderer left is availableSkillsBlock, and only the assembler calls it.
	//
	// "Known topics" — surfaces the (user, agent) topic accumulator so
	// memory_save / memory_search reuse existing snake_case slugs instead of
	// minting near-duplicates. Cheap to add; matches what the direct path does.
	block += t.renderKnownTopicsBlock()
	return block
}

// via, when supplied, is the live dispatch chain — the agent that dispatched
// this run and whoever dispatched that one — so the target can reach the
// channels its dispatcher reaches. Omit it for a run with no dispatching parent
// (a schedule, a monitor wake): the target then keeps its own scope.
func (T *OrchestrateApp) RunAgentSync(ctx context.Context, agentOwner, runtimeUser, agentKey, message string, via ...string) (string, error) {
	// Dispatch sub-agents auto-approve tool calls — a parent agent (with a
	// human behind it) initiated the dispatch. Standing/autonomous runs must
	// NOT auto-approve; they call runAgentSyncConfirm with a deny-by-default
	// confirm so high-consequence tools route through approval instead.
	text, _, err := T.runAgentSyncConfirm(ctx, agentOwner, runtimeUser, agentKey, message,
		func(string, string) bool { return true }, via...)
	return text, err
}

// runAgentSyncConfirm returns (text, hitRoundCap, error) — hitRoundCap tells the
// caller the run stopped because it exhausted its worker rounds (so a standing
// run can flag itself incomplete rather than reporting a truncated result as ok).
func (T *OrchestrateApp) runAgentSyncConfirm(ctx context.Context, agentOwner, runtimeUser, agentKey, message string, confirm func(string, string) bool, via ...string) (string, bool, error) {
	if T == nil || T.LLM == nil {
		return "", false, errors.New("orchestrate runtime not initialized")
	}
	if agentOwner == "" {
		return "", false, errors.New("agentOwner is required")
	}
	if runtimeUser == "" {
		runtimeUser = agentOwner
	}
	if strings.TrimSpace(message) == "" {
		return "", false, errors.New("message is required")
	}
	ownerDB := UserDB(T.DB, agentOwner)
	if ownerDB == nil {
		return "", false, fmt.Errorf("no per-user db for agentOwner %q", agentOwner)
	}
	target, ok := findAgentByNameOrID(ownerDB, agentOwner, agentKey)
	if !ok {
		return "", false, fmt.Errorf("agent %q not found in agentOwner %q store", agentKey, agentOwner)
	}
	// Standing fire / monitor wake / external dispatch to a retiring archetype
	// seed → materialize the owner's own copy and run that.
	target = materializeIfRetiringSeed(ownerDB, agentOwner, target)
	runtimeDB := ownerDB
	if runtimeUser != agentOwner {
		runtimeDB = UserDB(T.DB, runtimeUser)
		if runtimeDB == nil {
			return "", false, fmt.Errorf("no per-user db for runtimeUser %q", runtimeUser)
		}
		// Layered rules (enabler #2): a scoped INSTANCE adds its own rules over the
		// template's base. Modifying the local target copy means the standard prompt
		// assembler (prependAgentContext) renders base+overlay with no changes.
		target.Rules = mergeScopeRules(target.Rules, listScopeRules(runtimeDB, target.ID))
	}
	subSessID := "external-dispatch:" + runtimeUser + ":" + target.ID
	subSess := &ToolSession{
		LLM:           T.LLM,
		LeadLLM:       T.LeadLLM,
		Username:      runtimeUser,
		DB:            runtimeDB,
		ChatSessionID: subSessID,
		AgentID:       target.ID,
		IntentText:    message, // Tier-1 tool elevation matches against the brief
		// Credential scope travels with the agent — a dispatched run can
		// fetch_url too, so enforce the same deny-set here.
		DeniedCredentials: credentialDenySet(target, runtimeUser),
	}
	// Inherit the delegator's workspace when there is one — the sub-agent is
	// producing something its parent will read — and otherwise run in this
	// agent's own directory rather than the shared user root.
	if inherited := InheritedWorkspaceDir(ctx); inherited != "" {
		subSess.WorkspaceDir = inherited
	} else if ws, werr := EnsureAgentWorkspaceDir(runtimeUser, subSess.AgentID); werr == nil {
		subSess.WorkspaceDir = ws
	}
	if root, rerr := EnsureWorkspaceDir(runtimeUser); rerr == nil && root != subSess.WorkspaceDir {
		subSess.WorkspaceFallback = root
	}
	// A Builder run dispatched ON SOMEONE'S BEHALF — the async twin of a live
	// Fleet dispatch — must stamp its creations OwnedBy the requester, or an
	// approved build_agent request would produce a loose top-level agent instead
	// of a sub-agent of the agent that asked. The requester rides the dispatch
	// chain (via); its origin is the parent. Non-Builder delegations are
	// unaffected (this only sets the field for Builder).
	if isBuilderAgent(target.ID) && len(via) > 0 {
		if p := strings.TrimSpace(via[len(via)-1]); p != "" {
			subSess.DispatchParentAgentID = p
		}
	}
	defer clearAuthoringInProgress(runtimeDB, subSessID)
	defer DeleteSessionTempTools(runtimeDB, subSessID)
	// Register this dispatch in the runs ledger so a delegated sub-agent shows
	// in the live ribbon / "Active now" like any other run, nested under the run
	// that spawned it (parentRunFromCtx). Created HERE — before the sub-agent's
	// tools are built below — and the ctx re-tagged with THIS run's ID, so a
	// dispatch the sub-agent makes in turn captures this ID and nests one level
	// deeper. defer marks Failed; the success path marks Completed first
	// (idempotent — first call wins).
	liveRun := T.runsRegistry().Create(runtimeUser, target.ID, subSessID, nil).
		Describe("dispatch", target.Name, truncateObs(message, 100)).
		Parent(parentRunFromCtx(ctx))
	defer liveRun.Complete(RunStatusFailed)
	ctx = withParentRun(ctx, liveRun.ID)
	// A delegated run bills to ITSELF. Its spend used to ride the
	// delegator's context tracker, so a turn that dispatched three
	// sub-agents printed one line carrying all four runs' tokens, with no
	// way to see which of them was the expensive one. Scoping it out gives
	// it its own line — nested dispatch chains included, since each level
	// shadows the tracker above it.
	ctx, reportUsage := WithSubUsage(ctx, "dispatch "+target.Name+" "+liveRun.ID)
	defer reportUsage()
	// Hand the turn's context to the session. Two things depend on it and both
	// were silently off: a tool can only DETACH when it can find the run that
	// owns it, which it reads off this context (the log said "image stayed
	// inline: no owning agent to deliver as" on every image call this path
	// made, so a fifteen-minute render held the turn open instead of running in
	// the background), and cancellation only reaches a tool that is watching
	// the turn's context rather than a background one.
	subSess.Ctx = ctx
	// Clone so the force-adds below never mutate the stored agent's AllowedTools.
	toolNames := append([]string(nil), target.AllowedTools...)
	if len(toolNames) == 0 {
		for _, td := range RegisteredChatTools() {
			toolNames = append(toolNames, td.Name())
		}
	} else if !isNoToolsSentinel(toolNames) {
		// A curated allowlist still gets the always-on tools the interactive
		// turn force-includes (runner.go): workspace (the delivery primitive
		// every producer routes through) and the framework utilities
		// (calculate, date_math, time_in_zone). Without this, a channel or
		// dispatched agent with a tight list can't tell the time or deliver an
		// attachment — the always-on contract has to hold on every path.
		has := func(n string) bool {
			for _, x := range toolNames {
				if x == n {
					return true
				}
			}
			return false
		}
		for _, n := range append([]string{"workspace"}, frameworkUtilityTools...) {
			if !has(n) {
				toolNames = append(toolNames, n)
			}
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
	// Any authoring-capable target (the Builder seed OR an Author-flagged agent)
	// gets its unregistered authoring tools appended here on the sync-dispatch
	// path, so a dispatched/woken author behaves the same as on its own surface.
	if agentCanAuthor(target) {
		tools = append(tools, builderAuthoringTools(subSess, nil)...)
	}
	// Fleet targets get their exclusive fleet-management + delegation +
	// event-monitor catalog here too, so a dispatched/woken fleet agent
	// behaves the same as on its own chat surface. Mirrors the runner.go
	// catalog hook. Drop the generic interval scheduler (it schedules
	// through the fleet instead). Authors also get these so a delegated
	// build can wire its tool into a schedule/monitor (create_event_monitor).
	if target.Fleet || agentCanAuthor(target) {
		tools = append(tools, operatorManagementTools(subSess, target.ID)...)
		// Unified recall spans folded-away history; skip the standalone
		// recall_history / expand_history pair when it's active.
		if !unifiedMemoryEnabled() {
			tools = append(tools, operatorHistoryTools(subSess, target.ID)...)
		}
		tools, _ = dropToolsByName(tools, nil, "recurring")
	}
	// Channel-scoped chat tools — any agent that has channels gets list_chats /
	// read_chat over ITS channels (independent of Fleet). Mirrors runner.go.
	// via carries the dispatch chain on a DELEGATED run, so a delegate can also
	// address the channels its delegator reaches (empty for a schedule / wake,
	// which have no dispatching parent).
	if chTools := channelChatTools(subSess, agentOwner, target.ID, inheritedChannelChain(target, via)...); len(chTools) > 0 {
		tools = append(tools, chTools...)
	}
	// App-contributed tools, for bindings the runtime does not own — see
	// core/agent_tool_providers.go. Empty for an agent no app has bound
	// anything to, which is most of them.
	tools = append(tools, AgentProvidedTools(subSess, agentOwner, target.ID)...)
	// Parent-tool inheritance on the sync-dispatch path (standing-agent fires,
	// delegations, event-monitor wakes). An owned sub-agent that opted in pulls
	// its parent's non-consequential catalog (read_phantom_chat etc.) at runtime
	// — without this a Builder-authored summarizer scheduled to run on a clock
	// would lack the very tool it was built to use. Parent record lives in the
	// owner's store; guarded to top-level parents; deduped.
	if target.InheritParentTools && target.OwnedBy != "" {
		if parent, ok := loadAgent(ownerDB, target.OwnedBy); ok && parent.OwnedBy == "" {
			pseudo := &chatTurn{app: T, agent: target, user: runtimeUser, udb: runtimeDB, ctx: ctx, network: subSess.Network}
			tools = mergeToolsDedup(tools, pseudo.inheritableParentTools(parent, subSess))
		}
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
	extraTools, availableBlock, customToolPrompt, subTurn := T.buildDispatchTurnExtrasWithOwner(ctx, target, runtimeUser, runtimeDB, subSess, agentOwner, ownerDB)
	// Same reason as the continuing-dispatch path below: no *session on a
	// dispatched turn means no trail, and a guard that leaves no breadcrumb is
	// a guard nobody can account for after the fact.
	subTurn.beginDispatchDiag(target.ID, subSessID)
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
	sysPrompt := dispatchSystemPrompt(target, subFacts, availableBlock, customToolPrompt, subSessID, runtimeDB, runtimeUser)
	// Only Builder reads the delegated marker (to skip its intake/confirm
	// workflow); other agents ignore it. ask_user / approvals are already
	// framework-gated off the dispatch path, so we don't add the marker for
	// agents that don't act on it.
	deliveredMessage := message
	if isBuilderAgent(target.ID) {
		deliveredMessage = markAsDelegated(message)
	}
	think := resolveDispatchThink(target)
	// Telemetry — each RunAgentSync invocation gets its own per-turn
	// accumulator so pipeline agent stages, external dispatches, and
	// any other sync sub-agent run leaves a grep-able forensic record
	// in the log. Without this, pipeline-internal tool calls are a
	// black box from the parent's perspective.
	telem := newTurnTelemetry()
	dispatchMsgs, gDecline := subTurn.applyInputGuardrail([]Message{{Role: "user", Content: deliveredMessage}})
	resp, syncTranscript, runErr := T.RunAgentLoop(ctx, dispatchMsgs, AgentLoopConfig{
		// A terminal-rule pre_input block refused this request outright: the loop
		// delivers this text and never calls a model. Empty on every other turn.
		PreEmptedReply: gDecline,
		SendGuardKey:   sendGuardKey,
		SystemPrompt:   sysPrompt,
		Tools:          tools,
		MaxRounds:      resolveMaxWorkerRounds(target),
		StampLocation:  UserLocation(runtimeUser), // stamp the turn in the acting user's zone
		ThinkBudget:    target.ThinkBudget,        // per-agent override; 0 = inherit route/global
		OnStep:         func(info StepInfo) { telem.record(info); liveRun.SetProgress(info.Round, info.ToolCalls) },
		TurnNotes:      func(user string) string { return turnNotes(subSess, runtimeDB, subSessID, user) },
		TurnClaimJudge: T.turnClaimJudge(ctx),
		// And whether the reply KNOWS what it asserts. This site had the claim
		// judge and not this one — an inconsistency rather than a decision, and
		// the kind that is invisible because the path still works: a reply here
		// was checked for describing work it had not done, and not for stating
		// an unchecked claim as fact.
		//
		// No LiveClaimSpeaker: this path has no third party writing to it, so
		// there is no live claim and nothing to be cautious about.
		TurnGroundingJudge:  T.turnGroundingJudge(ctx),
		UncheckedClaims:     UncheckedFactNotes(subFacts),
		DeliveredCount:      func() int { return len(subSess.Images) + len(subSess.Videos) + len(subSess.Files) },
		Backgrounded:        func() bool { return subSess.Detach.Any() },
		BackgroundEstimate:  func() string { return subSess.Detach.EstimateText() },
		Confirm:             confirm,
		GuardrailCheck:      subTurn.guardrailEnforcer().Check,
		GuardrailActionGate: subTurn.guardrailEnforcer().ActionGate,
		GuardrailHalted:     subTurn.guardrailEnforcer().Halted,
		GuardrailReject:     subTurn.guardrailEnforcer().Reject,
		GuardrailDeclines:   subTurn.agent.GuardrailDeclines,
		// Custom-tool resolution, same as the web runPlan: lazyToolFallback
		// resolves a direct call to a has-args custom tool; dynamicNewTempTools
		// surfaces tools the LLM loaded via load_tool this turn.
		ToolFallbackResolver: subTurn.lazyToolFallback,
		DynamicTools:         subTurn.dynamicNewTempTools(subSess),
		// Feed view_video's sampled frames to the model on the next round so a
		// channel agent (phantom) actually sees a reel it was asked to watch.
		DrainViewImages: subSess.DrainViewImages,
		BeforeToolRound: func() { SnapshotImageRefs(subSess) },
		// Nothing here surfaces a non-final round's prose: only resp.Content is
		// returned, no stream is wired, no round is settled into a transcript, and
		// the telemetry above keeps len(info.Content) rather than the text. So the
		// periodic guardrail check has nothing to protect and its per-round model
		// call is pure latency — pre_output judges the reply that is handed back.
		InterimContentHidden: true,
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(think),
		},
	})
	Log("[orchestrate.RunAgentSync] owner=%s runtime=%s target=%s msg_chars=%d err=%v",
		agentOwner, runtimeUser, target.ID, len(message), runErr)
	// Close the books on what this turn said it would do. See commitment_ledger.go.
	if runErr == nil && resp != nil {
		recordTurnCommitment(runtimeDB, subSessID, resp.Content, len(persistedToolCallsFromTranscript(syncTranscript)) > 0)
	}
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
	// Status reflects the real outcome: a superseded/cancelled turn is CANCELED,
	// not FAILED (the deferred RunStatusFailed only stands if we returned before
	// reaching here — a genuine setup failure).
	liveRun.Complete(runOutcomeStatus(runErr, resp != nil))
	if runErr != nil {
		return "", false, runErr
	}
	if resp == nil {
		return "", false, errors.New("agent returned no response")
	}
	return strings.TrimSpace(resp.Content), resp.HitRoundCap, nil
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
// AgentSyncRun carries the inputs for RunAgentSyncContinuingRich. The channel
// path uses StatusCallback (mid-turn pings) and reads AgentSyncResult.Images
// (the agent's produced attachments); the legacy text-only callers go through
// the RunAgentSyncContinuing wrapper.
type AgentSyncRun struct {
	AgentOwner       string
	RuntimeUser      string
	AgentKey         string
	SubSessionID     string
	InjectionQueueID string
	// DeliverySessionID is the CONVERSATION anything this run starts in the
	// background must come home to. A dispatch runs under its own sub-session
	// id so its ephemeral state stays off the caller's thread; a picture
	// started inside it belongs to the thread all the same. Empty means the
	// sub-session id is also the conversation, which is true for a dispatch
	// with no parent thread. See ToolSession.DeliverySessionID.
	DeliverySessionID string
	Message           string
	FreshSession      bool
	// Kind labels this run in the live-activity ribbon / "Active now" pane
	// ("channel" for an inbound channel/iMessage turn, else it defaults to
	// "dispatch"). Cosmetic only — how the run is described, not how it runs.
	Kind           string
	StatusCallback func(string) // optional: wired to the sub-session's StatusCallback
	// Stream, when set, receives assistant text deltas AS THEY GENERATE, wired
	// straight to the agent loop's stream handler. Without it a caller only
	// gets AgentSyncResult.Text when the whole turn is done — fine for a
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
	// Title, when set, names a FRESH session (used only if it has no title
	// yet). Channel rooms pass the contact's display name so the rail row and
	// transcript read as the conversation partner rather than the raw id.
	Title string
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
	AppTools []AgentToolDef
	// Loop, when set, supplies rich agent-loop knobs (pacing callbacks, chat
	// options, round budget) a sophisticated instance needs that the default
	// dispatch run doesn't — so e.g. servitor's investigator runs through the
	// scoped path with FULL behavior parity (step pacing, stuck detection,
	// plan-aware wrap-up) instead of a generic loop. nil ⇒ dispatch defaults.
	Loop *AgentLoopOverrides
	// SystemPromptOverride, when set, REPLACES the prompt the dispatch normally
	// assembles from the template record (+ facts + available blocks). An app
	// whose instance builds its own complete, per-run system prompt (servitor's
	// investigator via buildLeadSystemPrompt) passes it here so the scoped run
	// uses that prompt verbatim — gaining scoped sessions + recording-to-scope
	// without changing the prompt content. Empty ⇒ the assembled prompt.
	SystemPromptOverride string
}

// AgentLoopOverrides carries the subset of AgentLoopConfig a scoped/template run
// may override. Zero fields inherit the dispatch defaults; set only what the
// instance's loop needs. See AgentSyncRun.Loop.
type AgentLoopOverrides struct {
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
	TierOverride  LLMTier
	ChatOptions   []ChatOption     // APPENDED to the dispatch defaults (last wins per option)
	OnRoundReset  func() bool      // per-step pacing reset
	OnRoundStart  func() []Message // pre-round injection (stuck nudges, etc.)
	PendingWorkFn func() int       // remaining-work count for the wrap-up nudge
}

// AgentSyncResult is the bound agent's output: reply text plus any attachments
// it produced this turn (base64).
type AgentSyncResult struct {
	Text   string
	Images []string
	// Videos are base64 video attachments the turn produced (video tools via
	// sess.Videos, or a [ATTACH: file.mp4] marker). Kept SEPARATE from Images so
	// they ride out as videos, not mislabeled images — restoring the outbound
	// video channel phantom had before the bridges migration dropped it.
	Videos []string
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
	// ToolsUsed names the tools the turn actually called, in first-use order and
	// deduped. A channel turn's whole record in the standing thread was its
	// inbound text and its reply, so anything it DID on the owner's behalf —
	// searched, edited a picture, messaged someone, armed a monitor — left no
	// trace there at all. Names only: the standing thread is bounded by a
	// rolling summary, and arguments would crowd out the awareness it exists for.
	ToolsUsed []string
	// Silenced reports that the model DELIBERATELY chose to say nothing —
	// stay_silent fired. Distinct from Text being empty by accident, which is a
	// failure. Callers that substitute a fallback for empty output must not do
	// so here: on a messaging channel, "don't reply to this" produced the
	// agent's "I wasn't able to put together a response" line, which is the
	// framework overriding an instruction the user gave and the model obeyed.
	Silenced bool
}

// buildInboundMediaManifest individuates the media that arrived on THIS turn,
// giving the model a stable, referenceable handle per item (media#1, media#2, …)
// with sender attribution instead of an anonymous count. It is the media analogue
// of the source-provenance rule in the grounding block: a stable id per item lets
// the model bind "the image Alice sent" to a specific object rather than guess
// across indistinguishable items in a busy group thread, and the ids are the
// handles the delivery tools resolve against, so the model never has to recall a
// filename from memory. RUN-ONLY (the caller appends it to the live message, never
// the persisted transcript): it describes bytes that ride only on this turn.
// Returns "" when the turn carried no media. Written without em-dashes so it does
// not model that tic to the worker (house style).
func buildInboundMediaManifest(sender string, imageCount int) string {
	if imageCount <= 0 {
		return ""
	}
	who := strings.TrimSpace(sender)
	var b strings.Builder
	b.WriteString("\n[media on this turn. Each item below is shown to you directly as multimodal content you can see now; no tool is needed to FETCH, download, or find it. Refer to a specific item by its id.]")
	for i := 1; i <= imageCount; i++ {
		fmt.Fprintf(&b, "\n  media#%d: image", i)
		if who != "" {
			b.WriteString(" from " + who)
		}
	}
	b.WriteString("\n  To CHANGE one of these pictures, pass its id to the image tool: image(action=\"edit\", images=[\"media#1\"], prompt=\"...\"). That is how \"put the cat in this photo\", \"make it night\", or \"blend these two\" is done. Do NOT generate a new picture from a text prompt when you were asked to change one you were sent: generating invents a different scene, and what came back was supposed to be THEIR photo.")
	b.WriteString("\n  Everyone in THIS conversation already received these, so do NOT send one back UNCHANGED; re-attaching a photo that was just posted only echoes it to the same group. An EDITED version is a new picture and is fine to send here. Reference a media#N to forward it to a DIFFERENT recipient, by passing attachments:[\"media#N\"] to a messaging tool. Refer to an item by its id in your text; do NOT retype or invent a filename for it, the id is its only handle.")
	return b.String()
}

// inboundCaptionPrompt asks the vision LLM for a single plain-fact depiction of
// an inbound image. Kept deliberately terse and anti-embellishment: the model
// tends to editorialize ("a funny meme about...") which is exactly the
// confabulation we are trying to prevent from entering the durable record.
const inboundCaptionPrompt = "Describe what is actually shown in this image in one plain factual sentence: the main subject and scene, plus any legible on-image text. Do not guess intent or humor, do not call it a meme unless it obviously is one, and add no commentary."

// captionInboundImages produces a brief factual depiction of each inbound image
// so the PERSISTED transcript keeps a durable, grounded record of what arrived.
// The pixels ride only on the live turn (run.Images) and the individuated
// manifest is run-only; both vanish next turn. Without a persisted depiction a
// later reference to the image ("is this the worst of the Internet today?")
// reaches a model with an empty slot where the content should be, and it
// confabulates the subject/sender/type. Captioning once on arrival and
// persisting the result is the durable half of the media manifest, the same way
// SurfaceContext is the run-only half.
//
// Generated ONCE, on the arrival turn, and persisted by the caller — never
// recomputed on replay (that would vary the stored history and break prompt-
// cache determinism). Fails soft: an errored or empty item leaves that slot
// blank and the record degrades to the bare count for it. Written without
// em-dashes (house style; don't model the tic to the worker).
func captionInboundImages(ctx context.Context, llm LLM, images [][]byte) []string {
	if llm == nil || len(images) == 0 {
		return nil
	}
	out := make([]string, len(images))
	for i, img := range images {
		if len(img) == 0 {
			continue
		}
		resp, err := llm.Chat(ctx,
			[]Message{{Role: "user", Content: inboundCaptionPrompt, Images: [][]byte{img}}},
			WithCaller("orchestrate/inbound_caption"),
			WithMaxRetries(0),
			WithThink(false),
		)
		if err != nil || resp == nil {
			continue
		}
		out[i] = strings.TrimSpace(resp.Content)
	}
	return out
}

// buildInboundImageRecord is the PERSISTED, past-tense record of images that
// arrived on a turn. Unlike buildInboundMediaManifest (run-only, present-tense,
// describing live pixels) this is written to the transcript and replayed on
// every later turn, so it must NOT claim the images are still visible. It
// records how many arrived, who sent them, and — the durable grounding fix — a
// one-line depiction per image so a later reference has a real anchor instead of
// an empty slot the model fills by guessing. captions may be short/empty (the
// caption call failed or an item was blank); a missing entry degrades to a bare
// note for that item, and if NONE captioned it falls back to the prior bare
// count form. Deliberately does NOT use the run-scoped media#N ids: those are
// re-minted per turn and would dangle here on replay. Em-dash-free (house
// style). Returns "" for an empty turn.
func buildInboundImageRecord(sender string, count int, captions []string) string {
	if count <= 0 {
		return ""
	}
	who := strings.TrimSpace(sender)
	captionAt := func(i int) string {
		if i < len(captions) {
			return strings.TrimSpace(captions[i])
		}
		return ""
	}
	hasCaption := false
	for i := 0; i < count; i++ {
		if captionAt(i) != "" {
			hasCaption = true
			break
		}
	}
	if !hasCaption {
		if who != "" {
			return fmt.Sprintf("\n[%d image(s) attached from %s]", count, who)
		}
		return fmt.Sprintf("\n[%d image(s) attached]", count)
	}
	var b strings.Builder
	if count == 1 {
		b.WriteString("\n[1 image attached")
		if who != "" {
			b.WriteString(" from " + who)
		}
		if c := captionAt(0); c != "" {
			b.WriteString(". Depicts: " + c)
		}
		b.WriteString("]")
		return b.String()
	}
	b.WriteString(fmt.Sprintf("\n[%d images attached", count))
	if who != "" {
		b.WriteString(" from " + who)
	}
	b.WriteString(".")
	for i := 0; i < count; i++ {
		if c := captionAt(i); c != "" {
			fmt.Fprintf(&b, " image %d depicts: %s;", i+1, c)
		} else {
			fmt.Fprintf(&b, " image %d: (no description);", i+1)
		}
	}
	b.WriteString("]")
	return b.String()
}

// RunAgentSyncContinuing is the text-only wrapper kept for existing callers
// (goal conversations, dispatch_agent, event-monitor wakes).
func (T *OrchestrateApp) RunAgentSyncContinuing(ctx context.Context, agentOwner, runtimeUser, agentKey, subSessionID, injectionQueueID, message string, freshSession bool) (string, error) {
	res, err := T.RunAgentSyncContinuingRich(ctx, AgentSyncRun{
		AgentOwner: agentOwner, RuntimeUser: runtimeUser, AgentKey: agentKey,
		SubSessionID: subSessionID, InjectionQueueID: injectionQueueID,
		Message: message, FreshSession: freshSession,
	})
	return res.Text, err
}

func (T *OrchestrateApp) RunAgentSyncContinuingRich(ctx context.Context, run AgentSyncRun) (AgentSyncResult, error) {
	agentOwner := run.AgentOwner
	runtimeUser := run.RuntimeUser
	agentKey := run.AgentKey
	subSessionID := run.SubSessionID
	injectionQueueID := run.InjectionQueueID
	message := run.Message
	freshSession := run.FreshSession
	if T == nil || T.LLM == nil {
		return AgentSyncResult{}, errors.New("orchestrate runtime not initialized")
	}
	if agentOwner == "" {
		return AgentSyncResult{}, errors.New("agentOwner is required")
	}
	if runtimeUser == "" {
		runtimeUser = agentOwner
	}
	if strings.TrimSpace(message) == "" && len(run.Images) == 0 {
		return AgentSyncResult{}, errors.New("message is required")
	}
	ownerDB := UserDB(T.DB, agentOwner)
	if ownerDB == nil {
		return AgentSyncResult{}, fmt.Errorf("no per-user db for agentOwner %q", agentOwner)
	}
	target, ok := findAgentByNameOrID(ownerDB, agentOwner, agentKey)
	if !ok {
		return AgentSyncResult{}, fmt.Errorf("agent %q not found in agentOwner %q store", agentKey, agentOwner)
	}
	target = materializeIfRetiringSeed(ownerDB, agentOwner, target)
	runtimeDB := ownerDB
	if runtimeUser != agentOwner {
		runtimeDB = UserDB(T.DB, runtimeUser)
		if runtimeDB == nil {
			return AgentSyncResult{}, fmt.Errorf("no per-user db for runtimeUser %q", runtimeUser)
		}
		// Layered rules (enabler #2): instance overlay over the template's base —
		// see the matching block in runAgentSyncConfirm.
		target.Rules = mergeScopeRules(target.Rules, listScopeRules(runtimeDB, target.ID))
	}
	if subSessionID == "" {
		subSessionID = "external-dispatch:" + runtimeUser + ":" + target.ID
	}
	subSess := &ToolSession{
		LLM:                T.LLM,
		LeadLLM:            T.LeadLLM,
		Username:           runtimeUser,
		DB:                 runtimeDB,
		ChatSessionID:      subSessionID,
		DeliverySessionID:  strings.TrimSpace(run.DeliverySessionID),
		AgentID:            target.ID,
		IntentText:         message,                // Tier-1 tool elevation matches against the brief
		ReplyAuthorizedKey: run.ReplyAuthorizedKey, // in-thread reply skips the send approval gate
		ChannelChatID:      run.ChannelChatID,      // so a background task can still reach this conversation
		ChannelHandle:      run.ChannelHandle,
		DeniedCredentials:  credentialDenySet(target, runtimeUser),
	}
	// Inherit the delegator's workspace when there is one — the sub-agent is
	// producing something its parent will read — and otherwise run in this
	// agent's own directory rather than the shared user root.
	if inherited := InheritedWorkspaceDir(ctx); inherited != "" {
		subSess.WorkspaceDir = inherited
	} else if ws, werr := EnsureAgentWorkspaceDir(runtimeUser, subSess.AgentID); werr == nil {
		subSess.WorkspaceDir = ws
	}
	if root, rerr := EnsureWorkspaceDir(runtimeUser); rerr == nil && root != subSess.WorkspaceDir {
		subSess.WorkspaceFallback = root
	}
	// Mid-turn status (channel path): the agent's send_status / progress
	// pings land here so the transport can deliver them before the final
	// reply. nil for the legacy text-only callers — graceful no-op.
	if run.StatusCallback != nil {
		subSess.StatusCallback = run.StatusCallback
	}
	defer clearAuthoringInProgress(runtimeDB, subSessionID)
	defer DeleteSessionTempTools(runtimeDB, subSessionID)
	// Register this channel/dispatch turn in the runs ledger, nested under the
	// run that spawned it (parentRunFromCtx). Created BEFORE the sub-agent's
	// tools build below, with ctx re-tagged to THIS run's ID, so a dispatch the
	// sub-agent makes nests one level deeper. Kind defaults to "dispatch"; the
	// channel caller passes "channel". defer marks Failed; success marks
	// Completed first (idempotent — first call wins).
	liveKind := run.Kind
	if liveKind == "" {
		liveKind = "dispatch"
	}
	liveRun := T.runsRegistry().Create(runtimeUser, target.ID, subSessionID, nil).
		Describe(liveKind, target.Name, truncateObs(message, 100)).
		Parent(parentRunFromCtx(ctx))
	defer liveRun.Complete(RunStatusFailed)
	ctx = withParentRun(ctx, liveRun.ID)
	// Same as the sync path: this run's spend is its own, not the
	// delegator's. Labelled with the run KIND so a channel turn and a
	// delegation are told apart in the log.
	ctx, reportUsage := WithSubUsage(ctx, liveKind+" "+target.Name+" "+liveRun.ID)
	defer reportUsage()
	// Hand the turn's context to the session. Two things depend on it and both
	// were silently off: a tool can only DETACH when it can find the run that
	// owns it, which it reads off this context (the log said "image stayed
	// inline: no owning agent to deliver as" on every image call this path
	// made, so a fifteen-minute render held the turn open instead of running in
	// the background), and cancellation only reaches a tool that is watching
	// the turn's context rather than a background one.
	subSess.Ctx = ctx
	// Clone so the force-adds below never mutate the stored agent's AllowedTools.
	toolNames := append([]string(nil), target.AllowedTools...)
	if len(toolNames) == 0 {
		for _, td := range RegisteredChatTools() {
			toolNames = append(toolNames, td.Name())
		}
	} else if !isNoToolsSentinel(toolNames) {
		// A curated allowlist still gets the always-on tools the interactive
		// turn force-includes (runner.go): workspace (the delivery primitive
		// every producer routes through) and the framework utilities
		// (calculate, date_math, time_in_zone). Without this, a channel or
		// dispatched agent with a tight list can't tell the time or deliver an
		// attachment — the always-on contract has to hold on every path.
		has := func(n string) bool {
			for _, x := range toolNames {
				if x == n {
					return true
				}
			}
			return false
		}
		for _, n := range append([]string{"workspace"}, frameworkUtilityTools...) {
			if !has(n) {
				toolNames = append(toolNames, n)
			}
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
	if agentCanAuthor(target) {
		tools = append(tools, builderAuthoringTools(subSess, nil)...)
	}
	// Fleet targets get their fleet-management + delegation + event-monitor
	// catalog here too — this is the WAKE path (event monitors run the
	// channel agent on its channel thread through RunAgentSyncContinuing),
	// so without it a woken fleet agent would have no delegate / monitor
	// tools. Authors also get these so a delegated build can schedule/monitor.
	if target.Fleet || agentCanAuthor(target) {
		tools = append(tools, operatorManagementTools(subSess, target.ID)...)
		// Unified recall spans folded-away history; skip the standalone
		// recall_history / expand_history pair when it's active.
		if !unifiedMemoryEnabled() {
			tools = append(tools, operatorHistoryTools(subSess, target.ID)...)
		}
		tools, _ = dropToolsByName(tools, nil, "recurring")
	}
	// Channel-scoped chat tools — any agent that has channels gets list_chats /
	// read_chat over ITS channels (independent of Fleet). Mirrors runner.go.
	// run.DispatchedBy widens this to the delegator's channels on a delegated
	// run; it's empty for a wake / channel inbound.
	if chTools := channelChatTools(subSess, agentOwner, target.ID, inheritedChannelChain(target, run.DispatchedBy)...); len(chTools) > 0 {
		tools = append(tools, chTools...)
	}
	tools = append(tools, AgentProvidedTools(subSess, agentOwner, target.ID)...)
	// Parent-tool inheritance on the sync-dispatch path (standing-agent fires,
	// delegations, event-monitor wakes). An owned sub-agent that opted in pulls
	// its parent's non-consequential catalog (read_phantom_chat etc.) at runtime
	// — without this a Builder-authored summarizer scheduled to run on a clock
	// would lack the very tool it was built to use. Parent record lives in the
	// owner's store; guarded to top-level parents; deduped.
	if target.InheritParentTools && target.OwnedBy != "" {
		if parent, ok := loadAgent(ownerDB, target.OwnedBy); ok && parent.OwnedBy == "" {
			pseudo := &chatTurn{app: T, agent: target, user: runtimeUser, udb: runtimeDB, ctx: ctx, network: subSess.Network}
			tools = mergeToolsDedup(tools, pseudo.inheritableParentTools(parent, subSess))
		}
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
	extraTools, availableBlock, customToolPrompt, subTurn := T.buildDispatchTurnExtrasWithOwner(ctx, target, runtimeUser, runtimeDB, subSess, agentOwner, ownerDB)
	// A channel inbound is the one path with a third party on the other end: the
	// run identity is a synthetic per-chat user while the agent record lives in
	// the owner's store, and MessageSender is the contact's own display name.
	// Both go to the guardrail warden (chatTurn.requester) so a rule can name an
	// audience rather than being written for the worst-case asker. Kind is set by
	// the framework and is trusted; MessageSender is the contact's to choose and
	// is not, which is why requester() derives the owner flag from neither.
	subTurn.requesterName = run.MessageSender
	if run.Kind == "channel" {
		subTurn.requesterChannel = run.Kind
	}
	// Name the trail this turn writes its breadcrumbs to. A dispatched run has
	// no *session — the record belongs to the caller — so without this every
	// turnDiag on this path hit the "no trail to write to" return and vanished.
	//
	// That is the whole of a channel/cortex turn's diagnostics: the guardrail
	// that blocked a reply computed WHICH rule fired, wrote it to the trail, and
	// the trail was discarded, leaving the owner a decline on their phone and no
	// way to find out what stopped it. Same reasoning as the scheduled-fire path
	// (scheduled_updates.go), which was given a trail for exactly this reason;
	// this is the other half of it.
	subTurn.beginDispatchDiag(target.ID, subSessionID)
	// The owner texting their own agent runs as phantom:<chatID> exactly like a
	// stranger does, so without this they are an "outside party" on their own
	// phone and their own carve-outs shut them out. Decided on the TRANSPORT
	// handle via the bridge's own comparison — never on MessageSender, which is
	// the sender's to choose and would let anyone claim to be the owner by
	// renaming themselves.
	if h := strings.TrimSpace(run.SenderHandle); h != "" || run.Kind == "channel" {
		// Kept raw as well as classified: the boolean answers "is this the
		// owner", and an authorized-identities roster has to be matched against
		// the handle itself. Same source, same trust level, still never content.
		subTurn.requesterHandle = h
		if link, ok := ActiveMessagingLink(); ok && link.IsOwnerHandle(agentOwner, h) {
			subTurn.requesterOwnerHandle = true
		}
		// The same three facts on the SESSION, for tools rather than guardrails.
		// The kept-image library needs them to anchor "a picture of Rory" to the
		// person who actually sent it, and it must anchor on the handle for the
		// reason spelled out right above: a display name is the sender's to
		// choose, so keying a face library on one lets anybody take over
		// anybody's entry by renaming themselves.
		subSess.SpeakerName = run.MessageSender
		subSess.SpeakerHandle = h
		subSess.SpeakerIsOwner = subTurn.requesterOwnerHandle
	}
	tools = append(tools, extraTools...)
	// Caller-injected per-instance tools (e.g. an app's appliance-bound closures
	// passed via AgentSyncRun.AppTools / RunScopedAgent). Mirror onto subTurn so
	// custom-tool resolution (lazyToolFallback) sees them too.
	if len(run.AppTools) > 0 {
		tools = append(tools, run.AppTools...)
		subTurn.appTools = append(subTurn.appTools, run.AppTools...)
	}
	// ForcePrivate enforcement — see applyForcePrivateToDispatch.
	// No-op when target.ForcePrivate is false.
	ctx, tools = applyForcePrivateToDispatch(ctx, subSess, tools, target)
	var subFacts []MemoryFact
	if !isPhantomDispatch {
		subFacts = ListMemoryFacts(runtimeDB, factsNamespace(target.ID))
	}
	sysPrompt := dispatchSystemPrompt(target, subFacts, availableBlock, customToolPrompt, subSessionID, runtimeDB, runtimeUser)
	// The third-party doctrine goes in the SYSTEM prompt, not on every message.
	// It names nobody, so the text is byte-identical turn after turn and caches
	// for the life of the thread; the volatile half — which of them is writing
	// now — is one line on the message itself.
	if !subTurn.requesterOwnerHandle && strings.TrimSpace(subTurn.requesterHandle) != "" {
		sysPrompt = ThirdPartyClaimDoctrine() + sysPrompt
	}
	if s := strings.TrimSpace(run.SystemPromptOverride); s != "" {
		sysPrompt = s // app supplies its own complete per-run prompt (e.g. servitor's investigator)
	}
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
	// Name a fresh, untitled session from the caller (channel rooms pass the
	// contact's display name), so the rail labels it by conversation partner.
	if priorSession.Title == "" && run.Title != "" {
		priorSession.Title = run.Title
	}
	// The delegated-invocation marker only signals a CONVERSATIONAL agent
	// (Builder) to skip its intake/confirm workflow and run headless from the
	// brief — it's the only agent whose prompt reads it. ask_user / approval
	// pauses are already framework-gated (those tools aren't in the dispatch
	// catalog; approvals auto-approve), so we don't instruct the LLM about them.
	// Skip it for everyone else, and always on an interactive surface (a channel
	// has a human who answers follow-ups in the next message).
	deliveredMessage := message
	if !run.Interactive && isBuilderAgent(target.ID) {
		deliveredMessage = markAsDelegated(message)
	}
	// Inbound images (channel path): the decoded bytes ride on the user message
	// below as multimodal content the model sees THIS turn. Two representations,
	// deliberately different (mirrors the SurfaceContext split just below):
	//   - PERSISTED (deliveredMessage): a compact, PAST-TENSE record so a replayed
	//     transcript states plainly that images WERE sent on that turn, WITHOUT the
	//     present-tense "already provided inline, you can see them, don't fetch"
	//     language. That language is a lie on replay: the bytes are attached only
	//     to the live turn (run.Images below) and are never re-sent, so persisting
	//     the present-tense banner made the model describe images it could no
	//     longer see and confabulate media across turns.
	//   - RUN-ONLY (llmMessage, further below): the individuated manifest with a
	//     stable id + sender per item, appended alongside SurfaceContext so it is
	//     NOT persisted.
	if n := len(run.Images); n > 0 {
		// Caption each inbound image ONCE, here on the arrival turn (worker is
		// vision-capable), and persist the depiction into deliveredMessage so a
		// later reference to the image has a durable anchor instead of an empty
		// slot the model confabulates into (the Wiwee "meme from Henry about the
		// Alamo" failure). Fails soft — a blank caption degrades to the bare count.
		captions := captionInboundImages(ctx, subSess.LLM, run.Images)
		deliveredMessage += buildInboundImageRecord(run.MessageSender, n, captions)
		captioned := 0
		for _, c := range captions {
			if strings.TrimSpace(c) != "" {
				captioned++
			}
		}
		// Log arrival + caption yield. Absence of this line for a turn that was
		// supposed to carry a photo is itself the signal that the bytes never
		// arrived (e.g. an unfetched link), which is a separate delivery issue.
		Log("[orchestrate.inbound_media] runtime=%s target=%s sub=%s images=%d captioned=%d",
			runtimeUser, target.ID, subSessionID, n, captioned)
		// Register each inbound image in the session's addressable media registry
		// (media#1, media#2, …) so the model can post a SPECIFIC one back by id.
		// Order matches the manifest enumeration appended to llmMessage below.
		for _, img := range run.Images {
			subSess.RegisterInboundMedia("image", img, run.MessageSender)
		}
	}
	// Bound the run-view with the same rolling-summary compaction the Cortex
	// thread uses, so a long-running channel / dispatch session doesn't load
	// its entire history into the prompt (and eventually blow the window).
	// Storage is bounded separately at the save site below (trimStoredHistory,
	// summary + generous tail; older content stays recoverable via recall).
	// No-op until the thread grows past the fold trigger, so short dispatches
	// are unaffected; fact extraction honors the agent's memory setting (see
	// compactOperatorHistory).
	bounded := T.compactOperatorHistory(runtimeDB, runtimeUser, target, subSessionID, priorSession.Messages)
	llmMessages := make([]Message, 0, len(bounded)+1)
	for _, m := range bounded {
		llmMessages = append(llmMessages, Message{Role: m.Role, Content: llmHistoryContent(m)})
	}
	// Provenance for the LLM ONLY — appended to the run-time copy of the user
	// message so the agent knows which channel/transport this arrived on, but
	// NOT persisted (the stored message below stays clean, so a long thread
	// doesn't replay the banner on every turn).
	llmMessage := deliveredMessage
	if sc := strings.TrimSpace(run.SurfaceContext); sc != "" {
		llmMessage += "\n" + sc
	}
	// Individuated media manifest — RUN-ONLY, same non-persisted treatment as
	// SurfaceContext. Replaces the anonymous "N image(s)" count with a stable
	// handle per inbound item (media#1, media#2, …) plus sender attribution, so
	// the model can bind "the image Alice sent" to a specific object instead of
	// guessing across indistinguishable items in a busy group thread. These are
	// the ids the delivery tools resolve against (post-by-id), so the model never
	// recalls a filename from memory. Not persisted: it describes bytes that are
	// present only on this turn (see the deliveredMessage split above).
	if m := buildInboundMediaManifest(run.MessageSender, len(run.Images)); m != "" {
		llmMessage += m
	}
	// Attribute the live turn to its sender too (same rendering as history, so
	// the prompt prefix stays cache-stable when this message replays next turn).
	// Without this the LLM sees every group-room turn as an anonymous "user" and
	// can't tell participants apart — the names are stored + shown in Cortex, but
	// the model itself never saw them.
	llmMessages = append(llmMessages, Message{Role: "user", Content: attributeSender("user", run.MessageSender, llmMessage), Images: run.Images})

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

	think := resolveDispatchThink(target)
	if run.Think != nil {
		think = *run.Think
	}
	loopCfg := AgentLoopConfig{
		SendGuardKey:        sendGuardKey,
		SystemPrompt:        sysPrompt,
		Tools:               tools,
		MaxRounds:           resolveMaxWorkerRounds(target),
		ThinkBudget:         target.ThinkBudget, // per-agent override; 0 = inherit route/global
		Confirm:             func(name, args string) bool { return true },
		GuardrailCheck:      subTurn.guardrailEnforcer().Check,
		GuardrailActionGate: subTurn.guardrailEnforcer().ActionGate,
		GuardrailHalted:     subTurn.guardrailEnforcer().Halted,
		GuardrailReject:     subTurn.guardrailEnforcer().Reject,
		GuardrailDeclines:   subTurn.agent.GuardrailDeclines,
		// Custom-tool resolution, same as the web runPlan (see RunAgentSync).
		ToolFallbackResolver: subTurn.lazyToolFallback,
		DynamicTools:         subTurn.dynamicNewTempTools(subSess),
		// InjectionDrain (not OnRoundStart): the queue-drain closure
		// returns nil when empty, so the loop's pre-finalize re-call
		// terminates. A mid-flight note pushed while this dispatch runs
		// gets picked up at the next round AND right before finalizing.
		InjectionDrain: onRoundStart,
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(think),
		},
	}
	// Caller loop overrides (enabler #3) — a template instance with a sophisticated
	// loop (servitor's investigator: step pacing, stuck detection, plan-aware
	// wrap-up) supplies these so the scoped run matches its bespoke RunAgentLoop.
	if lc := run.Loop; lc != nil {
		if lc.MaxRounds > 0 {
			loopCfg.MaxRounds = lc.MaxRounds
		}
		if lc.SerialTools {
			loopCfg.SerialTools = true
		}
		if lc.TierOverride != TierUnset {
			loopCfg.TierOverride = lc.TierOverride
		}
		loopCfg.ChatOptions = append(loopCfg.ChatOptions, lc.ChatOptions...) // appended → last wins
		if lc.OnRoundReset != nil {
			loopCfg.OnRoundReset = lc.OnRoundReset
		}
		if lc.OnRoundStart != nil {
			loopCfg.OnRoundStart = lc.OnRoundStart
		}
		if lc.PendingWorkFn != nil {
			loopCfg.PendingWorkFn = lc.PendingWorkFn
		}
	}
	// Caller-supplied token stream (a voice/HTTP caller waiting on first word).
	if run.Stream != nil {
		loopCfg.Stream = run.Stream
	}
	// Feed view_video's sampled frames to the model on the next round.
	loopCfg.DrainViewImages = subSess.DrainViewImages
	loopCfg.BeforeToolRound = func() { SnapshotImageRefs(subSess) }
	loopCfg.StampLocation = UserLocation(runtimeUser) // stamp the turn in the acting user's zone
	// What this turn has run a deliverable producer for, tracked live so the
	// phantom check below can tell an unattached caption from ordinary prose.
	produced := new(deliveryWatch)
	loopCfg.OnStep = func(info StepInfo) {
		produced.note(info.ToolCalls)
		liveRun.SetProgress(info.Round, info.ToolCalls)
	}
	// Recent-image ids when the message is about a picture. This is the path a
	// channel reply takes, and the one where "blend these two photos" arrives
	// with no filenames anywhere in it. See imageSpaceNote.
	loopCfg.TurnNotes = func(user string) string { return turnNotes(subSess, runtimeDB, subSessionID, user) }
	// Last look before the reply reaches the channel. See turn_judge.go.
	loopCfg.TurnClaimJudge = T.turnClaimJudge(ctx)
	// And whether it KNOWS what it asserts. On this path the live claim matters
	// more than the stored ones: a contact says something in a room and the
	// agent can adopt it and repeat it to everyone inside the same turn.
	//
	// The speaker is taken from the OWNER classification, not the display name:
	// a non-owner's message is in scope whatever they call themselves, and the
	// owner's never is — treating the principal's own message as an unverified
	// claim would hedge the instructions they just gave.
	loopCfg.TurnGroundingJudge = T.turnGroundingJudge(ctx)
	loopCfg.UncheckedClaims = UncheckedFactNotes(subFacts)
	if !subTurn.requesterOwnerHandle && strings.TrimSpace(subTurn.requesterHandle) != "" {
		loopCfg.LiveClaimSpeaker = chFirst(strings.TrimSpace(subTurn.requesterName), "the sender")
		// Someone the owner listed by account or handle skips the HOLD and
		// nothing else. Trust and verification are different things: a partner
		// saying the invoice is paid is exactly as unverified as a stranger
		// saying it, so their world-claims stay marked. What being listed buys
		// is not being interrupted — and interruption with no safety gain is
		// what gets a gate switched off.
		//
		// Matched the same way guardrails match, so there is ONE roster. A
		// second list would drift from the first, and the two would disagree
		// about who is trusted with nothing to say which is right.
		if _, _, via := subTurn.resolveAuthorization(); via != "" {
			loopCfg.LiveClaimTrusted = true
		}
	}
	loopCfg.DeliveredCount = func() int { return len(subSess.Images) + len(subSess.Videos) + len(subSess.Files) }
	loopCfg.Backgrounded = func() bool { return subSess.Detach.Any() }
	loopCfg.BackgroundEstimate = func() string { return subSess.Detach.EstimateText() }
	// Catch a reply that promises a file it never made, while the loop can still
	// do something about it. Without this the claim reaches the channel, strips
	// to an empty reply, and the contact is asked to rephrase.
	loopCfg.PhantomDeliveryRefs = func(reply string) []string {
		return phantomDeliveryRefs(subSess, reply, produced.producedKind())
	}
	// Nothing on this path shows or keeps a non-final round's prose: only the
	// final reply is persisted (one assistant ChatMessage, below), OnStep forwards
	// the round number and tool calls but never content, and no SettleRound folds
	// rounds into a transcript. So the periodic guardrail check has nothing to
	// protect here and its per-round model call is pure latency — pre_output
	// judges the reply, which is the only text that leaves.
	//
	// UNLESS the caller wired a token stream: then interim deltas reach a person
	// as they generate, the words ARE delivered, and the check has to run. Checked
	// against loopCfg (not run.Stream) so it reads the value actually in effect.
	loopCfg.InterimContentHidden = loopCfg.Stream == nil
	llmMessages, gDecline := subTurn.applyInputGuardrail(llmMessages)
	// A terminal-rule pre_input block refused this request outright; the loop
	// delivers the decline without calling a model.
	loopCfg.PreEmptedReply = gDecline
	resp, transcript, runErr := T.RunAgentLoop(ctx, llmMessages, loopCfg)
	Log("[orchestrate.RunAgentSyncContinuing] owner=%s runtime=%s target=%s sub=%s prior_msgs=%d msg_chars=%d err=%v",
		agentOwner, runtimeUser, target.ID, subSessionID, len(priorSession.Messages), len(message), runErr)
	// A superseded/cancelled turn is CANCELED, not FAILED.
	liveRun.Complete(runOutcomeStatus(runErr, resp != nil))
	if runErr != nil {
		return AgentSyncResult{}, runErr
	}
	if resp == nil {
		return AgentSyncResult{}, errors.New("agent returned no response")
	}
	cleanReply := strings.TrimSpace(resp.Content)
	// Round-cap fallback: the loop ran out of its budget without producing any
	// text (and even the loop's forced-final-answer rescue came up empty). Don't
	// hand the caller (channel reply, MCP client, inline dispatch) an empty
	// string — that reads as the agent silently doing nothing. Surface an
	// explicit out-of-rounds note so there's always a reply. Mirrors the web
	// runner's HitRoundCap fallback.
	if cleanReply == "" && resp.HitRoundCap {
		cleanReply = "I ran out of working rounds before I could finish this and didn't have a partial answer to show. Try narrowing the request, or ask me to continue."
	}
	// Empty on a CLEAN finish is the other shape, and the one that used to
	// vanish: the loop ended without error, without exhausting its rounds, and
	// with nothing to say. Downstream that becomes a channel's "I wasn't able to
	// put together a response", which reads as a comprehension failure and sends
	// the user off rephrasing a request that was understood perfectly well —
	// while the actual cause (a round that called tools and never spoke) leaves
	// no trace anywhere. Say what ran.
	// Every tool this turn ran, paired call-to-result. Computed once here and
	// used three ways below: the empty-reply diagnostic, the staged-deliverable
	// recovery, and — the one that was missing — the stored message itself.
	turnToolCalls := persistedToolCallsFromTranscript(transcript)
	if cleanReply == "" && !resp.HitRoundCap {
		trace := turnToolCalls
		detail := "The agent finished without producing any reply text"
		if n := len(trace); n > 0 {
			detail += fmt.Sprintf(" after %d tool call(s), the last being %s", n, trace[n-1].Name)
		} else {
			detail += " and called no tools"
		}
		detail += ". The caller substituted a fallback message. A turn that ends on a tool call without a closing sentence is the usual cause."
		Log("[orchestrate.RunAgentSyncContinuing] EMPTY REPLY owner=%s agent=%s sub=%s tools=%d", agentOwner, target.ID, subSessionID, len(trace))
		appendSessionDiag(runtimeDB, target.ID, subSessionID, "empty-reply", detail)
	}
	// Persist the new exchange for the next continuation.
	now := time.Now()
	// Sender carries the author for channel-room transcripts: the inbound
	// contact on the user message, the bound agent on its reply. Both empty for
	// plain dispatch sessions (no MessageSender passed), leaving web sessions
	// anonymous as before.
	assistantSender := ""
	if run.MessageSender != "" {
		assistantSender = target.Name
	}
	// Persist under the per-session append lock and re-read first, so a
	// recorded-only channel message that was mirrored into this same session
	// WHILE the run was in flight (recordChannelSilent) isn't clobbered by our
	// stale in-memory copy. The stored thread is only ever appended to (no
	// concurrent run rewrites it — same-session dispatches serialize through the
	// coalescer), so anything past the count we loaded is a mid-run mirror to graft.
	// What this reply is sending out goes to the transport as bytes and to the
	// stored thread as ids, so opening the conversation on the web later shows
	// the picture the contact received rather than a reply describing one.
	deliveredIDs := keepDeliveredAttachments(subSess.Username, subSess.Images)
	withSessionAppend(target.ID, subSessionID, func() {
		baseCount := len(priorSession.Messages)
		if latest, ok := loadChatSession(runtimeDB, target.ID, subSessionID); ok && len(latest.Messages) > baseCount {
			priorSession.Messages = append(priorSession.Messages, latest.Messages[baseCount:]...)
		}
		priorSession.Messages = append(priorSession.Messages,
			ChatMessage{Role: "user", Content: deliveredMessage, Created: now, Sender: run.MessageSender},
			// ToolCalls is what a replayed turn shows as its tool chips, and
			// this path never set it — so a channel turn opened later in
			// cortex showed the reply with no sign of the six tools behind it,
			// while the same turn run from web chat showed all six. The trace
			// was already being computed on this path for the diagnostics and
			// then dropped. Same field, same shape, same renderer as runner.go
			// and scheduled_updates.go; a rule on one side of that symmetry is
			// a bug.
			ChatMessage{Role: "assistant", Content: cleanReply, Created: now, Sender: assistantSender, Attachments: deliveredIDs, ToolCalls: turnToolCalls},
		)
		// Bound STORAGE the same way the Cortex home thread does (runner.go
		// handleSend): drop leading messages already folded into the summary
		// AND archived to recall, cursor kept consistent. Without this, a
		// long-lived channel / phantom thread grew without limit and was
		// loaded whole every turn.
		priorSession.Messages = T.trimStoredHistory(runtimeDB, target, subSessionID, priorSession.Messages)
		if _, serr := saveChatSession(runtimeDB, priorSession); serr != nil {
			Log("[orchestrate.RunAgentSyncContinuing] WARN failed to persist sub-session %s: %v", subSessionID, serr)
		}
	})
	// Attachments: the agent may deliver an image either by calling
	// workspace(action="attach") — which folds into subSess.Images — OR by the
	// fire-and-forget [ATTACH: file] reply-text marker. The channel auto-reply
	// path (and any other AgentSync caller) forwards ONLY these Images and then
	// scrubs the marker text downstream, so resolving the marker HERE is what
	// keeps a marker-delivered image from silently vanishing. Same helper the
	// phantom messaging tools use, so channel replies reach parity with them.
	// cleanReply still carries the marker at this point (textutil.StripMetaTags runs later).
	// Close the books on what this turn said it would do. This is the path the
	// observed failure took: "On it — let me grab some reference photos", no tool
	// call, turn over — and then the next message ("are you really?") answered by
	// a model with no idea it had promised anything.
	recordTurnCommitment(runtimeDB, subSessionID, cleanReply, len(turnToolCalls) > 0)
	imgs, vids := collectMessageMedia(subSess, cleanReply)
	// Phantom-delivery backstop: the model produced a file (find/generate/fetch)
	// but never called workspace(attach), then wrote a reply CLAIMING it sent it
	// ("here are the pics") — so collectMessageMedia found nothing and the reply
	// would go out with no attachment. Recover the staged file the claim refers
	// to and attach it, turning the phantom delivery into a real one. Scoped to a
	// delivery claim (the model's own vetting signal), so it only ships what the
	// model said it's sending. Does NOT depend on the model doing anything.
	phantomDelivery := false
	if len(imgs) == 0 && len(vids) == 0 {
		// Name the markers that pointed at nothing. This is the shape the logs
		// kept showing: a reply consisting of ONE delivery marker, the file
		// already cleaned up by an earlier attach, the marker resolving to
		// nothing, and the whole reply stripping to empty — delivered to the
		// contact as "I wasn't able to put together a response to that".
		if missing := unresolvedAttachMarkers(subSess, cleanReply); len(missing) > 0 {
			// Nothing attached, nothing recoverable, and the reply names a file
			// that does not exist: the turn promised a picture it never made.
			// Carried out so the caller can say something true about it instead
			// of asking the person to rephrase a request that was fine.
			phantomDelivery = true
			Log("[orchestrate.dispatch] reply carried %d delivery marker(s) that resolve to nothing: %v", len(missing), missing)
			appendSessionDiag(runtimeDB, target.ID, subSessionID, "attach-marker-unresolved",
				fmt.Sprintf("The reply asked to send %v, but no such file was in the workspace — most often because an earlier attach already delivered it with cleanup=true. Nothing was attached; the framework recovered the most recent staged file where it could.", missing))
		}
		if staged := recoverStagedDeliverable(subSess, cleanReply, turnProducedDeliverable(turnToolCalls)); staged != "" {
			if b64 := resolveWorkspaceImages(subSess, []string{staged}); len(b64) > 0 {
				Log("[orchestrate.dispatch] reply claimed a delivery but attached nothing — backstop attaching staged %q", staged)
				if isVideoAttachment(staged) {
					vids = append(vids, b64...)
				} else {
					imgs = append(imgs, b64...)
				}
			}
		}
	}
	// Status already finalized above (runOutcomeStatus) — reaching here is the
	// success case, already marked Completed.
	if len(imgs) > 0 || len(vids) > 0 {
		phantomDelivery = false // the backstop recovered something after all
	}
	return AgentSyncResult{Text: cleanReply, Images: imgs, Videos: vids, HitRoundCap: resp.HitRoundCap, PhantomDelivery: phantomDelivery, ToolsUsed: toolNamesFromTranscript(transcript), Silenced: subSess != nil && subSess.Silenced}, nil
}

// toolNamesFromTranscript reads the tools a run called out of the transcript
// the loop already returns — no new plumbing, and it cannot drift from what
// actually ran. First-use order, deduped: "searched twice then sent one
// message" is the same awareness as "searched, sent a message", and the
// standing thread pays for every line it keeps.
func toolNamesFromTranscript(msgs []Message) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			brief := toolCallBrief(tc)
			if brief == "" || seen[brief] {
				continue
			}
			seen[brief] = true
			out = append(out, brief)
		}
	}
	return out
}

// toolCallBriefKeys are the argument names worth showing, in priority order.
// A tool call's identity lives in one or two of these: for a shell call it is
// the command, for a search the query, for a grouped tool the action. Ordered
// rather than alphabetical because "which of these is the interesting one" is
// a judgement, not a sort.
var toolCallBriefKeys = []string{
	"command", "cmd", "script", "query", "q", "url", "path", "file",
	"action", "prompt", "message", "to", "name", "id",
}

// toolCallBrief renders one call as "name(salient args)".
//
// The names alone were what the cortex card recorded, and "used: shell" tells
// the owner nothing — the command IS the content of a shell call, and a
// standing thread reading its own history could not see what it had already
// done. Two arguments at most and every value clipped: this is a pointer to
// what happened, in a feed whose whole design rule is pointers rather than
// bodies.
func toolCallBrief(tc ToolCall) string {
	name := strings.TrimSpace(tc.Name)
	if name == "" {
		return ""
	}
	if len(tc.Args) == 0 {
		return name
	}
	var parts []string
	used := map[string]bool{}
	add := func(k string) {
		v, ok := tc.Args[k]
		if !ok || len(parts) >= 2 || used[k] {
			return
		}
		if sv := briefArgValue(v); sv != "" {
			used[k] = true
			// The key is noise when it is already implied by the tool ("shell",
			// "command"); keep it when it disambiguates ("action").
			if k == "command" || k == "cmd" || k == "script" || k == "query" || k == "q" || k == "prompt" {
				parts = append(parts, sv)
			} else {
				parts = append(parts, k+"="+sv)
			}
		}
	}
	for _, k := range toolCallBriefKeys {
		add(k)
	}
	// Nothing recognized — fall back to a stable pick so the brief is still
	// more than a bare name.
	if len(parts) == 0 {
		keys := make([]string, 0, len(tc.Args))
		for k := range tc.Args {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			add(k)
		}
	}
	if len(parts) == 0 {
		return name
	}
	return name + "(" + strings.Join(parts, ", ") + ")"
}

// briefArgValue renders one argument compactly, or "" for something not worth
// showing (an empty value, or a nested structure that would swamp the line).
func briefArgValue(v any) string {
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case bool, int, int64, float64:
		s = fmt.Sprint(t)
	case []any:
		return fmt.Sprintf("[%d items]", len(t))
	case map[string]any:
		return fmt.Sprintf("{%d fields}", len(t))
	default:
		return ""
	}
	s = strings.Join(strings.Fields(s), " ") // collapse newlines: one card line
	if s == "" {
		return ""
	}
	const max = 60
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
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
	return "[DELEGATED INVOCATION] Headless one-shot run — no back-and-forth; work from the brief as a self-contained spec, making reasonable defaults for anything unspecified.\n\nBrief: " + msg
}

// attributeSender prefixes a user message with its author's name so the LLM
// reads a multi-party thread by who-said-what — the same "who: text" rendering
// the read_chat tool uses (channel_tools.go). Only user turns are attributed;
// assistant turns are the agent's own. A no-op when no sender is known (plain
// dispatch / web sessions), so those stay anonymous as before. Content stays
// clean in storage — attribution is a render-time concern applied identically
// to history and the live turn, keeping the prompt prefix cache-stable.
func attributeSender(role, sender, content string) string {
	if role != "user" {
		return content
	}
	if sender = strings.TrimSpace(sender); sender == "" {
		return content
	}
	return sender + ": " + content
}

// llmHistoryContent renders ONE stored message the way the LLM must read it.
//
// A stored message carries its authorship in two disjoint fields: Sender names
// the person who typed a user turn in a multi-party room, and ReportFrom marks
// an assistant-role card whose body is something that HAPPENED rather than
// something the agent said. Both are attribution, and dropping either one
// rewrites who said what.
//
// This exists because the two history builders each applied one and ignored the
// other: the web-chat builder attached the report marker but discarded Sender,
// so every participant in a group room collapsed into one anonymous "user"
// voice — reading, to a model answering the owner, as the owner having said all
// of it; the dispatch builder attributed Sender but discarded ReportFrom, so an
// observation card carrying somebody else's message read as the agent's own
// past words. Same thread, two builders, two different ways to lose the speaker.
// Both call this now; neither renders history on its own.
func llmHistoryContent(m ChatMessage) string {
	// Automated reports store a clean body (the UI shows the producer in a card
	// header); re-attach an origin marker for the LLM so it reads as an
	// automated report, not something it said itself. Wrapped in <gohort-meta>
	// so that if the model echoes it into a reply it's scrubbed (a bare
	// [standing agent …] would leak); the model still reads it as input
	// (StripMetaTags only touches output).
	if strings.TrimSpace(m.ReportFrom) != "" {
		return fmt.Sprintf("<gohort-meta>automated report from %q — context, not user input</gohort-meta>\n%s", strings.TrimSpace(m.ReportFrom), m.Content)
	}
	return attributeSender(m.Role, m.Sender, m.Content)
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
	// A name the user gave their OWN agent beats a framework seed or app agent
	// carrying the same one, at EVERY tier below.
	//
	// The registry of app agents is process-global and its entries are hidden,
	// so a user naming an agent "Investigator" has no way to know one already
	// answers to that. Before this, whichever record listAgents happened to
	// emit first won, which meant the answer depended on registration order —
	// on which apps are compiled in. A caller asking for the agent they built
	// and named would be handed a hidden framework agent instead, and nothing
	// would say so.
	//
	// Precedence, not a tighter match: the tiers keep their own semantics
	// (exact beats normalized beats tag-stripped beats unique-partial, and each
	// fuzzy tier still refuses on a genuine ambiguity). They just run over the
	// user's own agents first and over the framework's only if that finds
	// nothing.
	own, framework := splitOwnAndFrameworkAgents(listAgents(udb, owner))
	for _, group := range [][]AgentRecord{own, framework} {
		if a, ok := matchAgentByName(group, key); ok {
			return a, true
		}
	}
	return AgentRecord{}, false
}

// splitOwnAndFrameworkAgents divides a listing into the agents the user made
// and the ones the framework supplied (orchestrate's own seeds plus every
// registered app agent — isSeedID walks both).
//
// Keyed on the ID via the registry rather than on Owner, for the reason
// appAgentForcesPrivate spells out: a customized seed carries a per-user shadow
// whose Owner IS the user, so Owner would call it theirs and hand a shadowed
// framework agent the same precedence as one they built and named.
func splitOwnAndFrameworkAgents(agents []AgentRecord) (own, framework []AgentRecord) {
	for _, a := range agents {
		if isSeedID(a.ID) {
			framework = append(framework, a)
			continue
		}
		own = append(own, a)
	}
	return own, framework
}

// matchAgentByName runs the name-resolution tiers over one group of agents,
// strongest first. Split out of findAgentByNameOrID so the same cascade can be
// applied to the user's own agents and then, only if it comes up empty, to the
// framework's.
func matchAgentByName(agents []AgentRecord, key string) (AgentRecord, bool) {
	if len(agents) == 0 {
		return AgentRecord{}, false
	}
	low := strings.ToLower(key)
	for _, a := range agents {
		if strings.ToLower(a.Name) == low {
			return a, true
		}
	}
	// Slug-tolerant fallback: a target stored as "wiwee-summary" must still
	// resolve to an agent named "WiWee Summary" or "wiwee_summary". Standing
	// agents store the raw name string typed at creation, so any separator or
	// case drift between that string and the agent's Name would otherwise
	// orphan the schedule while the agent still lists fine. Matched last so an
	// exact name always wins over a normalized collision.
	keyNorm := normalizeAgentKey(key)
	for _, a := range agents {
		if normalizeAgentKey(a.Name) == keyNorm {
			return a, true
		}
	}
	// Tag-tolerant fallback, matched last. Agent names carry display tags in
	// brackets — "Kwik [Cortex]", "Market Research [Fleet]" — which is this
	// framework's OWN convention, printed in every listing an agent reads. So
	// a caller naturally writes the name without the tag, and exact matching
	// then fails on an agent that plainly exists: reported live as Builder
	// looking up "Moltbook Conversational Agent", being told it was not found,
	// and having to stop and ask which agent was meant while the agent sat in
	// the list it had just read.
	//
	// Ambiguity is NOT resolved by guessing. If two agents share a base name
	// and differ only by tag, the bare name means neither, and the caller gets
	// the not-found path — where the suggestion list names both and the choice
	// stays theirs.
	if base := stripAgentTag(keyNorm); base != keyNorm {
		if a, ok := uniqueAgentByBaseName(agents, base); ok {
			return a, true
		}
	}
	var matches []AgentRecord
	for _, a := range agents {
		if stripAgentTag(normalizeAgentKey(a.Name)) == keyNorm {
			matches = append(matches, a)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	// Last resort: a UNIQUE partial name. "moltbook" identifies "Moltbook
	// Conversational Agent [Cortex]" as surely as the whole string does, and
	// callers shorten names — a model reading a fleet listing writes the
	// distinctive word, not the four-word title with its tag. Reported live:
	// Builder looked up "moltbook", was told no such agent existed, and
	// repeated that to the user as fact about an agent it had just seen listed.
	//
	// Same rule as everywhere above: unique or nothing. A prefix matching two
	// agents means neither, because resolving it would edit whichever happened
	// to sort first — and an authoring tool silently rewriting the wrong agent
	// is the one outcome worse than a failed lookup.
	if a, ok := uniqueAgentByPartialName(agents, keyNorm); ok {
		return a, true
	}
	return AgentRecord{}, false
}

// uniqueAgentByPartialName resolves a name fragment when exactly one agent
// contains it. Prefix matches are preferred over interior ones: "research"
// should mean "Research Agent" rather than "Deep Dive Research", and only
// falls through to interior matching when no name begins with the fragment.
func uniqueAgentByPartialName(agents []AgentRecord, frag string) (AgentRecord, bool) {
	if len(frag) < 3 {
		// Too short to identify anything. A two-letter fragment matching one
		// agent today matches three after the next one is added, so the
		// resolution would be correct only until the fleet grew.
		return AgentRecord{}, false
	}
	var prefix, interior []AgentRecord
	for _, a := range agents {
		name := stripAgentTag(normalizeAgentKey(a.Name))
		switch {
		case strings.HasPrefix(name, frag):
			prefix = append(prefix, a)
		case strings.Contains(name, frag):
			interior = append(interior, a)
		}
	}
	if len(prefix) == 1 {
		return prefix[0], true
	}
	if len(prefix) == 0 && len(interior) == 1 {
		return interior[0], true
	}
	return AgentRecord{}, false
}

// stripAgentTag removes a trailing bracketed display tag from an already-
// normalized name: "kwik [cortex]" becomes "kwik".
func stripAgentTag(norm string) string {
	i := strings.LastIndexByte(norm, '[')
	if i <= 0 || !strings.HasSuffix(strings.TrimSpace(norm), "]") {
		return norm
	}
	return strings.TrimSpace(norm[:i])
}

// uniqueAgentByBaseName returns the single agent whose tag-stripped name
// matches, or reports false when none or several do.
func uniqueAgentByBaseName(agents []AgentRecord, base string) (AgentRecord, bool) {
	var found AgentRecord
	n := 0
	for _, a := range agents {
		if stripAgentTag(normalizeAgentKey(a.Name)) == base {
			found, n = a, n+1
		}
	}
	return found, n == 1
}

// suggestAgents lists the agents closest to a name that did not resolve.
//
// "agent %q not found" against a fleet of thirty-eight is a dead end: the
// caller cannot tell whether the agent is missing, renamed, or spelled
// differently, and an LLM's next move is to guess again or to stop and ask.
// Naming the near misses turns that into a correction. Mirrors the "did you
// mean" the tool loop already offers for tool names.
func suggestAgents(agents []AgentRecord, key string) string {
	want := stripAgentTag(normalizeAgentKey(key))
	if want == "" {
		return ""
	}
	var near []string
	for _, a := range agents {
		got := stripAgentTag(normalizeAgentKey(a.Name))
		if strings.Contains(got, want) || strings.Contains(want, got) {
			near = append(near, a.Name)
		}
	}
	if len(near) == 0 {
		return ""
	}
	sort.Strings(near)
	if len(near) > 5 {
		near = near[:5]
	}
	return " Did you mean: " + strings.Join(near, ", ") + "?"
}

// normalizeAgentKey lowercases and collapses separator runs (- _ and
// whitespace) to a single space, so display-name vs slug drift doesn't
// break name resolution. "WiWee Summary", "wiwee-summary", and
// "wiwee_summary" all normalize to "wiwee summary".
func normalizeAgentKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevSep := false
	for _, r := range s {
		if r == '-' || r == '_' || r == ' ' || r == '\t' {
			if !prevSep && b.Len() > 0 {
				b.WriteByte(' ')
			}
			prevSep = true
			continue
		}
		b.WriteRune(r)
		prevSep = false
	}
	return strings.TrimSpace(b.String())
}

// dispatchToAgentToolDef removed — the LLM-facing dispatch surface
// now lives on the grouped `agents` tool (action="run") in
// agents_grouped_tool.go. RunAgentSync above is the cross-app
// dispatch path (Phantom → Agency); the in-LLM path is the agents
// tool's run action. Both share the same plumbing (delegated marker,
// Builder-exclusivity gate, sub-session setup, target memory/facts
// loading).
