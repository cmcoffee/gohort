package orchestrate

import . "github.com/cmcoffee/gohort/core"

// agentTurnWorkspace answers where a turn for one agent should run, and what
// it may read outside that.
//
// Every ordinary agent turn runs in .agents/<user>/<agent>/ so one agent
// cannot list a file another produced and report it as its own work. Paths
// that build a session by hand — a scheduled fire, a task wake, a monitor's
// tool invoker — kept using the shared user root instead, and the divergence
// was invisible until an artifact had to survive the trip between them: a
// background download wrote video-<id>.mp4 into the agent's directory, the
// wake resolved that bare name against the root, and the model recovered the
// only way it could, by downloading the whole clip again.
//
// fallback is the user's own root, for READS only (see ResolveWorkspaceRead),
// so files a person put there stay reachable from any agent. It is empty when
// the turn is already running at the root — there is nothing to fall back to.
func agentTurnWorkspace(owner, agentID string) (dir, fallback string) {
	if ws, err := EnsureAgentWorkspaceDir(owner, agentID); err == nil {
		dir = ws
	} else if ws, err := EnsureWorkspaceDir(owner); err == nil {
		return ws, ""
	}
	if root, err := EnsureWorkspaceDir(owner); err == nil && root != dir {
		fallback = root
	}
	return dir, fallback
}

// turnWorkspace answers where THIS turn is running: the directory, the managed
// workspace id when it is one, and what it may read outside that.
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
// Otherwise the turn runs in ITS AGENT's directory, not the shared user root.
// One agent could otherwise list a file another produced, attach it, and report
// it as its own work — the same unanchored-reference failure the image ring is
// scoped per agent to avoid.
//
// fallback is the user's own root, for READS only (see ResolveWorkspaceRead),
// so files a person put there — or that predate per-agent directories — stay
// reachable from every agent. Writes never fall back. Empty when the turn is
// already running at the root, or in a managed workspace the caller chose.
func (t *chatTurn) turnWorkspace() (dir, wsID, fallback string) {
	if t.activeWorkspaceID != "" {
		if w, ok := LoadManagedWorkspace(t.activeWorkspaceID); ok && w.Owner == t.user {
			if d, derr := ManagedWorkspaceDir(w.Owner, w.ID); derr == nil {
				dir, wsID = d, w.ID
			}
		}
	}
	if dir == "" {
		dir, fallback = agentTurnWorkspace(t.user, t.agent.ID)
		return dir, "", fallback
	}
	if root, err := EnsureWorkspaceDir(t.user); err == nil && root != dir {
		fallback = root
	}
	return dir, wsID, fallback
}
