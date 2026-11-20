package orchestrate

// One history per app (docs/design/app-history.md), stage 1: every app has a
// git repository of its folder, at <workspaces>/<owner>/.apps/<slug>, and
// every save writes the app's files into it. The live app stays what is
// served; the repository is the record of the work, and a finished, tested
// piece of it is a commit (app_history_commit.go).
//
// The files are written as the save ends, and only the ones whose contents
// changed, so a save costs milliseconds; the one git command a save runs is
// making the repository, the first time, with the app as saved as its first
// commit. Without git nothing is written: apps save, run and keep revisions
// as they always have, and only this history is missing.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/applocal"
)

// appHistoryDirName is the folder under an owner's workspaces root that holds
// one repository per app.
const appHistoryDirName = ".apps"

func init() {
	RegisterAppSpecSavedHook(func(s AppSpec) {
		if err := syncAppRepo(s); err != nil {
			Log("[app-history] %s/%s: the app's files could not be written to its history: %v", s.Owner, s.Slug, err)
		}
	})
	RegisterAppSpecDeletedHook(func(owner, slug string) {
		if dir, ok := appRepoDir(owner, slug); ok {
			os.RemoveAll(dir)
		}
	})
	applocal.SetHistoryNote(appHistoryNote)
}

// appHistoryNote records in owner's app slug's history an event that changes
// none of its files (an administrator's stop), as a commit of by's with no
// changes. Saves waiting from someone else are committed first, as theirs.
// Nothing when the app has no history yet: it starts at its next save.
func appHistoryNote(owner, slug, by, message string) {
	if appGit() == "" || strings.TrimSpace(by) == "" {
		return
	}
	dir, ok := appRepoDir(owner, slug)
	if !ok {
		return
	}
	mu := appRepoLock(dir)
	mu.Lock()
	defer mu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return
	}
	if err := liveBranchLocked(dir); err != nil {
		Log("[app-history] %s/%s: its history could not be moved to its %s branch: %v", owner, slug, AppHistoryBranch, err)
		return
	}
	if err := commitWaiting(dir, waitingInterrupted); err != nil {
		Log("[app-history] %s/%s: saves waiting could not be committed before %s's note: %v", owner, slug, by, err)
		return
	}
	if _, err := appRepoGit(dir, "-c", "user.name="+by, "-c", "user.email="+by+"@users.gohort.invalid",
		"commit", "-q", "--allow-empty", "-m", strings.TrimSpace(message)); err != nil {
		Log("[app-history] %s/%s: %s's note could not be recorded: %v", owner, slug, by, err)
	}
}

// appRepoDir is where owner's app slug keeps its history, and whether there
// is a place for it (a workspaces folder, and names that stay inside it).
func appRepoDir(owner, slug string) (string, bool) {
	base := WorkspacesDir()
	if base == "" || owner == "" || slug == "" {
		return "", false
	}
	for _, n := range []string{owner, slug} {
		if strings.ContainsAny(n, `/\`) || strings.Contains(n, "..") || strings.HasPrefix(n, ".") {
			return "", false
		}
	}
	return filepath.Join(base, owner, appHistoryDirName, slug), true
}

// appGit is the git program, or "" when this machine has none.
var appGit = sync.OnceValue(func() string {
	p, err := exec.LookPath("git")
	if err != nil {
		Log("[app-history] git is not installed: app history is kept as revisions only")
		return ""
	}
	return p
})

// AppHistoryUnavailable is why apps have no history in git here, for the
// admin page, or "" when they do.
func AppHistoryUnavailable() string {
	if appGit() == "" {
		return "git is not installed: app history is kept as revisions only"
	}
	return ""
}

// appRepoLocks keeps one write to an app's history at a time.
var appRepoLocks sync.Map

func appRepoLock(dir string) *sync.Mutex {
	m, _ := appRepoLocks.LoadOrStore(dir, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// appPending is whose saves wait to be committed in an app's history, and the
// change notes they carried. Kept in the repository's .git, not among the
// app's files. Waiting saves are always one author's: a save by someone else
// commits them first, under their own name (appWorkInterrupted).
type appPending struct {
	By    string   `json:"by"`
	Via   string   `json:"via,omitempty"`
	Notes []string `json:"notes,omitempty"`
}

func appPendingPath(dir string) string { return filepath.Join(dir, ".git", "gohort-pending.json") }

func loadAppPending(dir string) appPending {
	var p appPending
	if b, err := os.ReadFile(appPendingPath(dir)); err == nil {
		json.Unmarshal(b, &p)
	}
	return p
}

func saveAppPending(dir string, p appPending) error {
	b, _ := json.Marshal(p)
	return os.WriteFile(appPendingPath(dir), b, 0o600)
}

// Why waiting saves are committed before their work is marked finished.
const (
	waitingInterrupted = "Committed when someone else changed the app, before this work was marked finished."
	waitingForCheckout = "Committed so a checkout could take the app as it is live, before this work was marked finished."
)

// appPendingMessage is the commit message for waiting saves committed before
// they were marked finished, saying why.
func appPendingMessage(p appPending, why string) string {
	subject := "Work in progress by " + p.By
	var rest []string
	for i, n := range p.Notes {
		first, _, _ := strings.Cut(strings.TrimSpace(n), "\n")
		if i == 0 && first != "" {
			subject = first
			continue
		}
		if first != "" {
			rest = append(rest, "- "+first)
		}
	}
	msg := subject + "\n\n" + why
	if len(rest) > 0 {
		msg += "\n\nAlso in it:\n" + strings.Join(rest, "\n")
	}
	return msg
}

// commitWaiting commits the saves waiting in dir as their author's, saying
// why they were committed unfinished, and forgets them.
func commitWaiting(dir, why string) error {
	p := loadAppPending(dir)
	if p.By == "" {
		return nil
	}
	if err := appRepoCommit(dir, p.By, p.Via, appPendingMessage(p, why)); err != nil {
		return err
	}
	return os.Remove(appPendingPath(dir))
}

// syncAppRepo writes spec as saved into its app's repository, making the
// repository (and its first commit) when there is none. A person's own
// change is committed at once, as theirs; an agent's waits for the work to
// be finished (CommitAppHistory). Saves waiting from someone else are
// committed first, so a commit is only ever its author's work.
func syncAppRepo(spec AppSpec) error {
	if appGit() == "" || len(spec.Sections) == 0 {
		return nil
	}
	dir, ok := appRepoDir(spec.Owner, spec.Slug)
	if !ok {
		return nil
	}
	mu := appRepoLock(dir)
	mu.Lock()
	defer mu.Unlock()
	return syncAppRepoLocked(dir, spec)
}

// AppHistoryBranch is the branch an app's history is kept on: what the app
// served, version by version (docs/design/app-history.md, stage 3, S2).
const AppHistoryBranch = "live"

// appHistoryOldBranch is the name stage 1 kept it under, renamed the first
// time the history is written or read after.
const appHistoryOldBranch = "main"

// liveBranchLocked renames the history in dir from appHistoryOldBranch to
// AppHistoryBranch, once, with the history held. Read from .git/HEAD, so a
// history already on its branch costs no git run.
func liveBranchLocked(dir string) error {
	head, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(head)) != "ref: refs/heads/"+appHistoryOldBranch {
		return nil
	}
	_, err = appRepoGit(dir, "branch", "-m", appHistoryOldBranch, AppHistoryBranch)
	return err
}

// syncAppRepoLocked is syncAppRepo with the app's history already held.
func syncAppRepoLocked(dir string, spec AppSpec) error {
	fresh := false
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if _, err := appRepoGit(dir, "init", "-q"); err != nil {
			return err
		}
		// Named here, not by the git version's or host's default.
		if _, err := appRepoGit(dir, "symbolic-ref", "HEAD", "refs/heads/"+AppHistoryBranch); err != nil {
			return err
		}
		fresh = true
	}
	by, via := strings.TrimSpace(spec.ChangedBy), strings.TrimSpace(spec.ChangedVia)
	if by == "" {
		by = spec.Owner
	}
	if !fresh {
		if err := liveBranchLocked(dir); err != nil {
			return err
		}
		if p := loadAppPending(dir); p.By != "" && (p.By != by || p.Via != via) {
			if err := commitWaiting(dir, waitingInterrupted); err != nil {
				return err
			}
		}
	}
	changed, err := writeAppRepoFiles(dir, spec)
	if err != nil {
		return err
	}
	if fresh {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			name = spec.Slug
		}
		return appRepoCommit(dir, spec.Owner, "", "Start of "+name+"'s history\n\nThe app as it was when its history began.")
	}
	if !changed {
		return nil
	}
	note := strings.TrimSpace(spec.ChangeNote)
	if via == "" {
		if note == "" {
			note = "Change by " + by
		}
		return appRepoCommit(dir, by, "", note)
	}
	// A save that puts the app back as its newest version has (an install
	// refused after it saved, saving the app back) leaves nothing waiting:
	// its record goes, or its note would ride into the next commit's message.
	if out, err := appRepoGit(dir, "status", "--porcelain"); err == nil && strings.TrimSpace(out) == "" {
		if err := os.Remove(appPendingPath(dir)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	p := loadAppPending(dir)
	p.By, p.Via = by, via
	if note != "" && (len(p.Notes) == 0 || p.Notes[len(p.Notes)-1] != note) {
		p.Notes = append(p.Notes, note)
	}
	return saveAppPending(dir, p)
}

// ErrNoAppHistory is a commit where apps keep no history in git.
var ErrNoAppHistory = errors.New("git is not installed here, so apps keep their history as revisions only")

// CommitAppHistory commits the finished work waiting in owner's app slug as
// by's, crediting via (the agent that did it, or ""), with message. Another
// person's waiting saves are committed first, as theirs. The app's files are
// written again first, so what changed since its last save (an asset added)
// is in it. It reports whether a commit of by's was made: false when nothing
// of theirs was waiting.
func CommitAppHistory(owner, slug, by, via, message string) (bool, error) {
	if appGit() == "" {
		return false, ErrNoAppHistory
	}
	dir, ok := appRepoDir(owner, slug)
	if !ok {
		return false, fmt.Errorf("no place for the history of %q", slug)
	}
	spec, found := LoadAppSpec(owner, slug)
	if !found {
		return false, fmt.Errorf("no app %q", slug)
	}
	mu := appRepoLock(dir)
	mu.Lock()
	defer mu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false, fmt.Errorf("%s has no history yet: it starts at the app's next save", spec.Name)
	}
	if err := liveBranchLocked(dir); err != nil {
		return false, err
	}
	if p := loadAppPending(dir); p.By != "" && (p.By != by || p.Via != via) {
		if err := commitWaiting(dir, waitingInterrupted); err != nil {
			return false, err
		}
	}
	if _, err := writeAppRepoFiles(dir, spec); err != nil {
		return false, err
	}
	started := time.Now()
	head, _ := appRepoGit(dir, "rev-parse", "HEAD")
	if err := appRepoCommit(dir, by, via, message); err != nil {
		return false, err
	}
	os.Remove(appPendingPath(dir))
	after, _ := appRepoGit(dir, "rev-parse", "HEAD")
	done := strings.TrimSpace(after) != strings.TrimSpace(head)
	if done {
		// What stage 1 is to measure before the later stages are settled
		// (docs/design/app-history.md, step 5): what a commit costs, and how
		// large an app's history grows.
		Log("[app-history] %s/%s: committed in %s; its history is %s", owner, slug, time.Since(started).Round(time.Millisecond), HumanSize(dirBytes(filepath.Join(dir, ".git"))))
	}
	return done, nil
}

// dirBytes is the size of the files under dir.
func dirBytes(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// writeAppRepoFiles makes the repository's files the app's: written where
// they differ, removed where the app no longer has them, and reports whether
// any changed. .git is the repository's own.
func writeAppRepoFiles(dir string, spec AppSpec) (bool, error) {
	return writeAppRepoFilesOwned(dir, spec, nil)
}

// writeAppRepoFilesOwned is writeAppRepoFiles with what the app owns as
// owned has it (a draft's), nil for as it is live.
func writeAppRepoFilesOwned(dir string, spec AppSpec, owned map[string][]byte) (bool, error) {
	return writeAppRepoFolder(dir, spec, liveAppAssets(spec.Owner, spec.Slug), owned)
}

// liveAppAssets are owner's app slug's assets as it has them, by name.
func liveAppAssets(owner, slug string) map[string][]byte {
	assets := map[string][]byte{}
	if names, err := ListAppAssets(owner, slug); err == nil {
		for _, n := range names {
			if data, _, err := ReadAppAsset(owner, slug, n); err == nil {
				assets[n] = data
			}
		}
	}
	return assets
}

// writeAppRepoFolder is writeAppRepoFilesOwned with the app's assets as
// assets has them (an update's, from its file), not as the app has them.
func writeAppRepoFolder(dir string, spec AppSpec, assets, owned map[string][]byte) (bool, error) {
	changed := false
	stage, err := os.MkdirTemp("", "app-history-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(stage)
	// No live marker: the repository records the app, it is not a folder
	// to install from.
	if _, err := writeAppFolderOwned(stage, spec, assets, "", owned); err != nil {
		return false, err
	}
	want := map[string]bool{}
	err = filepath.WalkDir(stage, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(stage, p)
		want[rel] = true
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, rel)
		if have, err := os.ReadFile(dst); err == nil && bytes.Equal(have, data) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		changed = true
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		return false, err
	}
	// What the app no longer has goes: a data source removed, an asset
	// deleted. Folders left empty go with it.
	var dirs []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if rel != "." {
				dirs = append(dirs, p)
			}
			return nil
		}
		if !want[rel] {
			os.Remove(p)
			changed = true
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i]) // only an empty one goes
	}
	return changed, nil
}

// appRepoGit runs git in an app's repository on this machine, with no
// configuration of the host's or the user's and no hooks: the repository is
// gohort's own, and nothing in it should run.
func appRepoGit(dir string, args ...string) (string, error) {
	full := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command(appGit(), full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// appRepoCommit commits everything in the repository as author, with
// coAuthor (an agent's name, or "") credited in a trailer. Nothing to commit
// is not an error.
// rewordFirstCommit gives owner's app slug's history, when it is only its
// start, the message and authors of the work that made the app: a new app made
// live by an agent's commit (app_draft.go) begins with that work, not with a
// note that its history began.
func rewordFirstCommit(owner, slug, author, coAuthor, msg string) error {
	if appGit() == "" {
		return nil
	}
	dir, ok := appRepoDir(owner, slug)
	if !ok {
		return nil
	}
	mu := appRepoLock(dir)
	mu.Lock()
	defer mu.Unlock()
	if n, err := appRepoGit(dir, "rev-list", "--count", "HEAD"); err != nil || strings.TrimSpace(n) != "1" {
		return err
	}
	msg = strings.TrimSpace(msg)
	if coAuthor != "" {
		msg += "\n\nCo-Authored-By: " + coAuthor + " <" + strings.ToLower(coAuthor) + "@agents.gohort.invalid>"
	}
	_, err := appRepoGit(dir, "-c", "user.name="+author, "-c", "user.email="+author+"@users.gohort.invalid",
		"commit", "-q", "--amend", "--reset-author", "-m", msg)
	return err
}

func appRepoCommit(dir, author, coAuthor, msg string) error {
	if _, err := appRepoGit(dir, "add", "-A", "--", "."); err != nil {
		return err
	}
	if out, _ := appRepoGit(dir, "status", "--porcelain"); strings.TrimSpace(out) == "" {
		return nil
	}
	msg = strings.TrimSpace(msg)
	if coAuthor != "" {
		msg += "\n\nCo-Authored-By: " + coAuthor + " <" + strings.ToLower(coAuthor) + "@agents.gohort.invalid>"
	}
	_, err := appRepoGit(dir, "-c", "user.name="+author, "-c", "user.email="+author+"@users.gohort.invalid", "commit", "-q", "-m", msg)
	return err
}

// changedVia is what made this turn's changes for its person: an override
// (an install from Anvil), else the turn's agent (Builder), else "" for the
// person's own hand.
func (t *chatTurn) changedVia() string {
	if t.via != "" {
		return t.via
	}
	if t.agent.ID != "" {
		return firstNonEmptyStr(t.agent.Name, t.agent.ID)
	}
	return ""
}

// appDefCommit is app_def commit: the work this turn's person did on an app
// with this agent, recorded in the app's history once it is finished and
// tested (verify passed on the revision serving now), with the agent as
// co-author. Saves wait uncommitted until then, as Claude Code commits.
func (t *chatTurn) appDefCommit(args map[string]any) (string, error) {
	slug := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, _, ok := t.appForTurn(slug)
	if slug == "" || !ok {
		return "", appNotFound(args, "")
	}
	msg := strings.TrimSpace(firstNonEmptyStr(stringArg(args, "message"), stringArg(args, "note")))
	if msg == "" {
		return "", errors.New("NOT COMMITTED: give message, the commit's message: a first line saying what the work changed (under 72 characters), a blank line, and why")
	}
	if spec.Verify == nil || !spec.Verify.Current(spec) || !spec.Verify.Pass {
		return "", fmt.Errorf("NOT COMMITTED: a commit is finished, tested work, and %q has not passed verify on the version serving now (%s). Run app_def(action=\"verify\", id=%q), fix what it finds, then commit", spec.Name, spec.VerifyStatus(), spec.Slug)
	}
	subject, _, _ := strings.Cut(msg, "\n")
	subject = strings.TrimSpace(subject)
	// The draft goes live, and the commit is the change going live
	// (app_draft.go).
	if d, ok := t.appDraftOf(spec.Owner, spec.Slug); ok {
		_, wasLive := LoadAppSpec(spec.Owner, spec.Slug)
		// The message is the change's note: a new app's first commit is this
		// work, and an existing app's waiting save carries it.
		d.Spec.ChangeNote = msg
		live, err := t.goLive(d, subject)
		var conflict errDraftConflict
		switch {
		case errors.Is(err, errMergedVerifyAgain):
			return "", fmt.Errorf("NOT LIVE YET: %v. Verify your draft again (app_def(action=\"verify\", id=%q)), fix what it finds, then commit", err, spec.Slug)
		case errors.As(err, &conflict):
			return "", fmt.Errorf("NOT LIVE: %v. Nothing changed: the live app and your draft are as they were. Tell the person what overlaps, and ask whether to drop your draft (app_def(action=\"discard\", id=%q)) and make your change again on the app as it is now", err, spec.Slug)
		case errors.Is(err, errLiveMoved):
			return "", fmt.Errorf("NOT LIVE: %v (a person's change, Anvil, or a maintainer), and it cannot be merged into your draft here, so your draft would undo it. Nothing changed: the live app and your draft are as they were. Tell the person, and ask whether to drop your draft (app_def(action=\"discard\", id=%q)) and make your change again on the app as it is now", err, spec.Slug)
		}
		if err != nil {
			return "", fmt.Errorf("NOT LIVE: %w", err)
		}
		if _, err := CommitAppHistory(live.Owner, live.Slug, t.user, t.changedVia(), msg); err != nil && !errors.Is(err, ErrNoAppHistory) && !strings.Contains(err.Error(), "has no history yet") {
			Log("[app-history] %s/%s: live, but the commit was not recorded: %v", live.Owner, live.Slug, err)
		}
		if !wasLive {
			// A new app's history starts with this work, under its message.
			if err := rewordFirstCommit(live.Owner, live.Slug, t.user, t.changedVia(), msg); err != nil {
				Log("[app-history] %s/%s: its first commit keeps its starting message: %v", live.Owner, live.Slug, err)
			}
			return fmt.Sprintf("%q is live now at /apps/%s/, its first version: %s. It is in the person's My Apps.", live.Name, live.Slug, subject), nil
		}
		return fmt.Sprintf("Committed to %q's history, and live now: %s", live.Name, subject), nil
	}
	done, err := CommitAppHistory(spec.Owner, spec.Slug, t.user, t.changedVia(), msg)
	if errors.Is(err, ErrNoAppHistory) {
		return "Nothing committed: " + err.Error() + ". The work is saved and verified; its revisions keep it.", nil
	}
	if err != nil {
		return "", err
	}
	if !done {
		return fmt.Sprintf("Nothing of yours is waiting to be committed in %q: the history already has it.", spec.Name), nil
	}
	return fmt.Sprintf("Committed to %q's history: %s", spec.Name, strings.TrimSpace(subject)), nil
}

// uncommittedWorkNotice holds back a reply that would end the turn while
// work of this turn's person on the app it is working on waits in the app's
// history verified and uncommitted: finished, tested work is committed
// before the turn ends. Said once a turn; "" otherwise.
func (t *chatTurn) uncommittedWorkNotice() string {
	if t.commitAsked || t.session == nil {
		return ""
	}
	slug := strings.TrimSpace(t.session.App)
	if slug == "" {
		return ""
	}
	spec, _, ok := t.appForTurn(slug)
	if !ok || spec.Verify == nil || !spec.Verify.Current(spec) || !spec.Verify.Pass {
		return ""
	}
	// Verified work in a draft is not live until it is committed, with git
	// or without (app_draft.go).
	if t.hasAppDraft(spec.Owner, spec.Slug) {
		t.commitAsked = true
		return fmt.Sprintf("Your reply was held back: your work on %q is verified and NOT LIVE YET: it is in your draft. Commit it to make it live before you finish: app_def(action=\"commit\", id=%q, message=...), a first line saying what the work changed (under 72 characters), a blank line, and why. Then give your reply.", spec.Name, spec.Slug)
	}
	if appGit() == "" {
		return ""
	}
	dir, ok := appRepoDir(spec.Owner, spec.Slug)
	if !ok {
		return ""
	}
	if p := loadAppPending(dir); p.By != t.user || p.Via != t.changedVia() {
		return ""
	}
	t.commitAsked = true
	return fmt.Sprintf("Your reply was held back: your work on %q is verified and not yet in its history. Commit it before you finish: app_def(action=\"commit\", id=%q, message=...), a first line saying what the work changed (under 72 characters), a blank line, and why. Then give your reply.", spec.Name, spec.Slug)
}

// BundleAppHistory writes owner's app slug's history to dest as a git bundle,
// for a checkout of it (Anvil) to clone or fetch from: the history is
// gohort's, and a checkout's git runs in the sandbox, which reaches a file it
// is handed and nothing else. It is brought up to date with the app first:
// made, for an app saved before it had one; waiting saves committed as their
// author's; anything changed outside a save (an asset) committed as the
// owner's. So what a checkout takes is the app as it is live, but for the
// saves waiting from keepBy through keepVia, which are the checkout's own
// work (an install from it not yet finished) and stay waiting; "" commits
// every waiting save, for a checkout made new.
func BundleAppHistory(owner, slug, dest, keepBy, keepVia string) error {
	if appGit() == "" {
		return ErrNoAppHistory
	}
	dir, ok := appRepoDir(owner, slug)
	if !ok {
		return fmt.Errorf("no place for the history of %q", slug)
	}
	spec, found := LoadAppSpec(owner, slug)
	if !found {
		return fmt.Errorf("no app %q", slug)
	}
	mu := appRepoLock(dir)
	mu.Lock()
	defer mu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		spec.ChangedBy, spec.ChangedVia = owner, ""
		if err := syncAppRepoLocked(dir, spec); err != nil {
			return err
		}
	}
	if err := liveBranchLocked(dir); err != nil {
		return err
	}
	own := false
	if p := loadAppPending(dir); keepBy != "" && p.By == keepBy && p.Via == keepVia {
		own = true
	} else if err := commitWaiting(dir, waitingForCheckout); err != nil {
		return err
	}
	if changed, err := writeAppRepoFiles(dir, spec); err != nil {
		return err
	} else if changed && !own {
		if err := appRepoCommit(dir, owner, "", "Bring the history up to date with the app\n\nChanges made outside a save, such as an asset added."); err != nil {
			return err
		}
	}
	_, err := appRepoGit(dir, "bundle", "create", dest, AppHistoryBranch)
	return err
}

// AppLiveMarker is what a folder of owner's app slug records as the live
// version it holds (appFolderBaseFile), so an install from it is not taken
// for undoing a change made elsewhere.
func AppLiveMarker(owner, slug string) (string, error) {
	spec, ok := LoadAppSpec(owner, slug)
	if !ok {
		return "", fmt.Errorf("no app %q", slug)
	}
	return appLiveFingerprint(spec), nil
}

// AppLiveMarkerFile is the name of that record in a folder.
const AppLiveMarkerFile = appFolderBaseFile
