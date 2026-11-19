package orchestrate

// Builder's work on an app is a draft until it is committed (docs/design/
// app-history.md, stage 3, S3). An agent changing an app in a chat edits its
// draft of it (core/appdraft), tests that draft in a browser through the
// app's preview, and its commit is what makes the change live: saved through
// every gate a save passes, and committed on the app's live branch. A turn
// that stops short changes nothing live. A person's own change (an Install
// from Anvil, an import, a setting) still saves the live app at once.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appdraft"
	"github.com/cmcoffee/gohort/core/apptools"
)

// editsDrafts reports whether this turn's app changes go to a draft: an
// agent's turn in a chat. A person's own turn (personTurn, for Install and
// Preview) and an import change the live app.
func (t *chatTurn) editsDrafts() bool {
	return t != nil && t.agent.ID != "" && t.via == ""
}

// appDraftOf is this turn's draft of owner's app slug, when it edits drafts
// and has one.
func (t *chatTurn) appDraftOf(owner, slug string) (appdraft.Draft, bool) {
	if !t.editsDrafts() {
		return appdraft.Draft{}, false
	}
	return appdraft.Load(owner, slug, t.user)
}

// ownDraft is this turn's draft of an app of its person's that is not live
// yet (a new app), for the reads that find apps by the person's own slug.
func (t *chatTurn) ownDraft(slug string) (AppSpec, bool) {
	d, ok := t.appDraftOf(t.user, slug)
	return d.Spec, ok
}

// draftVerdictCarries reports whether a draft's verify verdict still holds
// after a change from prior to next: only its words and bookkeeping
// changed, as core's own save carries a live app's (appVerdictCarries).
func draftVerdictCarries(prior, next AppSpec) bool {
	strip := func(s AppSpec) []byte {
		s.Name, s.Desc, s.Notes, s.ChangeNote = "", "", "", ""
		s.Created, s.Updated, s.Verify, s.Schema = "", "", nil, 0
		s.ChangedBy, s.ChangedVia = "", ""
		b, _ := json.Marshal(s)
		return b
	}
	return bytes.Equal(strip(prior), strip(next))
}

// saveAppEdit saves spec as this turn's change to it: to its draft for a turn
// that edits drafts, starting one from the live app when there is none, else
// to the live app (SaveAppSpecBy). It returns the app as saved.
func (t *chatTurn) saveAppEdit(spec AppSpec, reason string) AppSpec {
	if !t.editsDrafts() {
		return SaveAppSpecBy(spec, reason, t.user, t.changedVia())
	}
	spec.ChangedBy, spec.ChangedVia = t.user, t.changedVia()
	d, had := appdraft.Load(spec.Owner, spec.Slug, t.user)
	if !had {
		d = appdraft.Draft{Editor: t.user}
		if live, ok := LoadAppSpec(spec.Owner, spec.Slug); ok {
			d.Base = appLiveMark(live)
			d.BaseCommit = appLiveCommit(spec.Owner, spec.Slug)
		}
	}
	keep := had && draftVerdictCarries(d.Spec, spec)
	if keep {
		spec.Verify = d.Spec.Verify
	}
	d.Spec = spec
	return appdraft.Save(d, keep).Spec
}

// undoAppEdit puts back what an edit replaced, when a check after it found
// the edit broke the app: before as it stood, which was the draft when there
// was one (hadDraft), else the live app, whose draft this edit started and
// is now dropped. A turn that does not edit drafts saves before back live.
func (t *chatTurn) undoAppEdit(before AppSpec, hadDraft bool) {
	if !t.editsDrafts() {
		SaveAppSpecBy(before, AppSaveNoHistory, t.user, t.changedVia())
		return
	}
	if !hadDraft {
		appdraft.Delete(before.Owner, before.Slug, t.user)
		return
	}
	d, ok := appdraft.Load(before.Owner, before.Slug, t.user)
	if !ok {
		return
	}
	d.Spec = before
	appdraft.Save(d, false)
}

// hasAppDraft reports whether this turn has a draft of owner's app slug.
func (t *chatTurn) hasAppDraft(owner, slug string) bool {
	_, ok := t.appDraftOf(owner, slug)
	return ok
}

// recordVerdict stores a verify outcome on the version of spec this turn
// checked: its draft when spec is the draft, else the live app.
func (t *chatTurn) recordVerdict(spec AppSpec, pass bool, summary string) {
	if d, ok := t.appDraftOf(spec.Owner, spec.Slug); ok && d.Spec.Updated == spec.Updated {
		appdraft.RecordVerify(spec.Owner, spec.Slug, t.user, pass, summary)
		return
	}
	spec.RecordVerify(pass, summary)
}

// appCheckCookies are what a browser check of spec carries to load spec
// itself, for a turn that edits drafts: the preview of it, which serves
// whatever version it is handed (the draft, or the version before an edit, to
// tell whether the edit broke a check). checkedAs is who the check loads the
// page as: this turn's person, whose preview it is. A turn that does not edit
// drafts checks the live app, as its owner, with no cookie.
func (t *chatTurn) appCheckCookies(spec AppSpec) (cookies []PageCheckCookie, checkedAs string) {
	if !t.editsDrafts() || appdraft.PreviewCookie == nil {
		return nil, spec.Owner
	}
	c, ok := appdraft.PreviewCookie(t.user, spec)
	if !ok {
		return nil, spec.Owner
	}
	return []PageCheckCookie{c}, t.user
}

// appDraftNote is what an app_def result says of a change saved to a draft:
// that it is not live, where to see it, and how it goes live.
func (t *chatTurn) appDraftNote(spec AppSpec) string {
	if !t.hasAppDraft(spec.Owner, spec.Slug) {
		return ""
	}
	link := ""
	if appdraft.PreviewLink != nil {
		link = appdraft.PreviewLink(t.user, spec)
	}
	note := "\n\nNOT LIVE YET: this change is in your draft of the app, not the live app. "
	if link != "" {
		note += "The person sees it at " + link + " (a preview, on a copy of their records). "
	}
	return note + fmt.Sprintf("It goes live when the work is verified and you commit it: app_def(action=\"commit\", id=%q, message=...).", spec.Slug)
}

// appLiveMark is what a draft remembers of the live app it started from, to
// know at its commit whether someone changed it since: the whole app and
// what it owns, but its
// bookkeeping, and but what a draft does not decide and a commit keeps as the
// live app has it (who it is shared with, whether it runs, who maintains it).
// Its content, not its stamp: two saves in one second have one stamp.
func appLiveMark(s AppSpec) string {
	s.Created, s.Updated, s.Verify, s.Schema = "", "", nil, 0
	s.ChangedBy, s.ChangedVia, s.ChangeNote = "", "", ""
	s.Shared, s.SharedGroups, s.SharedWith, s.PublicToken, s.Disabled = false, nil, nil, "", false
	s.Maintainers, s.Managers = nil, nil
	s.Sample = nil
	b, _ := json.Marshal(s)
	h := sha256.New()
	h.Write(b)
	// And what it owns, as files: an agent of the app changed live (by
	// Anvil) is a change the draft's copy of it would undo.
	owned := appOwnedFiles(s.Owner, s.Slug)
	names := make([]string, 0, len(owned))
	for n := range owned {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		h.Write([]byte("\x00" + n + "\x00"))
		h.Write(owned[n])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// errLiveMoved is a commit refused because the live app changed since the
// draft started, by someone else, and could not be merged into it (no
// history to merge in: no git, or a draft started before it had one).
var errLiveMoved = errors.New("the live app changed since your draft started")

// errMergedVerifyAgain is an agent's commit stopped after the live app's
// changes since the draft started were merged into it: the two together have
// not been tested.
var errMergedVerifyAgain = errors.New("the live app changed since your draft started (a person's change, Anvil, or a maintainer), and those changes are now merged into your draft, which has not been verified with them")

// errDraftConflict is a merge stopped because the draft and what was saved
// to the live app since it started change the same lines.
type errDraftConflict struct{ files []string }

func (e errDraftConflict) Error() string {
	return "the live app changed since your draft started, and those changes and yours change the same lines in " + strings.Join(e.files, ", ")
}

// mergeLiveIntoDraft brings what was saved to the live app since d started
// into d, the way git merges: d's files committed on the commit it started
// from, the live app's history merged into them, and the result read back as
// the app. Done in a clone of the history, so neither the history nor the
// live app changes; an overlap is errDraftConflict. It returns d holding the
// merge, its base moved to the live app as it is now.
func mergeLiveIntoDraft(d appdraft.Draft) (appdraft.Draft, error) {
	owner, slug := d.Spec.Owner, d.Spec.Slug
	o, hosted := agenthostApp()
	live, isLive := LoadAppSpec(owner, slug)
	dir, ok := appRepoDir(owner, slug)
	if appGit() == "" || d.BaseCommit == "" || !hosted || !isLive || !ok {
		return d, errLiveMoved
	}
	mu := appRepoLock(dir)
	mu.Lock()
	defer mu.Unlock()
	head, err := historyAtLiveLocked(dir, live)
	if err != nil {
		return d, err
	}
	tmp, err := os.MkdirTemp("", "app-draft-merge-")
	if err != nil {
		return d, err
	}
	defer os.RemoveAll(tmp)
	clone := filepath.Join(tmp, slug)
	if _, err := appRepoGit(tmp, "clone", "-q", "--no-checkout", dir, clone); err != nil {
		return d, err
	}
	if _, err := appRepoGit(clone, "checkout", "-q", "--detach", d.BaseCommit); err != nil {
		return d, fmt.Errorf("the commit your draft started from is not in the app's history: %w", err)
	}
	if _, err := writeAppRepoFilesOwned(clone, d.Spec, d.Owned); err != nil {
		return d, err
	}
	if err := appRepoCommit(clone, d.Editor, "", "Draft"); err != nil {
		return d, err
	}
	if _, err := appRepoGit(clone, "-c", "user.name="+d.Editor, "-c", "user.email="+d.Editor+"@users.gohort.invalid",
		"merge", "-q", "--no-edit", head); err != nil {
		out, _ := appRepoGit(clone, "diff", "--name-only", "--diff-filter=U")
		files := strings.Fields(out)
		if len(files) == 0 {
			return d, fmt.Errorf("the live app's changes could not be merged into your draft: %w", err)
		}
		return d, errDraftConflict{files: files}
	}
	merged, err := o.BuildAppFolder(context.Background(), owner, clone)
	if err != nil {
		return d, fmt.Errorf("your draft merged with the live app's changes does not build: %w", err)
	}
	// What it owns, merged too, when the draft changes it; left as it is live
	// when the draft does not.
	if d.Owned != nil {
		d.Owned = folderOwnedFiles(clone)
	}
	merged.ChangedBy, merged.ChangedVia, merged.ChangeNote = d.Spec.ChangedBy, d.Spec.ChangedVia, d.Spec.ChangeNote
	merged.Verify, merged.Created, merged.Updated = nil, d.Spec.Created, d.Spec.Updated
	d.Spec = merged
	d.Base, d.BaseCommit = appLiveMark(live), head
	return d, nil
}

// goLive makes this turn's draft of an app the live app: saved through the
// save path's gates, its verify verdict carried, the assets it took out
// removed, and the draft dropped. It returns the app as now live.
func (t *chatTurn) goLive(d appdraft.Draft, reason string) (AppSpec, error) {
	return goDraftLive(d, reason, false)
}

// goDraftLive is goLive, for a turn or for the person's own Make live
// (byPerson). A live app changed since the draft started is merged into the
// draft first (mergeLiveIntoDraft). For an agent's commit, a merge that
// brought in what changes how the app works is kept in the draft and the
// commit refused (errMergedVerifyAgain): the two together have not been
// tested. The person making it live takes the merge as it is.
func goDraftLive(d appdraft.Draft, reason string, byPerson bool) (AppSpec, error) {
	live, isLive := LoadAppSpec(d.Spec.Owner, d.Spec.Slug)
	switch {
	case isLive && appLiveMark(live) != d.Base:
		merged, err := mergeLiveIntoDraft(d)
		if err != nil {
			return AppSpec{}, err
		}
		keep := draftVerdictCarries(d.Spec, merged.Spec) && sameFiles(d.Owned, merged.Owned)
		if keep {
			merged.Spec.Verify = d.Spec.Verify
		}
		merged = appdraft.Save(merged, keep)
		if !keep && !byPerson {
			return AppSpec{}, errMergedVerifyAgain
		}
		d = merged
		live, isLive = LoadAppSpec(d.Spec.Owner, d.Spec.Slug)
		if appLiveMark(live) != d.Base {
			return AppSpec{}, errLiveMoved
		}
	case !isLive && d.Base != "":
		return AppSpec{}, fmt.Errorf("the app %q was deleted since your draft started", d.Spec.Name)
	}
	spec := d.Spec
	verdict := spec.Verify
	spec.Verify = nil
	if isLive {
		// What the draft does not decide stays as the live app has it: who it
		// is shared with and whether it runs, set on the live app since.
		spec.Shared, spec.SharedGroups, spec.SharedWith, spec.PublicToken = live.Shared, live.SharedGroups, live.SharedWith, live.PublicToken
		spec.Maintainers, spec.Managers, spec.Disabled = live.Maintainers, live.Managers, live.Disabled
		spec.Created = live.Created
	}
	// The version it replaces is kept under who changed it: a maintainer is
	// named, as their save always was.
	if d.Editor != spec.Owner {
		reason += " by " + d.Editor
	}
	// What it owns is checked before anything is saved: a draft's files that
	// no longer read stop the commit with the live app as it was.
	if d.Owned != nil {
		if _, err := ownedFolderFrom(d.Owned); err != nil {
			return AppSpec{}, fmt.Errorf("what the app owns in your draft does not read: %w", err)
		}
	}
	saved := SaveAppSpecBy(spec, reason, d.Editor, spec.ChangedVia)
	// Then what it owns, as the draft has it (its agents, pipelines,
	// machines, tools, collections).
	if _, err := applyDraftOwned(d); err != nil {
		Log("[app-draft] %s/%s: live, but what it owns was not all saved: %v", saved.Owner, saved.Slug, err)
		appdraft.Delete(saved.Owner, saved.Slug, d.Editor)
		return saved, fmt.Errorf("the app is live, but what it owns in your draft was not all saved: %w", err)
	}
	if d.Owned != nil {
		saved, _ = LoadAppSpec(saved.Owner, saved.Slug)
	}
	if verdict != nil && verdict.Against == d.Spec.Updated {
		saved.RecordVerify(verdict.Pass, verdict.Summary)
		saved, _ = LoadAppSpec(saved.Owner, saved.Slug)
	}
	for _, name := range d.DropAssets {
		if err := DeleteAppAsset(saved.Owner, saved.Slug, name); err != nil {
			Log("[app-draft] %s/%s: the asset %q could not be removed on commit: %v", saved.Owner, saved.Slug, name, err)
		}
	}
	appdraft.Delete(saved.Owner, saved.Slug, d.Editor)
	if appdraft.PreviewEnd != nil {
		appdraft.PreviewEnd(d.Editor, saved.Slug)
	}
	return saved, nil
}

func init() { appdraft.MakeLive = makeDraftLive }

// makeDraftLive makes d live as by's own change, from My Apps: the person's
// choice, through the same gates, finished and verified or not. The history
// records it as theirs, with the agent that did the work credited.
func makeDraftLive(d appdraft.Draft, by string) (string, error) {
	_, wasLive := LoadAppSpec(d.Spec.Owner, d.Spec.Slug)
	via := strings.TrimSpace(d.Spec.ChangedVia)
	who := firstNonEmptyStr(via, "the agent")
	msg := "Make " + who + "'s changes live\n\nMade live by " + by
	if v := d.Spec.Verify; v == nil || !v.Current(d.Spec) || !v.Pass {
		msg += ", before " + who + " had verified them"
	}
	msg += "."
	if note := strings.TrimSpace(d.Spec.ChangeNote); note != "" {
		msg += " Its last change: " + note
	}
	subject, _, _ := strings.Cut(msg, "\n")
	d.Spec.ChangeNote = msg
	live, err := goDraftLive(d, subject, true)
	if err != nil {
		return "", err
	}
	if _, err := CommitAppHistory(live.Owner, live.Slug, by, via, msg); err != nil && !errors.Is(err, ErrNoAppHistory) && !strings.Contains(err.Error(), "has no history yet") {
		Log("[app-history] %s/%s: live, but the commit was not recorded: %v", live.Owner, live.Slug, err)
	}
	if !wasLive {
		if err := rewordFirstCommit(live.Owner, live.Slug, by, via, msg); err != nil {
			Log("[app-history] %s/%s: its first commit keeps its starting message: %v", live.Owner, live.Slug, err)
		}
		return live.Name + " is live now, its first version.", nil
	}
	return who + "'s changes to " + live.Name + " are live now.", nil
}

// appDefDiscard drops this turn's draft of an app, when the person asks:
// the app is as it is live again. An app never live goes with it, and what
// was made for it.
func (t *chatTurn) appDefDiscard(args map[string]any) (string, error) {
	slug := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, _, ok := t.appForTurn(slug)
	if slug == "" || !ok {
		return "", appNotFound(args, "")
	}
	d, ok := t.appDraftOf(spec.Owner, spec.Slug)
	if !ok {
		return fmt.Sprintf("You have no draft of %q: it is as it is live.", spec.Name), nil
	}
	appdraft.Delete(spec.Owner, spec.Slug, t.user)
	if appdraft.PreviewEnd != nil {
		appdraft.PreviewEnd(t.user, spec.Slug)
	}
	t.forgetAppStanding(spec.Slug)
	if _, live := LoadAppSpec(spec.Owner, spec.Slug); !live && spec.Owner == t.user {
		gone, _ := DeleteAppOwned(t.user, spec.Slug)
		DeleteAppAssets(t.user, spec.Slug)
		msg := fmt.Sprintf("Dropped %q: it was never live.", d.Spec.Name)
		if len(gone) > 0 {
			msg += " What was made for it went too: " + strings.Join(gone, ", ") + "."
		}
		return msg, nil
	}
	return fmt.Sprintf("Dropped your draft of %q: the app is as it is live.", d.Spec.Name), nil
}

// What an app owns, in a draft (docs/design/app-history.md, stage 3, S3):
// its agents, pipelines, machines, tools and collections are changed for a
// live app as the files of its folder, published into the draft
// (appdraft.Draft.Owned) and applied when the draft is committed. The draft's
// preview and checks read them in place of the live ones where the draft is
// tried: the app's Ask (its agent) and its tool space.

// draftOwned is what spec's app owns as this turn's draft of it has it: the
// files, or nil when the draft leaves them as they are live (or there is no
// draft).
func (t *chatTurn) draftOwned(spec AppSpec) map[string][]byte {
	if d, ok := t.appDraftOf(spec.Owner, spec.Slug); ok {
		return d.Owned
	}
	return nil
}

// folderOwnedFiles are the files of what an app owns in the folder abs: every
// file under its agents/, pipelines/, machines/, tools/ and knowledge/.
func folderOwnedFiles(abs string) map[string][]byte {
	out := map[string][]byte{}
	for _, dir := range appOwnedDirs {
		root := filepath.Join(abs, dir)
		filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			if b, err := os.ReadFile(p); err == nil {
				rel, _ := filepath.Rel(abs, p)
				out[filepath.ToSlash(rel)] = b
			}
			return nil
		})
	}
	return out
}

// sameFiles reports whether a and b hold the same files, byte for byte.
func sameFiles(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || !bytes.Equal(v, w) {
			return false
		}
	}
	return true
}

// ownedFolderFrom reads files of what an app owns, as a folder holds them.
func ownedFolderFrom(files map[string][]byte) (appOwnedFolder, error) {
	tmp, err := os.MkdirTemp("", "app-draft-owned-")
	if err != nil {
		return appOwnedFolder{}, err
	}
	defer os.RemoveAll(tmp)
	for name, data := range files {
		clean := filepath.Clean(filepath.FromSlash(name))
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return appOwnedFolder{}, fmt.Errorf("a file outside the app's folder: %q", name)
		}
		p := filepath.Join(tmp, clean)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return appOwnedFolder{}, err
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return appOwnedFolder{}, err
		}
	}
	return readAppOwnedFolder(tmp, "the draft")
}

// keepOwnedInDraft records the files of what the app owns, as the folder abs
// has them, in this turn's draft of spec's app: nil again when they are as
// they are live. It reports whether the draft now changes what it owns.
func (t *chatTurn) keepOwnedInDraft(spec AppSpec, abs string) (bool, error) {
	d, ok := t.appDraftOf(spec.Owner, spec.Slug)
	if !ok {
		return false, nil
	}
	files := folderOwnedFiles(abs)
	if _, err := ownedFolderFrom(files); err != nil {
		return false, err
	}
	if sameFiles(files, appOwnedFiles(spec.Owner, spec.Slug)) {
		files = nil
	}
	if sameFiles(files, d.Owned) {
		return files != nil, nil
	}
	d.Owned = files
	// What it owns is part of how the app works: a verdict on it before is
	// not one on it now.
	appdraft.Save(d, false)
	return files != nil, nil
}

// applyDraftOwned applies what the app owns as the draft d has it, as the
// draft goes live: made by the agent that made the draft, for its owner.
func applyDraftOwned(d appdraft.Draft) (string, error) {
	if d.Owned == nil {
		return "", nil
	}
	o, ok := agenthostApp()
	if !ok {
		return "", errors.New("nothing here can save what the app owns")
	}
	f, err := ownedFolderFrom(d.Owned)
	if err != nil {
		return "", err
	}
	t := o.personTurn(context.Background(), d.Spec.Owner)
	t.via = firstNonEmptyStr(d.Spec.ChangedVia, "Builder")
	return t.applyAppOwned(d.Spec.Slug, f)
}

// draftAgentFor is agent, an app's own, as the draft whose files ctx carries
// has it (appdraft.WithOwned): the version a draft's preview and checks ask.
// The agent as it is live when the draft does not change it.
func draftAgentFor(ctx context.Context, agent AgentRecord) AgentRecord {
	owned, ok := appdraft.OwnedFrom(ctx)
	if !ok || strings.TrimSpace(agent.OwningApp) == "" {
		return agent
	}
	f, err := ownedFolderFrom(owned)
	if err != nil {
		return agent
	}
	for _, exp := range f.agents {
		if strings.EqualFold(strings.TrimSpace(exp.Name), strings.TrimSpace(agent.Name)) {
			rec := exp.AgentRecord
			keepAgentUse(&rec, agent)
			// Still the app's, whatever the file says: what stops it with
			// its app, and who reaches it, read this.
			rec.OwningApp = agent.OwningApp
			return rec
		}
	}
	return agent
}

// draftAppTools are an app's own tools as the draft whose files ctx carries
// has them, and whether it does: the tool space a draft's preview runs on.
func draftAppTools(ctx context.Context) ([]apptools.Tool, bool) {
	owned, ok := appdraft.OwnedFrom(ctx)
	if !ok {
		return nil, false
	}
	f, err := ownedFolderFrom(owned)
	if err != nil {
		return nil, false
	}
	out := make([]apptools.Tool, 0, len(f.tools))
	for _, tt := range f.tools {
		out = append(out, apptools.Tool{Tool: tt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool.Name < out[j].Tool.Name })
	return out, true
}

// appPartDraftRefusal is why maker may not change what owner's live app slug
// owns directly, "" when they may: Builder changes what a live app owns in
// its draft, through the app's folder, so the change is tried and goes live
// with the app. A new app not live yet, and anyone else, are not refused here.
func appPartDraftRefusal(owner, slug, maker, kind string) error {
	slug = strings.TrimSpace(slug)
	if maker != "Builder" || slug == "" {
		return nil
	}
	if _, live := LoadAppSpec(owner, slug); !live {
		return nil
	}
	return fmt.Errorf("NOT CHANGED: the app %q is live, and what it owns changes in your draft of it, to be tried and to go live with the app: check it out (app_def(action=\"checkout\", id=%q)), change the %s's file in %s.app/ (or add one), publish the folder, verify, then commit", slug, slug, kind, slug)
}

// draftsOfUser are the person's drafts of their own apps not yet live, for
// the app list.
func (t *chatTurn) draftsOfUser() []appdraft.Draft {
	if !t.editsDrafts() {
		return nil
	}
	var out []appdraft.Draft
	for _, d := range appdraft.List(t.user, "") {
		if d.Editor == t.user {
			out = append(out, d)
		}
	}
	return out
}

// appLiveCommit is the commit owner's app slug's history holds the live app
// at, brought up to date with it first, so a draft started from it merges
// from the app as it was (historyAtLiveLocked); "" where it has none.
func appLiveCommit(owner, slug string) string {
	live, isLive := LoadAppSpec(owner, slug)
	dir, ok := appRepoDir(owner, slug)
	if appGit() == "" || !isLive || !ok {
		return ""
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return ""
	}
	mu := appRepoLock(dir)
	mu.Lock()
	defer mu.Unlock()
	head, err := historyAtLiveLocked(dir, live)
	if err != nil {
		Log("[app-draft] %s/%s: its history could not be brought up to the live app: %v", owner, slug, err)
		return ""
	}
	return head
}

// historyAtLiveLocked brings the history in dir up to the live app, with it
// held: saves waiting committed as their author's, anything else changed
// outside a save (an asset added) as the owner's. It returns the commit.
func historyAtLiveLocked(dir string, live AppSpec) (string, error) {
	if err := liveBranchLocked(dir); err != nil {
		return "", err
	}
	if err := commitWaiting(dir, waitingInterrupted); err != nil {
		return "", err
	}
	if changed, err := writeAppRepoFiles(dir, live); err != nil {
		return "", err
	} else if changed {
		if err := appRepoCommit(dir, live.Owner, "", "Bring the history up to date with the app\n\nChanges made outside a save, such as an asset added."); err != nil {
			return "", err
		}
	}
	head, err := appRepoGit(dir, "rev-parse", "HEAD")
	return strings.TrimSpace(head), err
}
