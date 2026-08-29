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
