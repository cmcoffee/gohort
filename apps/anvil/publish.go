// A repository that starts here and leaves from here.
//
// Start a new repository makes an empty one, for anyone: it brings nothing
// into gohort, so none of the reasons adding a project is an administrator's
// applies. Commit records the work in it, as the person pressing the button
// or by the assistant as them (commit.go). Publish pushes it to a repository elsewhere,
// and Download as a bundle hands over the whole history with no network.
//
// The push runs on the server, because the sandbox has no network, and so it
// never runs in the checkout: the checkout's .git is the run tool's to write,
// and git started there on the server would run whatever hook or program that
// config names. So the sandbox writes the history out as a bundle, a plain
// file; the server fetches it into a bare repository it made itself, which
// nothing in the project can reach, and pushes from there, held to the same
// public addresses as a clone (remotePin).
package anvil

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/sandbox"
	"github.com/cmcoffee/gohort/core/ui"
)

// sourceNew marks a repository started empty in Anvil.
const sourceNew = "new"

const (
	newRepoBranch    = "main"
	maxProjectName   = 100
	maxCommitMessage = 2000
	pushTimeout      = 10 * time.Minute
	publishKey       = "publish"
)

// publishSettings are the administrator's. Stored as the exception, so a
// server that never saved them lets users publish.
type publishSettings struct {
	NoUserPublish bool
}

// publishSettingsForm is the settings as the admin form reads them.
type publishSettingsForm struct {
	UsersMayPublish bool `json:"users_may_publish"`
}

func (T *Anvil) publishSettings() publishSettings {
	var s publishSettings
	if T.DB != nil {
		T.DB.Get(bundleSettingsTable, publishKey, &s)
	}
	return s
}

// mayPublish is whether this caller may push a project of theirs elsewhere.
func (T *Anvil) mayPublish(isAdmin bool) bool {
	return isAdmin || !T.publishSettings().NoUserPublish
}

func (T *Anvil) handlePublishSettings(c *jsonapi.Call) (any, error) {
	if !callerIsAdmin(c.R) {
		return nil, jsonapi.Forbidden("these are an administrator's settings")
	}
	return publishSettingsForm{UsersMayPublish: !T.publishSettings().NoUserPublish}, nil
}

func (T *Anvil) handlePublishSettingsSave(c *jsonapi.Call, in publishSettingsForm) (any, error) {
	if !callerIsAdmin(c.R) {
		return nil, jsonapi.Forbidden("these are an administrator's settings")
	}
	T.DB.Set(bundleSettingsTable, publishKey, publishSettings{NoUserPublish: !in.UsersMayPublish})
	return map[string]any{"ok": true}, nil
}

// publishAdminSection is the setting, on Anvil's entry in the admin page's
// Apps tab.
func publishAdminSection() ui.Section {
	api := "/anvil/api/publish/settings"
	return ui.Section{
		Group:    "Apps",
		Title:    "Publishing repositories",
		Subtitle: "Whether users may push a repository they started in Anvil, or brought as a bundle, to a repository elsewhere.",
		Detail: "Anyone may start a new repository in Anvil: it brings nothing in, and counts against their Anvil disk. Publishing sends it out: the person gives an https address and a token, and the server pushes their committed history there.\n\n" +
			"For anyone but an administrator the address must be https and on the public internet: the host is looked up once, refused when it is on a private or internal network, and git is held to the address checked, as for a clone. Only the person presses Push; the assistant commits but cannot push.\n\n" +
			"Off: only administrators can publish. Download as a bundle stays, the way out with no network.",
		Body: ui.FormPanel{Source: api, PostURL: api, Fields: []ui.FormField{
			{Field: "users_may_publish", Label: "Users may publish their repositories", Type: "toggle"},
		}},
	}
}

// --- who may do what ---------------------------------------------------------

// commitRefusal is why the caller may not commit in p, or "" when they may.
// Only a checkout Anvil made: a directory on this host is somebody's real
// tree, and an app's commits are its installs.
func commitRefusal(p Project, user string, isAdmin bool) string {
	switch {
	case p.Kind != KindClone:
		return "Anvil commits only in a checkout it made; a directory on this host is committed by whoever works in it"
	case p.Source == sourceApp:
		return "an app's commits are its installs: Install commits your changes as it puts them live"
	case p.Unpack != "":
		return "this checkout is not ready"
	case !writeAllowed(p, user, isAdmin):
		return "you may read this project, not change it"
	}
	return ""
}

// publishRefusal is why the caller may not publish p, or "" when they may.
func (T *Anvil) publishRefusal(p Project, user string, isAdmin bool) string {
	switch {
	case p.Kind != KindClone || (p.Source != sourceNew && p.Source != sourceBundle):
		return "only a repository started in Anvil, or brought as a bundle, can be published from here"
	case p.Unpack != "":
		return "this checkout is not ready"
	case !strings.EqualFold(p.Owner, user):
		return "this repository is not yours"
	case !T.mayPublish(isAdmin):
		return "publishing is turned off for users on this server; Download as a bundle gives you the history to push yourself"
	}
	return ""
}

// --- start a new repository --------------------------------------------------

// newRepoForm is the home page's Start a new repository dialog.
func newRepoForm() ui.FormPanel {
	return ui.FormPanel{
		PostURL:        "api/projects/new",
		SubmitLabel:    "Start",
		BusyLabel:      "Starting...",
		RedirectURL:    "?project={id}",
		RedirectTarget: "_self",
		Fields: []ui.FormField{
			{Field: "name", Label: "Name", Type: "text", Required: true, Placeholder: "What you'll call it in the picker",
				Help: "An empty git repository of your own, on branch " + newRepoBranch + ". Commit records your work in it; Publish pushes it to a repository elsewhere when you want."},
		},
	}
}

func (T *Anvil) handleNewProject(c *jsonapi.Call, in struct {
	Name string `json:"name"`
}) (any, error) {
	user := c.User
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, jsonapi.BadRequest("a project needs a name")
	}
	if len([]rune(name)) > maxProjectName {
		return nil, jsonapi.BadRequest(fmt.Sprintf("a project's name is up to %d characters", maxProjectName))
	}
	set := T.bundleSettings()
	if anvilDiskUsed(user) >= int64(set.DiskMB)<<20 {
		return nil, jsonapi.Status(http.StatusConflict, fmt.Sprintf("your Anvil folder is at its %d MB limit; remove a project to make room", set.DiskMB))
	}
	p := Project{ID: UUIDv4(), Name: name, Kind: KindClone, Owner: user, Source: sourceNew,
		Branch: newRepoBranch, CreatedAt: time.Now().UTC()}
	base, err := p.baseDir()
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if err := startRepo(base, user); err != nil {
		os.RemoveAll(base)
		return nil, jsonapi.Status(http.StatusInternalServerError, "the repository could not be made: "+err.Error())
	}
	T.saveProject(c.UDB, p)
	return map[string]any{"ok": true, "id": p.ID}, nil
}

// startRepo makes base an empty repository with one empty commit, so Changes,
// Undo and the branch line work from the first edit.
func startRepo(base, user string) error {
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	if entries, err := os.ReadDir(base); err != nil {
		return err
	} else if len(entries) > 0 {
		return fmt.Errorf("the checkout folder is not empty")
	}
	if _, err := runGit(base, "init", "-q"); err != nil {
		return err
	}
	if _, err := runGit(base, "symbolic-ref", "HEAD", "refs/heads/"+newRepoBranch); err != nil {
		return err
	}
	_, err := runGit(base, append(gitIdentity(user), "commit", "-q", "--allow-empty", "-m", "Started in Anvil")...)
	return err
}

// gitIdentity commits as the person.
func gitIdentity(user string) []string {
	return []string{"-c", "user.name=" + user, "-c", "user.email=" + user + "@users.gohort.invalid", "-c", "commit.gpgsign=false"}
}

// --- commit ------------------------------------------------------------------

// pendingLine says what differs from the last commit, or "" when nothing.
func pendingLine(base string) (string, int) {
	changes, err := workingChanges(base)
	if err != nil || len(changes) == 0 {
		return "", 0
	}
	var names []string
	for i, ch := range changes {
		if i == 5 {
			names = append(names, fmt.Sprintf("and %d more", len(changes)-5))
			break
		}
		names = append(names, ch.Path)
	}
	files := "files"
	if len(changes) == 1 {
		files = "file"
	}
	return fmt.Sprintf("%d %s: %s", len(changes), files, strings.Join(names, ", ")), len(changes)
}

// commitForm is the workspace's Commit dialog for p.
func commitForm(p Project) ui.FormPanel {
	api := "api/project/" + p.ID + "/commit"
	return ui.FormPanel{
		Source: api, PostURL: api,
		SubmitLabel: "Commit", BusyLabel: "Committing...", OnSuccess: "anvil_git_done",
		Fields: []ui.FormField{
			{Field: "pending", Label: "To commit", Type: "readonly"},
			{Field: "message", Label: "Message", Type: "textarea", Rows: 4, Required: true,
				Placeholder: "What these changes do, in a line or two",
				Help:        "Everything that differs from the last commit is committed, new files included, except what the project's .gitignore leaves out."},
		},
	}
}

func (T *Anvil) handleCommitState(c *jsonapi.Call) (any, error) {
	p, err := T.projectOf(c)
	if err != nil {
		return nil, err
	}
	base, err := p.baseDir()
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	line, _ := pendingLine(base)
	if line == "" {
		line = "Nothing differs from the last commit."
	}
	return map[string]any{"pending": line, "message": ""}, nil
}

func (T *Anvil) handleCommit(c *jsonapi.Call, in struct {
	Message string `json:"message"`
}) (any, error) {
	p, err := T.projectOf(c)
	if err != nil {
		return nil, err
	}
	if why := commitRefusal(p, c.User, callerIsAdmin(c.R)); why != "" {
		return nil, jsonapi.Forbidden(why)
	}
	msg, err := commitMessage(in.Message)
	if err != nil {
		return nil, err
	}
	base, err := p.baseDir()
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	_, n := pendingLine(base)
	if n == 0 {
		return nil, jsonapi.Status(http.StatusConflict, "nothing differs from the last commit, so there is nothing to commit")
	}
	if err := commitCheckout(base, c.User, msg); err != nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, err.Error())
	}
	return map[string]any{"ok": true, "message": committedLine(n)}, nil
}

func commitMessage(raw string) (string, error) {
	msg := strings.TrimSpace(raw)
	if msg == "" {
		return "", jsonapi.BadRequest("a commit needs a message")
	}
	if len([]rune(msg)) > maxCommitMessage {
		return "", jsonapi.BadRequest(fmt.Sprintf("a commit message is up to %d characters", maxCommitMessage))
	}
	return msg, nil
}

func committedLine(n int) string {
	if n == 1 {
		return "Committed 1 file."
	}
	return fmt.Sprintf("Committed %d files.", n)
}

// --- publish -----------------------------------------------------------------

// publishForm is the workspace's Publish dialog for p.
func publishForm(p Project) ui.FormPanel {
	api := "api/project/" + p.ID + "/publish"
	return ui.FormPanel{
		Source: api, PostURL: api,
		SubmitLabel: "Push", BusyLabel: "Pushing...", SlowText: "Still pushing.", OnSuccess: "anvil_git_done",
		Fields: []ui.FormField{
			{Field: "last_push", Label: "Last pushed", Type: "readonly"},
			{Field: "push_url", Label: "Repository URL", Type: "text", Required: true, Placeholder: "https://host/owner/name.git",
				Help: "An https address: an empty repository made for this, or one whose history this project's starts from. A push never overwrites commits the repository has and this project does not."},
			{Field: "push_branch", Label: "Branch", Type: "text", Placeholder: "The branch to push to",
				Help: "Leave blank for this project's own branch."},
			{Field: "push_token", Label: "Access token", Type: "password",
				Help: "One that may push to the repository. Kept for the next push and never shown again: leave blank to keep the saved one. A saved token is only sent to the host it was saved for."},
			{Field: "pending", Label: "Not committed", Type: "readonly"},
			{Field: "message", Label: "Commit them first", Type: "textarea", Rows: 3, ShowWhen: "pending",
				Placeholder: "A commit message for them; leave blank to push only what is committed"},
		},
	}
}

func (T *Anvil) handlePublishState(c *jsonapi.Call) (any, error) {
	p, err := T.projectOf(c)
	if err != nil {
		return nil, err
	}
	if why := T.publishRefusal(p, c.User, callerIsAdmin(c.R)); why != "" {
		return nil, jsonapi.Forbidden(why)
	}
	base, err := p.baseDir()
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	pending, n := pendingLine(base)
	if n > 0 {
		pending += ". Not sent unless you commit them."
	}
	last := ""
	if !p.PushedAt.IsZero() {
		last = p.PushBranch + " to " + p.PushURL + ", " + p.PushedAt.Local().Format("Jan 2 15:04")
	}
	branch := p.PushBranch
	if branch == "" {
		branch = currentBranch(p, base)
	}
	return map[string]any{
		"push_url": p.PushURL, "push_branch": branch, "push_token": "",
		"has_push_token": p.PushToken != "", "pending": pending, "message": "", "last_push": last,
	}, nil
}

// currentBranch is the branch the checkout is on, else the one it was made
// with.
func currentBranch(p Project, base string) string {
	if b := inspectRepo(base).Branch; b != "" {
		return b
	}
	if b := strings.TrimSpace(p.Branch); b != "" {
		return b
	}
	return newRepoBranch
}

type publishIn struct {
	PushURL    string `json:"push_url"`
	PushBranch string `json:"push_branch"`
	PushToken  string `json:"push_token"`
	Message    string `json:"message"`
}

func (T *Anvil) handlePublish(c *jsonapi.Call, in publishIn) (any, error) {
	p, err := T.projectOf(c)
	if err != nil {
		return nil, err
	}
	user, isAdmin := c.User, callerIsAdmin(c.R)
	if why := T.publishRefusal(p, user, isAdmin); why != "" {
		return nil, jsonapi.Forbidden(why)
	}
	base, err := p.baseDir()
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	run := publishRun{base: base, url: strings.TrimSpace(in.PushURL), branch: strings.TrimSpace(in.PushBranch), admin: isAdmin}
	if err := checkRepoURL(run.url, isAdmin); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if run.branch == "" {
		run.branch = currentBranch(p, base)
	}
	if err := checkBranch(run.branch); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	run.token = strings.TrimSpace(in.PushToken)
	if run.token == "" && sameHost(run.url, p.PushURL) {
		run.token = p.PushToken
	}
	// The push outlives a closed page: stopped halfway, a remote is left
	// with whatever had arrived.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.R.Context()), pushTimeout)
	defer cancel()
	if run.pin, err = remotePin(ctx, run.url, isAdmin); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	committed := ""
	if strings.TrimSpace(in.Message) != "" {
		if _, n := pendingLine(base); n > 0 {
			msg, err := commitMessage(in.Message)
			if err != nil {
				return nil, err
			}
			if err := commitCheckout(base, user, msg); err != nil {
				return nil, jsonapi.Status(http.StatusInternalServerError, err.Error())
			}
			committed = committedLine(n) + " "
		}
	}
	if err := run.push(ctx); err != nil {
		return nil, jsonapi.Status(http.StatusBadGateway, committed+err.Error())
	}
	p.PushURL, p.PushBranch, p.PushToken, p.PushedAt = run.url, run.branch, run.token, time.Now().UTC()
	T.saveProject(c.UDB, p)
	msg := committed + "Pushed " + run.branch + " to " + run.url + "."
	if _, n := pendingLine(base); n > 0 {
		msg += fmt.Sprintf(" %d uncommitted file(s) were not sent.", n)
	}
	return map[string]any{"ok": true, "message": msg}, nil
}

// sameHost reports whether two addresses name the same host, so a saved
// token is never sent somewhere it was not given for.
func sameHost(a, b string) bool {
	ua, err1 := url.Parse(strings.TrimSpace(a))
	ub, err2 := url.Parse(strings.TrimSpace(b))
	if err1 != nil || err2 != nil || ua.Host == "" {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}

// publishRun is one push of a checkout's HEAD to a branch elsewhere.
type publishRun struct {
	base, url, token, branch string
	pin                      []string
	admin                    bool
}

func (r publishRun) push(ctx context.Context) error {
	tmp, err := os.MkdirTemp("", "anvil-push-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "out")
	if err := os.Mkdir(out, 0o700); err != nil {
		return err
	}
	bundle, err := bundleOf(ctx, r.base, out, "HEAD")
	if err != nil {
		return err
	}
	// A bare repository the server makes, outside everything the project
	// can reach, holding only what the bundle carried.
	repo := filepath.Join(tmp, "push.git")
	if _, err := serverGit(ctx, tmp, []string{"init", "-q", "--bare", repo}); err != nil {
		return fmt.Errorf("could not prepare the push: %w", err)
	}
	ref := "refs/heads/" + r.branch
	if b, err := serverGit(ctx, repo, []string{"-c", "protocol.allow=never", "-c", "protocol.file.allow=always",
		"-c", "fetch.fsckObjects=true", "fetch", "-q", "--", bundle, "HEAD:" + ref}); err != nil {
		return fmt.Errorf("the history could not be read for the push: %s", gitFailure(string(b), err))
	}
	authURL, err := urlWithToken(r.url, r.token)
	if err != nil {
		return err
	}
	text, err := remoteGit{dir: repo, admin: r.admin, pin: r.pin, timeout: pushTimeout}.run(ctx, "push", "--", authURL, ref+":"+ref)
	if err != nil {
		return fmt.Errorf("%s", scrubToken(pushFailure(text, err), r.token))
	}
	return nil
}

// pushFailure says why a push was refused, in place of git's first line,
// which names the remote rather than the reason.
func pushFailure(text string, err error) string {
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "fetch first") || strings.Contains(low, "non-fast-forward"):
		return "the repository has commits this project does not, so the push was refused rather than overwrite them. Push to an empty repository, or to a new branch"
	case strings.Contains(low, "authentication failed") || strings.Contains(low, "could not read username") ||
		strings.Contains(low, "403") || strings.Contains(low, "permission"):
		return "the repository refused the push: check the token may push to it"
	case strings.Contains(low, "not found") || strings.Contains(low, "does not appear to be a git repository"):
		return "no repository answered at that address; make it first (an empty one is fine), and check the token may see it"
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "error:") || strings.HasPrefix(line, "fatal:") || strings.HasPrefix(line, "remote:") {
			return line
		}
	}
	return err.Error()
}

// --- download as a bundle ----------------------------------------------------

// bundleOf has the sandbox write the checkout's history as a bundle into
// out, a folder of the server's the sandbox may write, and returns its path.
// revs are what the bundle carries.
func bundleOf(ctx context.Context, base, out string, revs ...string) (string, error) {
	if head, err := runGit(base, "rev-parse", "--verify", "-q", "HEAD"); err != nil || strings.TrimSpace(head) == "" {
		return "", fmt.Errorf("nothing has been committed in this project yet")
	}
	git := "git"
	for _, a := range gitSafety {
		git += " '" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	script := git + " bundle create -q \"$ANVIL_OUT/repo.bundle\""
	for _, rev := range revs {
		script += " '" + strings.ReplaceAll(rev, "'", `'\''`) + "'"
	}
	env := map[string]string{"ANVIL_OUT": out}
	for _, kv := range gitQuietEnv {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	ctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()
	res := sandbox.RunSandboxedShellIn(ctx, sandbox.ShellRun{Command: script, WorkspaceDir: base, Env: env, Writable: []string{out}})
	if res.Err != nil {
		return "", fmt.Errorf("the history could not be bundled: %s", gitFailure(res.Output, res.Err))
	}
	path := filepath.Join(out, "repo.bundle")
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("the history could not be bundled")
	}
	return path, nil
}

// handleDownloadBundle answers the checkout's whole history as a git bundle,
// to clone at home and push from there. Anyone who may open the project may
// take it: they can already read its files and its log. Only a checkout
// Anvil made, since a directory on this host may be one folder of a larger
// repository, whose history is not theirs to take.
func (T *Anvil) handleDownloadBundle(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	p, found := T.findProject(udb, user, callerIsAdmin(r), r.PathValue("id"))
	if !found {
		writeErr(w, http.StatusNotFound, "no such project")
		return
	}
	if p.Kind != KindClone {
		writeErr(w, http.StatusConflict, "only a checkout Anvil made downloads whole: a directory on this host may be one folder of a larger repository")
		return
	}
	if p.Unpack != "" {
		writeErr(w, http.StatusConflict, "this checkout is not ready")
		return
	}
	base, err := p.baseDir()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if isShallowRepo(base) {
		writeErr(w, http.StatusConflict, "this checkout was made without its history; fetch the full history in its settings first")
		return
	}
	tmp, err := os.MkdirTemp("", "anvil-bundle-")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(tmp)
	path, err := bundleOf(r.Context(), base, tmp, "HEAD", "--branches", "--tags")
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := strings.Trim(patchNameUnsafe.ReplaceAllString(p.Name, "-"), "-.")
	if name == "" {
		name = "repository"
	}
	_, n := pendingLine(base)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.bundle"`, name, time.Now().Format("20060102")))
	w.Header().Set("X-Anvil-Uncommitted", strconv.Itoa(n))
	io.Copy(w, f)
}

// gitFormsJS hands the workspace the Commit and Publish forms for p, which
// its Git menu opens in a dialog. json.Marshal escapes "<", so nothing in a
// project's name can close the script.
func gitFormsJS(p Project) string {
	b, err := json.Marshal(map[string]any{"commit": commitForm(p), "publish": publishForm(p)})
	if err != nil {
		return ""
	}
	return "window.anvilGitForms = " + string(b) + ";"
}
