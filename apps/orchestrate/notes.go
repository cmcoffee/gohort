// update_notes — the LLM-in-band "Working notes" layer. A single bounded,
// REWRITABLE block of running state, distinct from store_fact (append durable
// rules) and memory_save (semantic pull-only recall). Opt-in per agent via
// AgentRecord.EnableNotes; Builder can seed the initial text via SeedNotes.
//
// Storage reuses the fact layer's per-(user, agent) scope: the same
// "agent:<id>" namespace in the caller's per-user sub-store, a different table
// (core_notes). See core/notesstore.go.

package orchestrate

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// agentOperatingNotes returns the effective Working notes for an agent, gated
// on EnableNotes and falling back to the record's SeedNotes when the store is
// empty. Used by the non-chatTurn prompt-assembly sites (dispatch, scheduled,
// eval). Returns the zero value (renders nothing) when notes are off.
func agentOperatingNotes(db Database, agent AgentRecord) OperatingNotes {
	if !agent.EnableNotes {
		return OperatingNotes{}
	}
	return ResolveOperatingNotes(db, factsNamespace(agent.ID), agent.SeedNotes)
}

// operatingNotes is the chatTurn variant: same as agentOperatingNotes but also
// suppresses notes in an incognito session (no stored state surfaced), matching
// how facts() is nil'd for incognito turns.
func (t *chatTurn) operatingNotes() OperatingNotes {
	if !t.agent.EnableNotes {
		return OperatingNotes{}
	}
	if t.session != nil && t.session.Incognito {
		return OperatingNotes{}
	}
	return ResolveOperatingNotes(t.udb, factsNamespace(t.agent.ID), t.agent.SeedNotes)
}

// updateNotesToolDef binds the always-in-prompt Working notes block. Unlike
// store_fact (append an atomic rule), this REPLACES the whole block — the agent
// keeps a compact, current running-state note.
func (t *chatTurn) updateNotesToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "update_notes",
			Description: fmt.Sprintf("REWRITE your always-in-prompt \"Working notes\" block — a compact scratchpad of the CURRENT state of your work (what you're mid-way through, the shape of the task, transient context). This REPLACES the whole block; it does NOT append. Re-state the complete current note each time, trimming what's now stale. Use it for running state that changes turn to turn (\"drafting section 3 of the guide\", \"user wants the terse version\", \"waiting on the export to finish\"). Do NOT use it for durable rules or preferences — those go in store_fact — and NOT to record a tool/API bug (that gets fixed in the tool, not remembered). Keep it under %d characters; if it won't fit, COMPRESS — the limit exists to force a summary, not a log. Pass empty text to clear the block.", OperatingNotesCap),
			Parameters: map[string]ToolParam{
				"text": {
					Type:        "string",
					Description: fmt.Sprintf("The complete new Working notes block — concise prose or short bullets. Replaces the current block entirely (this is not an append). Max %d characters. Empty string clears it.", OperatingNotesCap),
				},
			},
			Required: []string{"text"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			text := strings.TrimSpace(stringArg(args, "text"))
			if n := len([]rune(text)); n > OperatingNotesCap {
				return "", fmt.Errorf("notes are %d characters, over the %d limit — trim or summarize and try again (this block injects into every turn, so it must stay compact)", n, OperatingNotesCap)
			}
			_, over := SaveOperatingNotes(t.udb, factsNamespace(t.agent.ID), text)
			if over {
				return "", fmt.Errorf("over the %d character limit — trim and retry", OperatingNotesCap)
			}
			if text == "" {
				return "Working notes cleared.", nil
			}
			return "Working notes updated — this block now appears in full at the top of every future turn.", nil
		},
	}
}
