// The operator's view of custom apps: a row each in admin's list of apps, and
// a page of its own holding that app's operator controls.
//
// Custom apps are the only apps an operator could not administer. A compiled
// app registers its dials at startup; a custom app is a record somebody wrote
// at runtime, so there was nothing to register and nowhere to answer "who may
// reach this" or "revoke that link".
//
// A row and a page, not a section per app: a section per app made the tab's
// index grow with every app anybody built. The rows join the compiled apps in
// admin's one table.
//
// See docs/design/custom-app-admin.md.
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
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/appadmin"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

// adminApps lists every custom app, across every owner, as rows of admin's
// list of apps.
//
// A SOURCE and not a registration: these rows are records people write while
// the server runs, so a list fixed at startup is wrong by lunchtime.
func (T *CustomApps) adminApps(r *http.Request) []AdminAppEntry {
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

	out := make([]AdminAppEntry, 0, len(rows))
	for _, rw := range rows {
		spec := rw.spec
		e := AdminAppEntry{
			// Owner and slug: a slug is unique only among one owner's apps.
			Key:      rw.owner + "/" + spec.Slug,
			Name:     spec.Name,
			Owner:    rw.owner,
			Desc:     spec.Desc,
			Disabled: spec.Disabled,
			Access:   adminAccess(spec, rw.owner),
		}
		if body := appadmin.For(adminAppView(spec, rw.owner)); len(body) > 0 {
			e.Settings = plural(len(body), "control")
			e.Page = func() []ui.Section {
				return []ui.Section{{
					Title: "Operator controls",
					Subtitle: "Who may reach " + spec.Name + ", how it is exposed, and what it may cost. " +
						"Its owner builds and edits it; these are the deployment's limits on it.",
					Wide: true,
					Body: ui.Stack{Children: body},
				}}
			}
		}
		out = append(out, e)
	}
	return out
}

// adminAccess answers, in the list, the question an operator scans it for:
// who can open this app.
func adminAccess(spec AppSpec, owner string) string {
	var state []string
	if spec.PublicToken != "" {
		state = append(state, "public link")
	}
	if spec.Shared {
		state = append(state, "shared")
	}
	if live, _ := sharedGroupNames(spec); len(live) > 0 {
		state = append(state, "shared with "+strings.Join(live, ", "))
	}
	if len(state) == 0 {
		state = append(state, "its owner only")
	}
	if spec.Disabled {
		if st := appadmin.Load(RootDB, owner, spec.Slug); st.DisabledBy != "" {
			state = append(state, "switched off by "+st.DisabledBy)
		}
	}
	return strings.Join(state, " - ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
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

	// Exposure: clearing a token left over from before anonymous links were
	// removed. There is nothing to open, because nothing serves it any more;
	// what remains is a field on old specs, and an operator should be able to
	// tidy one away rather than read a state the deployment cannot honour.
	appadmin.Register(appadmin.Control{
		Key: "customapps.public_link", Label: "Public link", Group: "Exposure", Order: 10,
		Render: func(spec appadmin.App) ui.Component {
			if spec.PublicToken == "" {
				return nil // the ordinary case now: no control, rather than a dead button
			}
			return ui.Toolbar{Actions: []ui.ToolbarAction{
				{
					Label:   "Clear leftover link token",
					Method:  "post",
					Variant: "danger",
					Title:   "Anonymous links were removed; this token no longer serves anything",
					Confirm: "Clear this leftover token? Anonymous links no longer exist, so nothing is currently served from it.",
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
				Intro: "Narrows this ONE app. Leave every chip off and it stays open to everyone who can " +
					"reach My Apps at all; turning any on restricts it to those people regardless of what " +
					"they have been granted. This is ANDed with the app grant in Users, that one says whether " +
					"somebody uses My Apps, this one says who gets into this app. The owner always has access.",
				EmptyText: "This deployment has no other users to grant.",
			}
		},
	})

	// Cost: which tier each stage of the bound pipeline runs on.
	//
	// Registered HERE rather than from the pipeline layer, which is where the
	// concern nominally lives, because this is the package that already
	// resolves (owner, slug) to a definition and already has an admin-gated
	// mount. The registry does not care who registers; a second mount serving
	// one form would be the worse trade. The control's OWNER is still free to
	// take it over later without this package changing.
	RegisterCustomAppTierControl(base)
	RegisterCustomAppReviewControl(base)
}

// RegisterCustomAppTierControl adds the per-stage tier dials.
func RegisterCustomAppTierControl(base string) {
	appadmin.Register(appadmin.Control{
		Key: "customapps.pipeline_tiers", Label: "Model tier", Group: "Cost", Order: 10,
		Render: func(app appadmin.App) ui.Component {
			if app.PipelineID == "" {
				return nil // no pipeline: no stages, and no dial for nothing
			}
			def, ok := lookupAdminPipeline(app.Owner, app.PipelineID)
			if !ok || len(def.Stages) == 0 {
				return nil
			}
			// The dials are DERIVED from the stored definition every time this
			// renders, not registered when it was saved. That is what makes
			// them survive a restart, and what makes a renamed stage lose its
			// dial instead of keeping one that applies to nothing.
			fields := make([]ui.FormField, 0, len(def.Stages))
			for _, st := range def.Stages {
				if !stageTierHonoured(st.Kind) {
					continue
				}
				authored := "worker"
				if strings.EqualFold(strings.TrimSpace(st.Model), "lead") {
					authored = "lead"
				}
				opts := []ui.SelectOption{{
					Value: "",
					Label: "As authored (" + authored + ")",
					Help:  "No override: the stage runs on the tier the pipeline's author chose.",
				}}
				for _, v := range RouteValues() {
					opts = append(opts, ui.SelectOption{Value: v, Label: v})
				}
				fields = append(fields, ui.FormField{
					Field: st.Name, Label: st.Name, Type: "select", Options: opts,
					Help: "kind=" + string(st.Kind),
				})
			}
			if len(fields) == 0 {
				return nil // no stage here reads a tier
			}
			q := fmt.Sprintf("?owner=%s&slug=%s", url.QueryEscape(app.Owner), url.QueryEscape(app.Slug))
			return ui.FormPanel{
				Source:      base + "/tiers" + q,
				PostURL:     base + "/tiers" + q,
				SubmitLabel: "Save tiers",
				Fields:      fields,
			}
		},
	})
}

// stageTierHonoured reports whether a stage of this kind reads the tier
// override. Only the kinds that make their own model call do: worker,
// synthesize, fanout and panel. An agent stage runs on its agent's model, and
// loop, branch, tool and machine stages make no call of their own, so a dial
// for one would be a setting that silently does nothing.
func stageTierHonoured(kind PipelineStageKind) bool {
	switch kind {
	case StageWorker, StageSynthesize, StageFanout, StagePanel:
		return true
	}
	return false
}

// RegisterCustomAppReviewControl adds the script-review surface.
//
// The bundle import gate already lands an imported app disabled precisely so
// somebody looks before anything runs, and until now there was nowhere to look
// FROM. This is that place.
func RegisterCustomAppReviewControl(base string) {
	appadmin.Register(appadmin.Control{
		Key: "customapps.review", Label: "Scripts", Group: "Review", Order: 10,
		Render: func(app appadmin.App) ui.Component {
			spec, ok := loadSpec(app.Owner, app.Slug)
			if !ok || (len(spec.DataSources) == 0 && len(spec.Actions) == 0 && len(pageBrowserCode(spec)) == 0) {
				return nil // nothing to review
			}
			q := fmt.Sprintf("?owner=%s&slug=%s", url.QueryEscape(app.Owner), url.QueryEscape(app.Slug))
			return ui.DisplayPanel{
				Source: base + "/review" + q,
				Pairs: []ui.DisplayPair{
					{Label: "Data sources", Field: "sources"},
					{Label: "Action scripts", Field: "actions"},
					{Label: "Code in the page", Field: "page_code"},
					{Label: "Capabilities declared", Field: "capabilities"},
					{Label: "Runs as", Field: "runs_as"},
				},
				Actions: []ui.ToolbarAction{{
					Label:  "Show scripts",
					Method: "open",
					Title:  "Read the sandboxed code this app runs. Opening it is recorded.",
					URL:    base + "/scripts" + q,
				}},
			}
		},
	})
}

// lookupAdminPipeline resolves an app's bound pipeline against its OWNER, the
// same way serving it does.
func lookupAdminPipeline(owner, pipelineID string) (PipelineDef, bool) {
	orch := agenthost.Get()
	if orch == nil {
		return PipelineDef{}, false
	}
	return orch.LookupAppPipeline(owner, pipelineID)
}

// --- the endpoints ----------------------------------------------------------

// adminOnly answers anybody but an administrator as if the operator routes
// were not there. Checked here rather than trusting the page that rendered the
// control: a control's URL is a URL, and whoever finds it is not necessarily
// who was shown it.
func adminOnly(w http.ResponseWriter, r *http.Request) bool {
	if !AuthIsAdmin(AuthDB(), r) {
		http.NotFound(w, r)
		return false
	}
	return true
}

// adminRoute is one operator control's route: GET what it shows, POST a
// change.
func (T *CustomApps) adminRoute(sub string) http.HandlerFunc {
	return jsonapi.Methods{
		"GET": jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) {
			return T.handleAdminRead(c, sub)
		}, jsonapi.Authorize(adminOnly)),
		"POST": jsonapi.Post(T.DB, func(c *jsonapi.Call, raw json.RawMessage) (any, error) {
			return T.handleAdminWrite(c, sub, raw)
		}, jsonapi.Authorize(adminOnly), jsonapi.EmptyBodyOK),
	}.Serve
}

// adminApp is the app ?owner=&slug= names.
func adminApp(r *http.Request) (spec AppSpec, owner, slug string, found bool) {
	owner = strings.TrimSpace(r.URL.Query().Get("owner"))
	slug = strings.TrimSpace(r.URL.Query().Get("slug"))
	spec, found = loadSpec(owner, slug)
	return spec, owner, slug, found
}

// handleAdminRead serves what an operator control shows: who may open the app
// (reach), its stages' tiers (tiers), and what its scripts declare (review).
func (T *CustomApps) handleAdminRead(c *jsonapi.Call, sub string) (any, error) {
	spec, owner, slug, ok := adminApp(c.R)
	if !ok {
		return nil, errNoApp
	}
	switch sub {
	case "reach":
		// One response carries BOTH the options and the current selection,
		// which is what lets the picker skip a second fetch for a record
		// that does not exist as one.
		st := appadmin.Load(RootDB, owner, slug)
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
		return map[string]any{"users": users, "allowed": allowed}, nil
	case "tiers":
		def, ok := lookupAdminPipeline(owner, spec.PipelineID)
		if !ok {
			return nil, errNoApp
		}
		// Only the OVERRIDE is stored, so an unset stage comes back empty
		// and the form shows "As authored" — which is the true statement
		// about it, where a filled-in value would claim a decision nobody
		// made.
		out := map[string]any{}
		for _, st := range def.Stages {
			out[st.Name] = RouteOverride(PipelineStageRouteKey(def.ID, st.Name))
		}
		return out, nil
	case "review":
		return adminReview(spec, owner), nil
	}
	return nil, errNoApp
}

// handleAdminWrite serves the operator writes: clearing a leftover link token
// (revoke-link), who may open the app (reach), and its stages' tiers (tiers).
func (T *CustomApps) handleAdminWrite(c *jsonapi.Call, sub string, raw json.RawMessage) (any, error) {
	spec, owner, slug, ok := adminApp(c.R)
	if !ok {
		return nil, errNoApp
	}
	user := c.User
	switch sub {
	case "revoke-link":
		// Anonymous links no longer exist; the control stays only long enough
		// to clear a token left on a spec from before they were removed.
		if spec.PublicToken != "" {
			spec.PublicToken = ""
			SaveAppSpec(spec)
			Log("[customapps] admin %q cleared a leftover public token on %q/%q", user, owner, slug)
		}
		return map[string]any{"ok": true, "message": "Link revoked."}, nil
	case "reach":
		var in struct {
			Users []string `json:"users"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, jsonapi.BadRequest("bad request")
		}
		st := appadmin.Load(RootDB, owner, slug)
		st.AllowedUsers = in.Users
		st.UpdatedBy = user
		st.Updated = time.Now().UTC().Format(time.RFC3339)
		appadmin.Save(RootDB, owner, slug, st)
		Log("[customapps] admin %q set the allowlist on %q/%q to %d user(s)", user, owner, slug, len(st.AllowedUsers))
		return map[string]any{"ok": true, "message": "Saved."}, nil
	case "tiers":
		def, ok := lookupAdminPipeline(owner, spec.PipelineID)
		if !ok {
			return nil, errNoApp
		}
		var in map[string]any
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, jsonapi.BadRequest("bad request")
		}
		legal := map[string]bool{"": true}
		for _, v := range RouteValues() {
			legal[v] = true
		}
		for _, st := range def.Stages {
			raw, present := in[st.Name]
			if !present || !stageTierHonoured(st.Kind) {
				continue
			}
			val := strings.TrimSpace(fmt.Sprint(raw))
			if !legal[val] {
				return nil, jsonapi.BadRequest("invalid tier for stage " + st.Name)
			}
			key := PipelineStageRouteKey(def.ID, st.Name)
			if val == "" {
				// Clearing an override returns the stage to its author, which
				// is a different thing from pinning it to worker and has to
				// stay expressible.
				RootDB.Unset(RoutingTable, key)
				continue
			}
			RootDB.Set(RoutingTable, key, val)
		}
		Log("[customapps] admin %q set stage tiers on %q/%q", user, owner, slug)
		return map[string]any{"ok": true, "message": "Saved."}, nil
	}
	return nil, errNoApp
}

// adminReview is what the app's scripts declare, for the Scripts control.
func adminReview(spec AppSpec, owner string) map[string]any {
	caps := map[string]bool{}
	for _, d := range spec.DataSources {
		for _, c := range d.Capabilities {
			caps[c] = true
		}
	}
	for _, a := range spec.Actions {
		for _, c := range a.Capabilities {
			caps[c] = true
		}
	}
	declared := make([]string, 0, len(caps))
	for c := range caps {
		declared = append(declared, c)
	}
	sort.Strings(declared)
	shown := strings.Join(declared, ", ")
	if shown == "" {
		// Not the same as "none declared and therefore harmless": fetch is
		// granted by default and the owner's credentials are auto-granted
		// to app scripts, so an empty list is a statement about the
		// DECLARATION, not about the reach.
		shown = "none declared (fetch and the owner's credentials are granted by default)"
	}
	pageCode := "none"
	if n := len(pageBrowserCode(spec)); n > 0 {
		pageCode = fmt.Sprintf("%d block(s) of HTML or script that run in each viewer's browser, isolated (no gohort session, only this app's own endpoints): read them under Show scripts", n)
	}
	return map[string]any{
		"sources":      len(spec.DataSources),
		"actions":      len(spec.Actions),
		"page_code":    pageCode,
		"capabilities": shown,
		"runs_as":      owner,
	}
}

// handleAdminScripts is the app's code, as plain text for an operator to read.
func (T *CustomApps) handleAdminScripts(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok || !adminOnly(w, r) {
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	spec, owner, slug, found := adminApp(r)
	if !found {
		http.NotFound(w, r)
		return
	}
	// Logged, always. Reading somebody else's code is a legitimate operator
	// act and an unrecorded one is indistinguishable from a quiet look.
	Log("[customapps] admin %q read the scripts of %q/%q", user, owner, slug)
	var b strings.Builder
	fmt.Fprintf(&b, "%s, %s\nowner: %s\nscripts run in the owner's sandbox, as the owner\n",
		spec.Name, spec.Slug, owner)
	for _, d := range spec.DataSources {
		fmt.Fprintf(&b, "\n=== data source: %s (%s) ===\ncapabilities: %s\n\n%s\n",
			d.Name, firstNonEmptyText(d.Language, "python"), capsOrNone(d.Capabilities), d.Script)
	}
	for _, a := range spec.Actions {
		fmt.Fprintf(&b, "\n=== action: %s (%s) ===\ncapabilities: %s\n\n%s\n",
			a.Name, firstNonEmptyText(a.Language, "python"), capsOrNone(a.Capabilities), a.Script)
	}
	// The page's own HTML and script. It runs in each viewer's browser,
	// in an isolated frame with no origin, reaching only the app's own
	// endpoints (isolateAppHTML): not as the viewer on this site.
	for i, code := range pageBrowserCode(spec) {
		fmt.Fprintf(&b, "\n=== page code %d (runs in each viewer's browser, isolated: no gohort session, only this app's own endpoints) ===\n\n%s\n", i+1, code)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// pageBrowserCode collects every string in an app's page and sections that
// carries HTML or script: what a Card or Frame renders into the viewer's own
// session. The share review listed only the sandboxed scripts, so an app
// whose page carried code read as having "nothing to review".
func pageBrowserCode(spec AppSpec) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			low := strings.ToLower(t)
			if strings.Contains(low, "<script") || strings.Contains(low, "javascript:") ||
				(strings.Contains(t, "<") && strings.Contains(t, ">") && strings.Contains(low, " on")) ||
				strings.Contains(low, "<iframe") || strings.Contains(low, "<html") || strings.Contains(low, "<style") {
				out = append(out, t)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for k, e := range t {
				if s, ok := e.(string); ok && (k == "html" || k == "srcdoc" || k == "script" || k == "js") && strings.TrimSpace(s) != "" {
					out = append(out, s)
					continue
				}
				walk(e)
			}
		}
	}
	for _, raw := range [][]byte{spec.Page, spec.Sections} {
		var v any
		if len(raw) > 0 && json.Unmarshal(raw, &v) == nil {
			walk(v)
		}
	}
	return out
}

func capsOrNone(caps []string) string {
	if len(caps) == 0 {
		return "none declared"
	}
	return strings.Join(caps, ", ")
}

func firstNonEmptyText(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
