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
	if err := os.WriteFile(filepath.Join(base, ".gitignore"), []byte(".live\n"), 0o644); err != nil {
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
