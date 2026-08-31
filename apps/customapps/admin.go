// The operator's view of custom apps: one admin tab, the apps down the left,
// that app's operator controls on the right.
//
// Custom apps are the only apps an operator could not administer. A compiled
// app registers its dials at startup; a custom app is a record somebody wrote
// at runtime, so there was nothing to register and nowhere to answer "who may
// reach this" or "revoke that link".
//
// The rail is NOT a NavShell. Admin Tuning had an embedded one and it was
// deliberately replaced by one ui.Section per entry sharing a Group, which the
// page's own SectionNav draws as the side index — see apps/admin/page.go. This
// follows that, so the tab navigates like every other admin tab.
//
// See docs/custom-app-admin.md.
package customapps

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appadmin"
	"github.com/cmcoffee/gohort/core/ui"
)

const customAppsAdminGroup = "Custom Apps"

// adminSections builds one section per custom app, across every owner.
//
// A SOURCE and not a registration: these rows are records people write while
// the server runs, so a list fixed at startup is wrong by lunchtime.
func (T *CustomApps) adminSections(r *http.Request) []AdminSectionEntry {
	if !AuthIsAdmin(AuthDB(), r) {
		// The source's own responsibility, exactly as it is for a dashboard
		// card source. The admin page is gated too; this is the second lock,
		// because a source that trusts its caller is a source that leaks the
		// moment somebody renders it somewhere else.
		return nil
	}
	type row struct {
		spec  AppSpec
		owner string
	}
	var rows []row
	for _, u := range AuthListUsers(AuthDB()) {
		for _, s := range ListAppSpecs(u.Username) {
			rows = append(rows, row{spec: s, owner: u.Username})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].owner != rows[j].owner {
			return rows[i].owner < rows[j].owner
		}
		return strings.ToLower(rows[i].spec.Name) < strings.ToLower(rows[j].spec.Name)
	})

	if len(rows) == 0 {
		// An empty rail reads as a broken tab. Say what is true instead.
		return []AdminSectionEntry{{Section: ui.Section{
			Title: "Custom Apps",
			Group: customAppsAdminGroup,
			Body: ui.EmptyState{
				Icon:  "🧩",
				Title: "No custom apps yet",
				Hint:  "Apps authored from a chat with the Builder appear here, one per row, with the operator controls for each.",
			},
		}}}
	}

	out := make([]AdminSectionEntry, 0, len(rows))
	for _, rw := range rows {
		spec := rw.spec
		body := appadmin.For(adminAppView(spec, rw.owner))
		if len(body) == 0 {
			body = []ui.Component{ui.EmptyState{
				Icon: "—", Title: "Nothing to set", Hint: "This app has no operator controls that apply to it.",
			}}
		}
		out = append(out, AdminSectionEntry{Section: ui.Section{
			Title:    spec.Name,
			Subtitle: adminRowSubtitle(spec, rw.owner),
			Group:    customAppsAdminGroup,
			Wide:     true,
			Body:     ui.Stack{Children: body},
		}})
	}
	return out
}

// adminRowSubtitle answers, in the rail, the question an operator is scanning
// for: whose app is this and what state is it in.
func adminRowSubtitle(spec AppSpec, owner string) string {
	var state []string
	if spec.Disabled {
		if st := appadmin.Load(RootDB, owner, spec.Slug); st.DisabledBy != "" {
			state = append(state, "disabled by "+st.DisabledBy)
		} else {
			state = append(state, "disabled")
		}
	}
	if spec.PublicToken != "" {
		state = append(state, "public link")
	}
	if spec.Shared {
		state = append(state, "shared")
	}
	if len(state) == 0 {
		return owner
	}
	return owner + " · " + strings.Join(state, " · ")
}

// adminAppView narrows a stored spec to what a control needs. ListAppSpecs
// keys by owner rather than carrying it, so the owner is supplied here.
func adminAppView(spec AppSpec, owner string) appadmin.App {
	return appadmin.App{
		Owner:       owner,
		Slug:        spec.Slug,
		Name:        spec.Name,
		Shared:      spec.Shared,
		Disabled:    spec.Disabled,
		PublicToken: spec.PublicToken,
		PipelineID:  spec.PipelineID,
	}
}

// --- the controls -----------------------------------------------------------

func (T *CustomApps) registerAdminControls() {
	base := T.WebPath() + "/_admin"

	// Exposure: the anonymous capability link. The owner can revoke it from
	// their own index; an operator needs to as well, and unlike a setting it is
	// not an edit to what the app IS.
	appadmin.Register(appadmin.Control{
		Key: "customapps.public_link", Label: "Public link", Group: "Exposure", Order: 10,
		Render: func(spec appadmin.App) ui.Component {
			if spec.PublicToken == "" {
				return nil // nothing published: no control, rather than a dead button
			}
			return ui.Toolbar{Actions: []ui.ToolbarAction{
				{
					Label:  "Open public link",
					Method: "open",
					Title:  "Anyone with this link loads the page and runs its data sources, in the owner's sandbox",
					URL:    DashboardURL() + T.WebPath() + "/pub/" + spec.PublicToken + "/",
				},
				{
					Label:   "Revoke link",
					Method:  "post",
					Variant: "danger",
					Confirm: "Revoke this link? Anyone holding it loses access immediately, and re-publishing mints a different one.",
					URL: fmt.Sprintf("%s/revoke-link?owner=%s&slug=%s",
						base, url.QueryEscape(spec.Owner), url.QueryEscape(spec.Slug)),
				},
			}}
		},
	})

	// Access: who may open a SHARED app. Empty is every authenticated user,
	// which is what sharing has always meant, so an unset list changes nothing.
	appadmin.Register(appadmin.Control{
		Key: "customapps.allowed_users", Label: "Who can open it", Group: "Access", Order: 10,
		Render: func(spec appadmin.App) ui.Component {
			if !spec.Shared {
				return nil // an unshared app is already only its owner's
			}
			q := fmt.Sprintf("?owner=%s&slug=%s", url.QueryEscape(spec.Owner), url.QueryEscape(spec.Slug))
			return ui.ChipPicker{
				OptionsSource: base + "/reach" + q,
				RecordsField:  "users",
				AttachedField: "allowed",
				PostTo:        base + "/reach" + q,
				SaveKey:       "users",
				NameField:     "name",
				Intro: "Leave every chip off for all signed-in users, which is what sharing means on its own. " +
					"Turning any on narrows it to those people. The owner always has access.",
				EmptyText: "This deployment has no other users to grant.",
			}
		},
	})
}

// --- the endpoints ----------------------------------------------------------

// handleAdmin serves the operator writes. Admin-gated here rather than trusting
// the page that rendered the control: a control's URL is a URL, and whoever
// finds it is not necessarily who was shown it.
func (T *CustomApps) handleAdmin(w http.ResponseWriter, r *http.Request, user string, sub string) {
	if !AuthIsAdmin(AuthDB(), r) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	spec, ok := loadSpec(owner, slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch sub {
	case "revoke-link":
		if spec.PublicToken != "" {
			T.DB.Unset(publicAppsIndex, spec.PublicToken)
			spec.PublicToken = ""
			SaveAppSpec(spec)
			Log("[customapps] admin %q revoked the public link on %q/%q", user, owner, slug)
		}
		writeJSON(w, map[string]any{"ok": true, "message": "Link revoked."})
	case "reach":
		st := appadmin.Load(RootDB, owner, slug)
		if r.Method == http.MethodGet {
			// One response carries BOTH the options and the current selection,
			// which is what lets the picker skip a second fetch for a record
			// that does not exist as one.
			users := []map[string]any{}
			for _, u := range AuthListUsers(AuthDB()) {
				if u.Username == owner {
					continue // the owner always reaches their own app
				}
				users = append(users, map[string]any{"name": u.Username})
			}
			allowed := st.AllowedUsers
			if allowed == nil {
				allowed = []string{}
			}
			writeJSON(w, map[string]any{"users": users, "allowed": allowed})
			return
		}
		var in struct {
			Users []string `json:"users"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		st.AllowedUsers = in.Users
		st.UpdatedBy = user
		st.Updated = time.Now().UTC().Format(time.RFC3339)
		appadmin.Save(RootDB, owner, slug, st)
		Log("[customapps] admin %q set the allowlist on %q/%q to %d user(s)", user, owner, slug, len(st.AllowedUsers))
		writeJSON(w, map[string]any{"ok": true, "message": "Saved."})
	default:
		http.NotFound(w, r)
	}
}
