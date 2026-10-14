// Package appscript runs a custom-app script (a data source or an action) as a
// sandboxed shell TempTool. It is the single execution seam shared by the host
// that serves /custom/<slug>/data|action/<name> (apps/customapps) and the
// authoring tool's test action (apps/orchestrate) — so a script a developer
// "tests" runs through byte-identical machinery to the one a user triggers, and
// a test pass can never disagree with production behavior.
//
// It lives in tools/ (a leaf importing core + temptool) rather than core/
// because temptool imports core, and rather than customapps because customapps
// imports orchestrate — either placement would cycle.
package appscript

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/gohort/core/bgrun"
	"github.com/cmcoffee/gohort/tools/temptool"
)

// Run executes one custom-app script and returns its stdout. The script is just
// a TempTool the framework dispatches on the app owner's behalf: it runs in the
// owner's workspace under the bwrap sandbox, reaches external data only through
// the gohort hook (fetch/log/…), and receives args as environment variables.
// The script file is named per (kind, slug, name) so concurrent apps/scripts
// don't collide in the owner's workspace.
func Run(user string, db Database, slug, kind, name, language, script string, caps []string, args map[string]any) (string, error) {
	return Job{Owner: user, DB: db, Slug: slug, Kind: kind, Name: name, Language: language, Script: script, Caps: caps, Args: args}.Run()
}

// Job is one script run. Caller is who the run is for (the person who
// clicked or is viewing; the owner when empty), which is whose daily
// allowance a model call from the script is charged to.
type Job struct {
	Owner    string
	DB       Database
	Slug     string
	Kind     string
	Name     string
	Language string
	Script   string
	Caps     []string
	Args     map[string]any
	Caller   string
	// Libs are the app's shared Python modules (AppSpec.Libraries), which a
	// Python script imports by name.
	Libs map[string]string
	// Spec is the app as the run should see it, for a check of an app not
	// saved yet (a folder's run). Nil: the saved app.
	Spec *AppSpec
	// Interactive is a person waiting on this run: a page loading, a button
	// pressed. Its ask and run_agent then go out in the person's lane rather
	// than waiting behind that same person's chat. A timer's run leaves it
	// false and stays background.
	Interactive bool
	// Long runs the script with no fixed time limit, for an action that may
	// take minutes (a download, an extraction): it is stopped only when it
	// prints nothing for the no-output limit, or would fill the disk, the
	// same limits as an agent's background command (core/bgrun). Ctx stops
	// it, and OnProgress hears its latest output line every two seconds.
	// Off, the run keeps the ordinary short limit.
	Long       bool
	Ctx        context.Context
	OnProgress func(line string)
}

// AppRunAgent and AppRunPipeline answer a script's gohort.run_agent and
// gohort.run_pipeline: one of the app's agents with its tools, or its pipeline
// to the end, under the same daily caps as ask. Set by the custom-apps host;
// nil, and refused.
var (
	AppRunAgent    func(ctx context.Context, spec AppSpec, caller, agent, prompt string) (string, error)
	AppRunPipeline func(ctx context.Context, spec AppSpec, caller, pipeline, input string) (string, error)
)

// longRunSecs is the run cap of a script that runs an agent or a pipeline: a
// whole turn with its tools does not fit the ordinary 90 seconds.
const longRunSecs = 300

// AppAsk answers a script's gohort.ask: the app's agent, no tools, the owner
// paying under the app's daily caps. Set by the custom-apps host, which owns
// the caps; nil, and ask is refused.
var AppAsk func(ctx context.Context, spec AppSpec, caller, prompt string, jsonMode bool) (string, error)

// Run executes the job.
func (j Job) Run() (string, error) {
	user, db, slug, kind, name, language, script, caps, args := j.Owner, j.DB, j.Slug, j.Kind, j.Name, j.Language, j.Script, j.Caps, j.Args
	ws, err := EnsureWorkspaceDir(user)
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	interp, ext := "python3", "py"
	if l := strings.ToLower(strings.TrimSpace(language)); l == "bash" || l == "sh" {
		interp, ext = "bash", "sh"
	}
	scriptName := fmt.Sprintf("%s_%s_%s.%s", kind, SanitizeName(slug), SanitizeName(name), ext)
	if caps == nil {
		caps = []string{"fetch", "log"} // sensible default: read external data + log
	} else if onlyAddedCaps(caps) {
		// Naming a tool to call is a grant on top of the defaults, not instead
		// of them: a script that declared tool:get_weather to reuse the
		// owner's forecast must not lose fetch for everything else it reads.
		caps = append([]string{"fetch", "log"}, caps...)
	}
	// Auto-grant fetch_via for every credential the app OWNER may use. A custom-
	// app script always runs in the owner's context (handleData/handleAction pass
	// the owner as `user`), and the owner can already call fetch_via on these
	// credentials from a hand-authored temptool — so an app data source shouldn't
	// have to redeclare capabilities:["fetch_via:<cred>"] just to reach one. The
	// omission failed SILENTLY: the hook denied fetch_via, the script's try/except
	// swallowed the error, and the panel rendered an empty table that even passed
	// verify (the roster-app trap). fetch_url already reaches a credential-covered
	// host via auto-routing; this brings fetch_via to the same parity. Merged +
	// deduped so an explicit declaration is preserved and never doubled.
	caps = mergeCaps(caps, ownerFetchViaCaps(user))
	// The file stores an admin granted this app: their folders are mounted
	// for the run (read, or read and write), their paths handed over in
	// GOHORT_STORES for gohort.store_path, and the app may ask for their
	// commands and pulls (the store checks which). See core/appstores.
	var grants []appstores.Grant
	if appstores.Provider != nil {
		grants = appstores.Provider.GrantsFor(user, slug)
	}
	if len(grants) > 0 {
		caps = mergeCaps(caps, []string{"store_command", "store_pull"})
		byStore := map[string]appstores.Grant{}
		for _, g := range grants {
			byStore[g.Store] = g
		}
		b, _ := json.Marshal(byStore)
		args = withArg(args, "GOHORT_STORES", string(b))
	}
	params := map[string]ToolParam{}
	for k := range args {
		params[k] = ToolParam{Type: "string"}
	}
	command := interp + " {workspace_dir}/" + scriptName
	if interp == "python3" && len(j.Libs) > 0 {
		dir, err := deployLibs(ws, slug, j.Libs)
		if err != nil {
			return "", err
		}
		// Ahead of what the sandbox already puts there (the gohort helper).
		command = "PYTHONPATH={workspace_dir}/" + dir + `:"$PYTHONPATH" ` + command
	}
	tt := &TempTool{
		Name:             "app_" + kind + ":" + slug + ":" + name,
		Description:      "custom app " + kind,
		Mode:             "shell",
		ScriptBody:       script,
		ScriptName:       scriptName,
		CommandTemplate:  command,
		HookCapabilities: caps,
		Params:           params,
	}
	for _, g := range grants {
		if g.Write {
			tt.GrantWrite = append(tt.GrantWrite, g.Path)
		} else {
			tt.GrantRead = append(tt.GrantRead, g.Path)
		}
	}
	for _, c := range caps {
		// A long run has no fixed limit to raise.
		if (c == "run_agent" || c == "run_pipeline") && !j.Long {
			tt.TimeoutSec = longRunSecs
		}
	}
	sess := &ToolSession{
		Username:     user,
		WorkspaceDir: ws,
		DB:           db,
		// The owner is acting in their own app — allow the hook's fetch/browse to
		// reach the network (the sandbox itself stays network-isolated).
		Network: NewNetworkConnector(false),
	}
	base := context.Background()
	if j.Long && j.Ctx != nil {
		base = j.Ctx
	}
	sess.Ctx = base
	if j.Interactive {
		sess.Ctx = WithRunLane(base, LaneInteractive)
	}
	sess.CallTool = func(name string, args map[string]any) (string, error) {
		return temptool.CallToolForScript(sess, name, args)
	}
	caller := strings.TrimSpace(j.Caller)
	if caller == "" {
		caller = user
	}
	app := func() (AppSpec, error) {
		if j.Spec != nil {
			return *j.Spec, nil
		}
		spec, ok := LoadAppSpec(user, slug)
		if !ok {
			return AppSpec{}, fmt.Errorf("no app %q", slug)
		}
		return spec, nil
	}
	sess.Ask = func(prompt string, jsonMode bool) (string, error) {
		if AppAsk == nil {
			return "", fmt.Errorf("ask is not available here")
		}
		spec, err := app()
		if err != nil {
			return "", err
		}
		return AppAsk(sess.Context(), spec, caller, prompt, jsonMode)
	}
	sess.RunAgent = func(agent, prompt string) (string, error) {
		if AppRunAgent == nil {
			return "", fmt.Errorf("run_agent is not available here")
		}
		spec, err := app()
		if err != nil {
			return "", err
		}
		return AppRunAgent(sess.Context(), spec, caller, agent, prompt)
	}
	sess.RunPipeline = func(pipeline, input string) (string, error) {
		if AppRunPipeline == nil {
			return "", fmt.Errorf("run_pipeline is not available here")
		}
		spec, err := app()
		if err != nil {
			return "", err
		}
		return AppRunPipeline(sess.Context(), spec, caller, pipeline, input)
	}
	storeFuncs(sess, user, slug)
	var out string
	if j.Long {
		out, err = j.runLong(sess, tt, args)
	} else {
		out, err = temptool.DispatchTempToolDirect(sess, tt, args)
	}
	names := scriptPathNames(ws, scriptName, slug, kind, name, ext)
	out = names.Replace(out)
	if err != nil {
		if msg := names.Replace(err.Error()); msg != err.Error() {
			err = renamedError{msg: msg, err: err}
		}
	}
	return out, err
}

// storeFuncs gives a run's session the store calls, on its own context, so a
// long command or pull reports to (and is stopped with) that run.
func storeFuncs(sess *ToolSession, owner, slug string) {
	if appstores.Provider == nil {
		return
	}
	sess.StoreCommand = func(store, folder, command, input string) (string, error) {
		return appstores.Provider.RunCommand(sess.Context(), owner, slug, store, folder, command, input)
	}
	sess.StorePull = func(store, caseName string, replace bool) (string, error) {
		return appstores.Provider.Pull(sess.Context(), owner, slug, store, caseName, replace)
	}
}

// withArg is args with one more, in a copy, so the caller's map is left as it
// was.
func withArg(args map[string]any, k string, v any) map[string]any {
	out := make(map[string]any, len(args)+1)
	for key, val := range args {
		out[key] = val
	}
	out[k] = v
	return out
}

// runLong runs the script under the background-run limits. The session has no
// conversation to deliver into, so the run never detaches: it stays here, as
// the caller's own job, with the fixed limit replaced by the no-output and disk
// limits. The script's hooks (call_tool, ask, run_agent) ride the session the
// run is handed, which outlives nothing but has the job's context.
func (j Job) runLong(sess *ToolSession, tt *TempTool, args map[string]any) (string, error) {
	return bgrun.BackgroundRun{Tool: tt.Name, Run: func(work *ToolSession) (string, error) {
		work.CallTool = func(name string, a map[string]any) (string, error) {
			return temptool.CallToolForScript(work, name, a)
		}
		work.Ask, work.RunAgent, work.RunPipeline = sess.Ask, sess.RunAgent, sess.RunPipeline
		storeFuncs(work, j.Owner, j.Slug)
		if j.OnProgress != nil {
			if act := bgrun.RunActivityFrom(work.Context()); act != nil {
				stop := make(chan struct{})
				defer close(stop)
				go func() {
					tick := time.NewTicker(2 * time.Second)
					defer tick.Stop()
					for {
						select {
						case <-stop:
							return
						case <-tick.C:
							if line := act.Summary(); line != "" {
								j.OnProgress(line)
							}
						}
					}
				}()
			}
		}
		return temptool.DispatchTempToolDirect(work, tt, args)
	}}.Start(sess)
}

// scriptPathNames maps where a script and its libraries are staged to run to
// the names its author knows them by, for the output and errors that come back.
//
// The staged copy sits at the owner's workspace root under a generated name
// (data_<app>_<name>.py), which is not in the author's own workspace and is
// named nowhere they wrote: a traceback pointing there sent an author to cd
// into a directory it could not reach, then search the disk for the file. The
// folder layout is fixed (data/, actions/, lib/), so the names are known.
func scriptPathNames(ws, scriptName, slug, kind, name, ext string) *strings.Replacer {
	dir := kind
	switch kind {
	case "data":
		dir = "data"
	case "action":
		dir = "actions"
	}
	shown := dir + "/" + name + "." + ext
	libDir := filepath.Join(".applib", SanitizeName(slug)) + "/"
	// Longest first: the full staged path before its bare file name.
	return strings.NewReplacer(
		filepath.Join(ws, scriptName), shown,
		filepath.Join(ws, libDir)+"/", "lib/",
		libDir, "lib/",
		scriptName, shown,
	)
}

// renamedError is an error whose text names the author's paths, still
// unwrapping to what the run returned.
type renamedError struct {
	msg string
	err error
}

func (e renamedError) Error() string { return e.msg }
func (e renamedError) Unwrap() error { return e.err }

// ownerFetchViaCaps returns a fetch_via:<name> capability for every credential
// the owner may use (their own + globals they're permitted). Empty owner → none.
func ownerFetchViaCaps(owner string) []string {
	if strings.TrimSpace(owner) == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, "fetch_via:"+name)
	}
	for _, c := range Secure().ListUser(owner) {
		add(c.Name)
	}
	for _, c := range Secure().List() {
		// Skip credentials another configuration owns. fetch_via IS a
		// declaring capability, so a secured credential is legitimately
		// usable this way — but the peer key is not a user capability at
		// all. It is an admin resource-sharing arrangement that appears
		// and disappears with its peer, and a script declared against it
		// stops working the day the peer is forgotten.
		if c.ManagedElsewhere() {
			continue
		}
		if Secure().UserMayUse(c, owner) {
			add(c.Name)
		}
	}
	return out
}

// mergeCaps concatenates two capability lists, dropping duplicates and empties so
// an explicitly-declared cap is preserved but never doubled.
func mergeCaps(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range append(append([]string{}, a...), b...) {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// SanitizeName reduces a slug/name to a safe filename fragment (alnum + _).
func SanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// onlyAddedCaps reports a capability list made only of grants that add to the
// defaults rather than replace them: "tool:<name>", "ask", "run_agent" and
// "run_pipeline".
func onlyAddedCaps(caps []string) bool {
	if len(caps) == 0 {
		return false
	}
	for _, c := range caps {
		if !strings.HasPrefix(c, "tool:") && c != "ask" && c != "run_agent" && c != "run_pipeline" {
			return false
		}
	}
	return true
}

// deployLibs writes an app's library modules into their own directory in the
// workspace, <ws>/.applib/<slug>/, and removes any the app no longer has.
// Their own directory, not the workspace root where the scripts land: a
// module there would overwrite a file of the owner's by that name, and two
// apps' engine.py would overwrite each other. Returns the directory relative
// to the workspace.
func deployLibs(ws, slug string, libs map[string]string) (string, error) {
	rel := filepath.Join(".applib", SanitizeName(slug))
	dir := filepath.Join(ws, rel)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("app libraries: %w", err)
	}
	for name, src := range libs {
		if name == "" || strings.ContainsAny(name, `/\.`) {
			return "", fmt.Errorf("app library %q: not a module name", name)
		}
		p := filepath.Join(dir, name+".py")
		if have, err := os.ReadFile(p); err == nil && string(have) == src {
			continue
		}
		// Written aside and renamed in, so a script of the app running at the
		// same moment imports the old module or the new one, never half.
		tmp, err := os.CreateTemp(dir, name+".*.tmp")
		if err != nil {
			return "", fmt.Errorf("app library %q: %w", name, err)
		}
		_, werr := tmp.WriteString(src)
		cerr := tmp.Close()
		if werr != nil || cerr != nil {
			os.Remove(tmp.Name())
			return "", fmt.Errorf("app library %q: could not write it", name)
		}
		if err := os.Rename(tmp.Name(), p); err != nil {
			os.Remove(tmp.Name())
			return "", fmt.Errorf("app library %q: %w", name, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".py") {
			if _, keep := libs[strings.TrimSuffix(n, ".py")]; !keep {
				os.Remove(filepath.Join(dir, n))
			}
		}
	}
	return rel, nil
}
