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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appdraft"
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
// know at its commit whether someone changed it since: the whole app but its
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
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// errLiveMoved is a commit refused because the live app changed since the
// draft started, by someone else.
var errLiveMoved = errors.New("the live app changed since your draft started")

// goLive makes this turn's draft of an app the live app: saved through the
// save path's gates, its verify verdict carried, the assets it took out
// removed, and the draft dropped. It returns the app as now live.
func (t *chatTurn) goLive(d appdraft.Draft, reason string) (AppSpec, error) {
	return goDraftLive(d, reason)
}

// goDraftLive is goLive, for a turn or for the person's own Make live.
func goDraftLive(d appdraft.Draft, reason string) (AppSpec, error) {
	live, isLive := LoadAppSpec(d.Spec.Owner, d.Spec.Slug)
	switch {
	case isLive && appLiveMark(live) != d.Base:
		return AppSpec{}, errLiveMoved
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
	saved := SaveAppSpecBy(spec, reason, d.Editor, spec.ChangedVia)
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
	live, err := goDraftLive(d, subject)
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

// appLiveCommit is the commit owner's app slug's history is at, "" where it
// has none.
func appLiveCommit(owner, slug string) string {
	if appGit() == "" {
		return ""
	}
	dir, ok := appRepoDir(owner, slug)
	if !ok {
		return ""
	}
	out, err := appRepoGit(dir, "rev-parse", "-q", "--verify", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
