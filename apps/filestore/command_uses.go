// How agents may use a registered command, once Builder has mapped it with an
// admin and the admin has approved the mapping.
//
// Builder explores a command freely while it maps it: the conversation is the
// admin's own, opened from the command's row, and what it writes is inert
// until the admin turns Agents on. The limit is on AGENTS: an agent runs only
// the command's allowed uses, each line exactly as written ("download
// --src={source} --dst={folder}"), filling only the placeholders. The admin may
// write uses up front to say what Builder should map; every line Builder maps
// is added to them, and what the admin approves is that list. Changing the
// uses turns Agents off.
//
// A use runs as written, directly as the server's account, the way a person's
// click on a store command runs: the binary, then the line with each
// placeholder filled by ONE value, no shell. That is what lets a binary with
// its own saved login (a file-sharing client) work, and it is safe because the
// line is one an admin approved and the model supplies only values.
//
// There used to be a second mode, "open", whose mapped lines ran in the
// sandbox. Once Builder mapped freely in both, the two differed only in where
// an approved line ran, so MigrateCommandUses folds it into this one.
package filestore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bgrun"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/pathhold"
)

// usageCurrent marks a command as one MigrateCommandUses has nothing left to
// do for: every mapped line is one of its allowed uses.
const usageCurrent = "specified"

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
	lines := a.allowedLines()
	if len(lines) == 0 {
		return "only as specified: none yet (Builder adds the lines it maps)"
	}
	return "only: " + strings.Join(lines, " | ")
}

// templateAllowed says whether a mapped action's command line is one an agent
// may run: exactly the binary and one of the command's allowed uses. Checked
// when an agent's tools are built and again at the call, so a use the admin
// has taken off the list stops running.
func (a StoreCommand) templateAllowed(tmpl string) error {
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

// --- running a use ------------------------------------------------------------

// specifiedToolDefs is a command's mapped actions as agent tools, each running
// its line as written (runSpecified), as a background-capable run so a long
// download moves to the background and reports back.
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
				Description: fmt.Sprintf("%s Runs exactly: %s %s, with your values in the {placeholders}; nothing else about the command can be changed.%s%s", strings.TrimSuffix(desc, ".")+".", cmd.Command, line, backgroundNote(cmd), guidePointer(st, cmd)),
				Parameters:  act.Params,
				Required:    act.Required,
				// CapWrite as well: a command changes things, so it comes
				// only through an attachment that allows changes.
				Caps: []Capability{CapExecute, CapWrite},
				Category:    "files",
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return bgrun.BackgroundRun{Tool: name, Label: cmd.Label + " on " + st.Name, DetachAfter: cmd.detachAfter(), Run: func(work *ToolSession) (string, error) {
					resolve := func(ref, v string) (string, error) { return ResolvePathScope(user, sess.AgentID, ref, v, true) }
					return runSpecified(work.Context(), "agent", st, cmd, act, args, resolve)
				}}.Start(sess)
			},
		})
	}
	return out
}

// specifiedOutputMax is how much of a run's output goes back to the
// model: the end, where the result and any error are.
const specifiedOutputMax = 32 << 10

// runSpecified runs one mapped action: the binary, then the approved line,
// split the way a person typing it means it (quotes hold a value with spaces),
// with each {placeholder} replaced by the agent's value as part of ONE
// argument, in the action's working folder (the store root unless it
// names one). A folder value is proved inside the store first; any other
// value that would stand alone as an argument may not start with '-', where
// it would be read as an option rather than a value. specifiedArgv says how
// values become words.
//
// who names the runner in the log ("agent", "app <owner>/<slug>"), and resolve
// proves a folder value: through the path scope for an agent, inside this
// store under the app's grant for an app.
func runSpecified(ctx context.Context, who string, st Store, cmd StoreCommand, act TempToolAction, args map[string]any, resolve func(ref, value string) (string, error)) (string, error) {
	if err := cmd.templateAllowed(act.CommandTemplate); err != nil {
		return "", err
	}
	vals := map[string][]string{}
	for name, p := range act.Params {
		vs := argValues(args[name])
		if len(vs) == 0 {
			continue
		}
		if ref := strings.TrimSpace(p.PathScope); ref != "" {
			for i, v := range vs {
				abs, err := resolve(ref, v)
				if err != nil {
					return "", fmt.Errorf("%s: %w", name, err)
				}
				vs[i] = abs
			}
		}
		vals[name] = vs
	}
	for _, r := range act.Required {
		if _, ok := vals[r]; !ok {
			return "", fmt.Errorf("missing required %q", r)
		}
	}
	argv, err := specifiedArgv(act, vals)
	if err != nil {
		return "", err
	}
	dir, err := SubRoot(st.Path, "")
	if err != nil {
		return "", err
	}
	if wd := strings.TrimSpace(act.WorkDir); wd != "" {
		if v, ok := vals[wd]; ok {
			dir = v[0]
		}
	}
	if err := st.roomFor(0); err != nil {
		return "", err
	}
	if a := bgrun.RunActivityFrom(ctx); a != nil {
		a.WatchDir(st.Path)
		for _, d := range growthDirs(act, vals, dir) {
			a.WatchGrowth(d)
		}
	}
	defer pathhold.Hold(dir)()
	defer st.wrote()
	out, err := runRegisteredCommand(ctx, dir, cmd.Command, argv...)
	if len(out) > specifiedOutputMax {
		out = "[... earlier output cut ...]\n" + out[len(out)-specifiedOutputMax:]
	}
	if err != nil {
		Log("[filestore.command] %s run of %s/%s %s failed: %v", who, st.Name, cmd.Name, act.Name, err)
		return fmt.Sprintf("%s\n[exit: %v]", out, err), nil
	}
	Log("[filestore.command] %s ran %s/%s %s", who, st.Name, cmd.Name, act.Name)
	if strings.TrimSpace(out) == "" {
		return "(ran, printed nothing)", nil
	}
	return out, nil
}

// handleCommandUses reads and sets how a command may be used: GET its uses and
// guide, POST new ones. Admin-only. A change switches Agents off, since what
// agents could do is what changed.
func (T *FileStoreApp) handleCommandUses(c *jsonapi.Call) (any, error) {
	cmd, err := T.adminCommand(c, false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"allowed_uses": strings.Join(cmd.allowedLines(), "\n"), "guide": cmd.Guide, "background": cmd.Background}, nil
}

// handleCommandUsesSave sets what agents may run a command with, and how.
func (T *FileStoreApp) handleCommandUsesSave(c *jsonapi.Call, body usesReq) (any, error) {
	cmd, err := T.adminCommand(c, false)
	if err != nil {
		return nil, err
	}
	before := cmd.usesLabel()
	cmd.Guide = strings.TrimSpace(body.Guide)
	// How it runs, not what it runs: the approval stands.
	cmd.Background = backgroundMode(body.Background)
	cmd.AllowedUses = strings.Split(body.AllowedUses, "\n")
	cmd.AllowedUses = cmd.allowedLines()
	for _, l := range cmd.AllowedUses {
		if strings.ContainsAny(l, ";|&`<>$") {
			return nil, jsonapi.BadRequest("an allowed use is arguments only: no ; | & ` < > or $ in " + strconv.Quote(l))
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
	Log("[filestore] %s set %s/%s uses: %s", c.User, cmd.Slug, cmd.Name, cmd.usesLabel())
	return map[string]any{"ok": true, "message": msg}, nil
}

// usesReq is what handleCommandUsesSave is posted.
type usesReq struct {
	AllowedUses string `json:"allowed_uses"`
	Guide       string `json:"guide"`
	Background  string `json:"background"`
}

// MigrateCommandUses brings commands from before there was one way to run
// them onto it. A command that was "open", or registered before there were
// modes at all, ran its mapped lines in the sandbox and kept no list of uses:
// each mapped line becomes one of its uses, and one that was approved is
// switched off, since it will now run directly as the server's account rather
// than in the sandbox, and that is the admin's to approve. Idempotent.
func MigrateCommandUses(db Database) {
	for _, cmd := range ListStoreCommands(db) {
		if cmd.Usage == usageCurrent {
			continue
		}
		have := map[string]bool{}
		for _, l := range cmd.allowedLines() {
			have[l] = true
		}
		for _, act := range cmd.Tools {
			if l := strings.Join(strings.Fields(argsAfterBinary(act.CommandTemplate)), " "); l != "" && !have[l] {
				cmd.AllowedUses = append(cmd.AllowedUses, l)
				have[l] = true
			}
		}
		was := cmd.Approved
		cmd.Approved = false
		cmd.Usage = usageCurrent
		db.Set(commandsTable, commandKey(cmd.Slug, cmd.Name), cmd)
		if was {
			Log("[filestore] command %s/%s now runs its mapped lines directly rather than in the sandbox: Agents is off until an admin approves it again", cmd.Slug, cmd.Name)
		}
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

// specifiedArgv fills an action's line with its values, one word at a time.
//
//   - A word whose placeholder was given no value is left out, and so is the
//     option just before it when the placeholder stands alone: in
//     "--src {source}" an absent source takes --src with it, or the next
//     word would be read as the source.
//   - A list fills its word once per item ("{files}" with two files is two
//     arguments; "--file={files}" is --file=a --file=b).
//   - A placeholder the action declares no parameter for is refused: the
//     mapping is out of date, and dropping the word would run something
//     other than what was approved.
func specifiedArgv(act TempToolAction, vals map[string][]string) ([]string, error) {
	words, err := splitArgs(argsAfterBinary(act.CommandTemplate))
	if err != nil {
		return nil, fmt.Errorf("the mapped line for %s does not parse (%v): re-map the command", act.Name, err)
	}
	var argv []string
	// lastOption is the index in argv of a literal option word just added,
	// or -1: the option an absent standalone value takes with it.
	lastOption := -1
	for _, word := range words {
		phs := templatePlaceholders(word)
		if len(phs) == 0 {
			argv = append(argv, word)
			lastOption = -1
			if strings.HasPrefix(word, "-") && !strings.Contains(word, "=") {
				lastOption = len(argv) - 1
			}
			continue
		}
		standalone := len(phs) == 1 && word == "{"+phs[0]+"}"
		missing, multi := false, ""
		for _, ph := range phs {
			if _, declared := act.Params[ph]; !declared {
				return nil, fmt.Errorf("the mapped line for %s uses {%s}, which it declares no parameter for: re-map the command", act.Name, ph)
			}
			vs, ok := vals[ph]
			if !ok {
				missing = true
				break
			}
			if len(vs) > 1 {
				if multi != "" {
					return nil, fmt.Errorf("{%s} and {%s} share one argument, so each takes one value, not a list", multi, ph)
				}
				multi = ph
			}
		}
		if missing {
			if standalone && lastOption == len(argv)-1 && lastOption >= 0 {
				argv = argv[:lastOption]
			}
			lastOption = -1
			continue
		}
		lastOption = -1
		fill := func(item string) string {
			out := word
			for _, ph := range phs {
				v := vals[ph][0]
				if ph == multi {
					v = item
				}
				out = strings.ReplaceAll(out, "{"+ph+"}", v)
			}
			return out
		}
		items := []string{""}
		if multi != "" {
			items = vals[multi]
		}
		for _, item := range items {
			filled := fill(item)
			if standalone && strings.HasPrefix(filled, "-") && act.Params[phs[0]].PathScope == "" {
				return nil, fmt.Errorf("%s may not start with '-': it would be read as an option of the command rather than a value", phs[0])
			}
			argv = append(argv, filled)
		}
	}
	return argv, nil
}

// argValues is one parameter's value as command-line text: nothing for an
// absent or blank value, one entry per item for a list, a number as its
// plain digits (a JSON number arrives as a float, which fmt prints as
// 1.2345678e+07), and anything structured as JSON.
func argValues(raw any) []string {
	switch v := raw.(type) {
	case nil:
		return nil
	case []any:
		var out []string
		for _, item := range v {
			if _, nested := item.([]any); nested {
				b, _ := json.Marshal(item)
				out = append(out, string(b))
				continue
			}
			out = append(out, argValues(item)...)
		}
		return out
	case []string:
		var out []string
		for _, item := range v {
			out = append(out, argValues(item)...)
		}
		return out
	}
	s := argText(raw)
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return []string{strings.TrimSpace(s)}
}

// argText is a single value as text.
func argText(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	case map[string]any:
		b, _ := json.Marshal(v)
		return string(b)
	}
	return fmt.Sprint(raw)
}

// backgroundDetach is how soon a command set to always run in the background
// moves there: long enough that a call refused at once (a bad argument)
// still answers inline.
const backgroundDetach = 3 * time.Second

// backgroundMode is a stored Background value: "always" or "".
func backgroundMode(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "always") {
		return "always"
	}
	return ""
}

// detachAfter is the command's own detach time, 0 for the deployment's.
func (a StoreCommand) detachAfter() time.Duration {
	if a.Background == "always" {
		return backgroundDetach
	}
	return 0
}

// growthDirs are the folders a run's output lands in: each folder value it
// was given (a file value's folder), or its working folder when it was given
// none. A run whose files grow there is working, even printing nothing.
func growthDirs(act TempToolAction, vals map[string][]string, workDir string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(d string) {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	for name, p := range act.Params {
		if strings.TrimSpace(p.PathScope) == "" {
			continue
		}
		for _, v := range vals[name] {
			if info, err := os.Stat(v); err == nil && info.IsDir() {
				add(v)
			} else {
				add(filepath.Dir(v))
			}
		}
	}
	if len(out) == 0 {
		add(workDir)
	}
	return out
}

// backgroundNote tells an agent that a command runs in the background, so it
// does not wait on the call for an answer.
func backgroundNote(cmd StoreCommand) string {
	if cmd.Background != "always" {
		return ""
	}
	return " It runs in the background: the call returns at once saying it has started, and you are woken with its result when it ends, so carry on or tell the person it is under way."
}
