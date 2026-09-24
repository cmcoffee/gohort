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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
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
		return cmd, Error("a mapping with no actions does nothing: map at least one thing the command can do")
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

		// work_dir names a parameter, so a name with nothing behind it is an
		// action that fails on its first call and nowhere sooner.
		if a.WorkDir != "" {
			if _, ok := a.Params[a.WorkDir]; !ok {
				return cmd, Error("action " + a.Name + " sets work_dir to " + a.WorkDir +
					" but declares no parameter called " + a.WorkDir + ", name the parameter the folder arrives in")
			}
			// Required by construction. An action that declares where it runs
			// cannot run without being told which folder, so leaving that to
			// the mapping to remember is leaving it to be forgotten — and a
			// forgotten one is refused at dispatch, one layer further from
			// whoever could fix it.
			if !slices.Contains(a.Required, a.WorkDir) {
				acts[i].Required = append(append([]string{}, a.Required...), a.WorkDir)
			}
		}
		// Folder parameters are PINNED to this command's store, here rather
		// than in the mapping handler that used to do it.
		//
		// The caller declares WHICH parameters are folders, because only it
		// knows what the binary takes. It does not choose which store they come
		// from: an admin decided that when the command was registered. Doing it
		// at the write means every path that persists a mapping gets it — the
		// check that lives in one caller is the check a second caller silently
		// does without, and an unpinned folder parameter does not fail, it
		// resolves nothing and the command runs somewhere else entirely.
		for name, p := range a.Params {
			if name == a.WorkDir || strings.TrimSpace(p.PathScope) != "" {
				p.PathScope = "files:" + cmd.Slug
				acts[i].Params[name] = p
			}
		}
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
		return cmd, Error("map this command first: there is nothing for an agent to call yet")
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
	defs := temptool.BuildAgentToolDefs(bound)
	for i := range defs {
		defs[i].Handler = refuseOptionArgs(tools, defs[i].Tool.Name, defs[i].Handler)
	}
	return defs
}

// refuseOptionArgs wraps a mapped command's handler so a value that fills a
// whole argument cannot start with '-'. The binary is fixed by an admin and
// the agent only supplies values, but "{name}" standing alone in the command
// line lets a value like "--output=/elsewhere" or "-e ..." become an option of
// that binary, which is choosing its behaviour rather than its input. Same
// rule gohort-desktop applies to declared commands. A folder resolved by
// path_scope is an absolute path and never trips it.
func refuseOptionArgs(tools []*TempTool, defName string, h ToolHandlerFunc) ToolHandlerFunc {
	if h == nil {
		return h
	}
	return func(ctx context.Context, args map[string]any) (string, error) {
		if act, ok := mappedActionFor(tools, defName, args); ok {
			for name := range wholeArgPlaceholders(act.CommandTemplate) {
				if v, ok := args[name]; ok && strings.HasPrefix(strings.TrimSpace(fmt.Sprint(v)), "-") {
					return "", fmt.Errorf("the value for %s starts with '-', which the command would read as an option: pass the value itself", name)
				}
			}
		}
		return h(ctx, args)
	}
}

// mappedActionFor finds which action a call reaches: a collapsed toolbox tool
// names it in args["action"], an expanded one is "<tool>_<action>".
func mappedActionFor(tools []*TempTool, defName string, args map[string]any) (TempToolAction, bool) {
	for _, t := range tools {
		for _, a := range t.Actions {
			if defName == t.Name+"_"+a.Name {
				return a, true
			}
			if defName == t.Name && strings.EqualFold(strings.TrimSpace(fmt.Sprint(args["action"])), a.Name) {
				return a, true
			}
		}
	}
	return TempToolAction{}, false
}

// wholeArgPlaceholders lists the params that make up an entire argument of a
// command template, quoted or not ("{file}", '{file}', {file}).
func wholeArgPlaceholders(tpl string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.Fields(tpl) {
		f = strings.Trim(f, `"'`)
		if len(f) > 2 && f[0] == '{' && f[len(f)-1] == '}' && !strings.ContainsAny(f[1:len(f)-1], "{} ") {
			out[f[1:len(f)-1]] = true
		}
	}
	return out
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
		return "-"
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
// handleCommandMapping serves what a mapping produced, for reading.
//
//	GET /filestore/api/commands/mapping?id=<slug>/<name>
//
// The row already says THAT a command is mapped ("cap_bundles - 3 actions -
// off") and cannot say what it was mapped AS. Deciding whether to re-map means
// knowing what an agent would actually run — the command line, which parameter
// carries the folder, whether it runs inside that folder — and until this
// existed the only way to see any of it was to reopen the mapping conversation
// and ask, which costs a model call to read data already on the record.
//
// Read-only and derived: every field here is a field of the StoreCommand, so
// there is nothing to keep in sync and no second place a mapping can be
// changed.
func (T *FileStoreApp) handleCommandMapping(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if !adminOnly(w, r) {
		return
	}
	slug, name, _ := strings.Cut(strings.TrimSpace(r.URL.Query().Get("id")), "/")
	cmd, ok := LoadStoreCommand(T.DB, slug, RefToolSlug(name))
	if !ok {
		http.NotFound(w, r)
		return
	}
	acts := make([]map[string]any, 0, len(cmd.Tools))
	for _, a := range cmd.Tools {
		// Parameters as one readable line each rather than a nested object:
		// this is a panel someone skims to answer "is that right?", and the
		// thing they are checking is which parameter carries the folder.
		params := make([]string, 0, len(a.Params))
		for pn := range a.Params {
			params = append(params, pn)
		}
		sort.Strings(params)
		for i, pn := range params {
			p := a.Params[pn]
			line := pn
			if p.Type != "" {
				line += " (" + p.Type + ")"
			}
			// The scope is the field that decides whether a folder name
			// becomes a real path, and it is invisible everywhere else.
			if sc := strings.TrimSpace(p.PathScope); sc != "" {
				line += " → " + sc
			}
			if pn == a.WorkDir {
				line += "  [runs here]"
			}
			params[i] = line
		}
		runsIn := a.WorkDir
		if runsIn == "" {
			runsIn = "the workspace"
		}
		// Joined here rather than sent as a list: these entries are already
		// elements of the actions array, and a list inside a list element is a
		// nesting depth this panel is not documented to render. One line per
		// action is also what someone skimming for the folder parameter wants.
		takes := strings.Join(params, ", ")
		if takes == "" {
			takes = "no parameters"
		}
		acts = append(acts, map[string]any{
			"name": a.Name, "description": a.Description,
			"command": a.CommandTemplate, "runs_in": runsIn,
			"params": takes, "disabled": a.Disabled,
		})
	}
	state := "Mapped, but switched off for agents"
	status := "warn"
	switch {
	case !cmd.Mapped():
		state, status = "Not mapped yet", "warn"
	case cmd.Approved:
		state, status = "Live: agents that reach this folder can call it", "ok"
	}
	writeJSON(w, map[string]any{
		"tool_name": cmd.ToolName(), "tool_desc": cmd.ToolDesc,
		"binary": cmd.Command, "state": state, "state_status": status,
		"actions": acts,
	})
}

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
