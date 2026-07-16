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
	if !target.ForcePrivate && NetworkAllowedFromContext(ctx) {
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
	return extraTools, availableBlock, customToolPrompt, subTurn
}

func (T *OrchestrateApp) RunAgentSync(ctx context.Context, agentOwner, runtimeUser, agentKey, message string) (string, error) {
	// Dispatch sub-agents auto-approve tool calls — a parent agent (with a
	// human behind it) initiated the dispatch. Standing/autonomous runs must
	// NOT auto-approve; they call runAgentSyncConfirm with a deny-by-default
	// confirm so high-consequence tools route through approval instead.
	return T.runAgentSyncConfirm(ctx, agentOwner, runtimeUser, agentKey, message,
		func(string, string) bool { return true })
}

func (T *OrchestrateApp) runAgentSyncConfirm(ctx context.Context, agentOwner, runtimeUser, agentKey, message string, confirm func(string, string) bool) (string, error) {
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
	}
	if ws, werr := EnsureWorkspaceDir(runtimeUser); werr == nil {
		subSess.WorkspaceDir = ws
	}
	defer clearAuthoringInProgress(runtimeDB, subSessID)
	defer DeleteSessionTempTools(runtimeDB, subSessID)
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
	// Builder gets its unregistered authoring tools appended here on
	// the sync-dispatch path. Identity-gated against the Builder
	// seed ID — a non-Builder target never receives the appendage.
	if isBuilderAgent(target.ID) {
		tools = append(tools, builderAuthoringTools(subSess, nil)...)
	}
	// Fleet targets get their exclusive fleet-management + delegation +
	// event-monitor catalog here too, so a dispatched/woken fleet agent
	// behaves the same as on its own chat surface. Mirrors the runner.go
	// catalog hook. Drop the generic interval scheduler (it schedules
	// through the fleet instead).
	if target.Fleet {
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
	if chTools := channelChatTools(subSess, agentOwner, target.ID); len(chTools) > 0 {
		tools = append(tools, chTools...)
	}
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
	resp, _, runErr := T.RunAgentLoop(ctx, []Message{{Role: "user", Content: deliveredMessage}}, AgentLoopConfig{
		SystemPrompt: sysPrompt,
		Tools:        tools,
		MaxRounds:    resolveMaxWorkerRounds(target),
		ThinkBudget:  target.ThinkBudget, // per-agent override; 0 = inherit route/global
		OnStep:       func(info StepInfo) { telem.record(info) },
		Confirm:      confirm,
		// Custom-tool resolution, same as the web runPlan: lazyToolFallback
		// resolves a direct call to a has-args custom tool; dynamicNewTempTools
		// surfaces tools the LLM loaded via load_tool this turn.
		ToolFallbackResolver: subTurn.lazyToolFallback,
		DynamicTools:         subTurn.dynamicNewTempTools(subSess),
		// Feed view_video's sampled frames to the model on the next round so a
		// channel agent (phantom) actually sees a reel it was asked to watch.
		DrainViewImages: subSess.DrainViewImages,
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
	Message          string
	FreshSession     bool
	StatusCallback   func(string) // optional: wired to the sub-session's StatusCallback
	// Title, when set, names a FRESH session (used only if it has no title
	// yet). Channel rooms pass the contact's display name so the rail row and
	// transcript read as the conversation partner rather than the raw id.
	Title string
	// MessageSender, when set, is stored as THIS message's author (ChatMessage
	// .Sender). Channel rooms pass the inbound contact's display name so a
	// GROUP thread renders real who-said-what — each inbound carries its own
	// sender, unlike Title which only names the session once.
	MessageSender string
	// Images carries decoded inbound image bytes (a contact's photo on a
	// channel) to ride on the user message as multimodal content the vision
	// model sees this turn. Empty for text-only callers.
	Images [][]byte
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
	MaxRounds     int              // >0 overrides the resolved default
	SerialTools   bool             // run tool calls one-at-a-time
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
	b.WriteString("\n[media on this turn. Each item below is shown to you directly as multimodal content you can see now; do NOT call any tool to fetch, download, or find it. Refer to a specific item by its id.]")
	for i := 1; i <= imageCount; i++ {
		fmt.Fprintf(&b, "\n  media#%d: image", i)
		if who != "" {
			b.WriteString(" from " + who)
		}
	}
	b.WriteString("\n  Everyone in THIS conversation already received these, so do NOT send one back here; re-attaching a photo that was just posted only echoes it to the same group. Reference a media#N only to FORWARD it to a DIFFERENT recipient, by passing attachments:[\"media#N\"] to a messaging tool. Refer to an item by its id in your text; do NOT retype or invent a filename for it, the id is its only handle.")
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
		ReplyAuthorizedKey: run.ReplyAuthorizedKey, // in-thread reply skips the send approval gate
	}
	if ws, werr := EnsureWorkspaceDir(runtimeUser); werr == nil {
		subSess.WorkspaceDir = ws
	}
	// Mid-turn status (channel path): the agent's send_status / progress
	// pings land here so the transport can deliver them before the final
	// reply. nil for the legacy text-only callers — graceful no-op.
	if run.StatusCallback != nil {
		subSess.StatusCallback = run.StatusCallback
	}
	defer clearAuthoringInProgress(runtimeDB, subSessionID)
	defer DeleteSessionTempTools(runtimeDB, subSessionID)
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
	if isBuilderAgent(target.ID) {
		tools = append(tools, builderAuthoringTools(subSess, nil)...)
	}
	// Fleet targets get their fleet-management + delegation + event-monitor
	// catalog here too — this is the WAKE path (event monitors run the
	// channel agent on its channel thread through RunAgentSyncContinuing),
	// so without it a woken fleet agent would have no delegate / monitor
	// tools.
	if target.Fleet {
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
	if chTools := channelChatTools(subSess, agentOwner, target.ID); len(chTools) > 0 {
		tools = append(tools, chTools...)
	}
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
		llmMessages = append(llmMessages, Message{Role: m.Role, Content: attributeSender(m.Role, m.Sender, m.Content)})
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
	loopCfg := AgentLoopConfig{
		SystemPrompt: sysPrompt,
		Tools:        tools,
		MaxRounds:    resolveMaxWorkerRounds(target),
		ThinkBudget:  target.ThinkBudget, // per-agent override; 0 = inherit route/global
		Confirm:      func(name, args string) bool { return true },
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
	// Feed view_video's sampled frames to the model on the next round.
	loopCfg.DrainViewImages = subSess.DrainViewImages
	resp, _, runErr := T.RunAgentLoop(ctx, llmMessages, loopCfg)
	Log("[orchestrate.RunAgentSyncContinuing] owner=%s runtime=%s target=%s sub=%s prior_msgs=%d msg_chars=%d err=%v",
		agentOwner, runtimeUser, target.ID, subSessionID, len(priorSession.Messages), len(message), runErr)
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
	withSessionAppend(target.ID, subSessionID, func() {
		baseCount := len(priorSession.Messages)
		if latest, ok := loadChatSession(runtimeDB, target.ID, subSessionID); ok && len(latest.Messages) > baseCount {
			priorSession.Messages = append(priorSession.Messages, latest.Messages[baseCount:]...)
		}
		priorSession.Messages = append(priorSession.Messages,
			ChatMessage{Role: "user", Content: deliveredMessage, Created: now, Sender: run.MessageSender},
			ChatMessage{Role: "assistant", Content: cleanReply, Created: now, Sender: assistantSender},
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
	imgs, vids := collectMessageMedia(subSess, cleanReply)
	// Phantom-delivery backstop: the model produced a file (find/generate/fetch)
	// but never called workspace(attach), then wrote a reply CLAIMING it sent it
	// ("here are the pics") — so collectMessageMedia found nothing and the reply
	// would go out with no attachment. Recover the staged file the claim refers
	// to and attach it, turning the phantom delivery into a real one. Scoped to a
	// delivery claim (the model's own vetting signal), so it only ships what the
	// model said it's sending. Does NOT depend on the model doing anything.
	if len(imgs) == 0 && len(vids) == 0 {
		if staged := recoverStagedDeliverable(subSess, cleanReply); staged != "" {
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
	return AgentSyncResult{Text: cleanReply, Images: imgs, Videos: vids, HitRoundCap: resp.HitRoundCap}, nil
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
	agents := listAgents(udb, owner)
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
	return AgentRecord{}, false
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
