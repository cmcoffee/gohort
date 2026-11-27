// Package appdraft keeps the work in progress on an app (docs/design/
// app-history.md, stage 3, S3): a draft of the app record, one per app per
// person changing it, that Builder edits and tests instead of the live app.
// The live app changes only when the draft is committed, through every gate
// a save passes; until then the person sees the draft in a preview.
//
// A draft is kept in the app owner's store, beside the app, whoever is
// changing it (the owner, or a maintainer). It is never served to anyone but
// its editor, never exported, and goes with the app when the app is deleted.
package appdraft

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appcheck"
)

// table holds the drafts in an owner's store, keyed by slug and editor.
const table = "app_drafts"

// Draft is one person's work in progress on one app.
type Draft struct {
	// Spec is the app as the draft has it. Its Updated stamp is the draft's
	// own revision, which a verify verdict on the draft is pinned to.
	Spec core.AppSpec `json:"spec"`
	// Editor is who is changing it: the owner, or a maintainer.
	Editor string `json:"editor"`
	// Base is a mark of the live app when the draft started (orchestrate's
	// appLiveMark, its content), so a commit knows whether the live app
	// moved since; "" for an app that is not live yet.
	Base string `json:"base,omitempty"`
	// BaseCommit is the live app's commit in its history when the draft
	// started, what a commit merges from when the live app moved; "" where
	// there is no history.
	BaseCommit string `json:"base_commit,omitempty"`
	// Started is when the draft was begun.
	Started string `json:"started"`
	// DropAssets are the assets the draft takes out of the app, taken out of
	// the live app when it is committed: until then its page may use them.
	DropAssets []string `json:"drop_assets,omitempty"`
	// Owned is what the app owns as the draft has it, when the draft changes
	// it: every file of its folder's agents/, pipelines/, machines/, tools/
	// and knowledge/, by its path there. Nil leaves what the app owns as it
	// is live. Applied when the draft is committed.
	Owned map[string][]byte `json:"owned,omitempty"`
	// Update is set when the draft is an update to an imported app, from the
	// copy it was taken from (docs/design/app-history.md, updates for
	// imported apps): what the person is shown before making it live, and
	// what moves when they do.
	Update *Update `json:"update,omitempty"`
}

// Update is what a draft that is an update to an imported app keeps of it.
type Update struct {
	// Exported is when the copy it came from exported it.
	Exported string `json:"exported"`
	// Commit is the author's version as a commit of the app's history, what
	// the next update is merged against once this one is live; "" without
	// a history.
	Commit string `json:"commit,omitempty"`
	// Changed are the files of the app's folder the update changes.
	Changed []string `json:"changed,omitempty"`
	// Clashed are the files where the update and the changes made here since
	// changed the same lines: the draft has the author's lines there.
	Clashed []string `json:"clashed,omitempty"`
	// Stores is what the export said of the file stores the app was granted
	// there, asked for here when the update goes live.
	Stores []byte `json:"stores,omitempty"`
}

type ownedKey struct{}

// WithOwned is ctx carrying the files of what an app owns as a draft has
// them, for what runs the draft (its preview, its checks) to read them
// in place of the live ones.
func WithOwned(ctx context.Context, owned map[string][]byte) context.Context {
	if len(owned) == 0 {
		return ctx
	}
	return context.WithValue(ctx, ownedKey{}, owned)
}

// OwnedFrom is the files WithOwned put on ctx.
func OwnedFrom(ctx context.Context) (map[string][]byte, bool) {
	if ctx == nil {
		return nil, false
	}
	m, ok := ctx.Value(ownedKey{}).(map[string][]byte)
	return m, ok && len(m) > 0
}

func store(owner string) core.Database {
	if core.RootDB == nil || strings.TrimSpace(owner) == "" {
		return nil
	}
	return core.UserDB(core.RootDB, owner)
}

func key(slug, editor string) string { return slug + "/" + editor }

// Load is editor's draft of owner's app slug.
func Load(owner, slug, editor string) (Draft, bool) {
	db := store(owner)
	if db == nil || slug == "" || editor == "" {
		return Draft{}, false
	}
	var d Draft
	if !db.Get(table, key(slug, editor), &d) {
		return Draft{}, false
	}
	d.Spec.Checks = appcheck.Unpack(d.Spec.Checks)
	return d, true
}

// Save stores d, stamping its revision: the draft's Spec.Updated, with
// nanoseconds, so two saves in one second are two revisions and a verdict on
// the first never reads as one on the second. The verdict d carries is kept
// only when keepVerify says the change left what it judged as it was.
func Save(d Draft, keepVerify bool) Draft {
	db := store(d.Spec.Owner)
	if db == nil || d.Spec.Slug == "" || d.Editor == "" {
		return d
	}
	now := time.Now().UTC()
	if d.Started == "" {
		d.Started = now.Format(time.RFC3339)
	}
	if d.Spec.Created == "" {
		d.Spec.Created = now.Format(time.RFC3339)
	}
	prev := d.Spec.Updated
	d.Spec.Updated = now.Format(time.RFC3339Nano)
	switch {
	case keepVerify && d.Spec.Verify != nil && d.Spec.Verify.Against == prev:
		v := *d.Spec.Verify
		v.Against = d.Spec.Updated
		d.Spec.Verify = &v
	default:
		d.Spec.Verify = nil
	}
	stored := d
	stored.Spec.Checks = appcheck.Pack(d.Spec.Checks)
	db.Set(table, key(d.Spec.Slug, d.Editor), stored)
	return d
}

// RecordVerify stores a verify outcome for the draft's current revision,
// as core's AppSpec.RecordVerify does for the live app, without making a new
// revision of it.
func RecordVerify(owner, slug, editor string, pass bool, summary string) bool {
	d, ok := Load(owner, slug, editor)
	if !ok {
		return false
	}
	d.Spec.Verify = &core.AppVerifyState{
		At:      time.Now().UTC().Format(time.RFC3339),
		Against: d.Spec.Updated,
		Pass:    pass,
		Summary: strings.TrimSpace(summary),
	}
	stored := d
	stored.Spec.Checks = appcheck.Pack(d.Spec.Checks)
	store(owner).Set(table, key(slug, editor), stored)
	return true
}

// Delete drops editor's draft of owner's app slug.
func Delete(owner, slug, editor string) {
	if db := store(owner); db != nil {
		db.Unset(table, key(slug, editor))
	}
}

// List is every draft of owner's apps, or of the app slug alone when slug is
// given, oldest first.
func List(owner, slug string) []Draft {
	db := store(owner)
	if db == nil {
		return nil
	}
	var out []Draft
	for _, k := range db.Keys(table) {
		if slug != "" && !strings.HasPrefix(k, slug+"/") {
			continue
		}
		var d Draft
		if db.Get(table, k, &d) {
			d.Spec.Checks = appcheck.Unpack(d.Spec.Checks)
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Started < out[j].Started })
	return out
}

// DeleteAll drops every draft of owner's app slug, with the app.
func DeleteAll(owner, slug string) {
	db := store(owner)
	if db == nil {
		return
	}
	for _, k := range db.Keys(table) {
		if strings.HasPrefix(k, slug+"/") {
			db.Unset(table, k)
		}
	}
}

// PreviewCookie is the cookie a browser check carries to load editor's draft
// spec instead of the live app, set by the custom-apps host (its preview):
// nil, or false, where there is none.
var PreviewCookie func(editor string, spec core.AppSpec) (core.PageCheckCookie, bool)

// PreviewLink is the link that shows editor their draft spec, for a reply to
// give them, and PreviewEnd ends their preview of an app once its draft is
// gone; both set by the custom-apps host.
var (
	PreviewLink func(editor string, spec core.AppSpec) string
	PreviewEnd  func(editor, slug string)
)

// CheckPreview is a preview of spec for one browser load as editor, beside
// any preview the person has, on a fresh copy of their records when
// copyRecords; end ends it. Set by the custom-apps host; nil where there is
// none.
var CheckPreview func(editor string, spec core.AppSpec, copyRecords bool) (cookie core.PageCheckCookie, end func(), err error)

// MakeLive makes d live as by's own change, through every gate a save
// passes, whether or not its work was finished and verified: the person's
// choice, from My Apps. It returns what to tell them. Set by the host that
// keeps the apps' history (orchestrate).
var MakeLive func(d Draft, by string) (string, error)

func init() {
	core.RegisterAppSpecDeletedHook(DeleteAll)
}
