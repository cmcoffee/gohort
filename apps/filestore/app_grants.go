// Apps an admin has granted a store.
//
// A store is deployment configuration, so which app may use one is an admin's
// decision, made on the store's page. The grant belongs to the APP (owner and
// slug): it covers whoever uses the app, the way a tool carries what its
// author was allowed, and the store's own list of users does not apply inside
// it. Reading is always part of a grant; writing (add, change and delete
// files), running the store's commands, and pulling files and folders into it are each
// granted on their own.
//
// The app's scripts reach a granted store three ways: its folder mounted in
// their sandbox, read-only or read-write (core/appstores, tools/appscript);
// gohort.store_command, a registered command on one of its folders; and
// gohort.store_pull, a file or folder copied in from the store it pulls from. The last
// two are answered here, and check the grant again.
package filestore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/gohort/core/bgrun"
	"github.com/cmcoffee/gohort/core/pathhold"
)

// AppGrant is one app's access to one store.
type AppGrant struct {
	App      string `json:"app"`      // "<owner>/<slug>"
	Write    bool   `json:"write"`    // add, change and delete files
	Commands bool   `json:"commands"` // run this store's registered commands
	Pull     bool   `json:"pull"`     // pull files and folders into this store
}

func appKey(owner, slug string) string { return owner + "/" + slug }

// rights says what a grant allows, for the admin table.
func (g AppGrant) rights() string {
	r := []string{"read"}
	if g.Write {
		r = append(r, "write")
	}
	if g.Commands {
		r = append(r, "run commands")
	}
	if g.Pull {
		r = append(r, "pull files and folders in")
	}
	return strings.Join(r, ", ")
}

// grantFor is the store's grant to the app, if it has one.
func (s Store) grantFor(owner, slug string) (AppGrant, bool) {
	key := appKey(owner, slug)
	for _, g := range s.AppGrants {
		if g.App == key {
			return g, true
		}
	}
	return AppGrant{}, false
}

// storeHost answers app scripts for the stores they were granted.
type storeHost struct{ app *FileStoreApp }

func (h storeHost) GrantsFor(owner, slug string) []appstores.Grant {
	var out []appstores.Grant
	for _, st := range ListStores(h.app.DB) {
		g, ok := st.grantFor(owner, slug)
		if !ok || st.Valid() != nil {
			continue
		}
		out = append(out, appstores.Grant{Store: st.Slug, Name: st.Name, Path: st.Path,
			Read: true, Write: g.Write, Commands: g.Commands, Pull: g.Pull})
	}
	return out
}

// Stores lists the stores owner may reach for an app's author, with what
// each holds, its commands and slug's grant on it (appstores.Catalog).
func (h storeHost) Stores(owner, slug string) []appstores.StoreInfo {
	var out []appstores.StoreInfo
	for _, st := range StoresForUser(h.app.DB, owner) {
		if st.Valid() != nil {
			continue
		}
		info := appstores.StoreInfo{Store: st.Slug, Name: st.Name, About: strings.TrimSpace(st.Description),
			Temporary: st.Ephemeral, FolderCount: len(h.app.listScope(owner, st.Slug))}
		if src, ok := st.pullSource(h.app.DB); ok {
			info.PullsFrom, info.PullPart = src.Slug, st.PullSubpath
			if info.PullPart == "" {
				info.PullPart = "prepared"
			}
		}
		for _, c := range StoreCommandsFor(h.app.DB, st.Slug) {
			ci := appstores.CommandInfo{Name: c.Name, Label: c.Label, TwoPhase: c.TwoPhase,
				InputLabel: c.InputLabel, Note: strings.TrimSpace(c.Help), Guide: strings.TrimSpace(c.Guide)}
			if c.Mapped() && !c.Approved {
				ci.Mapped = true
			}
			if c.Approved {
				for _, act := range c.runnableActions() {
					ci.Actions = append(ci.Actions, actionInfo(act))
				}
			}
			info.Commands = append(info.Commands, ci)
		}
		if slug != "" {
			if g, ok := st.grantFor(owner, slug); ok {
				info.Granted = &appstores.Grant{Store: st.Slug, Name: st.Name, Read: true, Write: g.Write, Commands: g.Commands, Pull: g.Pull}
			}
		}
		out = append(out, info)
	}
	return out
}

// grantedStore loads store and the app's grant on it, or says why not.
func (h storeHost) grantedStore(owner, slug, store string) (Store, AppGrant, error) {
	st, ok := LoadStore(h.app.DB, strings.TrimSpace(store))
	if !ok {
		return Store{}, AppGrant{}, fmt.Errorf("there is no file store %q", store)
	}
	g, ok := st.grantFor(owner, slug)
	if !ok {
		return Store{}, AppGrant{}, fmt.Errorf("this app has no grant on %s; an admin grants one on the store's page (Admin, Files)", st.Name)
	}
	return st, g, nil
}

func (h storeHost) RunCommand(ctx context.Context, owner, slug, store, folder, command, input string) (string, error) {
	st, g, err := h.grantedStore(owner, slug, store)
	if err != nil {
		return "", err
	}
	if !g.Commands {
		return "", fmt.Errorf("this app may not run %s's commands; an admin can allow it on the store's page", st.Name)
	}
	act, ok := LoadStoreCommand(h.app.DB, st.Slug, command)
	if !ok {
		var names []string
		for _, c := range StoreCommandsFor(h.app.DB, st.Slug) {
			names = append(names, c.Name)
		}
		return "", fmt.Errorf("%s has no command %q (registered: %s)", st.Name, command, strings.Join(names, ", "))
	}
	dir, err := SubRoot(st.Path, folder)
	if err != nil {
		return "", err
	}
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "-") {
		return "", Error("the response starts with '-', which the command would read as an option rather than a value")
	}
	if err := st.roomFor(0); err != nil {
		return "", err
	}
	defer pathhold.Hold(dir)()
	defer st.wrote()
	stop := keepAlive(ctx, fmt.Sprintf("running %s on %s", act.Name, folderLabel(folder)), dir)
	args := []string{dir}
	if input != "" {
		args = append(args, input) // never logged: it may be the secret half of an exchange
	}
	out, err := runRegisteredCommand(ctx, dir, act.Command, args...)
	stop()
	if err != nil {
		Log("[filestore.command] app %s: %s on %s/%s failed: %v", appKey(owner, slug), act.Name, st.Name, folderLabel(folder), err)
		return "", Error(chFirstNonEmpty(out, err.Error()))
	}
	Log("[filestore.command] app %s ran %s on %s/%s", appKey(owner, slug), act.Name, st.Name, folderLabel(folder))
	return out, nil
}

// RunAction runs one of command's mapped actions for the app: the line an
// admin approved (the command's Agents switch), with the app's values in its
// placeholders, exactly as an agent's tool for it runs. A folder value is
// proved inside this store; the app's grant, not the owner's own access, is
// what lets it reach the store.
func (h storeHost) RunAction(ctx context.Context, owner, slug, store, command, action string, values map[string]any) (string, error) {
	st, g, err := h.grantedStore(owner, slug, store)
	if err != nil {
		return "", err
	}
	if !g.Commands {
		return "", fmt.Errorf("this app may not run %s's commands; an admin can allow it on the store's page", st.Name)
	}
	cmd, ok := LoadStoreCommand(h.app.DB, st.Slug, command)
	if !ok {
		var names []string
		for _, c := range StoreCommandsFor(h.app.DB, st.Slug) {
			names = append(names, c.Name)
		}
		return "", fmt.Errorf("%s has no command %q (registered: %s)", st.Name, command, strings.Join(names, ", "))
	}
	if !cmd.Mapped() {
		return "", fmt.Errorf("%s's command %q has no mapped actions: call it with store_command, or an admin maps it with Builder on the store's page", st.Name, cmd.Name)
	}
	if !cmd.Approved {
		return "", fmt.Errorf("%s's command %q is mapped but not approved yet: an admin turns on Agents on its row, and that switch is the approval", st.Name, cmd.Name)
	}
	var names []string
	for _, act := range cmd.runnableActions() {
		if act.Name != strings.TrimSpace(action) {
			names = append(names, act.Name)
			continue
		}
		resolve := func(ref, v string) (string, error) {
			if _, pinned := SplitPathScopeRef(ref); pinned != st.Slug {
				return "", fmt.Errorf("this value is a folder in another store (%s)", ref)
			}
			return st.resolveValue(v)
		}
		stop := keepAlive(ctx, fmt.Sprintf("running %s %s on %s", cmd.Name, act.Name, st.Name), "")
		defer stop()
		return runSpecified(ctx, "app "+appKey(owner, slug), st, cmd, act, values, resolve)
	}
	return "", fmt.Errorf("%s's command %q has no action %q (its actions: %s)", st.Name, cmd.Name, action, strings.Join(names, ", "))
}

// actionInfo describes a mapped action for an app's author.
func actionInfo(act TempToolAction) appstores.ActionInfo {
	ai := appstores.ActionInfo{Name: act.Name, Description: strings.TrimSpace(act.Description),
		Line: strings.Join(strings.Fields(argsAfterBinary(act.CommandTemplate)), " ")}
	var names []string
	for n := range act.Params {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := act.Params[n]
		ai.Values = append(ai.Values, appstores.ValueInfo{Name: n, Type: p.Type, Description: strings.TrimSpace(p.Description),
			Required: slices.Contains(act.Required, n), Folder: strings.TrimSpace(p.PathScope) != ""})
	}
	return ai
}

func (h storeHost) Pull(ctx context.Context, owner, slug, store, item string, replace bool) (string, error) {
	dest, g, err := h.grantedStore(owner, slug, store)
	if err != nil {
		return "", err
	}
	if !g.Pull {
		return "", fmt.Errorf("this app may not pull into %s; an admin can allow it on the store's page", dest.Name)
	}
	job, err := h.app.pullJobFor(dest, item, "app "+appKey(owner, slug), replace)
	if err != nil {
		return "", err
	}
	return waitForPull(ctx, job)
}

func folderLabel(folder string) string {
	if strings.TrimSpace(folder) == "" {
		return "(the store's top level)"
	}
	return folder
}

// keepAlive shows what the call is doing beside the run, every ten seconds,
// until stop, and watches dir (when given) for what it writes. The line is a
// status, not activity: a registered command that prints nothing is alive
// while its files grow there, and stuck when they stop (bgrun's no-output
// limit), rather than kept going by this ticking.
func keepAlive(ctx context.Context, line, dir string) (stop func()) {
	act := bgrun.RunActivityFrom(ctx)
	if act == nil {
		return func() {}
	}
	act.WatchGrowth(dir)
	done := make(chan struct{})
	start := time.Now()
	act.Note(line)
	go func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				act.Note(fmt.Sprintf("%s (%s)", line, time.Since(start).Round(time.Second)))
			}
		}
	}()
	var once bool
	return func() {
		if !once {
			once = true
			close(done)
		}
	}
}

// --- the admin side -------------------------------------------------------

// handleAppGrants is the store panel's "Apps that may use this store".
//
//	GET    /filestore/api/app-grants?slug=<store>           the grants
//	POST   /filestore/api/app-grants?slug=<store>           add or change one {app, write, commands, pull}
//	DELETE /filestore/api/app-grants?slug=<store>&app=<k>   remove one
func (T *FileStoreApp) handleAppGrants(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok || !adminOnly(w, r) {
		return
	}
	st, ok := LoadStore(T.DB, r.URL.Query().Get("slug"))
	if !ok {
		http.Error(w, "no such store", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows := []map[string]any{}
		for _, g := range st.AppGrants {
			rows = append(rows, map[string]any{"app": g.App, "label": appLabel(g.App), "rights": g.rights()})
		}
		writeJSON(w, rows)
	case http.MethodPost:
		var g AppGrant
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&g); err != nil || strings.Count(g.App, "/") != 1 {
			http.Error(w, "pick an app", http.StatusBadRequest)
			return
		}
		kept := st.AppGrants[:0:0]
		for _, old := range st.AppGrants {
			if old.App != g.App {
				kept = append(kept, old)
			}
		}
		st.AppGrants = append(kept, g)
		if _, err := SaveStore(T.DB, st); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		Log("[filestore] %s granted app %s on %s: %s", user, g.App, st.Name, g.rights())
		writeJSON(w, map[string]any{"ok": true})
	case http.MethodDelete:
		app := r.URL.Query().Get("app")
		kept := st.AppGrants[:0:0]
		for _, g := range st.AppGrants {
			if g.App != app {
				kept = append(kept, g)
			}
		}
		st.AppGrants = kept
		if _, err := SaveStore(T.DB, st); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		Log("[filestore] %s removed app %s's grant on %s", user, app, st.Name)
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// appLabel names an app for a person: its name and its owner.
func appLabel(key string) string {
	owner, slug, _ := strings.Cut(key, "/")
	if spec, ok := LoadAppSpec(owner, slug); ok && strings.TrimSpace(spec.Name) != "" {
		return spec.Name + " (" + owner + ")"
	}
	return key + " (no longer exists)"
}

// handleAppOptions lists every app, for the grant picker.
func (T *FileStoreApp) handleAppOptions(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok || !adminOnly(w, r) {
		return
	}
	out := []map[string]any{}
	if AuthDB != nil {
		for _, u := range AuthListUsers(AuthDB()) {
			for _, spec := range ListAppSpecs(u.Username) {
				name := spec.Name
				if strings.TrimSpace(name) == "" {
					name = spec.Slug
				}
				out = append(out, map[string]any{"value": appKey(u.Username, spec.Slug), "label": name + " (" + u.Username + ")"})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["label"].(string) < out[j]["label"].(string) })
	writeJSON(w, out)
}
