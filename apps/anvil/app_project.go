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
	msg, err := installAppCheckout(c.R.Context(), c.User, p, base)
	if err != nil {
		return nil, jsonapi.Status(409, err.Error())
	}
	return map[string]any{"ok": true, "message": msg}, nil
}

// installAppCheckout installs the checkout as user and, when it went in,
// commits it, so the history is what was installed.
func installAppCheckout(ctx context.Context, user string, p Project, base string) (string, error) {
	o, err := appHost()
	if err != nil {
		return "", err
	}
	if _, err := o.InstallAppFolder(ctx, user, base, "Installed from Anvil"); err != nil {
		// The reason, without the folder's name: here it is the project's
		// id, which says nothing to the person reading it.
		if inner := errors.Unwrap(err); inner != nil {
			err = inner
		}
		return "", err
	}
	if err := commitCheckout(base, user, "Installed"); err != nil {
		Log("[anvil] %s installed, but the commit failed: %v", p.AppSlug, err)
	}
	return "Installed: " + p.Name + " now runs your changes, at /apps/" + p.AppSlug + "/.", nil
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
	base, err := p.baseDir()
	if err != nil {
		return err.Error()
	}
	if !holdsAppFolder(base) {
		return "the project has no app.json at its top: an app's folder holds app.json, with its page and scripts beside it"
	}
	return ""
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
		return nil, jsonapi.Status(409, err.Error())
	}
	if have, ok := LoadAppSpec(c.User, slug); ok {
		return nil, jsonapi.Status(409, fmt.Sprintf("you already have an app %q at /apps/%s/: to change it, open it with New project, One of your apps; for a new app, give this one another name (or slug) in app.json", have.Name, slug))
	}
	// The install leaves the folder's live marker in the checkout; it is the
	// install's business, not the history's.
	if err := excludeFromGit(base, appLiveMarker); err != nil {
		Log("[anvil] %s: the live marker could not be kept out of git: %v", p.ID, err)
	}
	if _, err := o.InstallAppFolder(ctx, c.User, base, "Installed from Anvil as a new app"); err != nil {
		if inner := errors.Unwrap(err); inner != nil {
			err = inner
		}
		return nil, jsonapi.Status(409, err.Error())
	}
	p.Source, p.AppSlug = sourceApp, slug
	T.saveProject(c.UDB, p)
	if err := commitCheckout(base, c.User, "Installed as a new app"); err != nil {
		Log("[anvil] %s installed as a new app, but the commit failed: %v", slug, err)
	}
	return map[string]any{"ok": true, "slug": slug,
		"message": "Installed: " + name + " is one of your apps now, at /apps/" + slug + "/. This project is its files from here on: Preview shows your changes to you alone, and Install puts them live."}, nil
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
		return "", err
	}
	return customapps.StartAppPreview(user, spec, p.PreviewReal)
}

// appFinish is an app project's part of the turn's ending, after the check:
// with Install on every save and an app nobody else uses, the changes are
// installed, and a refusal sends the assistant back with it; otherwise a
// running preview is brought up to date. Nothing happens without changes.
func (c *toolCtx) appFinish(edited bool) string {
	p := c.project
	if p.Source != sourceApp || c.readOnly || !edited {
		return ""
	}
	ctx := c.lastCtx()
	if p.InstallOnSave {
		if live, ok := LoadAppSpec(c.user, p.AppSlug); ok && !appUsedByOthers(live) {
			if _, err := installAppCheckout(ctx, c.user, p, c.base); err != nil {
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
			Description: "Run this app's checks (checks.json) on your working copy, as installing it would, and see each pass or fail. " +
				"Nothing is installed or saved. Run it after a change and before you say the work is done.",
			Parameters: map[string]ToolParam{},
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			o, err := appHost()
			if err != nil {
				return "", err
			}
			report, err := o.CheckAppFolder(ctx, c.user, c.base)
			if err != nil {
				return "", fmt.Errorf("the checks could not run: %w", err)
			}
			return report + "\nPage checks drive the page as served, so they run when the owner installs it.", nil
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
