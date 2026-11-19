package orchestrate

// An app's history as people and Builder read it, and going back to an
// earlier version (docs/design/app-history.md, stage 3, S5). The history is
// the app's live branch: each commit a version that went live, with who made
// it, the agent that did the work, and its message. Going back restores the
// app as a version had it, its files and what it owned then, as a new change
// through every gate a save passes: the history keeps what it replaced, so
// going back is itself undone the same way.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// AppCommit is one version in an app's history.
type AppCommit struct {
	Hash  string `json:"hash"`
	Short string `json:"short"`
	// Author is who made the change; Agent the agent that did the work for
	// them (Builder, Anvil), "" for a person's own change.
	Author  string `json:"author"`
	Agent   string `json:"agent,omitempty"`
	When    string `json:"when"`
	Subject string `json:"subject"`
	Body    string `json:"body,omitempty"`
	// Live marks the version serving now.
	Live bool `json:"live,omitempty"`
}

// appHistoryMax is how many versions a listing reads back at most.
const appHistoryMax = 200

// appCommitHash is a commit name a caller may hand in: hex, abbreviated or
// whole. Anything else (a branch name, an option) is refused before git sees
// it.
var appCommitHash = regexp.MustCompile(`^[0-9a-f]{4,40}$`)

// coAuthorLine reads the agent a commit credits (Co-Authored-By: Builder <...>).
var coAuthorLine = regexp.MustCompile(`(?m)^Co-Authored-By: ([^<\n]+?) <`)

// appHistoryDir is owner's app slug's history, when it has one.
func appHistoryDir(owner, slug string) (string, error) {
	if appGit() == "" {
		return "", ErrNoAppHistory
	}
	dir, ok := appRepoDir(owner, slug)
	if !ok {
		return "", fmt.Errorf("no place for the history of %q", slug)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return "", fmt.Errorf("%q has no history yet: it starts at the app's next change", slug)
	}
	return dir, nil
}

// AppHistoryLog is owner's app slug's versions, newest first, up to limit
// (appHistoryMax when 0). ErrNoAppHistory where there is no git.
func AppHistoryLog(owner, slug string, limit int) ([]AppCommit, error) {
	dir, err := appHistoryDir(owner, slug)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > appHistoryMax {
		limit = appHistoryMax
	}
	mu := appRepoLock(dir)
	mu.Lock()
	defer mu.Unlock()
	if err := liveBranchLocked(dir); err != nil {
		return nil, err
	}
	// Fields and records split by bytes no message carries.
	out, err := appRepoGit(dir, "log", fmt.Sprintf("-%d", limit), "--format=%H%x1f%h%x1f%an%x1f%cI%x1f%s%x1f%b%x1e", AppHistoryBranch, "--")
	if err != nil {
		return nil, err
	}
	head, _ := appRepoGit(dir, "rev-parse", "HEAD")
	head = strings.TrimSpace(head)
	var list []AppCommit
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.Split(strings.TrimLeft(rec, "\n"), "\x1f")
		if len(f) < 6 {
			continue
		}
		c := AppCommit{Hash: f[0], Short: f[1], Author: f[2], When: f[3], Subject: f[4], Body: cleanCommitBody(f[5])}
		if m := coAuthorLine.FindStringSubmatch(f[5]); m != nil {
			c.Agent = strings.TrimSpace(m[1])
		}
		c.Live = c.Hash == head
		list = append(list, c)
	}
	return list, nil
}

// cleanCommitBody is a commit's message after its subject, without the line
// crediting the agent (AppCommit.Agent says that).
func cleanCommitBody(body string) string {
	var kept []string
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if strings.HasPrefix(line, "Co-Authored-By: ") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// appHistoryDiffMax caps what a version's diff hands back.
const appHistoryDiffMax = 200 << 10

// resolveAppCommit is commit, a version on owner's app slug's live branch,
// as its whole hash; an error for anything not on it.
func resolveAppCommit(dir, commit string) (string, error) {
	commit = strings.ToLower(strings.TrimSpace(commit))
	if !appCommitHash.MatchString(commit) {
		return "", fmt.Errorf("%q is not a version of the app: name one by its hash, as the history lists it", commit)
	}
	full, err := appRepoGit(dir, "rev-parse", "-q", "--verify", commit+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("the app's history has no version %q", commit)
	}
	full = strings.TrimSpace(full)
	if _, err := appRepoGit(dir, "merge-base", "--is-ancestor", full, AppHistoryBranch); err != nil {
		return "", fmt.Errorf("%q is not a version the app went live as", commit)
	}
	return full, nil
}

// AppHistoryDiff is what the version commit of owner's app slug changed:
// which files, and how, capped at appHistoryDiffMax.
func AppHistoryDiff(owner, slug, commit string) (string, error) {
	dir, err := appHistoryDir(owner, slug)
	if err != nil {
		return "", err
	}
	mu := appRepoLock(dir)
	mu.Lock()
	defer mu.Unlock()
	full, err := resolveAppCommit(dir, commit)
	if err != nil {
		return "", err
	}
	out, err := appRepoGit(dir, "show", "--format=", "--stat", "--patch", "--no-color", full, "--")
	if err != nil {
		return "", err
	}
	if len(out) > appHistoryDiffMax {
		out = out[:appHistoryDiffMax] + "\n... (cut here: the rest of this version's changes is longer than the history shows)\n"
	}
	return out, nil
}

// appVersionFolder writes owner's app slug as the version commit had it into
// a new folder, and returns it with a function that removes it, and the
// version as the history has it. The folder carries the live app's marker, so
// installing it is going back, not an install that would undo a change made
// elsewhere without knowing.
func appVersionFolder(owner, slug, commit string) (string, AppCommit, func(), error) {
	dir, err := appHistoryDir(owner, slug)
	if err != nil {
		return "", AppCommit{}, nil, err
	}
	live, ok := LoadAppSpec(owner, slug)
	if !ok {
		return "", AppCommit{}, nil, fmt.Errorf("no app %q", slug)
	}
	mu := appRepoLock(dir)
	mu.Lock()
	full, err := resolveAppCommit(dir, commit)
	var meta string
	if err == nil {
		meta, err = appRepoGit(dir, "log", "-1", "--format=%H%x1f%h%x1f%an%x1f%cI%x1f%s", full)
	}
	tmp, terr := os.MkdirTemp("", "app-version-")
	done := func() { os.RemoveAll(tmp) }
	folder := filepath.Join(tmp, slug+".app")
	if err == nil && terr == nil {
		_, err = appRepoGit(tmp, "clone", "-q", "--no-checkout", dir, folder)
		if err == nil {
			_, err = appRepoGit(folder, "checkout", "-q", "--detach", full)
		}
	}
	mu.Unlock()
	if terr != nil {
		return "", AppCommit{}, nil, terr
	}
	if err != nil {
		done()
		return "", AppCommit{}, nil, err
	}
	var c AppCommit
	if f := strings.Split(strings.TrimSpace(meta), "\x1f"); len(f) >= 5 {
		c = AppCommit{Hash: f[0], Short: f[1], Author: f[2], When: f[3], Subject: f[4]}
	}
	os.RemoveAll(filepath.Join(folder, ".git"))
	if err := os.WriteFile(filepath.Join(folder, appFolderBaseFile), []byte(appLiveFingerprint(live)), 0o644); err != nil {
		done()
		return "", AppCommit{}, nil, err
	}
	return folder, c, done, nil
}

// goBackNote is the history's message for going back to version c.
func goBackNote(c AppCommit) string {
	when := c.When
	if len(when) >= 10 {
		when = when[:10]
	}
	return fmt.Sprintf("Go back to the version of %s\n\nRestores the app as it was at %s (%q, by %s).", when, c.Short, c.Subject, c.Author)
}

// RestoreAppVersion makes owner's app slug as the version commit had it the
// live app again, as user's change made from Anvil (its History): its files
// and what it owned then (an app's own agent changes only with Builder or
// Anvil), through every gate a save passes, committed under a message saying
// which version it went back to. What it owns that the version did not have
// is kept, never removed by leaving its file out.
func (T *OrchestrateApp) RestoreAppVersion(ctx context.Context, user, slug, commit string) (string, error) {
	folder, c, done, err := appVersionFolder(user, slug, commit)
	if err != nil {
		return "", err
	}
	defer done()
	t := T.personTurn(ctx, user)
	t.via = "Anvil"
	note := goBackNote(c)
	out, err := t.publishAppFolder(folder, filepath.Base(folder), folderPublish{note: note})
	if err == nil && t.savedBroken != "" {
		return out, AppSavedBroken{Problem: t.savedBroken, Report: out}
	}
	if err != nil {
		return "", err
	}
	// The going back, the app record and what it owns, is one commit.
	if _, cerr := CommitAppHistory(user, slug, user, t.via, note); cerr != nil && !errors.Is(cerr, ErrNoAppHistory) {
		Log("[app-history] %s/%s went back to %s, but its history commit failed: %v", user, slug, c.Short, cerr)
	}
	return fmt.Sprintf("%s is back to the version of %s (%s). The version it replaced is kept in its history, to go back to the same way.", slug, c.When, c.Subject), nil
}

// revertToVersion is app_def revert with to= a version in the app's history:
// the app as that version had it, in this turn's draft (or saved live, for a
// turn that does not edit drafts), with what it owned then.
func (t *chatTurn) revertToVersion(current AppSpec, commit string) (string, error) {
	o, ok := agenthostApp()
	if !ok {
		return "", errors.New("nothing here can read an earlier version")
	}
	folder, c, done, err := appVersionFolder(current.Owner, current.Slug, commit)
	if err != nil {
		return "", err
	}
	defer done()
	restored, err := o.BuildAppFolder(t.ctxOrBackground(), current.Owner, folder)
	if err != nil {
		return "", fmt.Errorf("that version does not build as the app now: %w", err)
	}
	restored.Disabled, restored.Shared, restored.PublicToken, restored.Created = current.Disabled, current.Shared, current.PublicToken, current.Created
	restored.Verify = nil
	restored.ChangeNote = goBackNote(c)
	saved := t.saveAppEdit(restored, "go back to "+c.Short)
	if t.editsDrafts() {
		if _, err := t.keepOwnedInDraft(saved, folder); err != nil {
			return "", err
		}
	}
	msg := fmt.Sprintf("Restored %q as it was at %s (%s, %q).", saved.Name, c.Short, c.When, c.Subject)
	if errs, checked := t.appPageErrors(saved); len(errs) > 0 {
		msg += "\n\nHEADS UP, that version has problems of its own in a real browser:\n- " + strings.Join(errs, "\n- ")
	} else if checked {
		t.recordVerdict(saved, true, "PASS: loaded in a real browser after going back, no JS errors")
		msg += " It was loaded in a real browser after restoring and came up clean."
	}
	return msg + t.appDraftNote(saved), nil
}

// ctxOrBackground is the turn's context, or a background one.
func (t *chatTurn) ctxOrBackground() context.Context {
	if t != nil && t.ctx != nil {
		return t.ctx
	}
	return context.Background()
}
