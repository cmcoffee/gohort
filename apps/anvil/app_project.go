// One of your apps, opened in Anvil (docs/design/apps-as-repositories.md).
//
// The project's checkout is the app's folder (orchestrate.WriteAppFolder): its
// app.json, page, scripts, shared modules, assets, notes and checks, kept in
// git so Changes and Download changes work on it, with each Install a commit.
// The installed app keeps serving everyone until Install, which goes through
// the same checks and gates a Builder save does; Preview shows the working
// copy to its owner alone, on a copy of their records unless they choose
// their real ones. For an app nobody else uses, Install on every save installs
// as each turn ends with changes, so the running app is what is being edited.
package anvil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/apps/customapps"
	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

// sourceApp marks a checkout that is one of its owner's apps.
const sourceApp = "app"

// appHost is the orchestrate app that installs and builds app folders.
func appHost() (*orchestrate.OrchestrateApp, error) {
	if o, ok := agenthost.Get().(*orchestrate.OrchestrateApp); ok && o != nil {
		return o, nil
	}
	return nil, fmt.Errorf("the app host is not running")
}

// appProjectFields are an app project's own settings.
func appProjectFields() []ui.FormField {
	return []ui.FormField{
		{Field: "install_on_save", Label: "Install on every save", Type: "toggle",
			Help: "When a turn ends with changes, install them, through the app's checks; a refused install sends the assistant back to fix it. Only for an app nobody else uses: a shared app is previewed while you work and installed when you press Install."},
		{Field: "preview_real", Label: "Preview on my real records", Type: "toggle",
			Help: "Off: the preview reads and writes a copy of your records, so trying a Delete changes nothing real. On: what you do in the preview is done to your records."},
	}
}

// appUsedByOthers reports whether anyone but its owner uses the app: it is
// shared with anyone, or maintained by someone.
func appUsedByOthers(spec AppSpec) bool {
	return spec.Shared || len(spec.SharedWith) > 0 || len(spec.SharedGroups) > 0 || len(spec.Maintainers) > 0
}

// appOptions are the person's own apps, for New project's "One of your apps".
func appOptions(user string) []ui.SelectOption {
	var out []ui.SelectOption
	for _, s := range ListAppSpecs(user) {
		if !appOpensAsFiles(s) {
			continue
		}
		label := strings.TrimSpace(s.Name)
		if label == "" {
			label = s.Slug
		}
		out = append(out, ui.SelectOption{Value: s.Slug, Label: label})
	}
	return out
}

// appOpensAsFiles reports whether an app can be written as its folder: one
// made before apps kept their page as editable sections cannot, until it is
// changed once with Builder.
func appOpensAsFiles(spec AppSpec) bool {
	return len(strings.TrimSpace(string(spec.Sections))) > 2
}

// appNotFiles is what someone opening such an app is told.
const appNotFiles = "it was made before apps kept their page in a form that can be edited as files. Change it once with Builder (any small change), and it opens here"

// appProjectForm is New project's "One of your apps".
func appProjectForm(user string) ui.FormPanel {
	return ui.FormPanel{
		PostURL:        "api/apps/open",
		SubmitLabel:    "Open",
		RedirectURL:    "?project={id}",
		RedirectTarget: "_self",
		Fields: []ui.FormField{
			{Field: "slug", Label: "App", Type: "select", Required: true, Options: appOptions(user),
				Help: "Its files open as a project of your own. Nothing changes for anyone using it until you press Install."},
		},
	}
}

// handleOpenApp opens one of the person's apps as a project, or the project
// it already is.
func (T *Anvil) handleOpenApp(c *jsonapi.Call, in struct {
	Slug string `json:"slug"`
}) (any, error) {
	user, udb := c.User, c.UDB
	slug := strings.TrimSpace(in.Slug)
	spec, ok := LoadAppSpec(user, slug)
	if slug == "" || !ok {
		return nil, jsonapi.NotFound("you have no app " + slug)
	}
	if !appOpensAsFiles(spec) {
		return nil, jsonapi.Status(409, spec.Name+" cannot be opened yet: "+appNotFiles)
	}
	for _, p := range T.listProjects(udb, user, callerIsAdmin(c.R)) {
		if p.Source == sourceApp && p.AppSlug == slug && strings.EqualFold(p.Owner, user) {
			return map[string]any{"ok": true, "id": p.ID}, nil
		}
	}
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		name = slug
	}
	p := Project{ID: UUIDv4(), Name: name, Kind: KindClone, Owner: user, Source: sourceApp, AppSlug: slug, CreatedAt: time.Now().UTC()}
	base, err := p.baseDir()
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if err := writeAppCheckout(user, slug, base); err != nil {
		os.RemoveAll(base)
		return nil, jsonapi.Status(500, "the app could not be opened: "+err.Error())
	}
	T.saveProject(udb, p)
	return map[string]any{"ok": true, "id": p.ID}, nil
}

// writeAppCheckout writes the app's folder into base and makes it a git
// repository whose first commit is the app as installed. The folder's live
// marker is the install's business, not the history's, so git leaves it be.
func writeAppCheckout(user, slug, base string) error {
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	// The app's own history, where there is one (app_history.go); a history
	// of the project's own where git is missing or the clone failed.
	switch err := cloneAppHistory(context.Background(), user, slug, base); {
	case err == nil:
		return nil
	case !errors.Is(err, orchestrate.ErrNoAppHistory):
		Log("[anvil] %s: the app's history could not be taken, so the project keeps one of its own: %v", slug, err)
		os.RemoveAll(base)
		if err := os.MkdirAll(base, 0o700); err != nil {
			return err
		}
	}
	if err := orchestrate.WriteAppFolder(user, slug, base); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(base, ".gitignore"), []byte(appLiveMarker+"\n"), 0o644); err != nil {
		return err
	}
	if _, err := runGit(base, "init", "-q"); err != nil {
		return err
	}
	return commitCheckout(base, user, "As installed")
}

// commitCheckout commits everything in the checkout, as the person.
func commitCheckout(base, user, msg string) error {
	if _, err := runGit(base, "add", "-A", "--", "."); err != nil {
		return err
	}
	if out, _ := runGit(base, "status", "--porcelain"); strings.TrimSpace(out) == "" {
		return nil
	}
	_, err := runGit(base, "-c", "user.name="+user, "-c", "user.email="+user+"@users.gohort.invalid",
		"-c", "commit.gpgsign=false", "commit", "-q", "-m", msg)
	return err
}

// appProjectOf is the app project {id} names, when it is the caller's own.
func (T *Anvil) appProjectOf(c *jsonapi.Call) (Project, string, error) {
	p, err := T.projectOf(c)
	if err != nil {
		return p, "", err
	}
	if p.Source != sourceApp || !strings.EqualFold(p.Owner, c.User) {
		return p, "", jsonapi.BadRequest("this project is not one of your apps")
	}
	base, err := p.baseDir()
	if err != nil {
		return p, "", jsonapi.BadRequest(err.Error())
	}
	return p, base, nil
}

// handleAppInstall installs the working copy, through every check and gate,
// and commits it.
func (T *Anvil) handleAppInstall(c *jsonapi.Call, _ struct{}) (any, error) {
	p, base, err := T.appProjectOf(c)
	if err != nil {
		return nil, err
	}
	msg, err := installAppCheckout(c.R.Context(), c.UDB, c.User, p, base)
	if broken, ok := savedBroken(err); ok {
		return map[string]any{"ok": true, "message": msg, "broken": broken.Problem}, nil
	}
	if err != nil {
		return nil, jsonapi.Status(409, err.Error())
	}
	return map[string]any{"ok": true, "message": msg}, nil
}

// installAppCheckout installs the checkout as user and, when it went in,
// commits it, so the history is what was installed.
// The message the assistant wrote for the changes (install_message) is the
// commit's and the app revision's note; without one, they say Installed.
func installAppCheckout(ctx context.Context, udb Database, user string, p Project, base string) (string, error) {
	o, err := appHost()
	if err != nil {
		return "", err
	}
	msg := installMessage(udb, p.ID)
	note, commit := "Installed from Anvil", "Installed"
	if msg != "" {
		note, commit = msg, msg
	}
	// What was saved to the app since this checkout took it comes in first,
	// merged with the work; an overlap stops the install as a conflict.
	inHistory, err := takeAppChanges(ctx, user, p.AppSlug, base)
	if err != nil {
		return "", err
	}
	_, err = o.InstallAppFolder(ctx, user, base, note)
	broken, isBroken := savedBroken(err)
	if err != nil && !isBroken {
		return "", folderError(base, err)
	}
	// Installed, working or not: what is live is what is committed.
	if udb != nil && msg != "" {
		udb.Unset(installMessageTable, p.ID)
	}
	// In the app's history the install made the commit; the checkout moves
	// onto it. A project with a history of its own commits there.
	// A broken install is not finished work: it is not committed, and the
	// change stays in the checkout for the next install.
	if inHistory {
		if !isBroken {
			if err := settleOnAppHistory(ctx, user, p.AppSlug, base); err != nil {
				Log("[anvil] %s installed, but the checkout could not take the app's commit: %v", p.AppSlug, err)
			}
		}
	} else if err := commitCheckout(base, user, commit); err != nil {
		Log("[anvil] %s installed, but the commit failed: %v", p.AppSlug, err)
	}
	if isBroken {
		return "Installed, but " + p.Name + " does not work yet: " + broken.Problem + ". It is live as it is, at /apps/" + p.AppSlug + "/: ask the assistant to fix it, then install again." + leftOutLine(base), broken
	}
	return "Installed: " + p.Name + " now runs your changes, at /apps/" + p.AppSlug + "/." + leftOutLine(base), nil
}

// savedBroken reports an install that went through and left the app broken
// (orchestrate.AppSavedBroken): live, but its page fails or a data source
// does. It is said, not taken for a refusal: the app changed.
func savedBroken(err error) (orchestrate.AppSavedBroken, bool) {
	var b orchestrate.AppSavedBroken
	return b, errors.As(err, &b)
}

// --- a project that holds an app ----------------------------------------------

// A project brought to Anvil some other way (an uploaded bundle, a
// repository, one started here) can hold an app's folder: app.json at its
// top, its page and scripts beside it. Installed as a new app, it becomes the
// app's project, as if it had been opened from One of your apps: Preview and
// Install from then on, and an install is a commit. Builder cannot reach a
// project's checkout, and Anvil's Install was only on a project opened from
// an app, so such a folder had no way to become one.

// holdsAppFolder reports whether the checkout at base has an app.json at its
// top.
func holdsAppFolder(base string) bool {
	fi, err := os.Stat(filepath.Join(base, "app.json"))
	return err == nil && fi.Mode().IsRegular()
}

// newAppRefusal is why user may not install p as a new app, or "" when they
// may.
func newAppRefusal(p Project, user string) string {
	if why := newAppOffRefusal(p, user); why != "" {
		return why
	}
	base, err := p.baseDir()
	if err != nil {
		return err.Error()
	}
	if !holdsAppFolder(base) {
		return "the project has no app.json at its top" + appJSONElsewhere(base) + ": an app's folder holds app.json, with its page and scripts beside it. Ask the assistant to start the app here (start_app), or move the app's files to the top"
	}
	return ""
}

// newAppOffRefusal is why the project can never be installed as a new app
// by user, whatever its files: what decides whether the button is offered.
// The bar is drawn once, so a project the assistant turns into an app
// during a turn must already carry it.
func newAppOffRefusal(p Project, user string) string {
	switch {
	case p.Kind != KindClone:
		return "only a checkout Anvil made can become an app"
	case p.Source == sourceApp:
		return "this project is an app already: Install puts its changes live"
	case p.Unpack != "":
		return "this checkout is not ready"
	case !strings.EqualFold(p.Owner, user):
		return "only the project's owner can make it an app"
	}
	return ""
}

// appJSONElsewhere names an app.json one folder down, for a refusal: the
// assistant may have built the app in a folder of its own.
func appJSONElsewhere(base string) string {
	found, _ := filepath.Glob(filepath.Join(base, "*", "app.json"))
	if len(found) == 0 {
		return ""
	}
	rel, _ := filepath.Rel(base, found[0])
	return " (there is one at " + filepath.ToSlash(rel) + ", inside a folder)"
}

// handleAppInstallNew installs the project's folder as a new app of the
// caller's, through every check and gate an install has, and makes the
// project that app's. An app of theirs with the same slug is never written
// over: that one is changed by opening it.
func (T *Anvil) handleAppInstallNew(c *jsonapi.Call, _ struct{}) (any, error) {
	p, err := T.projectOf(c)
	if err != nil {
		return nil, err
	}
	if why := newAppRefusal(p, c.User); why != "" {
		return nil, jsonapi.BadRequest(why)
	}
	base, err := p.baseDir()
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	o, err := appHost()
	if err != nil {
		return nil, jsonapi.Status(503, err.Error())
	}
	ctx := c.R.Context()
	slug, name, err := o.AppFolderSlug(ctx, c.User, base)
	if err != nil {
		return nil, jsonapi.Status(409, folderError(base, err).Error())
	}
	if have, ok := LoadAppSpec(c.User, slug); ok {
		return nil, jsonapi.Status(409, fmt.Sprintf("you already have an app %q at /apps/%s/: to change it, open it with New project, One of your apps; for a new app, give this one another name (or slug) in app.json", have.Name, slug))
	}
	// The install leaves the folder's live marker in the checkout; it is the
	// install's business, not the history's.
	if err := excludeFromGit(base, appLiveMarker); err != nil {
		Log("[anvil] %s: the live marker could not be kept out of git: %v", p.ID, err)
	}
	_, err = o.InstallAppFolder(ctx, c.User, base, "Installed from Anvil as a new app")
	broken, isBroken := savedBroken(err)
	if err != nil && !isBroken {
		return nil, jsonapi.Status(409, folderError(base, err).Error())
	}
	// The app exists now, working or not, so the project is its.
	p.Source, p.AppSlug = sourceApp, slug
	T.saveProject(c.UDB, p)
	if err := commitCheckout(base, c.User, "Installed as a new app"); err != nil {
		Log("[anvil] %s installed as a new app, but the commit failed: %v", slug, err)
	}
	if isBroken {
		return map[string]any{"ok": true, "slug": slug, "broken": broken.Problem,
			"message": "Installed: " + name + " is one of your apps now, at /apps/" + slug + "/, but it does not work yet: " + broken.Problem + ". Ask the assistant to fix it, then press Install. This project is its files from here on." + leftOutLine(base)}, nil
	}
	return map[string]any{"ok": true, "slug": slug,
		"message": "Installed: " + name + " is one of your apps now, at /apps/" + slug + "/. This project is its files from here on: Preview shows your changes to you alone, and Install puts them live." + leftOutLine(base)}, nil
}

// startAppTool starts a new app's folder at the top of a project that holds
// none: the starter Builder writes, app.json, a page wired to window.app and
// NOTES.md, each written as create_file would write it (a card, an undo). It
// answers with how the folder is built, so code brought here some other way
// can be ported into an app the owner then installs with Install as a new
// app.
func (c *toolCtx) startAppTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "start_app",
			Description: "Start a gohort app in this project: writes app.json, page.html and NOTES.md at the project's top, the starter of a new app, and returns how an app's folder is built (its files, its scripts, what its page may call, its checks). " +
				"Use it to port this project's code into a gohort app, then build the app in those files. The owner makes it one of their apps with Install as a new app (on the project's bar, or in its Project menu). Refused when the project already has an app.json at its top.",
			Parameters: map[string]ToolParam{
				"name":        {Type: "string", Description: "The app's name, as people will see it."},
				"slug":        {Type: "string", Description: "Optional: its address, /apps/<slug>/; made from the name when left out."},
				"description": {Type: "string", Description: "Optional: one line saying what the app does."},
			},
			Required: []string{"name"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := strings.TrimSpace(strArg(args, "name"))
			if name == "" {
				return "", fmt.Errorf("name the app")
			}
			if holdsAppFolder(c.base) {
				return "", fmt.Errorf("this project already has an app.json at its top: build in it (app_guide says how)")
			}
			files, slug := orchestrate.AppFolderStarter(name, strArg(args, "slug"), strArg(args, "description"))
			names := make([]string, 0, len(files))
			for f := range files {
				names = append(names, f)
			}
			sort.Strings(names)
			for _, f := range names {
				t, err := c.resolve(f)
				if err != nil {
					return "", err
				}
				if err := t.requireWritable(); err != nil {
					return "", err
				}
				if _, existed, err := readIfPresent(t.abs); err != nil {
					return "", err
				} else if existed {
					return "", fmt.Errorf("%s already exists in the project; move it aside, or write the app's files yourself (app_guide says how)", f)
				}
			}
			for _, f := range names {
				t, _ := c.resolve(f)
				if err := os.MkdirAll(filepath.Dir(t.abs), 0o755); err != nil {
					return "", fmt.Errorf("make the folder for %s: %w", f, err)
				}
				if err := os.WriteFile(t.abs, files[f], 0o644); err != nil {
					return "", fmt.Errorf("write %s: %w", f, err)
				}
				c.publish(journalWrite(c.udb, c.project.ID, c.session(), f, "", false, string(files[f]), false))
			}
			return fmt.Sprintf("Started app %q (/apps/%s/ once installed): %s. Build it in those files, and add data/, actions/, lib/ and checks.json as it needs. "+
				"When it is ready, the owner presses Install as a new app (on the project's bar, or in its Project menu), which runs its checks; there is no install command. Tell them so when you finish.\n\n%s",
				name, slug, strings.Join(names, ", "), orchestrate.AppFolderGuide()), nil
		},
	}
}

// appGuideTool is how an app's folder is built, for a project that holds
// one: the starter's guide again, for a turn after the one that started it.
func (c *toolCtx) appGuideTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "app_guide",
			Description: "How this project's gohort app is built: what app.json, page.html, data/, actions/, lib/ and checks.json hold, how a script reads its records and what it prints, what the page may call through window.app. Read it before writing or changing the app's files.",
			Parameters:  map[string]ToolParam{},
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			return orchestrate.AppFolderGuide(), nil
		},
	}
}

// folderError is an install's or a build's reason, as the person reads it:
// without the wrapping that names the folder, and with the folder's own name
// (the project's id, which says nothing to them) taken off each file it
// names, so "<id>/checks.json does not parse" reads "checks.json does not
// parse".
func folderError(base string, err error) error {
	if err == nil {
		return nil
	}
	if inner := errors.Unwrap(err); inner != nil {
		err = inner
	}
	id := filepath.Base(base)
	msg := strings.ReplaceAll(err.Error(), id+"/", "")
	msg = strings.ReplaceAll(msg, id, "the project")
	return errors.New(msg)
}

// leftOutLine names what is at the checkout's top that an install does not
// take, so a ported app that still reads one of them (flows.json) is not
// left to be found empty. "" when there is none.
func leftOutLine(base string) string {
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	part := map[string]bool{appLiveMarker: true, ".git": true, ".gitignore": true}
	for _, n := range orchestrate.AppFolderParts {
		part[n] = true
	}
	var out []string
	for _, e := range entries {
		if !part[e.Name()] {
			out = append(out, e.Name())
		}
	}
	if len(out) == 0 {
		return ""
	}
	if len(out) > 8 {
		out = append(out[:8], fmt.Sprintf("and %d more", len(out)-8))
	}
	return " Not part of the app, and not installed: " + strings.Join(out, ", ") + ". The app cannot read these: a data file it needs goes in assets/."
}

// appLiveMarker is the file an install leaves in an app's folder, saying
// which version of the live app the folder holds.
const appLiveMarker = ".live"

// excludeFromGit keeps name out of the checkout's history without changing a
// file of the project's own: the repository's local exclude list.
func excludeFromGit(base, name string) error {
	path := filepath.Join(base, ".git", "info", "exclude")
	have, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(have), "\n") {
		if strings.TrimSpace(line) == name || strings.TrimSpace(line) == "/"+name {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if len(have) > 0 && have[len(have)-1] != '\n' {
		have = append(have, '\n')
	}
	return os.WriteFile(path, append(have, []byte("/"+name+"\n")...), 0o644)
}

// handleAppPreview builds the working copy and starts, or refreshes, the
// person's preview of it, answering the link that opens it.
func (T *Anvil) handleAppPreview(c *jsonapi.Call, _ struct{}) (any, error) {
	p, base, err := T.appProjectOf(c)
	if err != nil {
		return nil, err
	}
	link, err := previewAppCheckout(c.R.Context(), c.User, p, base)
	if err != nil {
		return nil, jsonapi.Status(409, err.Error())
	}
	return map[string]any{"ok": true, "url": link}, nil
}

// previewAppCheckout builds the checkout and starts or refreshes its preview.
func previewAppCheckout(ctx context.Context, user string, p Project, base string) (string, error) {
	o, err := appHost()
	if err != nil {
		return "", err
	}
	spec, err := o.BuildAppFolder(ctx, user, base)
	if err != nil {
		return "", folderError(base, err)
	}
	return customapps.StartAppPreview(user, spec, p.PreviewReal)
}

// appFinish is an app project's part of the turn's ending, after the check:
// with Install on every save and an app nobody else uses, the changes are
// installed, and a refusal sends the assistant back with it; otherwise a
// running preview is brought up to date. Nothing happens without changes.
//
// Before either, any project holding an app (an app project, or code being
// ported into one) is checked for whether it would install, so a refusal
// reaches the assistant that can fix it rather than the person pressing
// Install ("check runs my_cases, which is not one of the app's data sources").
func (c *toolCtx) appFinish(edited bool) string {
	p := c.project
	if c.readOnly || !edited || (p.Source != sourceApp && !holdsAppFolder(c.base)) {
		return ""
	}
	ctx := c.lastCtx()
	if o, err := appHost(); err == nil {
		if err := o.AppFolderInstallable(ctx, c.user, c.base); err != nil {
			return "This app would not install: " + folderError(c.base, err).Error() + "\nFix what it names (app_checks checks again), then finish."
		}
	}
	if p.Source != sourceApp {
		return ""
	}
	if p.InstallOnSave {
		if live, ok := LoadAppSpec(c.user, p.AppSlug); ok && !appUsedByOthers(live) {
			if _, err := installAppCheckout(ctx, c.udb, c.user, p, c.base); err != nil {
				if broken, ok := savedBroken(err); ok {
					return "Your changes were installed, but the app does not work: " + broken.Problem + ". It is live as it is. Fix it (app_checks and preview show it), then finish."
				}
				return "Your changes were not installed: " + err.Error() + "\nFix what it names, then finish."
			}
			return ""
		}
	}
	if !customapps.HasAppPreview(c.user, p.AppSlug) {
		return ""
	}
	if _, err := previewAppCheckout(ctx, c.user, p, c.base); err != nil {
		return "The preview could not be brought up to date with your changes: " + err.Error() + "\nFix what it names, then finish."
	}
	return ""
}

// notesFit refuses a write that takes an app's NOTES.md over the cap the
// app's notes are held to. Told at the edit, the assistant writes it shorter
// in the same step; told only when the install at the end of the turn is
// refused, it answered twice that it could not and left it to the person. A
// write that shortens notes already over the cap goes through, so they can
// be trimmed in steps.
func (c *toolCtx) notesFit(rel, before, after string) error {
	if c.project.Source != sourceApp || path.Clean(filepath.ToSlash(rel)) != "NOTES.md" {
		return nil
	}
	limit := AppSpec{}.NotesCap()
	n := len([]rune(strings.TrimSpace(after)))
	if n <= limit || n < len([]rune(strings.TrimSpace(before))) {
		return nil
	}
	return fmt.Errorf("NOTES.md would be %d characters, over the %d the app's notes are held to, so the change was not made. It is a standing summary (purpose, decisions and why, open items), not a log: fold the new point into what is there and shorten older entries, then write it again", n, limit)
}

// appChecksTool runs the app's checks (checks.json) on the working copy, as
// a save would run them, and installs nothing. Without it the assistant had
// no way to run them: one wrote its own runner for checks.json and guessed at
// how a check is judged, another spent its turn looking for a command.
func (c *toolCtx) appChecksTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "app_checks",
			Description: "Check that this app would install, and run its checks (checks.json) on your working copy, each pass or fail: script checks, and page checks driving the working copy in a browser on each check's own records. " +
				"Nothing is installed or saved. Run it after a change and before you say the work is done. Whether it would install is also checked by itself as your turn ends, and a refusal comes back to you; the checks run only when you call this.",
			Parameters: map[string]ToolParam{},
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			o, err := appHost()
			if err != nil {
				return "", err
			}
			if err := o.AppFolderInstallable(ctx, c.user, c.base); err != nil {
				return "It would not install: " + folderError(c.base, err).Error() + "\nFix what it names, and run app_checks again.", nil
			}
			report, err := o.CheckAppFolder(ctx, c.user, c.base)
			if err != nil {
				return "", fmt.Errorf("the checks could not run: %w", folderError(c.base, err))
			}
			return report, nil
		},
	}
}

// previewBlock is the card a preview is shown in: the framework's viewer
// pane, the one the main chat opens a page in, with a card to reopen it.
const previewBlock = "html_artifact"

// previewTool lets the assistant show the person the app as its working
// copy stands, in the pane beside the conversation, as the main chat shows a
// page. It builds the working copy and starts (or refreshes) the person's
// preview; nothing is installed.
func (c *toolCtx) previewTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "preview",
			Description: "Show the person this app as your changes have it, in a pane beside the conversation: a preview only they see, on a copy of their records. " +
				"Use it when they should look at a change you made to the page; nothing is installed.",
			Parameters: map[string]ToolParam{},
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			link, err := previewAppCheckout(ctx, c.user, c.project, c.base)
			if err != nil {
				return "", fmt.Errorf("the preview could not be built: %w", err)
			}
			if c.emitBlock != nil {
				c.emitBlock(agenthost.Block{Type: previewBlock, ID: "anvil-preview-" + c.project.ID,
					Title: c.project.Name + " (preview)", URL: link})
			}
			return "The preview of " + c.project.Name + " is on a card beside your reply; the person opens it there. Only they see it, and nothing was installed. Its address: " + link, nil
		},
	}
}
