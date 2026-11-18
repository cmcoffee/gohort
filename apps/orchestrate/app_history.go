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
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
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

// syncAppRepo writes spec as saved into its app's repository, making the
// repository (and its first commit) when there is none.
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
	fresh := false
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if _, err := appRepoGit(dir, "init", "-q"); err != nil {
			return err
		}
		fresh = true
	}
	if err := writeAppRepoFiles(dir, spec); err != nil {
		return err
	}
	if fresh {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			name = spec.Slug
		}
		return appRepoCommit(dir, spec.Owner, "", "Start of "+name+"'s history\n\nThe app as it was when its history began.")
	}
	return nil
}

// writeAppRepoFiles makes the repository's files the app's: written where
// they differ, removed where the app no longer has them. .git is the
// repository's own.
func writeAppRepoFiles(dir string, spec AppSpec) error {
	stage, err := os.MkdirTemp("", "app-history-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	assets := map[string][]byte{}
	if names, err := ListAppAssets(spec.Owner, spec.Slug); err == nil {
		for _, n := range names {
			if data, _, err := ReadAppAsset(spec.Owner, spec.Slug, n); err == nil {
				assets[n] = data
			}
		}
	}
	// No live marker: the repository records the app, it is not a folder
	// to install from.
	if _, err := writeAppFolder(stage, spec, assets, ""); err != nil {
		return err
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
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		return err
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
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i]) // only an empty one goes
	}
	return nil
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
