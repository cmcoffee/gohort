// A repository brought as a git bundle (docs/design/anvil-bundle-upload.md).
//
// Adding a project is an administrator's because a folder path reaches the
// server's files and a repository address makes the server reach out from
// inside its own network. A bundle is neither: a file the person already
// holds, made on their own machine. So an administrator may let users bring
// one (off until they do), and it is unpacked in the sandbox, reaching only
// the uploaded file and the new checkout, under git's file transport alone and
// without the server's own git config, so no filter that config names runs.
// The way back out is Download changes (patch.go).
package anvil

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/sandbox"
	"github.com/cmcoffee/gohort/core/ui"
)

// sourceBundle marks a checkout made from an uploaded bundle. It has no
// repository address: the bundle was its only source, and it is gone.
const sourceBundle = "bundle"

// Unpack states kept on the project record, so a card says what happened to
// an unpack that outlived the page, and the server restarting mid-unpack.
const (
	unpackRunning = "unpacking"
	unpackFailed  = "failed"
)

// bundleSettings are the administrator's. Users may bring a bundle only when
// UsersMay is on; the limits hold for everyone.
type bundleSettings struct {
	UsersMay    bool `json:"users_may"`
	MaxUploadMB int  `json:"max_upload_mb"`
	DiskMB      int  `json:"disk_mb"`
}

const (
	bundleSettingsTable = "anvil_settings"
	bundleSettingsKey   = "bundles"
	defaultMaxUploadMB  = 500
	defaultDiskMB       = 5120
)

func (T *Anvil) bundleSettings() bundleSettings {
	s := bundleSettings{MaxUploadMB: defaultMaxUploadMB, DiskMB: defaultDiskMB}
	if T.DB != nil {
		T.DB.Get(bundleSettingsTable, bundleSettingsKey, &s)
	}
	if s.MaxUploadMB <= 0 {
		s.MaxUploadMB = defaultMaxUploadMB
	}
	if s.DiskMB <= 0 {
		s.DiskMB = defaultDiskMB
	}
	return s
}

// mayBringBundle is whether this caller may upload a bundle.
func (T *Anvil) mayBringBundle(isAdmin bool) bool {
	return isAdmin || T.bundleSettings().UsersMay
}

func (T *Anvil) handleBundleSettings(c *jsonapi.Call) (any, error) {
	if !callerIsAdmin(c.R) {
		return nil, jsonapi.Forbidden("these are an administrator's settings")
	}
	return T.bundleSettings(), nil
}

func (T *Anvil) handleBundleSettingsSave(c *jsonapi.Call, in bundleSettings) (any, error) {
	if !callerIsAdmin(c.R) {
		return nil, jsonapi.Forbidden("these are an administrator's settings")
	}
	if in.MaxUploadMB <= 0 || in.DiskMB <= 0 {
		return nil, jsonapi.BadRequest("both limits have to be above zero")
	}
	T.DB.Set(bundleSettingsTable, bundleSettingsKey, in)
	return map[string]any{"ok": true}, nil
}

// bundleAdminSection is the settings, on Anvil's own entry in the admin
// page's Apps tab.
func bundleAdminSection() ui.Section {
	api := "/anvil/api/bundle/settings"
	return ui.Section{
		Group:    "Apps",
		Title:    "Repositories brought as a bundle",
		Subtitle: "Whether users may bring a repository to Anvil as a git bundle file, and how much they may bring.",
		Detail: "Adding a repository by its address, or a folder on this server, stays an administrator's: an address makes the server reach out from inside its own network, and a path reaches its files. A bundle is a file the person already has, made with git bundle create on their own machine, so neither applies.\n\n" +
			"A bundle is unpacked in the sandbox, reaching only the uploaded file and the new checkout, and without this server's git config. The person takes their work back with Download as a bundle or Download changes, or pushes it elsewhere with Publish when publishing is allowed.\n\n" +
			"The limits hold for administrators too. The disk limit counts every checkout a person has in Anvil.",
		Body: ui.FormPanel{Source: api, PostURL: api, Fields: []ui.FormField{
			{Field: "users_may", Label: "Users may bring a repository as a bundle", Type: "toggle",
				Help: "Off: only administrators can upload a bundle."},
			{Field: "max_upload_mb", Label: "Largest bundle (MB)", Type: "number"},
			{Field: "disk_mb", Label: "Anvil disk per person (MB)", Type: "number",
				Help: "Every checkout the person has in Anvil, counted while a bundle unpacks: a small bundle can unpack large."},
		}},
	}
}

// --- the upload --------------------------------------------------------------

// uploadsDir is where a person's bundle waits to be unpacked: beside their
// checkouts, outside every one of them, so no run tool can reach it.
func uploadsDir(owner string) (string, error) {
	base := WorkspacesDir()
	if base == "" {
		return "", fmt.Errorf("workspaces dir not configured, and a bundle needs one")
	}
	if owner == "" || strings.ContainsAny(owner, `/\`) || strings.Contains(owner, "..") {
		return "", fmt.Errorf("invalid owner %q", owner)
	}
	return filepath.Join(base, owner, anvilSubdir, ".uploads"), nil
}

// anvilDiskUsed is what a person's Anvil folder holds now: every checkout and
// any upload waiting. Symlinks count as themselves, never what they name.
func anvilDiskUsed(owner string) int64 {
	base := WorkspacesDir()
	if base == "" || owner == "" {
		return 0
	}
	return dirSize(filepath.Join(base, owner, anvilSubdir))
}

func dirSize(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// bundleJob is one person's bundle on its way to a project. At most one at a
// time per person: the upload has one place to wait.
type bundleJob struct {
	FileName  string
	ProjectID string
	State     string // the running line, or "ready" / "failed"
	Err       string
	running   bool
}

var (
	bundleJobsMu sync.Mutex
	bundleJobs   = map[string]*bundleJob{}
)

func jobFor(user string) (bundleJob, bool) {
	bundleJobsMu.Lock()
	defer bundleJobsMu.Unlock()
	j, ok := bundleJobs[user]
	if !ok {
		return bundleJob{}, false
	}
	return *j, true
}

func setJob(user string, fn func(j *bundleJob)) {
	bundleJobsMu.Lock()
	defer bundleJobsMu.Unlock()
	j, ok := bundleJobs[user]
	if !ok {
		j = &bundleJob{}
		bundleJobs[user] = j
	}
	fn(j)
}

// handleBundleUpload takes one bundle file and keeps it for finalize. It is
// streamed to disk through MultipartReader, never buffered, and refused past
// the size limit or the person's disk.
func (T *Anvil) handleBundleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST a bundle file")
		return
	}
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if !T.mayBringBundle(callerIsAdmin(r)) {
		writeErr(w, http.StatusForbidden, addProjectAdminOnly)
		return
	}
	if j, ok := jobFor(user); ok && j.running {
		writeErr(w, http.StatusConflict, "a bundle of yours is unpacking already; wait for it to finish")
		return
	}
	set := T.bundleSettings()
	limit := int64(set.MaxUploadMB) << 20
	if room := int64(set.DiskMB)<<20 - anvilDiskUsed(user); room < limit {
		limit = room
	}
	if limit <= 0 {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("your Anvil folder is at its %d MB limit; remove a project to make room", set.DiskMB))
		return
	}
	if r.ContentLength > limit+(1<<20) {
		writeErr(w, http.StatusRequestEntityTooLarge, bundleTooLarge(limit))
		return
	}
	dir, err := uploadsDir(user)
	if err == nil {
		err = os.MkdirAll(dir, 0o700)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "send the bundle as a multipart file")
		return
	}
	for {
		part, err := mr.NextPart()
		if err != nil {
			writeErr(w, http.StatusBadRequest, "no bundle file in the request")
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		name := filepath.Base(part.FileName())
		staged := filepath.Join(dir, "incoming.bundle")
		f, err := os.OpenFile(staged, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		n, err := io.Copy(f, io.LimitReader(part, limit+1))
		f.Close()
		if err == nil && n > limit {
			err = fmt.Errorf("%s", bundleTooLarge(limit))
		}
		if err != nil {
			os.Remove(staged)
			code := http.StatusBadRequest
			var mbe *http.MaxBytesError
			if n > limit || errors.As(err, &mbe) {
				code, err = http.StatusRequestEntityTooLarge, fmt.Errorf("%s", bundleTooLarge(limit))
			}
			writeErr(w, code, err.Error())
			return
		}
		if _, err := readBundleHeader(staged); err != nil {
			os.Remove(staged)
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		setJob(user, func(j *bundleJob) { *j = bundleJob{FileName: name, State: "uploaded"} })
		writeJSONOK(w, map[string]any{"ok": true, "name": name, "size": n})
		return
	}
}

func bundleTooLarge(limit int64) string {
	return fmt.Sprintf("the bundle is larger than the %d MB you may bring (the size limit, or what is left of your Anvil disk)", limit>>20)
}

func writeJSONOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// bundleHeader is what a bundle says before its pack: the refs it carries,
// and which branch its HEAD is.
type bundleHeader struct {
	Branches []string
	Head     string // the branch HEAD names, or the only branch, or ""
}

// readBundleHeader reads a bundle's header and nothing past it. A bundle with
// prerequisites needs commits this server does not have, so it cannot start a
// project.
func readBundleHeader(path string) (bundleHeader, error) {
	var h bundleHeader
	f, err := os.Open(path)
	if err != nil {
		return h, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(io.LimitReader(f, 4<<20), 64<<10)
	sig, err := br.ReadString('\n')
	if err != nil || (sig != "# v2 git bundle\n" && sig != "# v3 git bundle\n") {
		return h, fmt.Errorf("that is not a git bundle. Make one with: git bundle create project.bundle --all")
	}
	refs := map[string]string{}
	var headSHA string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return h, fmt.Errorf("the bundle's header is cut short; make the bundle again")
		}
		line = strings.TrimRight(line, "\n")
		if line == "" {
			break
		}
		switch {
		case strings.HasPrefix(line, "@"):
			continue // v3 capability
		case strings.HasPrefix(line, "-"):
			return h, fmt.Errorf("the bundle needs commits it does not carry (it was made from a range). Make a whole one with: git bundle create project.bundle --all")
		}
		sha, ref, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch {
		case ref == "HEAD":
			headSHA = sha
		case strings.HasPrefix(ref, "refs/heads/"):
			b := strings.TrimPrefix(ref, "refs/heads/")
			if checkBranch(b) == nil {
				h.Branches = append(h.Branches, b)
				refs[b] = sha
			}
		}
	}
	if len(h.Branches) == 0 {
		return h, fmt.Errorf("the bundle carries no branch. Make it with --all, or name a branch: git bundle create project.bundle main")
	}
	for _, pref := range append([]string{}, h.Branches...) {
		if headSHA != "" && refs[pref] == headSHA {
			if h.Head == "" || pref == "main" || pref == "master" {
				h.Head = pref
			}
		}
	}
	if h.Head == "" {
		h.Head = h.Branches[0]
	}
	return h, nil
}

// handleBundleFinalize turns the uploaded bundle into a project and starts
// unpacking it.
func (T *Anvil) handleBundleFinalize(c *jsonapi.Call, _ struct{}) (any, error) {
	user, isAdmin := c.User, callerIsAdmin(c.R)
	if !T.mayBringBundle(isAdmin) {
		return nil, jsonapi.Forbidden(addProjectAdminOnly)
	}
	j, ok := jobFor(user)
	if !ok || j.State != "uploaded" {
		return nil, jsonapi.BadRequest("no bundle is waiting; upload it again")
	}
	dir, err := uploadsDir(user)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	staged := filepath.Join(dir, "incoming.bundle")
	h, err := readBundleHeader(staged)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	name := strings.TrimSuffix(j.FileName, filepath.Ext(j.FileName))
	if strings.TrimSpace(name) == "" {
		name = "Bundle"
	}
	p := Project{
		ID: UUIDv4(), Name: name, Kind: KindClone, Owner: user, Branch: h.Head,
		Source: sourceBundle, BundleName: j.FileName, Unpack: unpackRunning,
		CreatedAt: time.Now().UTC(),
	}
	base, err := p.baseDir()
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	T.saveProject(c.UDB, p)
	setJob(user, func(j *bundleJob) { j.ProjectID, j.State, j.running = p.ID, "Unpacking...", true })
	startUnpack(func() { T.unpackBundle(c.UDB, p, base, staged) })
	return map[string]any{"ok": true, "id": p.ID}, nil
}

// handleBundleStatus is what the upload panel polls: the running line, then
// "ready" or "failed" with why.
func (T *Anvil) handleBundleStatus(c *jsonapi.Call) (any, error) {
	j, ok := jobFor(c.User)
	if !ok {
		return map[string]any{"state": "failed", "error": "no bundle is unpacking"}, nil
	}
	return map[string]any{"state": j.State, "error": j.Err, "id": j.ProjectID}, nil
}

// startUnpack runs an unpack in the background; a test runs it inline.
var startUnpack = func(fn func()) { go fn() }

// bundleTick is how often an unpack is measured, for its line and its disk.
var bundleTick = 2 * time.Second

// unpackBundle clones the bundle into the checkout in the sandbox, measuring
// the checkout on each tick and stopping it past the person's disk. It runs on
// a context of its own: it outlives the request that started it.
func (T *Anvil) unpackBundle(udb Database, p Project, base, staged string) {
	defer os.Remove(staged)
	user := p.Owner
	// A project removed while it unpacked stays removed: saving the outcome
	// would bring the record back.
	removed := func() bool {
		var cur Project
		return !udb.Get(projectTable, p.ID, &cur)
	}
	fail := func(why string) {
		// Where this server keeps things is not the person's business.
		why = strings.ReplaceAll(strings.ReplaceAll(why, base, "the checkout"), staged, "the bundle")
		os.RemoveAll(base)
		p.Unpack, p.UnpackError = unpackFailed, why
		if !removed() {
			T.saveProject(udb, p)
		}
		setJob(user, func(j *bundleJob) { j.State, j.Err, j.running = "failed", why, false })
		Log("[anvil] unpacking %s for %s failed: %s", p.BundleName, user, why)
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		fail(err.Error())
		return
	}
	set := T.bundleSettings()
	room := int64(set.DiskMB)<<20 - (anvilDiskUsed(user) - dirSize(staged))
	ctx, cancel := context.WithTimeout(context.Background(), cloneTimeout)
	defer cancel()

	started := time.Now()
	var over bool
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTicker(bundleTick)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				size := dirSize(base)
				if size > room {
					over = true
					cancel()
					return
				}
				line := fmt.Sprintf("Unpacking: %s, %ds", HumanSize(size), int(time.Since(started).Seconds()))
				setJob(user, func(j *bundleJob) { j.State = line })
			}
		}
	}()
	out, err := cloneBundle(ctx, base, staged, p.Branch)
	close(done)
	<-stopped // over is the watcher's until it has stopped
	// Measured once more as it ends: an unpack quicker than the first tick
	// was never measured at all, and a bundle past the limit went in.
	if err == nil && dirSize(base) > room {
		over = true
	}
	switch {
	case over:
		fail(fmt.Sprintf("it unpacked past your Anvil disk limit of %d MB", set.DiskMB))
		return
	case err != nil:
		fail(firstLine(strings.TrimSpace(out + " " + err.Error())))
		return
	}
	// The remote names the upload, which is gone.
	runGit(base, "remote", "remove", "origin")
	if removed() {
		os.RemoveAll(base)
		setJob(user, func(j *bundleJob) {
			j.State, j.Err, j.running = "failed", "the project was removed while it unpacked", false
		})
		return
	}
	p.Unpack, p.UnpackError = "", ""
	T.saveProject(udb, p)
	setJob(user, func(j *bundleJob) { j.State, j.running = "ready", false })
	Log("[anvil] unpacked %s for %s in %s", p.BundleName, user, time.Since(started).Round(time.Second))
}

// cloneBundle runs the clone in the sandbox, in the empty checkout, reaching
// the bundle and nothing else outside it. Git's file transport alone (a
// bundle is read over it; every other transport is refused), and no host or
// global git config, so a filter that config names, git-lfs's among them,
// does not run on the checkout.
func cloneBundle(ctx context.Context, base, bundle, branch string) (string, error) {
	args := append(append([]string(nil), gitSafety...),
		"-c", "protocol.allow=never", "-c", "protocol.file.allow=always",
		"clone", "--single-branch", "--branch", branch, "--", bundle, ".")
	var cmd strings.Builder
	cmd.WriteString("git")
	for _, a := range args {
		cmd.WriteString(" '" + strings.ReplaceAll(a, "'", `'\''`) + "'")
	}
	res := sandbox.RunSandboxedShellIn(ctx, sandbox.ShellRun{Command: cmd.String(), WorkspaceDir: base, Env: bundleCloneEnv(), Reach: []string{bundle}})
	return res.Output, res.Err
}

// bundleCloneEnv is the unpack's environment: no global or system git config.
// A sandbox that mounts only what it is given already hides the server's
// home; one that reads the whole disk (Seatbelt on macOS) does not, and the
// server's ~/.gitconfig is where git-lfs install puts its smudge filter.
func bundleCloneEnv() map[string]string {
	env := map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1"}
	for _, kv := range gitQuietEnv {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return env
}

// bundleCardState is a bundle project's line on its card while it is not a
// checkout yet.
func bundleCardState(p Project) (string, bool) {
	switch p.Unpack {
	case unpackRunning:
		if j, ok := jobFor(p.Owner); ok && j.running && j.ProjectID == p.ID {
			return j.State, true
		}
		return "The unpack was interrupted: remove this project and upload the bundle again", true
	case unpackFailed:
		return "Unpacking failed: " + p.UnpackError, true
	}
	return "", false
}

// bundleUploadPanel is the New project dialog's upload.
func (T *Anvil) bundleUploadPanel() ui.UploadPanel {
	single := false
	set := T.bundleSettings()
	return ui.UploadPanel{
		URL:              "api/bundle/upload",
		Accept:           ".bundle",
		Multiple:         &single,
		MaxBytes:         int64(set.MaxUploadMB) << 20,
		FinalizeURL:      "api/bundle/finalize",
		StatusURL:        "api/bundle/status",
		StatusDone:       []string{"ready"},
		StatusFailed:     []string{"failed"},
		StatusErrorField: "error",
		ButtonLabel:      "Upload",
		ReloadOnDone:     true,
		Note: fmt.Sprintf("Make the bundle on your own machine, in the repository: git bundle create project.bundle --all. "+
			"Up to %d MB. It is unpacked into a checkout of your own, and Download changes gives you a patch of your edits to apply at home with git apply.", set.MaxUploadMB),
	}
}
