// What a mapped command gives an agent, and the switch that lets it.
//
// TWO NOUNS, not three. A STORE is a folder; a COMMAND is a binary registered
// against one. Mapping a command writes what it can do ONTO that command, and
// approving it is a switch on the same row.
//
// The arrangement this replaces (v0.6.539-545) made the mapping a third noun: a
// toolbox in its own table, which then had to be ATTACHED to the folder whose
// command it had just been mapped from. Every folder panel therefore had a
// second table, a second picker and a second explanation, and the common path —
// map the command on this folder, use it on this folder — cost an admin a
// round trip through both. The toolbox never existed apart from its command,
// and a record that cannot exist alone should not be stored alone.
//
// What the separation bought was reuse: one binary mapped once, attached to
// several folders. That was worth less than it looked. A command is registered
// per folder already, so a second folder was never free — it cost a
// registration, and the attachment only saved the mapping conversation. If that
// turns out to bite, the answer is a "copy the mapping from…" action on the
// command row: still one record, still one place to look.
//
// The approval survives the collapse, because it is not the same act as
// attaching. An admin registering a binary decides a PERSON may run it here.
// Approving decides an AGENT may call it unattended, with arguments a model
// chose, from a mapping a model wrote.

package filestore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"

	"github.com/cmcoffee/gohort/tools/temptool"
)

// SaveCommandTools records what a mapping conversation worked out, onto the
// command it was opened from.
//
// Never approves. A mapping arriving switched on would mean a model's proposal
// became a live capability the moment it was written, which is the one thing
// the switch exists to prevent — and a re-map keeps whatever the admin had
// already decided, so correcting a description does not silently disarm a
// command somebody is relying on.
func SaveCommandTools(db Database, slug, name, desc string, acts []TempToolAction) (StoreCommand, error) {
	cmd, ok := LoadStoreCommand(db, slug, name)
	if !ok {
		return cmd, Error("that command is not registered against this folder")
	}
	if strings.TrimSpace(desc) == "" {
		return cmd, Error("say what this command is for in a sentence: it is what an agent reads before opening it")
	}
	if len(acts) == 0 {
		return cmd, Error("a mapping with no actions does nothing — map at least one thing the command can do")
	}
	for i, a := range acts {
		switch {
		case strings.TrimSpace(a.Name) == "":
			return cmd, Error("every action needs a name")
		case strings.TrimSpace(a.CommandTemplate) == "":
			return cmd, Error("action " + a.Name + " has no command to run")
		// A local command mapped into an HTTP action is a mistake worth
		// catching here rather than at the first call, where it would read as
		// a network failure.
		case strings.TrimSpace(a.URLTemplate) != "":
			return cmd, Error("action " + a.Name + " declares a url_template; a mapped command is local, not an HTTP call")
		}
		acts[i].Name = strings.TrimSpace(a.Name)
	}
	cmd.Tools = acts
	cmd.ToolDesc = strings.TrimSpace(desc)
	db.Set(commandsTable, commandKey(cmd.Slug, cmd.Name), cmd)
	return cmd, nil
}

// SetCommandApproved flips the agent gate on one command.
func SetCommandApproved(db Database, slug, name string, on bool) (StoreCommand, error) {
	cmd, ok := LoadStoreCommand(db, slug, name)
	if !ok {
		return cmd, Error("that command is not registered against this folder")
	}
	// Approving something with nothing mapped would leave a row reading
	// "agents: on" that hands out no tools — a switch that lies is worse than
	// one that refuses.
	if on && !cmd.Mapped() {
		return cmd, Error("map this command first — there is nothing for an agent to call yet")
	}
	cmd.Approved = on
	db.Set(commandsTable, commandKey(cmd.Slug, cmd.Name), cmd)
	return cmd, nil
}

// asTempTool renders one approved command as the toolbox an agent sees.
//
// BoundOnly, always: a bundle mapped from one deployment's capture binary has
// no business in every chat, and the reason a folder's tools can be this narrow
// is that they arrive with the folder and leave with it.
func (a StoreCommand) asTempTool() TempTool {
	desc := strings.TrimSpace(a.ToolDesc)
	if desc == "" {
		desc = a.Label
	}
	return TempTool{
		Name:        a.ToolName(),
		Description: desc,
		Mode:        TempToolModeToolbox,
		BoundOnly:   true,
		Actions:     a.Tools,
	}
}

// commandToolDefs renders the approved, mapped commands of one folder as agent
// tools, so they arrive with the folder and nowhere else.
//
// Resolution goes through the same path servitor's appliance toolset uses — the
// tools carried on a session into temptool.BuildAgentToolDefs — so a mapped
// action behaves at call time exactly as any other toolbox does, rather than
// through a second implementation that drifts from the first.
func (s storeSource) commandToolDefs(sess *ToolSession, user string, st Store) []AgentToolDef {
	var tools []*TempTool
	for _, cmd := range StoreCommandsFor(s.app.DB, st.Slug) {
		if !cmd.Approved || !cmd.Mapped() {
			continue
		}
		t := cmd.asTempTool()
		tools = append(tools, &t)
	}
	if len(tools) == 0 {
		return nil
	}
	// AuthDB is a function VARIABLE, nil until startup wires it. Calling it
	// unguarded panics — on a path reached from a tool catalog, which is a
	// worse way to find out than a tool quietly not resolving.
	var authDB Database
	if AuthDB != nil {
		authDB = AuthDB()
	}
	bound := &ToolSession{Username: user, DB: authDB, Ctx: sess.Context()}
	if ws, err := EnsureWorkspaceDir(user); err == nil {
		bound.WorkspaceDir = ws
	}
	bound.TempTools = tools
	return temptool.BuildAgentToolDefs(bound)
}

// storeCarriesLabel summarises what a folder hands to an agent beyond its own
// search tools, for the stores list.
func storeCarriesLabel(db Database, slug string) string {
	live, mapped := 0, 0
	for _, cmd := range StoreCommandsFor(db, slug) {
		if !cmd.Mapped() {
			continue
		}
		mapped++
		if cmd.Approved {
			live++
		}
	}
	switch {
	case mapped == 0:
		return "—"
	case live == 0:
		// Mapped and switched off is the state worth naming: the work is done
		// and nothing can use it, which looks identical to unmapped otherwise.
		return countOf(mapped, "mapped command", "mapped commands") + ", none approved"
	case live < mapped:
		return fmt.Sprintf("%s of %d approved", countOf(live, "command", "commands"), mapped)
	default:
		return countOf(live, "command", "commands")
	}
}

// countOf renders a count with its noun, so a row reads as a sentence rather
// than as a number beside a label.
//
// Both forms are passed rather than derived: a rule that appends -s prints
// "2 toolboxs" the first time it meets a noun that takes -es. Spelling the
// plural is cheaper than a rule that is wrong for the second noun it sees.
func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// firstLine trims a description to its opening sentence for a row's subtitle —
// the rest is written for a model, not for a table.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:117] + "…"
	}
	return s
}

// handleCommandApprove flips the agent gate: POST ?id=<slug>/<name> with
// {"approved": true|false}.
func (T *FileStoreApp) handleCommandApprove(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if !adminOnly(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slug, name, _ := strings.Cut(strings.TrimSpace(r.URL.Query().Get("id")), "/")
	var body struct {
		Approved bool `json:"approved"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cmd, err := SetCommandApproved(T.DB, slug, RefToolSlug(name), body.Approved)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	Log("[filestore] %s %s %s on %s for agents", AuthCurrentUser(r),
		map[bool]string{true: "approved", false: "withdrew"}[body.Approved], cmd.Name, cmd.Slug)
	writeJSON(w, cmd)
}
