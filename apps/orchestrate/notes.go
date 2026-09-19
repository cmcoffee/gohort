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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notes"
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
// how facts() nils for incognito turns — a claim this comment carried for a
// while before facts() actually did it, which is how the gap went unnoticed.
func (t *chatTurn) operatingNotes() OperatingNotes {
	if !t.agent.EnableNotes {
		return OperatingNotes{}
	}
	if t.incognitoSession() {
		return OperatingNotes{}
	}
	return ResolveOperatingNotes(t.udb, factsNamespace(t.agent.ID), t.agent.SeedNotes)
}

// AgentStateNamespace is the store namespace an agent's per-agent state lives
// under — its Explicit Memory facts and its Working-notes block.
//
// Exported for a HOSTING APP that owns an agent's identity per turn. Anvil
// rewrites agent.ID to scope memory per project, so its own review surface has
// to address the same namespace the runtime wrote to. Deriving it a second time
// in the app is how the two come to disagree about where a note lives, and the
// symptom of that is a panel that shows nothing while the model reads something.
func AgentStateNamespace(agentID string) string { return factsNamespace(agentID) }

// --- HTTP handler ---------------------------------------------------------

// handleAgentNotes serves the Working notes block to the Memory modal.
//
//	GET  → the effective text (store, or the record's SeedNotes when the
//	       store is empty), when it was last rewritten, whether notes are
//	       enabled for this agent, and the cap.
//	POST → replaces the block wholesale, same semantics as update_notes.
//
// Notes were the one memory layer with no owner-facing surface: facts, graph
// and Reference Memory all had panels, notes had only the cross-layer text
// search. They are also the layer the model rewrites on its own, unprompted
// and unreviewed, and the one that renders nearest the top of the prompt — so
// a wrong note steered every turn with nowhere to go and look at it. That is
// how a stale "pending task: <tool> with <args>" survived across sessions.
//
// user is the STATE scope; the caller resolves and authorizes it, matching
// handleAgentFacts (RequireUser on the web surfaces, an appliance scope for
// the per-scope variant).
func (T *OrchestrateApp) handleAgentNotes(w http.ResponseWriter, r *http.Request, user, agentID string) {
	udb := UserDB(T.DB, user)
	if udb == nil {
		http.Error(w, "no store for user", http.StatusInternalServerError)
		return
	}
	if agentID == "" || strings.Contains(agentID, "/") {
		http.NotFound(w, r)
		return
	}
	a, ok := T.memoryAgent(r, udb, user, agentID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ns := factsNamespace(agentID)
	switch r.Method {
	case http.MethodGet:
		stored := LoadOperatingNotes(udb, ns)
		eff := ResolveOperatingNotes(udb, ns, a.SeedNotes)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"text":    eff.Text,
			"enabled": a.EnableNotes,
			// Whether the READER can turn them on. An app agent's flags come
			// from its code-registered spec and its record is hidden from the
			// pickers, so there is no editor to send anyone to — telling them
			// to go to one is the same unfollowable advice as pointing a macOS
			// operator at a Linux package. The panel hides itself instead.
			"can_enable": !isAppAgent(agentID),
			"cap":        OperatingNotesCap,
			// What each register costs, biggest first — the same measurement
			// the over-cap refusal quotes at the agent. Served rather than
			// computed in the panel so the person trimming the block and the
			// model trimming it read one number; a browser-side count would
			// disagree the first time a heading appeared inside a code fence.
			"sections": notes.SectionSizes(eff.Text),
			// Distinguishes "the agent wrote this" from "nobody has written
			// anything, you're looking at the configured seed" — clearing is
			// meaningless in the second case, and the panel says which it is.
			"from_seed":  strings.TrimSpace(stored.Text) == "" && strings.TrimSpace(eff.Text) != "",
			"updated_at": stored.UpdatedAt,
		})
	case http.MethodPost:
		var body struct {
			Text string `json:"text"`
			// Optional, and the same semantics update_notes has: replace one
			// named part, remove it when the text is empty, leave the rest
			// alone. The owner's path and the agent's path go through one
			// implementation because two of them is how they come to disagree
			// about where a note lives.
			Section string `json:"section"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		text := strings.TrimSpace(body.Text)
		if section := strings.TrimSpace(body.Section); section != "" {
			next, err := notes.ApplyNoteSection(ResolveOperatingNotes(udb, ns, a.SeedNotes).Text, section, text)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			text = next
		}
		if n := len([]rune(text)); n > OperatingNotesCap {
			msg := fmt.Sprintf("notes are %d characters, over the %d limit", n, OperatingNotesCap)
			if advice := notes.OverCapAdvice(text); advice != "" {
				msg += ". " + advice
			}
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if _, over := SaveOperatingNotes(udb, ns, text); over {
			http.Error(w, fmt.Sprintf("over the %d character limit", OperatingNotesCap), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// notesCopy names the block a write lands in. The write PATH is identical for
// the agent-wide block and a task's: same cap, same splice, same refusal naming
// the largest sections. The only thing that genuinely differs is where what you
// just wrote will turn up, and that is the one thing the model has to be told,
// so it is the only thing parameterized.
type notesCopy struct {
	name  string // "your Working notes" / "this task's notes"
	lands string // finishes the confirmation sentence after a whole-block write
}

var (
	agentNotesCopy = notesCopy{name: "your Working notes", lands: "this block now appears in full at the top of every future turn"}
	taskNotesCopy  = notesCopy{name: "this task's notes", lands: "the next run of this task reads this block, and nothing else of this run survives"}
)

// notesToolParams is the shared parameter schema. Shared rather than written
// twice because the section semantics (create, replace in place, empty removes)
// are the part a second copy would get subtly wrong, and a model told two
// different rules for one tool name follows whichever it read last.
func notesToolParams() map[string]ToolParam {
	return map[string]ToolParam{
		"text": {
			Type:        "string",
			Description: fmt.Sprintf("The new content. Without `section`: the complete new block, replacing the current one entirely (this is not an append). With `section`: just that section's body. Max %d characters for the whole block either way. Empty string clears the block, or removes the named section.", OperatingNotesCap),
		},
		"section": {
			Type:        "string",
			Description: "Optional. The name of the part to update: a short label you choose (\"in flight\", \"deployment quirks\"). Creates it the first time, replaces it in place afterwards, and leaves every other part of the block untouched. Matching ignores case and spacing, so write the same register the same way and it lands in the same place. Omit to rewrite the whole block.",
		},
	}
}

// notesWriteHandler is the write half of update_notes, shared by the agent-wide
// block and the per-task one.
//
// A section write splices into the RESOLVED notes, seed included. Against
// storage alone, an agent configured with a seed whose store is still empty
// would have its first section write silently discard everything it was set up
// with: the seed renders in the prompt but is not stored, so the splice would
// start from blank.
func notesWriteHandler(db Database, ns, seed string, c notesCopy) func(context.Context, map[string]any) (string, error) {
	return func(ctx context.Context, args map[string]any) (string, error) {
		if ns == "" {
			return "", fmt.Errorf("there is no notes block to write here")
		}
		text := strings.TrimSpace(stringArg(args, "text"))
		section := strings.TrimSpace(stringArg(args, "section"))
		if section != "" {
			current := ResolveOperatingNotes(db, ns, seed).Text
			next, err := notes.ApplyNoteSection(current, section, text)
			if err != nil {
				return "", err
			}
			if n := len([]rune(next)); n > OperatingNotesCap {
				// Named, not truncated. The model has the block in front of it
				// and can compress the part that is actually costing; a silent
				// trim leaves it reading a sentence that stops.
				msg := fmt.Sprintf("that would put %s at %d of %d characters", c.name, n, OperatingNotesCap)
				if advice := notes.OverCapAdvice(next); advice != "" {
					msg += ". " + advice
				}
				return "", fmt.Errorf("%s Trim one and retry: this block injects into every turn", msg)
			}
			if _, over := SaveOperatingNotes(db, ns, next); over {
				return "", fmt.Errorf("over the %d character limit: trim and retry", OperatingNotesCap)
			}
			if text == "" {
				return fmt.Sprintf("Removed the %q section from %s. The rest of the block is unchanged.", section, c.name), nil
			}
			return fmt.Sprintf("Updated the %q section of %s (%d of %d characters used). The rest of the block is unchanged.",
				section, c.name, len([]rune(next)), OperatingNotesCap), nil
		}
		if n := len([]rune(text)); n > OperatingNotesCap {
			return "", fmt.Errorf("notes are %d characters, over the %d limit: trim or summarize and try again (this block injects into every turn, so it must stay compact)", n, OperatingNotesCap)
		}
		_, over := SaveOperatingNotes(db, ns, text)
		if over {
			return "", fmt.Errorf("over the %d character limit: trim and retry", OperatingNotesCap)
		}
		if text == "" {
			return fmt.Sprintf("Cleared %s.", c.name), nil
		}
		return fmt.Sprintf("Updated %s: %s.", c.name, c.lands), nil
	}
}

// updateNotesToolDef binds the always-in-prompt Working notes block. Unlike
// store_fact (append an atomic rule), this REPLACES the whole block: the agent
// keeps a compact, current running-state note.
func (t *chatTurn) updateNotesToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "update_notes",
			Description: fmt.Sprintf("REWRITE your always-in-prompt \"Working notes\" block: a compact scratchpad of the CURRENT state of your work (what you're mid-way through, the shape of the task, transient context). This REPLACES the whole block; it does NOT append. Re-state the complete current note each time, trimming what's now stale. Use it for running state that changes turn to turn (\"drafting section 3 of the guide\", \"user wants the terse version\", \"waiting on the export to finish\"). Do NOT use it for durable rules or preferences (those go in store_fact), and NOT to record a tool/API bug (that gets fixed in the tool, not remembered). NEVER park a tool call here to make later (\"pending task: some_tool with x=y\"): a note cannot call a tool, and the invocation outlives the tool, next session its schema may not be loaded, and a remembered call you have no way to make becomes an improvised workaround. Record the GOAL (\"user wants a news roundup\"), not the call.\n\nPass `section` to update ONE named part and leave the rest alone: your own register, named by you, created the first time you write it. Use it when only that part changed; rewrite the whole block (text alone) when the shape of the work changes. Keep the WHOLE block under %d characters; sections share that budget rather than adding to it, so if it won't fit, COMPRESS: the limit exists to force a summary, not a log. Pass empty text to clear the block, or empty text WITH a section to remove just that section.", OperatingNotesCap),
			Parameters:  notesToolParams(),
			Required:    []string{"text"},
			Caps:        []Capability{CapWrite},
		},
		Handler: notesWriteHandler(t.udb, factsNamespace(t.agent.ID), t.agent.SeedNotes, agentNotesCopy),
	}
}
