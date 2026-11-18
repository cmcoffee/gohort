package anvil

// An app project works in the app's own history (docs/design/app-history.md,
// stage 1 step 4). The app's repository is gohort's (orchestrate's
// app_history.go), where every save of the app is written and Builder's
// finished work is committed; a project's checkout is a clone of it. Opening
// an app clones its history; before an install, what was saved to the app
// since (Builder's change) is fetched and merged into the work, and an
// overlap stops the install as a conflict, where it used to be refused as a
// folder gone stale; after an install, the checkout moves to the commit the
// install made in the app's history, so the two stay one.
//
// The checkout's git runs in the sandbox, which reaches only the checkout and
// what it is handed, so the history comes to it as a bundle the server writes
// (orchestrate.BundleAppHistory): the same way an uploaded bundle is
// unpacked. A project made before this has a history of its own; it keeps
// working as it did, its installs committed in its own history.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cmcoffee/gohort/apps/orchestrate"
)

// anvilVia is how the app's history names Anvil as what made a change
// (orchestrate's InstallAppFolder credits its installs so).
const anvilVia = "Anvil"

// appHistoryRef is where a checkout keeps what it fetched of the app's
// history.
const appHistoryRef = "refs/remotes/app/" + orchestrate.AppHistoryBranch

// appHistoryBundle writes user's app slug's history to a bundle file the
// sandbox can be handed, and returns it with a function that removes it.
// Beside the person's uploads, not in /tmp, which the sandbox has its own of.
// keepOwn leaves the person's waiting saves from Anvil uncommitted: they are
// the checkout's own work, already in it.
func appHistoryBundle(user, slug string, keepOwn bool) (string, func(), error) {
	up, err := uploadsDir(user)
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(up, 0o700); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(up, "app-history-")
	if err != nil {
		return "", nil, err
	}
	done := func() { os.RemoveAll(dir) }
	bundle := filepath.Join(dir, slug+".bundle")
	keepBy, keepVia := "", ""
	if keepOwn {
		keepBy, keepVia = user, anvilVia
	}
	if err := orchestrate.BundleAppHistory(user, slug, bundle, keepBy, keepVia); err != nil {
		done()
		return "", nil, err
	}
	return bundle, done, nil
}

// cloneAppHistory makes base a clone of user's app slug's history, holding
// the app as it is live. ErrNoAppHistory where there is none (no git).
func cloneAppHistory(ctx context.Context, user, slug, base string) error {
	bundle, done, err := appHistoryBundle(user, slug, false)
	if err != nil {
		return err
	}
	defer done()
	if out, err := cloneBundle(ctx, base, bundle, orchestrate.AppHistoryBranch); err != nil {
		return fmt.Errorf("the app's history could not be taken: %s", gitFailure(out, err))
	}
	// The remote names the bundle, which is gone; fetches name the next one.
	runGit(base, "remote", "remove", "origin")
	return markCheckoutLive(user, slug, base)
}

// markCheckoutLive records in base that it holds the app as it is live now,
// kept out of git.
func markCheckoutLive(user, slug, base string) error {
	mark, err := orchestrate.AppLiveMarker(user, slug)
	if err != nil {
		return err
	}
	if err := excludeFromGit(base, orchestrate.AppLiveMarkerFile); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(base, orchestrate.AppLiveMarkerFile), []byte(mark), 0o644)
}

// fetchAppHistory fetches the app's history into base's appHistoryRef.
func fetchAppHistory(ctx context.Context, user, slug, base string) error {
	bundle, done, err := appHistoryBundle(user, slug, true)
	if err != nil {
		return err
	}
	defer done()
	out, err := runGitSandboxed(ctx, base, []string{bundle},
		[]string{"-c", "protocol.file.allow=always", "fetch", "-q", bundle, "+" + orchestrate.AppHistoryBranch + ":" + appHistoryRef})
	if err != nil {
		return fmt.Errorf("the app's history could not be fetched: %s", gitFailure(string(out), err))
	}
	return nil
}

// errAppHistoryConflict is an install stopped because what was saved to the
// app since and the work in the checkout change the same lines.
type errAppHistoryConflict struct{ files []string }

func (e errAppHistoryConflict) Error() string {
	return "the app was changed since you started (by Builder, or someone else), and those changes and yours overlap in " +
		strings.Join(e.files, ", ") + ". The files now hold both, between conflict markers (<<<<<<< and >>>>>>>): keep what each should, remove the markers, then install again"
}

// takeAppChanges brings what was saved to the app since base took it into
// base's work, before an install: fetched, fast-forwarded, and the work in
// progress put back on top. It reports whether base works in the app's
// history at all (false for a project made before, which has its own). An
// overlap is errAppHistoryConflict, with the markers left in the files.
func takeAppChanges(ctx context.Context, user, slug, base string) (bool, error) {
	if err := fetchAppHistory(ctx, user, slug, base); err != nil {
		if errors.Is(err, orchestrate.ErrNoAppHistory) {
			return false, nil
		}
		return false, err
	}
	head, _ := runGit(base, "rev-parse", "HEAD")
	app, _ := runGit(base, "rev-parse", appHistoryRef)
	head, app = strings.TrimSpace(head), strings.TrimSpace(app)
	if head == "" || app == "" {
		return false, nil
	}
	if head == app {
		return true, markCheckoutLive(user, slug, base)
	}
	if _, err := runGit(base, "merge-base", "--is-ancestor", "HEAD", appHistoryRef); err != nil {
		// Not a clone of the app's history: a project made before it.
		return false, nil
	}
	st, _ := runGit(base, "status", "--porcelain", "--untracked-files=all")
	stashed := strings.TrimSpace(st) != ""
	if stashed {
		if _, err := runGit(base, "-c", "user.name="+user, "-c", "user.email="+user+"@users.gohort.invalid",
			"stash", "push", "-q", "--include-untracked", "-m", "work before taking the app's changes"); err != nil {
			return true, err
		}
	}
	if _, err := runGit(base, "merge", "-q", "--ff-only", appHistoryRef); err != nil {
		if stashed {
			runGit(base, "stash", "pop", "-q")
		}
		return true, err
	}
	if stashed {
		if _, err := runGit(base, "stash", "pop", "-q"); err != nil {
			out, _ := runGit(base, "diff", "--name-only", "--diff-filter=U")
			files := strings.Fields(out)
			if len(files) == 0 {
				return true, fmt.Errorf("your work could not be put back on the app's changes: %v", err)
			}
			return true, errAppHistoryConflict{files: files}
		}
	}
	return true, markCheckoutLive(user, slug, base)
}

// settleOnAppHistory moves base onto the commit an install just made in the
// app's history: the files are already what it holds, so only what base
// calls its last commit changes.
func settleOnAppHistory(ctx context.Context, user, slug, base string) error {
	if err := fetchAppHistory(ctx, user, slug, base); err != nil {
		return err
	}
	if _, err := runGit(base, "reset", "-q", appHistoryRef); err != nil {
		return err
	}
	return markCheckoutLive(user, slug, base)
}
