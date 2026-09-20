package customapps

// Retiring the anonymous capability link.
//
// The link served an app to whoever held a URL, with no account at all, running
// the owner's data sources under the owner's credentials. It was the one path
// on which a run could not be attributed to a person, which is why it needed an
// administrator's approval to exist. Sharing to signed-in users stays, with its
// per-user copies, so every opener is somebody the deployment knows.
//
// Removing the code stops the links being SERVED. This is the other half: the
// token index is emptied so nothing resolves through it, and every owner who
// had one is told, by name, which app it was. A capability that silently stops
// working is a support ticket; a capability that says it was withdrawn, and
// which one, is a decision somebody can act on.

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
)

// publicAppsIndexRetired is the table anonymous tokens used to live in. Named
// here rather than left in the main file so that a reader looking for the
// public surface finds this explanation instead of a live-looking index.
const publicAppsIndexRetired = "public_custom_apps"

// retirePublicLinks empties the token index once at startup and notifies each
// affected owner.
//
// Idempotent: the second run finds nothing and says nothing. The notice itself
// folds per owner and app, so even a repeated call could not produce a second
// alert about the same link.
//
// Tokens are also cleared from the specs that carry them, because a spec whose
// PublicToken is set reads as "this app is published" on every surface that
// checks the field, and it is not published anywhere any more.
func (T *CustomApps) retirePublicLinks() {
	if T.DB == nil {
		return
	}
	type retired struct{ owner, slug string }
	var found []retired
	for _, token := range T.DB.Keys(publicAppsIndexRetired) {
		var ref struct {
			Owner string `json:"owner"`
			Slug  string `json:"slug"`
		}
		if !T.DB.Get(publicAppsIndexRetired, token, &ref) {
			continue
		}
		T.DB.Unset(publicAppsIndexRetired, token)
		if strings.TrimSpace(ref.Owner) == "" || strings.TrimSpace(ref.Slug) == "" {
			continue // an entry nobody can be told about; dropping it is the whole fix
		}
		found = append(found, retired{ref.Owner, ref.Slug})
	}
	for _, r := range found {
		// The spec's own copy of the token, so nothing downstream still reads
		// the app as published.
		if spec, ok := loadSpec(r.owner, r.slug); ok && spec.PublicToken != "" {
			spec.PublicToken = ""
			SaveAppSpec(spec)
		}
		name := r.slug
		if spec, ok := loadSpec(r.owner, r.slug); ok && strings.TrimSpace(spec.Name) != "" {
			name = spec.Name
		}
		notices.Record(RootDB, notices.Notice{
			Owner: r.owner,
			Kind:  notices.KindStopped,
			Title: "The public link for " + name + " no longer works",
			Body: "Anonymous links have been removed from this deployment: they served an app to anyone holding a URL, with no account, running your data sources under your credentials. " +
				"Sharing is unaffected. If people need this app, share it to signed-in users from My Apps and each of them gets their own copy.",
		})
		Log("[customapps] retired the public link on %q/%q", r.owner, r.slug)
	}
}
