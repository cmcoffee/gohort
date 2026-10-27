package orchestrate

// The Memory pane's routes: an agent's facts, working notes, audit, search,
// graph, Reference Memory and knowledge, and its phantom sessions.
//
// Each surface is one route builder, served three ways: here for the signed-in
// user's agent named in the path, by the agents app for the agent its visitor
// is using (PublicHandle*), and by servitor for an appliance's scope
// (PublicHandle*ForScope). The builder says the methods once, so the three
// doors cannot drift apart.

import (
	"errors"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// memTarget is whose memory a call reads, which agent's, and the one entry it
// names (an entity, a chunk, a document, a session) when it names one.
type memTarget struct {
	user, agentID, item string
}

// memAt resolves a call to its target.
type memAt func(c *jsonapi.Call) memTarget

// ownAgent is the signed-in user's own memory of the agent the path names.
func ownAgent(c *jsonapi.Call) memTarget {
	return memTarget{user: c.User, agentID: c.R.PathValue("id"), item: c.R.PathValue("item")}
}

// callerAgent is the signed-in user's memory of an agent another app named.
func callerAgent(agentID, item string) memAt {
	return func(c *jsonapi.Call) memTarget { return memTarget{user: c.User, agentID: agentID, item: item} }
}

// scopeAgent is a scope's memory of an agent, for a caller that has already
// decided the signed-in user may see that scope.
func scopeAgent(scopeUser, agentID, item string) memAt {
	return func(*jsonapi.Call) memTarget { return memTarget{user: scopeUser, agentID: agentID, item: item} }
}

// memCall is a resolved target with its store and agent record.
type memCall struct {
	memTarget
	udb   Database
	agent AgentRecord
}

// memoryOf resolves at to the store and the agent the caller may read; a 404
// when the agent is not theirs to read.
func (T *OrchestrateApp) memoryOf(c *jsonapi.Call, at memAt) (memCall, error) {
	t := at(c)
	udb := UserDB(T.DB, t.user)
	if udb == nil {
		return memCall{}, errors.New("no store for user " + t.user)
	}
	if t.agentID == "" || strings.Contains(t.agentID, "/") {
		return memCall{}, errNotFound
	}
	a, ok := T.memoryAgent(c.R, udb, t.user, t.agentID)
	if !ok {
		return memCall{}, errNotFound
	}
	return memCall{memTarget: t, udb: udb, agent: a}, nil
}

// registerMemoryRoutes mounts the Memory pane's routes under /api/agents/{id}.
// registerAgentRoutes calls it beside the agent's other routes.
func (T *OrchestrateApp) registerMemoryRoutes() {
	at := ownAgent
	T.HandleFunc("/api/agents/{id}/facts", T.factsRoute(at))
	T.HandleFunc("/api/agents/{id}/notes", T.notesRoute(at))
	T.HandleFunc("/api/agents/{id}/memaudit", T.memoryAuditRoute(at))
	T.HandleFunc("/api/agents/{id}/memsearch", T.memorySearchRoute(at))
	T.HandleFunc("/api/agents/{id}/inferred", T.inferredListRoute(at))
	T.HandleFunc("/api/agents/{id}/inferred/{item}", T.inferredDeleteRoute(at))
	T.HandleFunc("/api/agents/{id}/graph", T.graphRoute(at))
	T.HandleFunc("/api/agents/{id}/graph/edge", T.graphEdgeDeleteRoute(at))
	T.HandleFunc("/api/agents/{id}/graph/entity/{item}", T.graphEntityDeleteRoute(at))
	T.HandleFunc("/api/agents/{id}/graph/entity/{item}/attr", T.graphAttrDeleteRoute(at))
	T.HandleFunc("/api/agents/{id}/graph/entity/{item}/alias", T.graphAliasDeleteRoute(at))
	T.HandleFunc("/api/agents/{id}/knowledge", T.knowledgeRoute(at))
	T.HandleFunc("/api/agents/{id}/knowledge/auto-inferred", T.autoInferredRoute(at))
	T.HandleFunc("/api/agents/{id}/knowledge/sources", T.knowledgeSourcesRoute(at))
	T.HandleFunc("/api/agents/{id}/knowledge/sources/{item}", T.knowledgeSourceDeleteRoute(at))
	T.HandleFunc("/api/agents/{id}/knowledge/scaffold-collection", jsonapi.Post(T.DB, T.handleAgentKnowledgeScaffoldCollection))
	T.HandleFunc("/api/agents/{id}/phantom-sessions", T.phantomSessionsRoute())
	T.HandleFunc("/api/agents/{id}/phantom-sessions/{item}", T.phantomSessionRoute())
}
