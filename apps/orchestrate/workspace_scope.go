package orchestrate

import (
	"context"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// workspaceFamily is the agent whose folder a's turns run in: for a sub-agent
// its top-level parent, which it works for and which delivers what it makes;
// for any other agent itself. udb holds the agent records.
func workspaceFamily(udb Database, a AgentRecord) string {
	if strings.TrimSpace(a.OwnedBy) == "" || udb == nil {
		return a.ID
	}
	return topAgentOf(udb, a.ID)
}

// workspaceFamilyOf is workspaceFamily for an agent known by id. An id with no
// record is its own family.
// init points core's per-family rule on managed workspaces at the agents'
// own families (core.WorkspaceFamilyOf).
func init() {
	WorkspaceFamilyOf = func(owner, agentID string) string {
		if orchestrateBaseDB == nil {
			return ""
		}
		return workspaceFamilyOf(UserDB(orchestrateBaseDB, owner), agentID)
	}
}

func workspaceFamilyOf(udb Database, id string) string {
	if a, ok := loadAgent(udb, id); ok {
		return workspaceFamily(udb, a)
	}
	return id
}

// agentWorkspace answers where a turn for one agent runs: its family's folder,
// and nothing outside it (core/workspace_agent.go).
//
// Every ordinary agent turn runs here, and so does every path that builds a
// session by hand (a scheduled fire, a task wake, a monitor's tool invoker): a
// background download wrote video-<id>.mp4 into the agent's directory while
// the wake resolved that bare name against the root, and the model recovered
// the only way it could, by downloading the whole clip again. Empty when the
// folder cannot be made, which disables the workspace tools rather than
// handing the turn somebody else's folder.
func agentWorkspace(udb Database, user string, a AgentRecord) string {
	ws, err := EnsureAgentWorkspaceDir(user, workspaceFamily(udb, a))
	if err != nil {
		return ""
	}
	return ws
}

// familyWorkspace is agentWorkspace for an agent known only by id, as a wake
// or a machine run knows it. No id is a run with no agent, which works at the
// user's own root.
func familyWorkspace(udb Database, user, agentID string) string {
	ws, err := EnsureAgentWorkspaceDir(user, workspaceFamilyOf(udb, agentID))
	if err != nil {
		return ""
	}
	return ws
}

// delegatedWorkspace is where a delegated run of target works: the folder the
// delegator was working in when it is of target's family, so a sub-agent
// producing something its parent will deliver writes where the parent is,
// a project workspace included; otherwise target's own family's folder. One
// family never works in another's folder: what it hands over goes as an
// attachment.
func delegatedWorkspace(ctx context.Context, udb Database, user string, target AgentRecord) string {
	if dir, from := InheritedWorkspace(ctx); dir != "" && from != "" &&
		workspaceFamilyOf(udb, from) == workspaceFamily(udb, target) {
		return dir
	}
	return agentWorkspace(udb, user, target)
}

// turnWorkspace answers where THIS turn is running: the directory, and the
// managed workspace id when it is one.
//
// Lifted out of newToolSession because a delegated sub-agent has to be able to
// ask the same question. It ran at the shared user root instead, and agents(run)
// hands its result back as TEXT — no attachments — so a file a sub-agent
// produced reached its parent only by path. The root was the one directory both
// could name, which made it work and made it the wrong answer: every delegated
// agent writing into one place, where a name collides with another agent's file
// and any agent can list, attach and report work it did not do.
//
// A prior step may have switched this turn to a managed workspace via
// workspace(create/use); restore it so multi-step authoring shares one
// workspace — a script written in step N is visible to a run in step N+1. Falls
// through when that workspace is gone (deleted, or not ours).
//
// Otherwise the turn runs in its agent family's folder (agentWorkspace).
func (t *chatTurn) turnWorkspace() (dir, wsID string) {
	if t.activeWorkspaceID != "" {
		if w, ok := LoadManagedWorkspace(t.activeWorkspaceID); ok && w.Owner == t.user &&
			(w.Family == "" || w.Family == workspaceFamily(t.udb, t.agent)) {
			if d, derr := ManagedWorkspaceDir(w.Owner, w.ID); derr == nil {
				return d, w.ID
			}
		}
	}
	return agentWorkspace(t.udb, t.user, t.agent), ""
}

// delegateWorkspace is where an agent this turn runs inline (agents(run))
// works, and the managed workspace id when it is one.
//
// A sub-agent of this turn's family runs WHERE THIS TURN IS RUNNING, a project
// workspace included: agents(run) hands back attachments but not files, so a
// file it produces reaches its parent by path, the way its parent will
// deliver it. Any other agent works in its own family's folder, and what it
// hands back goes as an attachment: one family reading another's files is
// what per-agent folders exist to stop.
//
// A ctx stamped by RunDelegation wins when it is of the family, since a chain
// deeper than one hop should stay in the workspace the chain started in
// rather than hop into each new delegate's.
func (t *chatTurn) delegateWorkspace(target AgentRecord) (dir, wsID string) {
	family := workspaceFamily(t.udb, target)
	if family != workspaceFamily(t.udb, t.agent) {
		return agentWorkspace(t.udb, t.user, target), ""
	}
	if d, from := InheritedWorkspace(t.ctx); d != "" && from != "" && workspaceFamilyOf(t.udb, from) == family {
		return d, ""
	}
	return t.turnWorkspace()
}
