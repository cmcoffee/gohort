// Working notes scoped to a SCHEDULE rather than to an agent.
//
// A scheduled fire is the case the agent-wide notes layer was built for and
// never reached: the run that learned something is over, its context is gone,
// and the next fire starts from the task's prompt and nothing else. What it
// worked out last night about which command actually works, or which half of
// the job is already done, had nowhere to live that the next fire would read.
//
// Three surfaces carry them, and each already has the two things this needs:
// a record with an identity, and a fire that assembles a prompt. The block
// rides the volatile tail beside the objective block; the tool is mounted BY
// THE FIRE, the way set_next_attempt is (core/pacing), because it is the turn
// that can use it.
//
// See docs/task-notes.md.

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

// taskNotes is one task's notes row, resolved for a fire. The zero value means
// "no task in scope" and every method on it is a no-op, so a caller that cannot
// identify its task degrades to today's behaviour instead of writing into a
// shared bucket.
//
// It carries the namespace only. The store is RootDB, beside the task records
// themselves: a schedule is not a per-user document, and keying the row by
// owner keeps the tenancy the records already have.
type taskNotes struct{ ns string }

func newTaskNotes(surface, owner, id string) taskNotes {
	return taskNotes{ns: notes.TaskNamespace(surface, owner, id)}
}

// live reports whether this fire has a notes row at all.
func (t taskNotes) live() bool { return t.ns != "" }

// load returns what earlier runs left. No seed: a task's notes are what runs
// wrote, never a restatement of its configuration, and the empty state is
// rendered rather than stored (notes.RenderTaskNotesBlock).
func (t taskNotes) load() OperatingNotes {
	if !t.live() {
		return OperatingNotes{}
	}
	return LoadOperatingNotes(RootDB, t.ns)
}

// block is the tail block this fire carries, including the one-line empty state
// that tells a first run the register exists.
func (t taskNotes) block() string {
	if !t.live() {
		return ""
	}
	return notes.RenderTaskNotesBlock(t.load())
}

// tool is update_notes bound to THIS task. Same name as the agent-wide tool on
// purpose: a fire never carries both (agentNotesSuppressed), so there is one
// tool called update_notes in any given prompt and no rule to get wrong about
// which scratchpad a write lands in.
func (t taskNotes) tool() []AgentToolDef {
	if !t.live() {
		return nil
	}
	return []AgentToolDef{{
		Tool: Tool{
			Name:        "update_notes",
			Description: fmt.Sprintf("REWRITE the notes for THIS SCHEDULED TASK: a compact scratchpad carried from one run of it to the next. This run's conversation ends when it does; this block is what the next run reads, so anything worked out here that is not written down is worked out again from scratch.\n\nRight for it: what is half-done and what the next step was, a command or path that turned out to work (or to look right and not work), a decision already made so it is not reopened, an id or name that took digging to find. Wrong for it: durable facts about the user (store_fact), anything the task's own prompt already says, and NEVER a tool call parked to make later, because a remembered call you have no way to make becomes an improvised workaround.\n\nThis REPLACES the whole block; it does NOT append. Pass `section` to update ONE named part and leave the rest: your own register, named by you, created the first time you write it. Keep the WHOLE block under %d characters; sections share that budget rather than adding to it, so if it will not fit, COMPRESS. Pass empty text to clear the block, or empty text WITH a section to remove just that section.", OperatingNotesCap),
			Parameters:  notesToolParams(),
			Required:    []string{"text"},
			Caps:        []Capability{CapWrite},
		},
		Handler: notesWriteHandler(RootDB, t.ns, "", taskNotesCopy),
	}}
}

// drop deletes the row. Called where a task is deleted or retires, never where
// one PARKS: a parked task is stopped and kept, Resume gives it a fresh
// allowance, and what it had worked out is most of what makes resuming
// different from starting over.
func (t taskNotes) drop() {
	if !t.live() {
		return
	}
	SaveOperatingNotes(RootDB, t.ns, "")
}

// --- the task in scope for a fire ------------------------------------------

type taskNotesCtxKey struct{}

// withTaskNotes marks a context as running under one task.
//
// On the context rather than in a signature because the prompt assembler that
// has to know (dispatchSystemPrompt) sits three calls below the fire, behind
// two entry points with their own argument shapes. The same reasoning as the
// maintenance-progress key: an operation reports with one call and no signature
// change along the way.
func withTaskNotes(ctx context.Context, t taskNotes) context.Context {
	if ctx == nil || !t.live() {
		return ctx
	}
	return context.WithValue(ctx, taskNotesCtxKey{}, t)
}

// taskNotesFromContext returns the task in scope, if any.
func taskNotesFromContext(ctx context.Context) taskNotes {
	if ctx == nil {
		return taskNotes{}
	}
	t, _ := ctx.Value(taskNotesCtxKey{}).(taskNotes)
	return t
}

// agentNotesSuppressed reports whether the agent-wide Working-notes block must
// stay out of this prompt because a task's notes are in it instead.
//
// Two always-in-prompt scratchpads with no rule for choosing between them is
// how running state ends up in the wrong one, and this codebase already knows
// what happens when a model has to pick between memory layers on its own. So
// for the duration of a fire the task's notes are the only ones: the agent's
// own block is not rendered and its tool is not mounted.
func agentNotesSuppressed(ctx context.Context) bool {
	return taskNotesFromContext(ctx).live()
}

// --- per-surface identity ---------------------------------------------------

// standingTaskNotes / monitorTaskNotes key on (owner, name), which is the
// record's own primary key in RootDB and is not editable after creation.
func standingTaskNotes(sa StandingAgent) taskNotes {
	return newTaskNotes(notes.TaskSurfaceStanding, sa.Owner, sa.Name)
}

func monitorTaskNotes(m EventMonitor) taskNotes {
	return newTaskNotes(notes.TaskSurfaceMonitor, m.Owner, m.Name)
}

// recurringTaskNotes keys on the payload's own UID, NOT on the scheduler task
// id the console shows.
//
// A recurring task has no record: it lives as its scheduler entry, and every
// fire arms a fresh entry with a fresh UUID (ScheduleTask mints one per arm).
// So the id a row is actioned by today names the next OCCURRENCE, not the task,
// and notes keyed on it would be written by one fire into a namespace no later
// fire could find. The UID is minted once at create and copied forward by every
// re-arm, because the arm copies the whole payload.
//
// Tasks armed before the field existed have no UID and fall back to the pair
// that has always been carried forward untouched: the session the task was
// created in and its creation time. Stable for the same reason, and unique
// unless two tasks were created in one session within the same second.
func recurringTaskNotes(p orchUpdatePayload) taskNotes {
	return newTaskNotes(notes.TaskSurfaceRecurring, p.Username, recurringTaskUID(p))
}

func recurringTaskUID(p orchUpdatePayload) string {
	if uid := strings.TrimSpace(p.UID); uid != "" {
		return uid
	}
	sess, born := strings.TrimSpace(p.SessionID), strings.TrimSpace(p.CreatedAt)
	if sess == "" || born == "" {
		return ""
	}
	return sess + "|" + born
}

// dropRecurringTaskNotes is called where a recurring task ENDS: cancelled by
// hand, reaped for idleness, or retired at its fire cap.
//
// Not called where one PARKS. A parked task is listed, stopped and resumable,
// and deleting what it had worked out would make Resume start over. That
// distinction is the whole lifetime rule, and it is easy to get wrong here
// because both paths call UnscheduleTask: the difference is whether a dormant
// tick is re-armed afterwards.
func dropRecurringTaskNotes(p orchUpdatePayload) { recurringTaskNotes(p).drop() }

// --- the owner's side -------------------------------------------------------

// taskNotesFor resolves a (kind, id) pair from the Scheduler into a notes row,
// and only for a task this user actually owns.
//
// Ownership is the lookup, not a check beside it: each kind is fetched from the
// owner's own records, so an id belonging to somebody else simply is not found.
// The kind comes from the row rather than being guessed from the id, because a
// monitor and a standing agent may share a name and guessing would hand one the
// other's notes.
func taskNotesFor(user, kind, id string) (taskNotes, bool) {
	user, kind, id = strings.TrimSpace(user), strings.TrimSpace(kind), strings.TrimSpace(id)
	if user == "" || id == "" {
		return taskNotes{}, false
	}
	switch kind {
	case schedKindStanding:
		if sa, ok := GetStandingAgent(RootDB, user, id); ok {
			return standingTaskNotes(sa), true
		}
	case schedKindMonitor:
		if m, ok := GetEventMonitor(RootDB, user, id); ok {
			return monitorTaskNotes(m), true
		}
	case schedKindRecurring:
		// The row is actioned by the id of the next OCCURRENCE, which is what
		// the console has to use for everything else about a recurring task.
		// The notes hang on the task's own identity, so the occurrence is
		// resolved to its payload first.
		for _, rt := range listAgentRecurringTasks(user, "") {
			if rt.TaskID == id {
				return recurringTaskNotes(rt.Payload), true
			}
		}
	}
	return taskNotes{}, false
}

// handleTaskNotes serves one task's notes to the Scheduler.
//
//	GET  -> the text, the cap, and what each section costs
//	POST -> replaces the block wholesale, or one section, same semantics as
//	        update_notes, because the owner-facing path and the agent-facing
//	        path drifting apart is how they come to disagree about a cap
//
// The task's own card is the panel on purpose. The agent-wide block spent seven
// hundred versions with no owner surface at all, filed under the agent where
// nobody was looking for it; a task's notes belong on the thing that wrote
// them, beside its goal, its attempts and its next fire.
func (T *OrchestrateApp) handleTaskNotes(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	q := r.URL.Query()
	tn, found := taskNotesFor(user, q.Get("kind"), q.Get("id"))
	if !found {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		n := tn.load()
		writeJSON(w, map[string]any{
			"text":       n.Text,
			"cap":        OperatingNotesCap,
			"updated_at": n.UpdatedAt,
			// The same measurement the over-cap refusal quotes at the agent,
			// served rather than counted in the browser so the person trimming
			// the block and the model trimming it read one number.
			"sections": notes.SectionSizes(n.Text),
		})
	case http.MethodPost:
		var body struct {
			Text    string `json:"text"`
			Section string `json:"section"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		args := map[string]any{"text": body.Text}
		if s := strings.TrimSpace(body.Section); s != "" {
			args["section"] = s
		}
		// Through the tool's own handler: one write path for both surfaces, so
		// the cap, the splice and the refusal are the same sentence whoever is
		// asking.
		msg, err := tn.tool()[0].Handler(r.Context(), args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "message": msg})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
