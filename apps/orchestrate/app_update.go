package orchestrate

// Updates for imported apps (docs/design/app-history.md, "Updates for
// imported apps"). An import keeps where the app came from (core/
// appsource) and, in the app's history, the author's version it took
// (appUpstreamRef): the app as it was imported here, in this deployment's
// terms (bound to its own agent and pipeline by id). A later export of the
// same copy, imported over it, is committed on top of that version, merged
// into the app as it is now (in a clone, so neither the history nor the
// live app changes), and kept as the owner's draft of the app: Preview tries
// it, Make live takes it through every gate a save passes, Discard keeps the
// app as it is. Where the update and the changes made here since change the
// same lines, the draft has the author's lines and says where; the author's
// version also waits as the history's branch "update", for Anvil to merge by
// hand. Without a history to merge from, an update is taken only into an app
// unchanged since it was taken.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appdraft"
	"github.com/cmcoffee/gohort/core/appsource"
)

// appUpstreamRef holds, in an imported app's history, the author's version
// it took last: what the next update is merged against.
const appUpstreamRef = "refs/gohort/upstream"

// appUpdateBranch is the branch an update's version waits on in the app's
// history, for Anvil to merge by hand.
const appUpdateBranch = "update"

func init() {
	RegisterAppUpdates(AppUpdateHooks{Imported: recordAppImported, For: appUpdateFor, Import: importAppUpdate})
}

// recordAppImported is core's AppUpdateHooks.Imported: where the app came from, and the
// app as it was taken marked in its history.
func recordAppImported(owner, slug string, origin AppOrigin) {
	live, ok := LoadAppSpec(owner, slug)
	if !ok {
		return
	}
	appsource.Save(owner, slug, appsource.Source{Copy: origin.Copy, Exported: origin.Exported, Taken: time.Now().UTC().Format(time.RFC3339), Mark: appLiveMark(live)})
	head := appLiveCommit(owner, slug)
	if head == "" {
		return
	}
	dir, _ := appRepoDir(owner, slug)
	mu := appRepoLock(dir)
	mu.Lock()
	defer mu.Unlock()
	if _, err := appRepoGit(dir, "update-ref", appUpstreamRef, head); err != nil {
		Log("[app-update] %s/%s: the version taken is not marked in its history: %v", owner, slug, err)
	}
}

// exportedWhen is an export's time as a person reads it.
func exportedWhen(stamp string) string {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return "at a time it does not say"
	}
	return "on " + t.UTC().Format("2 Jan 2006 at 15:04 UTC")
}

// appUpdateFor is core's AppUpdateHooks.For: a file exported from origin updates
// owner's app slug when the app was taken from that copy, the file is newer
// than the version taken, and nothing else is waiting as the owner's draft
// of it.
func appUpdateFor(owner, slug string, origin AppOrigin) (bool, string) {
	src, ok := appsource.Load(owner, slug)
	if !ok {
		return false, ""
	}
	if origin.Copy != src.Copy {
		return false, "you have an app with this slug, taken from another copy of it than this file was exported from: an update comes only from the copy you took it from"
	}
	next, err := time.Parse(time.RFC3339Nano, origin.Exported)
	if err != nil {
		return false, "this file does not say when it was exported, so it cannot be told apart from the version you have"
	}
	if have, err := time.Parse(time.RFC3339Nano, src.Exported); err == nil {
		switch {
		case next.Equal(have):
			return false, "you already have this version of it (exported " + exportedWhen(src.Exported) + ")"
		case next.Before(have):
			return false, "this file was exported " + exportedWhen(origin.Exported) + ", before the version of it you have (exported " + exportedWhen(src.Exported) + ")"
		}
	}
	if d, ok := appdraft.Load(owner, slug, owner); ok {
		if d.Update != nil {
			return false, "an update to it is already waiting in My Apps: make it live or discard it, then import this one"
		}
		return false, "you have changes to it in progress (in My Apps): make them live or discard them, then import the update"
	}
	return true, ""
}

// errNoUpdateBase is an app whose history holds no version taken to merge
// an update against (no git, or an app taken before its history did).
var errNoUpdateBase = errors.New("no version taken in its history")

// appUpdateMerge is an update merged with the app as it is here.
type appUpdateMerge struct {
	spec   AppSpec
	owned  map[string][]byte
	assets map[string][]byte
	// head is the commit the app is at here; commit the author's version.
	head, commit string
	// changed are the files the merge changes in the app; clashed those
	// where both sides changed the same lines.
	changed, clashed []string
	// ownChanges reports whether the app was changed here since it was
	// taken, so the update was merged with something.
	ownChanges bool
}

// mergeAppUpdate commits theirs (the update, with its assets and what it
// owns) on the version of owner's app taken last, and merges that into the
// app as it is now, in a clone: errNoUpdateBase when there is no version
// taken to start from.
func mergeAppUpdate(live, theirs AppSpec, assets, owned map[string][]byte, exported string) (appUpdateMerge, error) {
	var m appUpdateMerge
	owner, slug := live.Owner, live.Slug
	o, hosted := agenthostApp()
	dir, ok := appRepoDir(owner, slug)
	if appGit() == "" || !hosted || !ok {
		return m, errNoUpdateBase
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return m, errNoUpdateBase
	}
	mu := appRepoLock(dir)
	mu.Lock()
	defer mu.Unlock()
	up, err := appRepoGit(dir, "rev-parse", "--verify", "-q", appUpstreamRef)
	if err != nil {
		return m, errNoUpdateBase
	}
	up = strings.TrimSpace(up)
	if m.head, err = historyAtLiveLocked(dir, live); err != nil {
		return m, err
	}
	tmp, err := os.MkdirTemp("", "app-update-")
	if err != nil {
		return m, err
	}
	defer os.RemoveAll(tmp)
	clone := filepath.Join(tmp, slug)
	git := func(args ...string) (string, error) { return appRepoGit(clone, args...) }
	who := []string{"-c", "user.name=" + owner, "-c", "user.email=" + owner + "@users.gohort.invalid"}
	if _, err := appRepoGit(tmp, "clone", "-q", "--no-checkout", dir, clone); err != nil {
		return m, err
	}
	// The version taken is not always on the live branch (one taken by an
	// update is the author's, beside it), but a clone of a repository on
	// this disk has all of its objects.
	if _, err := git("checkout", "-q", "--detach", up); err != nil {
		return m, err
	}
	if _, err := writeAppRepoFolder(clone, theirs, assets, owned); err != nil {
		return m, err
	}
	if err := appRepoCommit(clone, owner, "", "The author's version, exported "+exportedWhen(exported)+"\n\nAs the copy this app was taken from exported it, on the version taken before."); err != nil {
		return m, err
	}
	commit, err := git("rev-parse", "HEAD")
	if err != nil {
		return m, err
	}
	m.commit = strings.TrimSpace(commit)
	if diff, _ := git("diff", "--name-only", up, m.head); strings.TrimSpace(diff) != "" {
		m.ownChanges = true
	}
	if _, err := git("checkout", "-q", "--detach", m.head); err != nil {
		return m, err
	}
	if _, err := git(append(who, "merge", "-q", "--no-edit", m.commit)...); err != nil {
		out, _ := git("diff", "--name-only", "--diff-filter=U")
		m.clashed = strings.Fields(out)
		if len(m.clashed) == 0 {
			return m, fmt.Errorf("the update could not be merged with your app: %w", err)
		}
		// Where both changed the same lines, the author's; the rest of
		// both merged as before.
		git("merge", "--abort")
		if _, err := git(append(who, "merge", "-q", "--no-edit", "-X", "theirs", m.commit)...); err != nil {
			return m, fmt.Errorf("the update could not be merged with your app: %w", err)
		}
	}
	diff, _ := git("diff", "--name-only", m.head, "HEAD")
	m.changed = strings.Fields(diff)
	if len(m.changed) > 0 {
		if m.spec, err = o.BuildAppFolder(context.Background(), owner, clone); err != nil {
			return m, fmt.Errorf("the update merged with your app does not build: %w", err)
		}
		m.owned = folderOwnedFiles(clone)
		m.assets = folderAssets(clone)
	}
	// The author's version waits in the history, for Anvil to merge by hand.
	if _, err := git("push", "-q", "-f", "origin", m.commit+":refs/heads/"+appUpdateBranch); err != nil {
		Log("[app-update] %s/%s: the update's version is not kept in its history: %v", owner, slug, err)
	}
	return m, nil
}

// folderAssets are the assets in the folder abs, by name.
func folderAssets(abs string) map[string][]byte {
	out := map[string][]byte{}
	entries, _ := os.ReadDir(filepath.Join(abs, "assets"))
	for _, e := range entries {
		if e.IsDir() || !ValidAppAssetName(e.Name()) {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(abs, "assets", e.Name())); err == nil {
			out[e.Name()] = b
		}
	}
	return out
}

// appFolderFiles are the files spec makes in its folder, with assets and
// owned (nil for as it owns them live), by path.
func appFolderFiles(spec AppSpec, assets, owned map[string][]byte) map[string][]byte {
	out := map[string][]byte{}
	tmp, err := os.MkdirTemp("", "app-update-files-")
	if err != nil {
		return out
	}
	defer os.RemoveAll(tmp)
	if _, err := writeAppFolderOwned(tmp, spec, assets, "", owned); err != nil {
		return out
	}
	filepath.WalkDir(tmp, func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			if b, err := os.ReadFile(p); err == nil {
				rel, _ := filepath.Rel(tmp, p)
				out[filepath.ToSlash(rel)] = b
			}
		}
		return nil
	})
	return out
}

// changedFiles are the paths that differ between a and b.
func changedFiles(a, b map[string][]byte) []string {
	var out []string
	for n, v := range a {
		if w, ok := b[n]; !ok || string(v) != string(w) {
			out = append(out, n)
		}
	}
	for n := range b {
		if _, ok := a[n]; !ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// importAppUpdate is core's AppUpdateHooks.Import: the update merged with the app
// as it is here and kept as the owner's draft of it, to try and make live;
// nothing live changes but an asset it adds or changes, there at once as an
// agent's are.
func importAppUpdate(u AppUpdate) (string, error) {
	owner, slug := u.Owner, strings.TrimSpace(u.Spec.Slug)
	if ok, why := appUpdateFor(owner, slug, u.Origin); !ok {
		return "", errors.New(firstNonEmptyStr(why, "this file is not an update to an app of yours"))
	}
	live, ok := LoadAppSpec(owner, slug)
	src, _ := appsource.Load(owner, slug)
	o, hosted := agenthostApp()
	if !ok || !hosted || o.DB == nil {
		return "", errors.New("nothing here can take an update to an app in")
	}
	// The update in this deployment's terms, as its import made the app:
	// bound to the app's own agent and pipeline by id.
	theirs := u.Spec
	theirs.Owner, theirs.Slug, theirs.Created = owner, slug, live.Created
	// Where its records are is this deployment's: an update never moves the
	// app onto another store, which would leave its records behind.
	theirs.PrivateDB, theirs.DataOwner = live.PrivateDB, live.DataOwner
	udb := UserDB(o.DB, owner)
	if id, ok := appScopedRef(udb, owner, slug, "agent", theirs.AgentID); ok {
		theirs.AgentID = id
	}
	if id, ok := appScopedRef(udb, owner, slug, "pipeline", theirs.PipelineID); ok {
		theirs.PipelineID = id
	}
	owned := map[string][]byte{}
	for n, b := range u.Owned {
		if n != appStoresFile {
			owned[n] = b
		}
	}
	assets := u.Assets
	if assets == nil {
		assets = map[string][]byte{}
	}

	up := appdraft.Update{Exported: u.Origin.Exported, Stores: u.Owned[appStoresFile]}
	d := appdraft.Draft{Editor: owner, Base: appLiveMark(live)}
	var draftAssets map[string][]byte
	ownChanges := false
	m, err := mergeAppUpdate(live, theirs, assets, owned, u.Origin.Exported)
	switch {
	case err == nil:
		if len(m.changed) == 0 {
			// Nothing in it the app does not have: the version is taken
			// with no draft.
			src.Exported = u.Origin.Exported
			appsource.Save(owner, slug, src)
			moveUpstream(owner, slug, m.commit)
			return live.Name + " already has everything in this version (exported " + exportedWhen(u.Origin.Exported) + "), so nothing waits to be made live.", nil
		}
		d.Spec, d.Owned, draftAssets = m.spec, m.owned, m.assets
		d.BaseCommit, up.Commit, up.Changed, up.Clashed = m.head, m.commit, m.changed, m.clashed
		ownChanges = m.ownChanges
	case errors.Is(err, errNoUpdateBase):
		// No history to merge from: taken whole, into an app unchanged since
		// it was taken.
		if src.Mark == "" || appLiveMark(live) != src.Mark {
			return "", fmt.Errorf("%s was changed here since you took it, and with no history of it here to merge the update with, taking it would undo those changes: nothing was changed", live.Name)
		}
		liveFiles := appFolderFiles(live, liveAppAssets(owner, slug), nil)
		up.Changed = changedFiles(liveFiles, appFolderFiles(theirs, assets, owned))
		if len(up.Changed) == 0 {
			src.Exported = u.Origin.Exported
			appsource.Save(owner, slug, src)
			return live.Name + " already has everything in this version (exported " + exportedWhen(u.Origin.Exported) + "), so nothing waits to be made live.", nil
		}
		d.Spec, d.Owned, draftAssets = theirs, owned, assets
	default:
		return "", err
	}
	if sameFiles(d.Owned, appOwnedFiles(owner, slug)) {
		d.Owned = nil
	}
	d.Spec.ChangedBy, d.Spec.ChangedVia = owner, ""
	d.Spec.ChangeNote = "Take the update from the author, exported " + exportedWhen(u.Origin.Exported)
	d.Spec.Verify = nil
	// Its assets: one it adds or changes is there at once, as an agent's
	// are (the live page may use the old one's name, never a new one's);
	// one it takes out stays until it is live.
	liveAssets := liveAppAssets(owner, slug)
	for n, b := range draftAssets {
		if have, ok := liveAssets[n]; ok && string(have) == string(b) {
			continue
		}
		if _, err := SaveAppAsset(owner, slug, n, b); err != nil {
			Log("[app-update] %s/%s: asset %q not taken in: %v", owner, slug, n, err)
		}
	}
	for n := range liveAssets {
		if _, ok := draftAssets[n]; !ok {
			d.DropAssets = append(d.DropAssets, n)
		}
	}
	sort.Strings(d.DropAssets)
	d.Update = &up
	appdraft.Save(d, false)
	Log("[app-update] %s/%s: an update exported %s waits as a draft (%d files changed, %d clashed)", owner, slug, u.Origin.Exported, len(up.Changed), len(up.Clashed))
	return appUpdateNote(live.Name, up, ownChanges), nil
}

// appUpdateNote is what an import says of an update waiting as a draft.
func appUpdateNote(name string, up appdraft.Update, ownChanges bool) string {
	note := "An update to " + name + ", exported " + exportedWhen(up.Exported)
	if ownChanges {
		note += ", merged with your changes since you took it,"
	}
	note += " waits in My Apps: try it with Preview, then Make live, or Discard to keep the app as it is."
	code, other := splitUpdateFiles(up.Changed)
	if len(code) > 0 {
		note += " Read what it changes in its scripts and tools first: " + strings.Join(code, ", ") + "."
	}
	if len(other) > 0 {
		note += " It also changes " + strings.Join(other, ", ") + "."
	}
	if len(up.Clashed) > 0 {
		note += " Where it and your changes changed the same lines (" + strings.Join(up.Clashed, ", ") + "), it has the author's: Discard keeps yours, or ask Anvil to merge the branch " + appUpdateBranch + " of the app's history by hand."
	}
	return note
}

// splitUpdateFiles are the files an update changes, those that run (its
// scripts and tools) apart from the rest.
func splitUpdateFiles(files []string) (code, other []string) {
	for _, f := range files {
		switch {
		case strings.HasPrefix(f, "data/"), strings.HasPrefix(f, "actions/"), strings.HasPrefix(f, "lib/"), strings.HasPrefix(f, "tools/"):
			code = append(code, f)
		default:
			other = append(other, f)
		}
	}
	return code, other
}

// appUpdateCommitMessage is the history's message for an update made live
// by its owner, by.
func appUpdateCommitMessage(up appdraft.Update, by string) string {
	msg := "Take the update from the author\n\nThe app as the copy it was taken from exported it " + exportedWhen(up.Exported) + ", merged with the changes made here since. Made live by " + by + "."
	if len(up.Clashed) > 0 {
		msg += " Where both changed the same lines (" + strings.Join(up.Clashed, ", ") + "), it has the author's."
	}
	return msg
}

// moveUpstream marks commit as the author's version owner's app slug took
// last, and drops the update's branch, taken.
func moveUpstream(owner, slug, commit string) {
	dir, ok := appRepoDir(owner, slug)
	if !ok || commit == "" || appGit() == "" {
		return
	}
	mu := appRepoLock(dir)
	mu.Lock()
	defer mu.Unlock()
	if _, err := appRepoGit(dir, "update-ref", appUpstreamRef, commit); err != nil {
		Log("[app-update] %s/%s: the version taken is not marked in its history: %v", owner, slug, err)
	}
	appRepoGit(dir, "branch", "-q", "-D", appUpdateBranch)
}

// finishAppUpdate records an update gone live: the version taken moved to
// it, the app pointed at its own agent and pipeline (one the update added
// is named by name until now), and what the update needs that only a
// deployment has asked of its owner. It returns the app as now live.
func finishAppUpdate(saved AppSpec, up appdraft.Update) AppSpec {
	owner, slug := saved.Owner, saved.Slug
	if o, ok := agenthostApp(); ok && o.DB != nil {
		bindAppOwnRefs(UserDB(o.DB, owner), owner, slug, "Point the app at its own agent and pipeline, as updated")
	}
	if live, ok := LoadAppSpec(owner, slug); ok {
		saved = live
	}
	src, _ := appsource.Load(owner, slug)
	src.Exported, src.Mark = up.Exported, appLiveMark(saved)
	appsource.Save(owner, slug, src)
	moveUpstream(owner, slug, up.Commit)
	recordAppNeeds(owner, slug, readAppStoresFile(map[string][]byte{appStoresFile: up.Stores}))
	return saved
}
