package customapps

// A preview: an app's working copy, served to the person changing it and
// nobody else (docs/design/apps-as-repositories.md). Anvil builds the app from
// its folder without installing it and starts a preview here; the preview's
// link sets a cookie on the app's own path, and while the browser carries it
// the person sees the working copy at the app's usual address, with a banner
// back to the installed version. Everyone else keeps the installed app.
//
// The working copy reads and writes a copy of the person's records and of
// the app's shared collections, held to a page check's rules (no outbound
// writes, notifications, agents or pipelines), so trying a Delete button or a
// broken save changes nothing real. Real records is a choice made when the
// preview starts. A preview the server no longer has, or that is someone
// else's, is ignored: the browser gets the installed app.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appdraft"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// previewCookie is the cookie a browser in an app's preview carries, on
// that app's path only.
const previewCookie = "gohort_app_preview"

// appPreview is one person's preview of one app.
type appPreview struct {
	Editor  string
	Spec    AppSpec
	Scratch string // the page-check store's token; "" for real records
	end     func()
	// oneLoad marks a preview made for one browser load by an assistant
	// (CheckPreview): not the person's preview, so starting, ending or
	// asking after theirs never finds it, and it ends with its load.
	oneLoad bool
}

var (
	previewsMu sync.Mutex
	previews   = map[string]*appPreview{} // token -> preview
	// hostApp is the custom-apps host previews are started on, set by Routes.
	hostApp *CustomApps
)

// StartAppPreview starts editor's preview of spec, the app built from its
// working copy, or refreshes the one they have of it: same address, same
// copied records, the new build. It returns the link that switches their
// browser to it. Only the app's owner previews it.
func StartAppPreview(editor string, spec AppSpec, realRecords bool) (string, error) {
	tok, err := startAppPreview(editor, spec, realRecords)
	if err != nil {
		return "", err
	}
	return hostApp.previewLink(spec.Slug, tok), nil
}

// startAppPreview is StartAppPreview's preview, by its token. Its owner
// previews an app, and so does a maintainer changing it (a draft of theirs,
// core/appdraft).
func startAppPreview(editor string, spec AppSpec, realRecords bool) (string, error) {
	T := hostApp
	if T == nil {
		return "", fmt.Errorf("the custom apps host is not running")
	}
	editor = strings.TrimSpace(editor)
	if editor == "" || strings.TrimSpace(spec.Slug) == "" || (spec.Owner != editor && !spec.MaintainedBy(editor)) {
		return "", fmt.Errorf("only an app's owner, or a maintainer changing it, previews it")
	}
	previewsMu.Lock()
	defer previewsMu.Unlock()
	for tok, p := range previews {
		if p.Editor == editor && p.Spec.Slug == spec.Slug && !p.oneLoad {
			if (p.Scratch == "") == realRecords {
				p.Spec = spec
				return tok, nil
			}
			if p.end != nil {
				p.end()
			}
			delete(previews, tok)
		}
	}
	tok := previewToken()
	p := &appPreview{Editor: editor, Spec: spec}
	if !realRecords {
		p.Scratch, p.end = T.previewScratch(spec, editor)
	}
	previews[tok] = p
	return tok, nil
}

// draftPreviewCookie is the cookie a browser check carries to load editor's
// draft of an app (core/appdraft): the draft's preview, on a copy of their
// records, the same one their own browser is shown.
func draftPreviewCookie(editor string, spec AppSpec) (PageCheckCookie, bool) {
	tok, err := startAppPreview(editor, spec, false)
	if err != nil {
		return PageCheckCookie{}, false
	}
	return PageCheckCookie{Name: previewCookie, Value: tok, Path: hostApp.WebPath() + "/" + spec.Slug + "/", HTTPOnly: true}, true
}

// DraftPreviewLink is the link that shows editor their draft of an app, for
// a reply to give them; "" where it cannot be previewed.
func DraftPreviewLink(editor string, spec AppSpec) string {
	tok, err := startAppPreview(editor, spec, false)
	if err != nil {
		return ""
	}
	return hostApp.previewLink(spec.Slug, tok)
}

// CheckPreview is a preview of spec for one browser load as editor, made for
// an assistant to look at its working copy: its own token, beside any
// preview the person has (which it neither replaces nor ends), on a fresh
// copy of editor's records when copyRecords, held to a page check's rules.
// A page check that brings its own scratch records passes false. end ends it
// and throws the copy away; call it when the load returns.
func CheckPreview(editor string, spec AppSpec, copyRecords bool) (PageCheckCookie, func(), error) {
	T := hostApp
	if T == nil {
		return PageCheckCookie{}, nil, fmt.Errorf("the custom apps host is not running")
	}
	editor = strings.TrimSpace(editor)
	if editor == "" || strings.TrimSpace(spec.Slug) == "" || (spec.Owner != editor && !spec.MaintainedBy(editor)) {
		return PageCheckCookie{}, nil, fmt.Errorf("only an app's owner, or a maintainer changing it, previews it")
	}
	p := &appPreview{Editor: editor, Spec: spec, oneLoad: true}
	if copyRecords {
		p.Scratch, p.end = T.previewScratch(spec, editor)
	}
	tok := previewToken()
	previewsMu.Lock()
	previews[tok] = p
	previewsMu.Unlock()
	end := func() {
		previewsMu.Lock()
		delete(previews, tok)
		previewsMu.Unlock()
		if p.end != nil {
			p.end()
		}
	}
	return PageCheckCookie{Name: previewCookie, Value: tok, Path: T.WebPath() + "/" + spec.Slug + "/", HTTPOnly: true}, end, nil
}

func init() {
	appdraft.CheckPreview = CheckPreview
	appdraft.PreviewCookie = draftPreviewCookie
	appdraft.PreviewLink = DraftPreviewLink
	appdraft.PreviewEnd = EndAppPreview
}

// EndAppPreview ends editor's preview of the app slug, if they have one.
func EndAppPreview(editor, slug string) {
	previewsMu.Lock()
	defer previewsMu.Unlock()
	for tok, p := range previews {
		if p.Editor == editor && p.Spec.Slug == slug && !p.oneLoad {
			if p.end != nil {
				p.end()
			}
			delete(previews, tok)
		}
	}
}

// HasAppPreview reports whether editor has a preview of the app slug
// running, so whatever changes the working copy can bring it up to date.
func HasAppPreview(editor, slug string) bool {
	previewsMu.Lock()
	defer previewsMu.Unlock()
	for _, p := range previews {
		if p.Editor == editor && p.Spec.Slug == slug && !p.oneLoad {
			return true
		}
	}
	return false
}

func previewToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (T *CustomApps) previewLink(slug, tok string) string {
	return T.WebPath() + "/" + slug + "/_preview?token=" + tok
}

// previewScratch is the copy a preview reads and writes: editor's records of
// the app and its shared collections, in a page check's store.
func (T *CustomApps) previewScratch(spec AppSpec, editor string) (string, func()) {
	live, ok := LoadAppSpec(spec.Owner, spec.Slug)
	if !ok {
		live = spec
	}
	tok, end := appscript.BeginCheckScratch(spec.Owner, spec.Slug, spec.RecordKey, appscript.ReadRecords(T.recordBase(live, editor), spec.Slug), nil)
	if sc, ok := appscript.CheckScratchFor(tok); ok {
		owners := T.recordBase(live, spec.Owner)
		for _, name := range spec.SharedCollections {
			tbl := sharedTable(spec.Slug, name)
			for _, k := range owners.Keys(tbl) {
				var v map[string]any
				if owners.Get(tbl, k, &v) {
					sc.DB.Set(tbl, k, v)
				}
			}
		}
	}
	return tok, end
}

// previewOf is the preview r's browser is in for the app slug: the person's
// own and still running. Anything else is no preview.
func previewOf(r *http.Request, user, slug string) (appPreview, bool) {
	c, err := r.Cookie(previewCookie)
	if err != nil || strings.TrimSpace(c.Value) == "" {
		return appPreview{}, false
	}
	previewsMu.Lock()
	defer previewsMu.Unlock()
	p, ok := previews[c.Value]
	if !ok || p.Editor != user || p.Spec.Slug != slug {
		return appPreview{}, false
	}
	return *p, true
}

// handlePreviewStart switches the browser to the preview its link names.
func (T *CustomApps) handlePreviewStart(w http.ResponseWriter, r *http.Request, user, slug string) {
	tok := strings.TrimSpace(r.URL.Query().Get("token"))
	previewsMu.Lock()
	p, ok := previews[tok]
	previewsMu.Unlock()
	if !ok || p.Editor != user || p.Spec.Slug != slug {
		http.Error(w, "This preview has ended. Start it again from where you are changing the app.", http.StatusNotFound)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: previewCookie, Value: tok, Path: T.WebPath() + "/" + slug + "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil})
	http.Redirect(w, r, T.WebPath()+"/"+slug+"/", http.StatusFound)
}

// handlePreviewStop takes the browser back to the installed app. The
// preview itself goes on, for the next time it is opened.
func (T *CustomApps) handlePreviewStop(w http.ResponseWriter, r *http.Request, slug string) {
	http.SetCookie(w, &http.Cookie{Name: previewCookie, Value: "", Path: T.WebPath() + "/" + slug + "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil})
	http.Redirect(w, r, T.WebPath()+"/"+slug+"/", http.StatusFound)
}

// previewBanner is the header line a preview's page carries, with the way
// back to the installed app.
func (T *CustomApps) previewBanner(p appPreview) (label, url string) {
	label = "Your working copy, not installed: show the installed app"
	if p.Scratch != "" {
		label = "Your working copy, on a copy of your records: show the installed app"
	}
	return label, T.WebPath() + "/" + p.Spec.Slug + "/_preview/stop"
}
