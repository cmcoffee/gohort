// Plan mode, undo from a message, and "# remember this": three habits of
// Claude Code's, in Anvil.
//
// Plan mode is a switch beside Send. While it is on the assistant reads,
// searches, keeps its plan and runs the project's check, and proposes the
// change instead of making it; the person turns it off, or says to go ahead
// with it off, when they agree. The switch is each person's, kept until they
// turn it off.
//
// Undo puts back every file the assistant changed from one message on: the
// page sends the changes shown below that message (anvil_edit cards), and
// they are reverted newest first, each with the check a single Revert makes.
//
// A message starting with "#" is not sent: its line is added to what the
// assistant keeps about the project (What Anvil learned).
package anvil

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

// prefTable holds each person's Anvil switches, keyed by name.
const prefTable = "anvil_prefs"

const planModeKey = "plan_mode"

// planModeOn is whether plan mode is on for this person.
func planModeOn(udb Database) bool {
	var on bool
	udb.Get(prefTable, planModeKey, &on)
	return on
}

// planModeSwitch is the switch beside Send.
func planModeSwitch() ui.ChatMode {
	return ui.ChatMode{
		Label:   "Plan mode",
		Title:   "Plan mode: the assistant looks around and proposes a change instead of making it. Turn it off to let it make the change.",
		GetURL:  "api/plan_mode",
		PostURL: "api/plan_mode",
		Field:   planModeKey,
	}
}

func (T *Anvil) handlePlanMode(c *jsonapi.Call) (any, error) {
	return map[string]any{planModeKey: planModeOn(c.UDB)}, nil
}

func (T *Anvil) handlePlanModeSave(c *jsonapi.Call, body struct {
	On bool `json:"plan_mode"`
}) (any, error) {
	if body.On {
		c.UDB.Set(prefTable, planModeKey, true)
	} else {
		c.UDB.Unset(prefTable, planModeKey)
	}
	return map[string]any{planModeKey: body.On}, nil
}

// planModePrompt is what the assistant is told on a turn in plan mode.
const planModePrompt = `

## Plan mode is on

The person turned on plan mode: this turn you can read, search, look at git, keep your plan and run the project's check, but you cannot change a file or run a command. Look into what they asked, then set out the change you propose: which files, what changes in each and why, and how it will be checked. Put its steps in your plan. End by asking whether to go ahead; they will turn plan mode off to let you make it. Do not say a change was made.`

// --- undo from a message ------------------------------------------------------

// handleUndo reverts the named changes, newest first, stopping at the first
// that cannot be put back (a file changed since): reverting past it would
// skip a change the person may still want. It says what was put back and,
// when it stopped, why.
func (T *Anvil) handleUndo(c *jsonapi.Call, body struct {
	IDs []string `json:"ids"`
}) (any, error) {
	udb, user, isAdmin := c.UDB, c.User, callerIsAdmin(c.R)
	var edits []EditEntry
	seen := map[string]bool{}
	for _, id := range body.IDs {
		if id = strings.TrimSpace(id); id == "" || seen[id] {
			continue
		}
		seen[id] = true
		var e EditEntry
		if !udb.Get(editTable, id, &e) || e.Reverted {
			continue
		}
		edits = append(edits, e)
	}
	if len(edits) == 0 {
		return map[string]any{"reverted": []string{}, "message": "Nothing to undo: no change from here on is still in place."}, nil
	}
	p, ok := T.findProject(udb, user, isAdmin, edits[0].ProjectID)
	if !ok {
		return nil, jsonapi.NotFound("those changes belong to a project you cannot open")
	}
	if !writeAllowed(p, user, isAdmin) {
		return nil, jsonapi.Forbidden("you can open this project but not change it")
	}
	base, err := p.baseDir()
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].At.After(edits[j].At) })
	reverted := []string{}
	var stopped string
	for _, e := range edits {
		if e.ProjectID != p.ID {
			continue
		}
		if err := revertEdit(udb, p, base, e); err != nil {
			stopped = err.Error()
			break
		}
		reverted = append(reverted, e.ID)
		haltForRevert(e)
	}
	msg := undoMessage(len(reverted), len(edits), stopped)
	if len(reverted) == 0 && stopped != "" {
		return nil, jsonapi.Status(http.StatusConflict, msg)
	}
	return map[string]any{"reverted": reverted, "message": msg}, nil
}

// undoMessage says how an undo went.
func undoMessage(done, of int, stopped string) string {
	if stopped == "" {
		if done == 1 {
			return "Put back 1 change."
		}
		return "Put back " + strconv.Itoa(done) + " changes."
	}
	if done == 0 {
		return "Nothing was put back: " + stopped
	}
	return "Put back " + strconv.Itoa(done) + " of " + strconv.Itoa(of) + " changes, then stopped: " + stopped
}

// --- "# remember this" --------------------------------------------------------

// handleRemember adds a line to the project's notes, which the assistant
// reads on every turn there.
func (T *Anvil) handleRemember(c *jsonapi.Call, body struct {
	Text string `json:"text"`
}) (any, error) {
	p, udb, ns, err := T.notesOf(c)
	if err != nil {
		return nil, err
	}
	if !writeAllowed(p, c.User, callerIsAdmin(c.R)) {
		return nil, jsonapi.Forbidden("you can open this project but not change it")
	}
	if err := rememberLine(udb, ns, body.Text); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "message": "Remembered for this project."}, nil
}

// rememberLine adds text, one line, to the notes under ns as "- text".
func rememberLine(udb Database, ns, text string) error {
	line := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(text), "#"))
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return jsonapi.BadRequest("write what to remember on one line after the #")
	}
	stored := strings.TrimSpace(LoadOperatingNotes(udb, ns).Text)
	next := "- " + line
	if stored != "" {
		next = stored + "\n" + next
	}
	if _, over := SaveOperatingNotes(udb, ns, next); over {
		return jsonapi.Status(http.StatusConflict, "The notes are full ("+strconv.Itoa(OperatingNotesCap)+" characters). Open What Anvil learned and trim them first.")
	}
	return nil
}
