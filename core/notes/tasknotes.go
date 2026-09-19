// Notes that belong to a TASK rather than to an agent.
//
// Same store, same cap, same section splice: what differs is the namespace and
// the lifetime. An agent's notes are permanent and live at "agent:<id>"; a
// task's are deleted when the task is, and live at "task:<surface>:<owner>:<id>".
//
// The lifetime IS the distinction. A working-notes block scoped to an agent
// outlives the work it describes, which is where every defect of that layer
// came from: a stale note steering every turn, a parked call outliving the tool
// it names, an audit rule to find both. A task's notes end with the task, so
// none of those can happen, and the model needs no rule for telling the two
// apart because it is never offered both at once.
//
// See docs/task-notes.md.

package notes

import "strings"

// The scheduling surfaces that carry notes. They are part of the key because
// ids are only unique WITHIN a surface: a standing agent and a monitor may both
// be called "nightly", and handing one the other's running state would be a
// silent cross-task leak rather than a visible error.
const (
	TaskSurfaceRecurring = "recurring"
	TaskSurfaceStanding  = "standing"
	TaskSurfaceMonitor   = "monitor"
)

// TaskNamespace is the notes row for one task. Owner is in the key because two
// people's schedules can share a name, and because the row lives in a store
// shared across the deployment, beside the task records themselves.
//
// Empty when anything it needs is missing, and every caller treats an empty
// namespace as "this task has no notes": a row keyed on a blank id would be one
// bucket that every unidentifiable task wrote into.
func TaskNamespace(surface, owner, id string) string {
	surface, owner, id = strings.TrimSpace(surface), strings.TrimSpace(owner), strings.TrimSpace(id)
	if surface == "" || owner == "" || id == "" {
		return ""
	}
	return "task:" + surface + ":" + owner + ":" + id
}

// RenderTaskNotesBlock is what a fire carries: the task's notes, or, when
// nothing has been written yet, one line saying the register exists.
//
// The empty state is the whole reason this is not RenderOperatingNotesBlock,
// which renders "" when there is nothing stored. That is what kept the
// agent-wide layer from ever starting: the framing that teaches it lives INSIDE
// the block, so an agent with notes enabled and nothing written was never told
// it had them, and the only way to bootstrap was a seed somebody wrote by hand.
// A task cannot be configured with a seed by anyone, so the empty line is the
// seed, and it costs about a hundred characters on the fires that never use it.
//
// Bracketed and one paragraph because this rides the VOLATILE TAIL beside the
// objective block, not the cached prefix: it changes every fire by definition,
// and the tail is the part of the prompt that never caches anyway.
func RenderTaskNotesBlock(n OperatingNotes) string {
	text := strings.TrimSpace(n.Text)
	if text == "" {
		return "[Notes for this task: nothing recorded yet. If this run works something out that the next one should not have to rediscover, write it down with update_notes. This block is the only thing that survives between runs.]"
	}
	var b strings.Builder
	b.WriteString("[Notes for this task, left by earlier runs. Keep them current by rewriting rather than appending: update_notes replaces the whole block, or one section of it. They are the only thing that survives between runs, so what is not here is lost.\n\n")
	b.WriteString(text)
	b.WriteString("]")
	return b.String()
}
