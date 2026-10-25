package orchestrate

// Build notes: the standing account of an agent family, for whoever changes it
// next. The agent-side twin of an app's notes (AppSpec.Notes).
//
// One set per family, held on the top-level agent. Everything else about a
// family is decided there already (limits, contacts, privacy, schedules), so a
// decision that binds every sub-agent ("none of them gets send_message; the
// router picks on topic, not tone") is written there too, and a sub-agent
// added later is checked against it. They are for authors, never the agent:
// nothing here reaches a run's prompt, so they do not compete with the persona.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// agentBuildNotesCap bounds BuildNotes, in runes: the same bound as an app's
// notes, for the same reason. A summary somebody reads before editing, not a
// log nobody does.
const agentBuildNotesCap = 3000

// familyTop is the top-level agent of rec's family: rec itself when it has no
// parent, or when its parent is gone (an orphan counts as top-level).
func familyTop(db Database, rec AgentRecord) AgentRecord {
	up := agentAncestors(db, rec.ID)
	if len(up) == 0 {
		return rec
	}
	if top, ok := loadAgent(db, up[len(up)-1]); ok {
		return top
	}
	return rec
}

// buildNotesRefusal refuses build_notes the save cannot keep: on a sub-agent
// (they are its parent's) or over the cap. Clearing them is always allowed, so
// notes left on an agent from before it moved can be dropped.
func buildNotesRefusal(db Database, rec AgentRecord, args map[string]any) string {
	if _, ok := args["build_notes"]; !ok {
		return ""
	}
	notes := strings.TrimSpace(stringArg(args, "build_notes"))
	if notes == "" {
		return ""
	}
	// Read through the parent, not rec: rec may be about to be saved under a
	// new one (a create, or a move in the same call), and its stored row
	// still names where it was. A parent that is gone leaves an orphan, which
	// counts as top-level.
	if p := strings.TrimSpace(rec.OwnedBy); p != "" {
		if parent, ok := loadAgent(db, p); ok {
			top := familyTop(db, parent)
			return fmt.Sprintf("build_notes are kept on the top-level agent of a family, here %s (id=%s): write them there with update_agent, as notes for the whole family. Nothing was saved.", top.Name, top.ID)
		}
	}
	if n := len([]rune(notes)); n > agentBuildNotesCap {
		return fmt.Sprintf("build_notes is %d characters over the %d-character cap: it is a standing SUMMARY (purpose, decisions and why, open items), not a log. A record of what changed belongs in the agent's version history, not here; trim and resend. Nothing was saved.", n-agentBuildNotesCap, agentBuildNotesCap)
	}
	return ""
}

// familyNotesHint is the family's build notes, for a tool result about one of
// its sub-agents, or "" when rec is top-level or the family has none.
func familyNotesHint(db Database, rec AgentRecord) string {
	if strings.TrimSpace(rec.OwnedBy) == "" {
		return ""
	}
	top := familyTop(db, rec)
	notes := strings.TrimSpace(top.BuildNotes)
	if top.ID == rec.ID || notes == "" {
		return ""
	}
	return fmt.Sprintf("\n\nBUILD NOTES for %s's family (on %s), which bind %s:\n%s", top.Name, top.Name, rec.Name, notes)
}

// familyNotesDone is the closing directive for a create or update result that
// carries familyNotesHint: the one case where a further update_agent is the
// right next call rather than a loop.
func familyNotesDone(rec AgentRecord) string {
	return fmt.Sprintf("DONE, unless %s departs from the family's build notes below: then fix that with ONE update_agent (or change the notes on the top-level agent, if the decision itself changed). Otherwise reply with a short summary and END THE TURN.", rec.Name)
}
