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
		tools = append(tools, builderInternalTools(subSess)...)
	}
	// Facts read from the RUNTIME user's DB, so dispatches from
	// different phantom chats see isolated state for the same agent
	// record. First call against a fresh runtimeUser starts clean —
	// no leakage from the owner's interactive Explicit Memory.
	subFacts := ListMemoryFacts(runtimeDB, factsNamespace(target.ID))
	sysPrompt := prependAgentContext(target.OrchestratorPrompt, target, subFacts)
	// Mark delegated invocations so the target agent knows there's no
	// human on the other end of ask_user / ask_user_form. Builder
	// keys off this marker to skip Phase 1 conversation + the Phase 3
	// confirmation pause; other agents can ignore it.
	deliveredMessage := markAsDelegated(message)
	f := false
	resp, _, runErr := T.RunAgentLoop(ctx, []Message{{Role: "user", Content: deliveredMessage}}, AgentLoopConfig{
		SystemPrompt: sysPrompt,
		Tools:        tools,
		MaxRounds:    resolveMaxWorkerRounds(target),
		Confirm:      func(name, args string) bool { return true },
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(f),
		},
	})
	Log("[orchestrate.RunAgentSync] owner=%s runtime=%s target=%s msg_chars=%d err=%v",
		agentOwner, runtimeUser, target.ID, len(message), runErr)
	if runErr != nil {
		return "", runErr
	}
	if resp == nil {
		return "", errors.New("agent returned no response")
	}
	return strings.TrimSpace(resp.Content), nil
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
