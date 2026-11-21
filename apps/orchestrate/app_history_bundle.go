package orchestrate

// An app's history in an export (docs/design/app-history.md, stage 3, S6).
// An export is the app as it is (its recipe); its history travels only when
// asked for, as a kind of its own beside the app, offered unticked: a history
// names the people who changed it (their user names, as its authors) and
// holds every earlier version, what was taken out of it on purpose included.
//
// It travels as a git bundle of the app's live branch: only what is
// committed, so exporting changes nothing in the history, and the recipe
// carries the app as it is. An import lands it after the app: the imported
// history becomes the start of the app's own, with the app as it was imported
// on top of it. Only on an app the import just made: a history imported alone
// never takes the place of the one an app here has kept.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appdraft"
	"github.com/cmcoffee/gohort/core/appsource"
)

// appHistoryArtifactType is the kind an app's history travels as.
const appHistoryArtifactType = "app_history"

// appHistoryBundleMax bounds one app's history in an export, as a bundle.
const appHistoryBundleMax = 16 << 20

// appHistoryFreshFor is how new an app must be for an imported history to
// become its own: the import that made it is that recent. With it, the app
// must still be as its import made it (appHistoryTakesImport).
const appHistoryFreshFor = 15 * time.Minute

// appHistoryImportedSubject is the subject of the version an imported
// history ends with: the app as it was imported, on top of it.
const appHistoryImportedSubject = "Imported here"

type appHistoryRecipe struct {
	Schema int    `json:"app_history"` // sniff key
	Name   string `json:"name"`        // the app's slug
	Bundle string `json:"bundle"`      // a git bundle of its live branch, base64
}

type appHistoryArtifact struct{}

func (appHistoryArtifact) ArtifactType() string  { return appHistoryArtifactType }
func (appHistoryArtifact) UserImportable() bool  { return true }
func (appHistoryArtifact) OptInDependency() bool { return true }
func (appHistoryArtifact) ImportsLate() bool     { return true }

// ContentKind: who changed the app and every version of it, not a recipe.
func (appHistoryArtifact) ContentKind() bool { return true }

func (appHistoryArtifact) SniffsRecipe(fields map[string]json.RawMessage) bool {
	_, ok := fields["app_history"]
	return ok
}

func init() {
	RegisterArtifactType(appHistoryArtifact{})
	// Offered beside the app, when it has a history to give.
	RegisterArtifactDependencies("custom_app", func(name, owner string) []ArtifactSel {
		spec, ok := LoadAppSpec(owner, strings.TrimSpace(name))
		if !ok || !hasAppHistory(owner, spec.Slug) {
			return nil
		}
		return []ArtifactSel{{Type: appHistoryArtifactType, Name: spec.Slug, Owner: owner}}
	})
}

// hasAppHistory reports whether owner's app slug keeps a history to export.
func hasAppHistory(owner, slug string) bool {
	_, err := appHistoryDir(owner, slug)
	return err == nil
}

func (appHistoryArtifact) ListArtifacts(_ Database) []ArtifactSel {
	adb := AuthDB()
	if adb == nil {
		return nil
	}
	var out []ArtifactSel
	for _, u := range AuthListUsers(adb) {
		for _, spec := range ListAppSpecs(u.Username) {
			if hasAppHistory(u.Username, spec.Slug) {
				out = append(out, ArtifactSel{Type: appHistoryArtifactType, Name: spec.Slug, Owner: u.Username})
			}
		}
	}
	return out
}

func (appHistoryArtifact) ExportArtifact(_ Database, name, owner string) (json.RawMessage, error) {
	slug := strings.TrimSpace(name)
	b, err := appHistoryBundle(owner, slug)
	if err != nil {
		return nil, err
	}
	return json.Marshal(appHistoryRecipe{Schema: 1, Name: slug, Bundle: base64.StdEncoding.EncodeToString(b)})
}

// Dependencies: a history belongs to its app, so exporting it brings the app.
func (appHistoryArtifact) Dependencies(_ Database, name, owner string) []ArtifactSel {
	return []ArtifactSel{{Type: "custom_app", Name: strings.TrimSpace(name), Owner: owner}}
}

func (appHistoryArtifact) RecipeDependencies(_ Database, recipe json.RawMessage, owner string, _ func(typ, name string) bool) []ArtifactSel {
	var r appHistoryRecipe
	if json.Unmarshal(recipe, &r) != nil || strings.TrimSpace(r.Name) == "" {
		return nil
	}
	return []ArtifactSel{{Type: "custom_app", Name: strings.TrimSpace(r.Name), Owner: owner}}
}

func (appHistoryArtifact) ImportArtifact(_ Database, recipe json.RawMessage, owner string) (string, string, error) {
	var r appHistoryRecipe
	if err := json.Unmarshal(recipe, &r); err != nil {
		return "", "", fmt.Errorf("invalid app history: %w", err)
	}
	slug := strings.TrimSpace(r.Name)
	if slug == "" {
		return "", "", Error("the app history names no app")
	}
	b, err := base64.StdEncoding.DecodeString(r.Bundle)
	if err != nil || len(b) == 0 {
		return slug, "its history does not read, so the app keeps a history of its own from here", nil
	}
	if why := importAppHistory(owner, slug, b); why != "" {
		return slug, why, nil
	}
	return slug, "", nil
}

// appHistoryBundle is owner's app slug's live branch as a git bundle: what is
// committed, nothing waiting.
func appHistoryBundle(owner, slug string) ([]byte, error) {
	dir, err := appHistoryDir(owner, slug)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "app-history-export-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	dest := filepath.Join(tmp, "history.bundle")
	mu := appRepoLock(dir)
	mu.Lock()
	err = liveBranchLocked(dir)
	if err == nil {
		_, err = appRepoGit(dir, "bundle", "create", "-q", dest, AppHistoryBranch)
	}
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(dest)
	if err != nil {
		return nil, err
	}
	if len(b) > appHistoryBundleMax {
		return nil, fmt.Errorf("the history of %q is more than %d MB, over what an export carries: export the app without it", slug, appHistoryBundleMax>>20)
	}
	return b, nil
}

// importAppHistory makes the history bundle b the start of owner's app slug's
// own, the app as it is here committed on top of it as the import's. It says
// why not, "" when it did: an app that is not one the import just made keeps
// the history it has, and so does one where the bundle does not read.
func importAppHistory(owner, slug string, b []byte) string {
	if appGit() == "" {
		return "this server keeps no app history (git is not installed), so the app came without its history"
	}
	spec, ok := LoadAppSpec(owner, slug)
	if !ok || spec.Owner != owner {
		return "no app " + slug + " of yours here: import the app with its history"
	}
	// An update waiting keeps the history it is merged against.
	if d, ok := appdraft.Load(owner, slug, owner); ok && d.Update != nil {
		return "app " + slug + " was updated from the copy it was taken from, and keeps the history the update is merged with"
	}
	if t, err := time.Parse(time.RFC3339, spec.Created); err != nil || time.Since(t) > appHistoryFreshFor {
		return "app " + slug + " was already here with a history of its own, which an imported one does not take the place of"
	}
	dir, err := appHistoryDir(owner, slug)
	if err != nil {
		return "app " + slug + " has no history here to start from the imported one: " + err.Error()
	}
	tmp, err := os.MkdirTemp("", "app-history-import-")
	if err != nil {
		return "its history could not be read: " + err.Error()
	}
	defer os.RemoveAll(tmp)
	bundle := filepath.Join(tmp, "history.bundle")
	if err := os.WriteFile(bundle, b, 0o600); err != nil {
		return "its history could not be read: " + err.Error()
	}
	// Its branch, live: an export brings the history onto it first.
	heads, err := appRepoGit(tmp, "bundle", "list-heads", bundle)
	if err != nil {
		return "its history does not read as a git bundle, so the app keeps a history of its own from here"
	}
	if !strings.Contains(heads+"\n", " refs/heads/"+AppHistoryBranch+"\n") {
		return "its history has no branch of an app's (live), so the app keeps a history of its own from here"
	}
	// Made beside the app's own history, on the same disk, so it moves into
	// place in one step.
	near, err := os.MkdirTemp(filepath.Dir(dir), ".import-")
	if err != nil {
		return "its history could not be taken in: " + err.Error()
	}
	defer os.RemoveAll(near)
	fresh := filepath.Join(near, "app")
	if _, err := appRepoGit(tmp, "clone", "-q", "--no-checkout", "-b", AppHistoryBranch, bundle, fresh); err != nil {
		return "its history could not be taken in: " + err.Error()
	}
	appRepoGit(fresh, "remote", "remove", "origin")
	mu := appRepoLock(dir)
	mu.Lock()
	defer mu.Unlock()
	// New is not enough: an app imported a minute ago and changed since, or
	// one that already took a history in, has versions of its own here, and
	// those are not given up for an imported history. Only an app still at
	// the version its import took, with no history taken in on it, takes one.
	if why := appHistoryTakesImport(dir, slug); why != "" {
		return why
	}
	// The app as it is here, on top: the same files its own history holds,
	// in a clone with nothing checked out, so what it no longer has goes.
	if _, err := writeAppRepoFiles(fresh, spec); err != nil {
		return "its history could not be taken in: " + err.Error()
	}
	// Committed even when the app is the history's last version file for
	// file (the usual case: an export's app is its history's tip): the
	// import is on record, and an app that took a history in says so
	// (appHistoryTakesImport).
	if _, err := appRepoGit(fresh, "add", "-A", "--", "."); err != nil {
		return "its history could not be taken in: " + err.Error()
	}
	if _, err := appRepoGit(fresh, "-c", "user.name="+owner, "-c", "user.email="+owner+"@users.gohort.invalid",
		"commit", "-q", "--allow-empty", "-m", appHistoryImportedSubject+"\n\nThe app as it was imported, on the history it came with."); err != nil {
		return "its history could not be taken in: " + err.Error()
	}
	// Swapped in whole: the app's own start goes, the imported history and
	// the import on top of it stay.
	old := filepath.Join(dir, ".git")
	keep := old + ".before-import"
	if err := os.Rename(old, keep); err != nil {
		return "its history could not be taken in: " + err.Error()
	}
	if err := os.Rename(filepath.Join(fresh, ".git"), old); err != nil {
		os.Rename(keep, old)
		return "its history could not be taken in: " + err.Error()
	}
	os.RemoveAll(keep)
	// The version taken, for a later update to merge against, is the app as
	// imported on top of the history it came with.
	if _, ok := appsource.Load(owner, slug); ok {
		if _, err := appRepoGit(dir, "update-ref", appUpstreamRef, "HEAD"); err != nil {
			Log("[app-history] %s/%s: the version taken is not marked in its history: %v", owner, slug, err)
		}
	}
	Log("[app-history] %s/%s: imported with its history", owner, slug)
	return ""
}

// appHistoryTakesImport says why the app whose history is at dir cannot take
// an imported one, "" when it can: its live version is still the one its
// import took (the import's own versions, what it owns pointed at here, are
// all it has made), and that version is not one a history was taken in with.
// Called with the history's lock held.
func appHistoryTakesImport(dir, slug string) string {
	kept := "app " + slug + " was already here with a history of its own, which an imported one does not take the place of"
	live, err := appRepoGit(dir, "rev-parse", "--verify", "-q", "refs/heads/"+AppHistoryBranch)
	if err != nil {
		return kept
	}
	taken, err := appRepoGit(dir, "rev-parse", "--verify", "-q", appUpstreamRef)
	if err != nil {
		return "app " + slug + " was not imported from an export that says which copy it was made from, so it keeps a history of its own"
	}
	if strings.TrimSpace(live) != strings.TrimSpace(taken) {
		return "app " + slug + " was changed here since it was imported, and keeps the history those changes are in"
	}
	if subj, _ := appRepoGit(dir, "log", "-1", "--format=%s", strings.TrimSpace(live)); strings.TrimSpace(subj) == appHistoryImportedSubject {
		return "app " + slug + " already took in the history it was imported with, and keeps it"
	}
	return ""
}
