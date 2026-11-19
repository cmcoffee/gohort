package customapps

// Builder's work in progress on the person's apps, in My Apps (docs/design/
// app-history.md, stage 3, S3): an app with a draft says so on its row, and a
// new app not live yet has a row of its own. The person sees the draft in a
// preview, makes it live themselves (through every gate a save passes, before
// Builder finished if they choose), or discards it. Builder never makes a
// draft live except by its own commit of verified work.

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appdraft"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// draftRoutes serves a draft's Preview, Make live and Discard.
func (T *CustomApps) draftRoutes() {
	T.HandleFunc("/_app/draft/preview", T.handleDraftPreview)
	T.HandleFunc("/_app/draft/live", jsonapi.Post(T.DB, T.handleDraftLive))
	T.HandleFunc("/_app/draft/discard", jsonapi.Post(T.DB, T.handleDraftDiscard))
}

// ownDraft is the caller's own draft of their app slug.
func ownDraft(user, slug string) (appdraft.Draft, bool) {
	slug = strings.TrimSpace(slug)
	if user == "" || slug == "" {
		return appdraft.Draft{}, false
	}
	return appdraft.Load(user, slug, user)
}

// handleDraftPreview opens the person's draft of an app in its preview.
func (T *CustomApps) handleDraftPreview(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	d, ok := ownDraft(user, r.URL.Query().Get("slug"))
	if !ok {
		http.Error(w, "There is no work in progress on that app: it is as it is live.", http.StatusNotFound)
		return
	}
	link := DraftPreviewLink(user, d.Spec)
	if link == "" {
		http.Error(w, "This draft cannot be previewed here.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, link, http.StatusFound)
}

// handleDraftLive makes the person's draft of an app live, as their own
// change: the same gates as any save, whether or not Builder finished.
func (T *CustomApps) handleDraftLive(c *jsonapi.Call, _ struct{}) (any, error) {
	d, ok := ownDraft(c.User, c.R.URL.Query().Get("slug"))
	if !ok {
		return nil, jsonapi.NotFound("there is no work in progress on that app")
	}
	if appdraft.MakeLive == nil {
		return nil, jsonapi.Status(http.StatusServiceUnavailable, "nothing here can make a draft live")
	}
	msg, err := appdraft.MakeLive(d, c.User)
	if err != nil {
		return nil, jsonapi.Status(http.StatusConflict, err.Error())
	}
	return map[string]any{"ok": true, "message": msg}, nil
}

// handleDraftDiscard drops the person's draft of an app: the live app is as
// it was. A new app never live goes with it, and what was made for it.
func (T *CustomApps) handleDraftDiscard(c *jsonapi.Call, _ struct{}) (any, error) {
	d, ok := ownDraft(c.User, c.R.URL.Query().Get("slug"))
	if !ok {
		return nil, jsonapi.NotFound("there is no work in progress on that app")
	}
	slug := d.Spec.Slug
	appdraft.Delete(c.User, slug, c.User)
	EndAppPreview(c.User, slug)
	msg := "Discarded the work in progress on " + d.Spec.Name + ": the live app is as it was."
	if _, live := loadSpec(c.User, slug); !live {
		if gone, _ := DeleteAppOwned(c.User, slug); len(gone) > 0 {
			msg = "Discarded " + d.Spec.Name + ", never live, and what was made for it: " + strings.Join(gone, ", ") + "."
		} else {
			msg = "Discarded " + d.Spec.Name + ": it was never live."
		}
		DeleteAppAssets(c.User, slug)
	}
	return map[string]any{"ok": true, "message": msg}, nil
}

// draftRows marks the person's own app rows that have work in progress, and
// adds a row for each new app of theirs not live yet. seen is the slugs
// already listed.
func draftRows(user string, rows []map[string]string, seen map[string]bool) []map[string]string {
	drafts := map[string]appdraft.Draft{}
	for _, d := range appdraft.List(user, "") {
		if d.Editor == user {
			drafts[d.Spec.Slug] = d
		}
	}
	if len(drafts) == 0 {
		return rows
	}
	for _, row := range rows {
		if row["mine"] != "1" {
			continue
		}
		if _, ok := drafts[row["slug"]]; ok {
			row["draft"] = "1"
			note := "changes in progress, not live yet"
			if row["note"] != "" {
				note = row["note"] + " - " + note
			}
			row["note"] = note
		}
	}
	for slug, d := range drafts {
		if seen[slug] {
			continue
		}
		rows = append(rows, map[string]string{
			"slug": slug, "name": firstNonEmptyText(d.Spec.Name, slug), "desc": d.Spec.Desc, "group": "Your apps",
			"state": "draft", "draft": "1", "not_open": "1", "can_change": "1",
			"note":   "new, not live yet: live once Builder commits it, or when you make it live",
			"status": "not live yet",
		})
	}
	return rows
}
