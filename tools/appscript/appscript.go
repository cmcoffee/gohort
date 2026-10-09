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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
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
	for _, c := range caps {
		if c == "run_agent" || c == "run_pipeline" {
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
	return temptool.DispatchTempToolDirect(sess, tt, args)
}

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
