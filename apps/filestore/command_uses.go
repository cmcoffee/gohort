// How agents may use a registered command, once Builder has mapped it with an
// admin and the admin has approved the mapping.
//
// Builder explores a command freely while it maps it: the conversation is the
// admin's own, opened from the command's row, and what it writes is inert
// until the admin turns Agents on. The limit is on AGENTS, in two modes the
// admin chooses:
//
//   - specified (the default): an agent runs only the command's allowed uses,
//     each line exactly as written ("download --src={source} --dst={folder}"),
//     filling only the placeholders. The admin may write uses up front to say
//     what Builder should map; every line Builder maps is added to them, and
//     what the admin approves is that list. Changing the uses turns Agents off.
//   - open: the mapped lines run in the sandbox.
//
// A specified command runs as written, directly as the server's account, the
// way a person's click on a store command runs: the binary, then the line with
// each placeholder filled by ONE value, no shell. That is what lets a binary
// with its own saved login (a file-sharing client) work, and it is safe because
// the line is one an admin approved and the model supplies only values.
package filestore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bgrun"
	"github.com/cmcoffee/gohort/core/pathhold"
)

// The two usage modes.
const (
	UsageSpecified = "specified"
	UsageOpen      = "open"
)

// open reports whether this command's mapped lines run in the sandbox rather
// than as written.
func (a StoreCommand) open() bool { return a.Usage == UsageOpen }

// allowedLines is the admin's allowed uses, each with its spacing normalized.
func (a StoreCommand) allowedLines() []string {
	var out []string
	for _, l := range a.AllowedUses {
		if f := strings.Fields(l); len(f) > 0 {
			out = append(out, strings.Join(f, " "))
		}
	}
	return out
}

// usesLabel says how the command may be used, for the admin table.
func (a StoreCommand) usesLabel() string {
	if a.open() {
		return "sandboxed: agents run the mapped lines in the sandbox"
	}
	lines := a.allowedLines()
	if len(lines) == 0 {
		return "only as specified: none yet (Builder adds the lines it maps)"
	}
	return "only: " + strings.Join(lines, " | ")
}

// templateAllowed says whether a mapped action's command line is one an agent
// may run: for an open command any (actionRunsTheCommand still applies), for a
// specified one exactly the binary and one of its allowed uses. Checked when
// an agent's tools are built and again at the call, so a use the admin has
// taken off the list stops running.
func (a StoreCommand) templateAllowed(tmpl string) error {
	if a.open() {
		return nil
	}
	lines := a.allowedLines()
	args := strings.Join(strings.Fields(argsAfterBinary(tmpl)), " ")
	for _, l := range lines {
		if args == l {
			return nil
		}
	}
	return fmt.Errorf("the line %q is not one of this command's allowed uses (%s): an admin adds it under Uses on the command's row, or maps it again with Builder", args, strings.Join(lines, " | "))
}

// argsAfterBinary is a command line without its first word (the binary,
// quoted or not).
func argsAfterBinary(tmpl string) string {
	t := strings.TrimSpace(tmpl)
	if strings.HasPrefix(t, "'") || strings.HasPrefix(t, `"`) {
		if end := strings.IndexByte(t[1:], t[0]); end >= 0 {
			return strings.TrimSpace(t[end+2:])
		}
	}
	if i := strings.IndexAny(t, " \t"); i >= 0 {
		return strings.TrimSpace(t[i:])
	}
	return ""
}

// --- running a specified action ---------------------------------------------

// specifiedToolDefs is a specified command's mapped actions as agent tools,
// each running its line as written (runSpecified), as a background-capable
// run so a long download moves to the background and reports back.
func (s storeSource) specifiedToolDefs(sess *ToolSession, user string, st Store, cmd StoreCommand) []AgentToolDef {
	var out []AgentToolDef
	for _, act := range cmd.runnableActions() {
		act := act
		name := cmd.ToolName() + "_" + act.Name
		line := strings.TrimSpace(argsAfterBinary(act.CommandTemplate))
		desc := strings.TrimSpace(act.Description)
		out = append(out, AgentToolDef{
			Tool: Tool{
				Name:        name,
				Description: fmt.Sprintf("%s Runs exactly: %s %s, with your values in the {placeholders}; nothing else about the command can be changed.%s", strings.TrimSuffix(desc, ".")+".", cmd.Command, line, guidePointer(st, cmd)),
				Parameters:  act.Params,
				Required:    act.Required,
				Caps:        []Capability{CapExecute},
				Category:    "files",
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return bgrun.BackgroundRun{Tool: name, Label: cmd.Label + " on " + st.Name, Run: func(work *ToolSession) (string, error) {
					return runSpecified(work.Context(), user, sess.AgentID, st, cmd, act, args)
				}}.Start(sess)
			},
		})
	}
	return out
}

// specifiedOutputMax is how much of a specified run's output goes back to the
// model: the end, where the result and any error are.
const specifiedOutputMax = 32 << 10

// runSpecified runs one action of a specified command: the binary, then the
// admin's line with each {placeholder} replaced by the agent's value as part
// of ONE argument, in the action's working folder (the store root unless it
// names one). A folder value is proved inside the store first; any other
// value that would stand alone as an argument may not start with '-', where
// it would be read as an option rather than a value.
func runSpecified(ctx context.Context, user, agentID string, st Store, cmd StoreCommand, act TempToolAction, args map[string]any) (string, error) {
	if err := cmd.templateAllowed(act.CommandTemplate); err != nil {
		return "", err
	}
	vals := map[string]string{}
	for name, p := range act.Params {
		raw, ok := args[name]
		v := strings.TrimSpace(fmt.Sprint(raw))
		if !ok || raw == nil || v == "" {
			continue
		}
		if ref := strings.TrimSpace(p.PathScope); ref != "" {
			abs, err := ResolvePathScope(user, agentID, ref, v, true)
			if err != nil {
				return "", fmt.Errorf("%s: %w", name, err)
			}
			v = abs
		}
		vals[name] = v
	}
	for _, r := range act.Required {
		if _, ok := vals[r]; !ok {
			return "", fmt.Errorf("missing required %q", r)
		}
	}
	var argv []string
	for _, word := range strings.Fields(argsAfterBinary(act.CommandTemplate)) {
		phs := templatePlaceholders(word)
		if len(phs) == 0 {
			argv = append(argv, word)
			continue
		}
		filled, skip := word, false
		for _, ph := range phs {
			v, ok := vals[ph]
			if !ok {
				skip = true // an optional value not given: its argument goes with it
				break
			}
			filled = strings.ReplaceAll(filled, "{"+ph+"}", v)
		}
		if skip {
			continue
		}
		if word == "{"+phs[0]+"}" && len(phs) == 1 && strings.HasPrefix(filled, "-") && act.Params[phs[0]].PathScope == "" {
			return "", fmt.Errorf("%s may not start with '-': it would be read as an option of the command rather than a value", phs[0])
		}
		argv = append(argv, filled)
	}
	dir, err := SubRoot(st.Path, "")
	if err != nil {
		return "", err
	}
	if wd := strings.TrimSpace(act.WorkDir); wd != "" {
		if v, ok := vals[wd]; ok {
			dir = v
		}
	}
	if err := st.roomFor(0); err != nil {
		return "", err
	}
	if a := bgrun.RunActivityFrom(ctx); a != nil {
		a.WatchDir(st.Path)
	}
	defer pathhold.Hold(dir)()
	defer st.wrote()
	out, err := runRegisteredCommand(ctx, dir, cmd.Command, argv...)
	if len(out) > specifiedOutputMax {
		out = "[... earlier output cut ...]\n" + out[len(out)-specifiedOutputMax:]
	}
	if err != nil {
		Log("[filestore.command] agent run of %s/%s %s failed: %v", st.Name, cmd.Name, act.Name, err)
		return fmt.Sprintf("%s\n[exit: %v]", out, err), nil
	}
	Log("[filestore.command] agent ran %s/%s %s", st.Name, cmd.Name, act.Name)
	if strings.TrimSpace(out) == "" {
		return "(ran, printed nothing)", nil
	}
	return out, nil
}

// handleCommandUses reads and sets how a command may be used: GET the mode and
// lines, POST new ones. Admin-only. A change switches Agents off, since what
// agents could do is what changed.
func (T *FileStoreApp) handleCommandUses(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok || !adminOnly(w, r) {
		return
	}
	slug, name, _ := strings.Cut(strings.TrimSpace(r.URL.Query().Get("id")), "/")
	cmd, ok := LoadStoreCommand(T.DB, slug, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		usage := UsageSpecified
		if cmd.open() {
			usage = UsageOpen
		}
		writeJSON(w, map[string]any{"usage": usage, "allowed_uses": strings.Join(cmd.allowedLines(), "\n"), "guide": cmd.Guide})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Usage       string `json:"usage"`
		AllowedUses string `json:"allowed_uses"`
		Guide       string `json:"guide"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	before := cmd.usesLabel()
	cmd.Guide = strings.TrimSpace(body.Guide)
	cmd.Usage = UsageSpecified
	if body.Usage == UsageOpen {
		cmd.Usage = UsageOpen
	}
	cmd.AllowedUses = strings.Split(body.AllowedUses, "\n")
	cmd.AllowedUses = cmd.allowedLines()
	for _, l := range cmd.AllowedUses {
		if strings.ContainsAny(l, ";|&`<>$") {
			http.Error(w, "an allowed use is arguments only: no ; | & ` < > or $ in "+strconv.Quote(l), http.StatusBadRequest)
			return
		}
	}
	// What agents may RUN changed: approval again. Only the guide changed:
	// that is what they are told, so the approval stands.
	msg := "Saved."
	if cmd.usesLabel() != before {
		cmd.Approved = false
		msg = "Saved. Agents is off for this command until you approve it again."
	}
	T.DB.Set(commandsTable, commandKey(cmd.Slug, cmd.Name), cmd)
	Log("[filestore] %s set %s/%s uses: %s", user, cmd.Slug, cmd.Name, cmd.usesLabel())
	writeJSON(w, map[string]any{"ok": true, "message": msg})
}

// MigrateCommandUses gives commands registered before usage modes existed the
// mode that matches how they were made: one already mapped was explored
// freely, so it stays open (an admin can restrict it); one not mapped yet
// starts as specified, like every new command. Idempotent.
func MigrateCommandUses(db Database) {
	for _, cmd := range ListStoreCommands(db) {
		if cmd.Usage != "" {
			continue
		}
		cmd.Usage = UsageSpecified
		if cmd.Mapped() {
			cmd.Usage = UsageOpen
		}
		db.Set(commandsTable, commandKey(cmd.Slug, cmd.Name), cmd)
		Log("[filestore] command %s/%s set to %s (registered before usage modes)", cmd.Slug, cmd.Name, cmd.Usage)
	}
}

// guidePointer tells an agent where the admin's guide to a command is, when
// there is one: in the store's command list, not repeated in every tool.
func guidePointer(st Store, cmd StoreCommand) string {
	if strings.TrimSpace(cmd.Guide) == "" {
		return ""
	}
	return " The admin wrote a guide to using it: list_" + st.Slug + "_commands shows it; read it before the first call."
}
